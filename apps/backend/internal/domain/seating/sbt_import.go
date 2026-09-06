// sbt_import.go implements ImportSBTSVG — the reader for the sbt/1.0
// seating-plan dialect Bil24 serves from `image?type=seatingPlan`
// (spec §13.3 of 08_architecture/18_bil24_compat_wave1_specification_ru.md).
//
// It is the exact inverse of the writer in
// internal/platform/httpserver/hseating/sbt10_svg.go, and the two are
// pinned against the same golden
// (tests/compat/bil24/testdata/wp/svg/palac_akropolis.sbt.svg).
//
// Why a second importer instead of extending ImportSVG: the Inkscape
// convention (§6) carries identity in inkscape:label / <title> text and
// binds a seat to its price category BY FILL COLOUR. The sbt dialect
// carries identity in attributes — sbt:sect / sbt:row / sbt:seat — binds
// by an explicit sbt:cat INDEX, and adds the upstream integer ids
// (sbt:id) that arena must preserve verbatim so a Bil24-imported session
// keeps addressing its seats by the same numbers on the wire. Merging
// the two rule sets into one function would make every rule conditional
// on a dialect flag; they stay separate and share only the XML tree
// helpers and the Geometry model.
//
// Wire shape consumed (spec §8):
//
//	<svg xmlns:sbt="http://www.w3.org/2015/sbt/1.0" viewBox="0 0 W H">
//	  <metadata>
//	    <sbt:category sbt:id="1000000020" sbt:index="1" sbt:name="Parter"
//	                  sbt:color="#e53935" sbt:price="900"/>
//	  </metadata>
//	  <g id="Decor">…</g>
//	  <g sbt:sect="Parter">
//	    <g sbt:row="3">
//	      <circle sbt:id="1731" sbt:state="1" sbt:cat="1" sbt:seat="12"
//	              cx="…" cy="…" r="6" fill="#e53935"/>
//
// Rules (§13.3), all enforced below and covered by sbt_import_test.go:
//
//   - viewBox is mandatory — unlike ImportSVG there is NO width/height
//     fallback, because the picker's coordinate transform is defined on
//     viewBox alone and a plan without it cannot be rendered faithfully.
//   - a seat is a <circle> carrying BOTH sbt:id and sbt:state. Anything
//     else — including a <circle> that is pure decoration — is decor.
//   - sector = nearest ancestor with sbt:sect; row = nearest ancestor
//     with sbt:row, or that ancestor's <title> text when the attribute
//     is absent; number = sbt:seat.
//   - a seat with no sbt:cat, or one referencing an index absent from
//     <metadata>, is a validation error: an unpriced seat must never be
//     silently materialised.
//   - a duplicate sbt:id is a validation error: system_seat_id is the
//     wire identity and must be injective.
//   - everything that is not <metadata> and not a sector subtree is
//     decor, re-serialised verbatim (no consolidate-transform pass —
//     arena renders its own SVG from geometry and only needs the
//     backdrop bytes).
//
// Canvas size is deliberately NOT capped here (MaxCanvasDimension is a
// §6 authoring rule for plans drawn for arena). A Bil24 plan is an
// upstream fact: rejecting it for being 2400 units wide would block an
// otherwise correct import.
package seating

import (
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// SBTNamespaceURI is the sbt/1.0 namespace both this importer and the
// hseating writer match attributes on. It is a fixed identifier, not a
// resolvable document.
const SBTNamespaceURI = "http://www.w3.org/2015/sbt/1.0"

// SBTCategory is one <sbt:category> entry from <metadata>: the price
// category as Bil24 knows it. Index is the value seats reference through
// sbt:cat; ExternalID is Bil24's `categoryPriceId`, which the importing
// endpoint (§13.2) registers through compatids so the same integer keeps
// identifying the tier on the wire.
//
// Price is kept verbatim as authored (major units, e.g. "900", "12.5")
// and PriceMinor is its minor-unit rendering — the DB column the tier
// upsert writes. Both are carried because the string is what a diff /
// warning message should quote, while the integer is what gets stored.
type SBTCategory struct {
	Index      int
	ExternalID int64
	Name       string
	Color      string
	Price      string
	PriceMinor int64
}

// SBTPlan is the result of ImportSBTSVG: the canonical geometry (already
// Canonicalize'd, so Checksum is stable) plus the category list in
// ascending Index order. The categories are also mirrored into
// Geometry.Categories — SBTPlan.Categories exists because the importing
// endpoint needs the upstream price, which the geometry model only
// carries as an untyped hint.
type SBTPlan struct {
	Geometry   Geometry
	Categories []SBTCategory
	// StatusVersion is the sbt:statusVersion cursor of the source
	// document, or 0 when absent. Informational: arena maintains its own
	// seat_status_version once the plan is bound to a session.
	StatusVersion int64
}

// ImportSBTSVG parses a Bil24 sbt/1.0 seating plan into a canonical
// Geometry. The contract mirrors ImportSVG: warnings never block the
// import, and a non-empty ValidationErrors means the caller MUST NOT
// persist the returned plan even though it may be partially populated
// for diagnostics.
func ImportSBTSVG(raw []byte) (SBTPlan, []ValidationError, ValidationErrors) {
	root, err := parseXMLTree(raw)
	if err != nil {
		return SBTPlan{}, nil, ValidationErrors{{
			Code:   ErrInvalidSVG,
			Detail: err.Error(),
		}}
	}

	var (
		warnings []ValidationError
		errs     ValidationErrors
	)

	canvas, canvasErr := parseSBTCanvas(root)
	if canvasErr != nil {
		errs = append(errs, *canvasErr)
	}

	cats, catByIndex, catErrs := parseSBTCategories(root)
	errs = append(errs, catErrs...)

	sections, seatErrs := parseSBTSections(root, catByIndex)
	errs = append(errs, seatErrs...)
	if len(sections) == 0 && !seatErrs.HasCode(ErrSBTSeatCategoryMissing) {
		errs = append(errs, ValidationError{
			Code:   ErrSBTSeatsMissing,
			Detail: "no <circle> with sbt:id and sbt:state found",
		})
	}

	geomCats := make([]Category, 0, len(cats))
	for _, c := range cats {
		geomCats = append(geomCats, Category{
			Index:      c.Index,
			Name:       c.Name,
			Color:      c.Color,
			PriceHint:  c.Price,
			ExternalID: c.ExternalID,
		})
	}

	g := Geometry{
		SchemaVersion: SchemaVersion,
		Canvas:        canvas,
		Categories:    geomCats,
		Sections:      sections,
		Tables:        []Table{},
		DecorSVG:      renderSBTDecorSVG(root),
	}

	return SBTPlan{
		Geometry:      Canonicalize(g),
		Categories:    cats,
		StatusVersion: parseInt64Attr(sbtAttr(root, "statusVersion")),
	}, warnings, errs
}

// parseSBTCanvas resolves the canvas from viewBox only (see the file
// docstring for why there is no width/height fallback).
func parseSBTCanvas(root *xmlNode) (Canvas, *ValidationError) {
	if root == nil {
		return Canvas{}, &ValidationError{
			Code:   ErrCanvasMissing,
			Detail: "root <svg> element is absent",
		}
	}
	vb := strings.TrimSpace(attr(root, "viewBox"))
	if vb == "" {
		return Canvas{}, &ValidationError{
			Code:    ErrCanvasMissing,
			Element: "svg",
			Detail:  "sbt seating plans must carry a viewBox",
		}
	}
	parts := strings.Fields(strings.ReplaceAll(vb, ",", " "))
	if len(parts) != 4 {
		return Canvas{}, &ValidationError{
			Code:    ErrCanvasMissing,
			Element: "svg",
			Detail:  fmt.Sprintf("viewBox %q is not four numbers", vb),
		}
	}
	w, errW := strconv.ParseFloat(parts[2], 64)
	h, errH := strconv.ParseFloat(parts[3], 64)
	if errW != nil || errH != nil || w <= 0 || h <= 0 {
		return Canvas{Width: w, Height: h}, &ValidationError{
			Code:    ErrCanvasMissing,
			Element: "svg",
			Detail:  fmt.Sprintf("viewBox %q has no positive width/height", vb),
		}
	}
	return Canvas{Width: w, Height: h}, nil
}

// parseSBTCategories reads <metadata><sbt:category …/> and returns the
// categories in ascending Index order plus an index→position lookup used
// for sbt:cat resolution.
func parseSBTCategories(root *xmlNode) ([]SBTCategory, map[int]SBTCategory, ValidationErrors) {
	var errs ValidationErrors
	meta := findDirectChild(root, "metadata")
	if meta == nil {
		return nil, nil, ValidationErrors{{
			Code:    ErrSBTCategoriesMissing,
			Element: "metadata",
			Detail:  "sbt seating plans must declare <metadata><sbt:category …/>",
		}}
	}

	byIndex := map[int]SBTCategory{}
	var cats []SBTCategory
	for _, ch := range meta.Children {
		el := ch.element
		if el == nil || el.Name.Local != "category" {
			continue
		}
		idxRaw := sbtAttr(el, "index")
		idx, err := strconv.Atoi(strings.TrimSpace(idxRaw))
		if err != nil || idx <= 0 {
			errs = append(errs, ValidationError{
				Code:    ErrSBTCategoryInvalid,
				Element: sbtAttr(el, "name"),
				Detail:  fmt.Sprintf("sbt:index %q is not a positive integer", idxRaw),
			})
			continue
		}
		if _, dup := byIndex[idx]; dup {
			errs = append(errs, ValidationError{
				Code:    ErrSBTDuplicateCategory,
				Element: strconv.Itoa(idx),
				Detail:  "two <sbt:category> elements share one sbt:index",
			})
			continue
		}
		price := strings.TrimSpace(sbtAttr(el, "price"))
		cat := SBTCategory{
			Index:      idx,
			ExternalID: parseInt64Attr(sbtAttr(el, "id")),
			Name:       strings.TrimSpace(sbtAttr(el, "name")),
			Color:      normalizeColor(sbtAttr(el, "color")),
			Price:      price,
			PriceMinor: majorToMinor(price),
		}
		byIndex[idx] = cat
		cats = append(cats, cat)
	}
	if len(cats) == 0 {
		errs = append(errs, ValidationError{
			Code:    ErrSBTCategoriesMissing,
			Element: "metadata",
			Detail:  "<metadata> declares no <sbt:category> elements",
		})
		return nil, byIndex, errs
	}
	sortCategoriesByIndex(cats)
	return cats, byIndex, errs
}

// sortCategoriesByIndex keeps the returned category list in the same
// ascending-index order the writer emits, so a diff of two imports lines
// up row by row.
func sortCategoriesByIndex(cats []SBTCategory) {
	for i := 1; i < len(cats); i++ {
		for j := i; j > 0 && cats[j].Index < cats[j-1].Index; j-- {
			cats[j], cats[j-1] = cats[j-1], cats[j]
		}
	}
}

// sbtSeatNode is one located seat circle with the ancestry context the
// keys are derived from.
type sbtSeatNode struct {
	el      *xmlNode
	sector  string
	rowName string
}

// parseSBTSections walks the document, collects every seat circle with
// its nearest sbt:sect / sbt:row ancestry, and folds them into canonical
// sections.
func parseSBTSections(root *xmlNode, catByIndex map[int]SBTCategory) ([]Section, ValidationErrors) {
	var (
		errs  ValidationErrors
		nodes []sbtSeatNode
	)

	var walk func(n *xmlNode, sector, rowName string)
	walk = func(n *xmlNode, sector, rowName string) {
		if n == nil {
			return
		}
		if n.Name.Local == "metadata" {
			return
		}
		if s := strings.TrimSpace(sbtAttr(n, "sect")); s != "" {
			sector = s
		}
		if r := strings.TrimSpace(sbtAttr(n, "row")); r != "" {
			rowName = r
		} else if sbtAttr(n, "sect") == "" && n.Name.Local == "g" {
			// A row group may name itself through a <title> child
			// instead of sbt:row (§13.3). Only consult it for groups
			// that are not themselves the sector group, otherwise a
			// sector's own <title> would be mistaken for a row name.
			if t := strings.TrimSpace(elementText(findDirectChild(n, "title"))); t != "" {
				rowName = t
			}
		}
		if isSBTSeat(n) {
			nodes = append(nodes, sbtSeatNode{el: n, sector: sector, rowName: rowName})
			return
		}
		for _, ch := range n.Children {
			if ch.element == nil {
				continue
			}
			walk(ch.element, sector, rowName)
		}
	}
	walk(root, "", "")

	// Sections and rows are accumulated through pointer maps with parallel
	// order slices: appending to a []Row while holding a *Row into it
	// would dangle on reallocation, so rows live outside their section
	// until assembly.
	secByKey := map[string]*Section{}
	rowByKey := map[string]*Row{}
	rowOrder := map[string][]string{}
	var secOrder []string
	seenKey := map[string]bool{}
	seenExternal := map[int64]bool{}

	for _, sn := range nodes {
		el := sn.el
		externalID := parseInt64Attr(sbtAttr(el, "id"))
		if externalID <= 0 {
			errs = append(errs, ValidationError{
				Code:    ErrSBTSeatIDInvalid,
				Element: sbtAttr(el, "id"),
				Detail:  "sbt:id must be a positive integer",
			})
			continue
		}
		if seenExternal[externalID] {
			errs = append(errs, ValidationError{
				Code:    ErrSBTDuplicateSeatID,
				Element: strconv.FormatInt(externalID, 10),
				Detail:  "sbt:id appears on more than one seat",
			})
			continue
		}

		number := strings.TrimSpace(sbtAttr(el, "seat"))
		if number == "" {
			// Fall back to <title>, which is how the §6 dialect names a
			// seat; a plan mixing the two conventions still imports.
			number = strings.TrimSpace(elementText(findDirectChild(el, "title")))
		}
		if number == "" {
			errs = append(errs, ValidationError{
				Code:    ErrSeatMissingNumber,
				Element: strconv.FormatInt(externalID, 10),
				Detail:  "seat <circle> has neither sbt:seat nor <title>",
			})
			continue
		}
		if sn.sector == "" {
			errs = append(errs, ValidationError{
				Code:    ErrRowMissingSectorLabel,
				Element: strconv.FormatInt(externalID, 10),
				Detail:  "seat has no ancestor carrying sbt:sect",
			})
			continue
		}
		if sn.rowName == "" {
			errs = append(errs, ValidationError{
				Code:    ErrRowMissingTitle,
				Element: strconv.FormatInt(externalID, 10),
				Detail:  "seat has no ancestor carrying sbt:row or a <title>",
			})
			continue
		}

		catRaw := strings.TrimSpace(sbtAttr(el, "cat"))
		catIdx, catErr := strconv.Atoi(catRaw)
		if catRaw == "" || catErr != nil {
			errs = append(errs, ValidationError{
				Code:    ErrSBTSeatCategoryMissing,
				Element: strconv.FormatInt(externalID, 10),
				Detail:  fmt.Sprintf("seat sbt:cat %q is not a category index", catRaw),
			})
			continue
		}
		if _, ok := catByIndex[catIdx]; !ok {
			errs = append(errs, ValidationError{
				Code:    ErrSBTSeatCategoryMissing,
				Element: strconv.FormatInt(externalID, 10),
				Detail: fmt.Sprintf(
					"seat sbt:cat=%d references no <sbt:category> in <metadata>", catIdx),
			})
			continue
		}

		secKey := normalizeKey(sn.sector)
		rowKey := normalizeKey(sn.rowName)
		key := SeatKey(secKey, rowKey, number)
		if seenKey[key] {
			errs = append(errs, ValidationError{
				Code:    ErrDuplicateSeat,
				Element: key,
				Detail:  "duplicate (sector,row,number) triple",
			})
			continue
		}
		seenKey[key] = true
		seenExternal[externalID] = true

		if _, ok := secByKey[secKey]; !ok {
			secByKey[secKey] = &Section{Key: secKey, Name: sn.sector}
			secOrder = append(secOrder, secKey)
		}
		row, ok := rowByKey[secKey+"|"+rowKey]
		if !ok {
			row = &Row{Key: rowKey, Name: sn.rowName}
			rowByKey[secKey+"|"+rowKey] = row
			rowOrder[secKey] = append(rowOrder[secKey], rowKey)
		}
		row.Seats = append(row.Seats, Seat{
			Key:           key,
			Number:        number,
			X:             parseDimAttr(attr(el, "cx")),
			Y:             parseDimAttr(attr(el, "cy")),
			Radius:        parseDimAttr(attr(el, "r")),
			CategoryIndex: catIdx,
			BarcodeHint:   nil,
			ExternalID:    externalID,
		})
	}

	out := make([]Section, 0, len(secOrder))
	for _, k := range secOrder {
		sec := *secByKey[k]
		for _, rk := range rowOrder[k] {
			sec.Rows = append(sec.Rows, *rowByKey[k+"|"+rk])
		}
		out = append(out, sec)
	}
	return out, errs
}

// isSBTSeat reports whether n is a seat circle: a <circle> carrying both
// sbt:id and sbt:state. The state attribute is part of the test because
// a decorative circle in the backdrop may legitimately carry an id.
func isSBTSeat(n *xmlNode) bool {
	if n == nil || n.Name.Local != "circle" {
		return false
	}
	return sbtAttr(n, "id") != "" && sbtAttr(n, "state") != ""
}

// renderSBTDecorSVG re-serialises everything that is neither <metadata>
// nor a sector subtree. Sector groups are elided whole: their seats are
// modelled in Sections and re-rendered by arena's own writer, so keeping
// them in the backdrop would double-draw every seat.
func renderSBTDecorSVG(root *xmlNode) string {
	if root == nil {
		return ""
	}
	skip := map[*xmlNode]bool{}
	var mark func(n *xmlNode)
	mark = func(n *xmlNode) {
		if n == nil {
			return
		}
		if n.Name.Local == "metadata" || strings.TrimSpace(sbtAttr(n, "sect")) != "" {
			skip[n] = true
			return
		}
		if isSBTSeat(n) {
			// A seat outside any sector group is still not decor.
			skip[n] = true
			return
		}
		for _, ch := range n.Children {
			if ch.element == nil {
				continue
			}
			mark(ch.element)
		}
	}
	mark(root)
	return renderDecorSVG(root, nil, nil, skipKeys(skip))
}

// skipKeys flattens the skip set into the slice renderDecorSVG takes.
func skipKeys(skip map[*xmlNode]bool) []*xmlNode {
	out := make([]*xmlNode, 0, len(skip))
	for n := range skip {
		out = append(out, n)
	}
	return out
}

// sbtAttr reads an attribute in the sbt/1.0 namespace. Both the resolved
// namespace URI and the bare "sbt" prefix are accepted: encoding/xml in
// non-strict mode leaves the prefix unresolved when the document forgets
// the xmlns declaration, and such a document still round-trips through
// every real consumer.
func sbtAttr(n *xmlNode, local string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attrs {
		if a.Name.Local != local {
			continue
		}
		if isSBTNamespace(a.Name) {
			return a.Value
		}
	}
	return ""
}

func isSBTNamespace(name xml.Name) bool {
	return name.Space == SBTNamespaceURI || name.Space == "sbt"
}

// parseInt64Attr parses an integer attribute, returning 0 for anything
// unparseable — callers that care validate the result explicitly.
func parseInt64Attr(s string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// majorToMinor converts an sbt:price major-unit decimal ("900", "12.5")
// into minor units. Rounding is half-away-from-zero on the cent, which
// matches the writer's formatMajor projection for every value it can
// emit; an unparseable price yields 0 (the category simply has no tier
// price hint yet, which spec §13.2 treats as "operator must set it").
func majorToMinor(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return int64(math.Round(v * 100))
}

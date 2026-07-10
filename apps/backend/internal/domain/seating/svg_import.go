// svg_import.go implements the Bil24-authoring-convention SVG parser
// / validator (§6 of 09_autoforge/seating_backlog.md).
//
// The parser is stdlib-only (encoding/xml) and stateless: given a byte
// slice it returns a canonical Geometry, a slice of validation warnings,
// and a slice of validation errors. It never touches disk or the
// network.
package seating

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// priceCategoryGroupID is the id attribute the Bil24 Editor stamps on
// the top-level group that carries the coloured category swatches
// (§6 rule 5).
const priceCategoryGroupID = "PriceCategory"

// legendGroupID is the id attribute the Bil24 Editor stamps on the
// top-level group that carries the status-swatch legend (§6 rule 6).
const legendGroupID = "Legend"

// priceCategoryTextID is the id of the tspan-container that carries
// %price% placeholders next to the PriceCategory swatches. It is a
// child of the PriceCategory group and is captured for price hints.
const priceCategoryTextID = "PriceCategoryText"

// hexColorRE is the canonical short/long hex-colour matcher applied to
// the seat-and-category "fill:#rrggbb" style. Longform is what the
// Bil24 Editor emits; shortform is preserved defensively.
var hexColorRE = regexp.MustCompile(`(?i)#([0-9a-f]{3}|[0-9a-f]{6})`)

// sectorPrefixRE strips the leading "Сектор"/"Sector" word from a §6
// sector label ("#Сектор Parter" → "Parter"). It is case-insensitive
// per the Bil24 Editor convention.
var sectorPrefixRE = regexp.MustCompile(`(?i)^\s*(?:сектор|sector)\s+`)

// ImportSVG parses raw SVG bytes into a canonical Geometry. The
// returned Geometry is already Canonicalize'd, so its Checksum is
// stable. warnings carries §6 advisories that do not fail import;
// errs carries hard §6 violations. If errs is non-empty the returned
// Geometry MAY still be partially populated for diagnostic purposes,
// but callers MUST NOT persist it.
func ImportSVG(raw []byte) (Geometry, []ValidationError, ValidationErrors) {
	root, err := parseXMLTree(raw)
	if err != nil {
		return Geometry{}, nil, ValidationErrors{{
			Code:   ErrInvalidSVG,
			Detail: err.Error(),
		}}
	}

	var (
		warnings []ValidationError
		errs     ValidationErrors
	)

	canvas, canvasErr := parseCanvas(root)
	if canvasErr != nil {
		errs = append(errs, *canvasErr)
	}

	priceCatGroup := findGroupByID(root, priceCategoryGroupID)
	legendGroup := findGroupByID(root, legendGroupID)

	if legendGroup == nil {
		warnings = append(warnings, ValidationError{
			Code:   WarnLegendMissing,
			Detail: "Legend group (id=\"Legend\") is absent",
		})
	}

	categories, catByColor, catErrs := parseCategories(priceCatGroup)
	errs = append(errs, catErrs...)

	// Row groups: any element with inkscape:label="#..." that is NOT a
	// descendant of PriceCategory / Legend. Categories/legend swatches
	// share the "#Name" convention but must not be treated as sectors.
	rowNodes := collectRowGroups(root, priceCatGroup, legendGroup)

	sections, seatErrs := parseSections(rowNodes, catByColor)
	errs = append(errs, seatErrs...)

	decor := renderDecorSVG(root, priceCatGroup, legendGroup, rowNodes)

	g := Geometry{
		SchemaVersion: SchemaVersion,
		Canvas:        canvas,
		Categories:    categories,
		Sections:      sections,
		StandingZones: []StandingZone{},
		Tables:        []Table{},
		DecorSVG:      decor,
	}
	g = Canonicalize(g)
	return g, warnings, errs
}

// parseCanvas extracts the Canvas from the root <svg> element. It
// prefers viewBox (the modern Bil24 convention) and falls back to the
// width/height attributes. §6 rule 1 caps both dimensions at
// MaxCanvasDimension.
func parseCanvas(root *xmlNode) (Canvas, *ValidationError) {
	if root == nil {
		return Canvas{}, &ValidationError{
			Code:   ErrCanvasMissing,
			Detail: "root <svg> element is absent",
		}
	}
	var w, h float64
	if vb := attr(root, "viewBox"); vb != "" {
		parts := strings.Fields(vb)
		if len(parts) == 4 {
			if wv, err := strconv.ParseFloat(parts[2], 64); err == nil {
				w = wv
			}
			if hv, err := strconv.ParseFloat(parts[3], 64); err == nil {
				h = hv
			}
		}
	}
	if w == 0 {
		w = parseDimAttr(attr(root, "width"))
	}
	if h == 0 {
		h = parseDimAttr(attr(root, "height"))
	}
	if w == 0 || h == 0 {
		return Canvas{Width: w, Height: h}, &ValidationError{
			Code:    ErrCanvasMissing,
			Element: "svg",
			Detail:  "canvas dimensions could not be resolved",
		}
	}
	if w > MaxCanvasDimension || h > MaxCanvasDimension {
		return Canvas{Width: w, Height: h}, &ValidationError{
			Code:    ErrCanvasTooLarge,
			Element: "svg",
			Detail: fmt.Sprintf("canvas %gx%g exceeds %dx%d",
				w, h, MaxCanvasDimension, MaxCanvasDimension),
		}
	}
	return Canvas{Width: w, Height: h}, nil
}

// parseDimAttr strips a trailing unit ("px", "mm", "cm", "pt") and
// returns the numeric part as float64. Bil24 authoring writes plain
// integers, but Inkscape occasionally adds a unit — accept both.
func parseDimAttr(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// Trim any trailing non-numeric characters.
	i := len(s)
	for i > 0 {
		c := s[i-1]
		if (c >= '0' && c <= '9') || c == '.' {
			break
		}
		i--
	}
	v, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0
	}
	return v
}

// parseCategories walks the PriceCategory group's direct <circle>
// children (§6 rule 5), extracts a Category per swatch, and returns a
// colour→index lookup used by seat-to-category binding (§6 rule 7).
// PriceCategoryText tspans supply optional price hints.
func parseCategories(group *xmlNode) ([]Category, map[string]int, ValidationErrors) {
	var errs ValidationErrors
	if group == nil {
		errs = append(errs, ValidationError{
			Code:    ErrPriceCategoryMissing,
			Element: priceCategoryGroupID,
			Detail:  "group id=\"PriceCategory\" is required",
		})
		return nil, nil, errs
	}
	priceHints := collectPriceHints(group)

	var cats []Category
	colorIdx := map[string]int{}
	for _, child := range group.Children {
		if child.element == nil {
			continue
		}
		el := child.element
		if el.Name.Local != "circle" {
			continue
		}
		label := stripHash(inkscapeLabel(el))
		if label == "" {
			errs = append(errs, ValidationError{
				Code:    ErrPriceCategoryUnlabelled,
				Element: attr(el, "id"),
				Detail:  "PriceCategory circle is missing inkscape:label",
			})
			continue
		}
		color := extractFillColor(el)
		if color == "" {
			errs = append(errs, ValidationError{
				Code:    ErrPriceCategoryUnlabelled,
				Element: label,
				Detail:  "PriceCategory circle has no fill colour",
			})
			continue
		}
		idx := len(cats) + 1
		cats = append(cats, Category{
			Index:     idx,
			Name:      label,
			Color:     color,
			PriceHint: priceHints[label],
		})
		colorIdx[color] = idx
	}
	if len(cats) == 0 {
		errs = append(errs, ValidationError{
			Code:    ErrPriceCategoryEmpty,
			Element: priceCategoryGroupID,
			Detail:  "no coloured category circles found",
		})
	}
	return cats, colorIdx, errs
}

// collectPriceHints walks the PriceCategoryText subtree of the given
// PriceCategory group and returns a label→hint map keyed by the tspan's
// inkscape:label ("#First" → "590").
func collectPriceHints(group *xmlNode) map[string]string {
	out := map[string]string{}
	textNode := findByID(group, priceCategoryTextID)
	if textNode == nil {
		return out
	}
	for _, child := range textNode.Children {
		if child.element == nil {
			continue
		}
		el := child.element
		if el.Name.Local != "tspan" {
			continue
		}
		label := stripHash(inkscapeLabel(el))
		if label == "" {
			continue
		}
		hint := strings.TrimSpace(elementText(el))
		if hint == "" || hint == "%price%" {
			continue
		}
		out[label] = hint
	}
	return out
}

// collectRowGroups returns every element in root that carries an
// inkscape:label starting with "#" (§6 rule 3) and is NOT inside
// PriceCategory / Legend. Order-of-return matches document order —
// canonicalisation later re-sorts by Key for stability.
func collectRowGroups(root, price, legend *xmlNode) []*xmlNode {
	if root == nil {
		return nil
	}
	var out []*xmlNode
	var walk func(*xmlNode, bool)
	walk = func(n *xmlNode, inSpecial bool) {
		if n == nil {
			return
		}
		if n == price || n == legend {
			return
		}
		label := inkscapeLabel(n)
		if !inSpecial && n != root && strings.HasPrefix(label, "#") {
			out = append(out, n)
			// Do not descend — row groups are leaves for our purposes.
			return
		}
		for _, ch := range n.Children {
			if ch.element == nil {
				continue
			}
			walk(ch.element, inSpecial)
		}
	}
	walk(root, false)
	return out
}

// parseSections turns a slice of row-group nodes into a canonical
// []Section: rows are grouped by section-key (derived from the
// inkscape:label), seats are extracted from the circles inside each
// group. §6 rules 2/3/4/7/8 are enforced here.
func parseSections(rows []*xmlNode, catByColor map[string]int) ([]Section, ValidationErrors) {
	var errs ValidationErrors
	// Section aggregation state: sectionKey → *Section (order-preserving
	// via parallel slice).
	secByKey := map[string]*Section{}
	var secOrder []string
	// Deduplication: sectionKey + rowKey + seatNumber must be unique.
	seen := map[string]bool{}

	for _, rowNode := range rows {
		label := stripHash(inkscapeLabel(rowNode))
		if label == "" {
			errs = append(errs, ValidationError{
				Code:    ErrRowMissingSectorLabel,
				Element: attr(rowNode, "id"),
				Detail:  "row group is missing inkscape:label",
			})
			continue
		}
		secName := strings.TrimSpace(sectorPrefixRE.ReplaceAllString(label, ""))
		if secName == "" {
			secName = label
		}
		secKey := normalizeKey(secName)

		rowName := strings.TrimSpace(elementText(findDirectChild(rowNode, "title")))
		if rowName == "" {
			errs = append(errs, ValidationError{
				Code:    ErrRowMissingTitle,
				Element: attr(rowNode, "id"),
				Detail:  fmt.Sprintf("row group %q missing <title> child", label),
			})
			continue
		}
		rowKey := normalizeKey(rowName)

		var seats []Seat
		for _, ch := range rowNode.Children {
			if ch.element == nil {
				continue
			}
			el := ch.element
			if el.Name.Local == "title" {
				continue
			}
			if el.Name.Local != "circle" {
				errs = append(errs, ValidationError{
					Code:    ErrSeatNotCircle,
					Element: attr(el, "id"),
					Detail: fmt.Sprintf(
						"only <circle> may represent seats; got <%s> in row %q",
						el.Name.Local, rowName),
				})
				continue
			}
			seatNumber := strings.TrimSpace(elementText(findDirectChild(el, "title")))
			if seatNumber == "" {
				errs = append(errs, ValidationError{
					Code:    ErrSeatMissingNumber,
					Element: attr(el, "id"),
					Detail:  fmt.Sprintf("seat <circle> in row %q has no <title>", rowName),
				})
				continue
			}
			color := extractFillColor(el)
			catIdx, ok := catByColor[color]
			if !ok {
				errs = append(errs, ValidationError{
					Code:    ErrSeatColorUnmatched,
					Element: attr(el, "id"),
					Detail: fmt.Sprintf(
						"seat %q in row %q fill %q matches no PriceCategory swatch",
						seatNumber, rowName, color),
				})
				continue
			}
			dedupeKey := secKey + "|" + rowKey + "|" + seatNumber
			if seen[dedupeKey] {
				errs = append(errs, ValidationError{
					Code:    ErrDuplicateSeat,
					Element: dedupeKey,
					Detail:  "duplicate (sector,row,number) triple",
				})
				continue
			}
			seen[dedupeKey] = true

			cx := parseDimAttr(attr(el, "cx"))
			cy := parseDimAttr(attr(el, "cy"))
			r := parseDimAttr(attr(el, "r"))
			seats = append(seats, Seat{
				Key:           SeatKey(secKey, rowKey, seatNumber),
				Number:        seatNumber,
				X:             cx,
				Y:             cy,
				Radius:        r,
				CategoryIndex: catIdx,
				BarcodeHint:   nil,
			})
		}
		if len(seats) == 0 {
			errs = append(errs, ValidationError{
				Code:    ErrEmptyRow,
				Element: fmt.Sprintf("%s/%s", secName, rowName),
				Detail:  "row contains no seats",
			})
			continue
		}
		sec, ok := secByKey[secKey]
		if !ok {
			sec = &Section{Key: secKey, Name: secName}
			secByKey[secKey] = sec
			secOrder = append(secOrder, secKey)
		}
		sec.Rows = append(sec.Rows, Row{
			Key:   rowKey,
			Name:  rowName,
			Seats: seats,
		})
	}

	out := make([]Section, 0, len(secOrder))
	for _, k := range secOrder {
		sec := *secByKey[k]
		if len(sec.Rows) == 0 {
			errs = append(errs, ValidationError{
				Code:    ErrEmptySection,
				Element: sec.Name,
				Detail:  "section contains no rows",
			})
			continue
		}
		out = append(out, sec)
	}
	return out, errs
}

// renderDecorSVG re-serialises the SVG tree with the PriceCategory,
// Legend, and every row-group subtree elided. §6 rule 9 — the decor
// backdrop the client draws under live seat status.
func renderDecorSVG(root, price, legend *xmlNode, rowGroups []*xmlNode) string {
	if root == nil {
		return ""
	}
	skip := map[*xmlNode]bool{}
	if price != nil {
		skip[price] = true
	}
	if legend != nil {
		skip[legend] = true
	}
	for _, r := range rowGroups {
		skip[r] = true
	}
	var buf bytes.Buffer
	// Only emit children of root (not the <svg> wrapper itself) so the
	// result is a fragment that plugs into any SVG viewer without
	// duplicating the outer <svg>.
	for _, ch := range root.Children {
		if ch.element != nil && skip[ch.element] {
			continue
		}
		writeChild(&buf, ch, skip)
	}
	return buf.String()
}

// --- XML tree helpers -----------------------------------------------------

// xmlNode is a lightweight, order-preserving XML AST node used by the
// importer. encoding/xml's built-in Unmarshal loses interleaved text /
// element order, so we walk tokens by hand.
type xmlNode struct {
	Name     xml.Name
	Attrs    []xml.Attr
	Children []xmlChild
}

// xmlChild carries either an element pointer or a run of character data;
// exactly one of the two fields is non-empty per instance.
type xmlChild struct {
	element *xmlNode
	text    string
}

// parseXMLTree consumes raw SVG bytes and returns the root <svg> node.
// Character data outside the root is discarded (SVGs may carry an XML
// declaration and processing instructions the importer does not care
// about).
func parseXMLTree(raw []byte) (*xmlNode, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	var root *xmlNode
	stack := []*xmlNode{}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			node := &xmlNode{
				Name:  t.Name,
				Attrs: append([]xml.Attr(nil), t.Attr...),
			}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, xmlChild{element: node})
			} else if root == nil {
				root = node
			} else {
				// Second root element — treat as sibling under root for
				// robustness; SVG spec forbids this so we silently ignore.
				continue
			}
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, xmlChild{text: string(t)})
			}
		}
	}
	if root == nil {
		return nil, errors.New("no root element found")
	}
	return root, nil
}

func attr(n *xmlNode, key string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attrs {
		if a.Name.Local == key {
			return a.Value
		}
	}
	return ""
}

func inkscapeLabel(n *xmlNode) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attrs {
		if a.Name.Local == "label" &&
			(a.Name.Space == "" ||
				strings.Contains(a.Name.Space, "inkscape")) {
			return a.Value
		}
	}
	return ""
}

func stripHash(s string) string {
	s = strings.TrimSpace(s)
	return strings.TrimPrefix(s, "#")
}

func findGroupByID(n *xmlNode, id string) *xmlNode {
	return findByID(n, id)
}

func findByID(n *xmlNode, id string) *xmlNode {
	if n == nil {
		return nil
	}
	if attr(n, "id") == id {
		return n
	}
	for _, ch := range n.Children {
		if ch.element == nil {
			continue
		}
		if found := findByID(ch.element, id); found != nil {
			return found
		}
	}
	return nil
}

func findDirectChild(n *xmlNode, local string) *xmlNode {
	if n == nil {
		return nil
	}
	for _, ch := range n.Children {
		if ch.element == nil {
			continue
		}
		if ch.element.Name.Local == local {
			return ch.element
		}
	}
	return nil
}

func elementText(n *xmlNode) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	for _, ch := range n.Children {
		if ch.element != nil {
			b.WriteString(elementText(ch.element))
			continue
		}
		b.WriteString(ch.text)
	}
	return b.String()
}

// extractFillColor pulls the "fill:#rrggbb" declaration out of the
// element's style attribute, or the standalone fill="..." attribute.
// The returned value is lowercase "#rrggbb" (or "" if no fill).
func extractFillColor(n *xmlNode) string {
	if n == nil {
		return ""
	}
	if style := attr(n, "style"); style != "" {
		for _, decl := range strings.Split(style, ";") {
			kv := strings.SplitN(decl, ":", 2)
			if len(kv) != 2 {
				continue
			}
			if strings.TrimSpace(kv[0]) != "fill" {
				continue
			}
			return normalizeColor(strings.TrimSpace(kv[1]))
		}
	}
	if f := attr(n, "fill"); f != "" {
		return normalizeColor(f)
	}
	return ""
}

// normalizeColor lowercases and expands short-form hex triplets so
// "#ABC" ↔ "#aabbcc". Non-hex values (colour keywords) are passed
// through untouched — the seat-vs-swatch match will simply fail and
// surface as ErrSeatColorUnmatched.
func normalizeColor(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	m := hexColorRE.FindStringSubmatch(s)
	if m == nil {
		return s
	}
	hex := m[1]
	if len(hex) == 3 {
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	}
	return "#" + hex
}

// normalizeKey turns a display label into a stable slug used as
// Section.Key / Row.Key. Lowercased, non-alphanumerics collapsed to a
// single "-". Diacritics are preserved (the source SVGs already use
// ASCII in labels, so this is a safe minimal implementation).
func normalizeKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := b.String()
	return strings.TrimRight(out, "-")
}

// writeChild serialises an xmlChild (element or text) into buf, eliding
// any subtree rooted at a node in skip.
func writeChild(buf *bytes.Buffer, ch xmlChild, skip map[*xmlNode]bool) {
	if ch.element != nil {
		if skip[ch.element] {
			return
		}
		writeElement(buf, ch.element, skip)
		return
	}
	// Escape text so the resulting fragment is well-formed XML.
	_ = xml.EscapeText(buf, []byte(ch.text))
}

// writeElement serialises a single element (recursively) into buf, in
// canonical form: attribute order is preserved, self-closing when the
// element has no children.
func writeElement(buf *bytes.Buffer, n *xmlNode, skip map[*xmlNode]bool) {
	buf.WriteByte('<')
	buf.WriteString(qname(n.Name))
	for _, a := range n.Attrs {
		buf.WriteByte(' ')
		buf.WriteString(qname(a.Name))
		buf.WriteString(`="`)
		_ = xml.EscapeText(buf, []byte(a.Value))
		buf.WriteByte('"')
	}
	if len(n.Children) == 0 {
		buf.WriteString("/>")
		return
	}
	buf.WriteByte('>')
	for _, ch := range n.Children {
		writeChild(buf, ch, skip)
	}
	buf.WriteString("</")
	buf.WriteString(qname(n.Name))
	buf.WriteByte('>')
}

func qname(name xml.Name) string {
	if name.Space == "" {
		return name.Local
	}
	// Namespaced names in re-emitted decor_svg use the "space:local"
	// form the Bil24 client understands; the outer <svg> declares the
	// namespaces so the fragment resolves.
	if isKnownNamespace(name.Space) {
		return prefixForNamespace(name.Space) + ":" + name.Local
	}
	return name.Local
}

// isKnownNamespace / prefixForNamespace pin the two namespaces Inkscape
// emits into Bil24 SVGs. Keeping this tiny hard-coded table (rather
// than round-tripping the xmlns declarations from the input) means the
// re-serialised decor_svg is byte-stable across runs.
func isKnownNamespace(ns string) bool {
	_, ok := knownNamespacePrefixes[ns]
	return ok
}

func prefixForNamespace(ns string) string {
	return knownNamespacePrefixes[ns]
}

var knownNamespacePrefixes = map[string]string{
	"http://www.inkscape.org/namespaces/inkscape":        "inkscape",
	"http://sodipodi.sourceforge.net/DTD/sodipodi-0.dtd": "sodipodi",
	"http://www.w3.org/2000/svg":                         "svg",
}

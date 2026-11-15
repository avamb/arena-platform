package seating

import "fmt"

// svgKind is the kind of seating plan a file is, decided from its parsed
// content.
type svgKind int

const (
	// kindAuthoring: Inkscape source (row groups labelled "#Sector", seat
	// titles), or any file with no sbt data at all. Goes to ImportSVG.
	kindAuthoring svgKind = iota
	// kindWire: a plan served by Bil24 (seat circles with sbt:id and
	// sbt:state). Goes to ImportSBTSVG.
	kindWire
	// kindTemplate: a download from the editor (sbt:sect, sbt:seat and
	// sbt:cat but no sbt:id / sbt:state). Adapted, then ImportSBTSVG.
	kindTemplate
	// kindMixed: authoring row groups plus sbt:seat circles outside them.
	kindMixed
)

// classifySVG decides the dialect of a parsed plan.
//
//  1. WIRE: at least one <circle> has both sbt:id and sbt:state.
//  2. Not wire and it has authoring row groups (inkscape:label "#...") ->
//     authoring. Stale sbt:* attributes left from an old editor download are
//     ignored, titles and labels are the truth. But if some circle with
//     sbt:seat sits outside those row groups, the file is a half-converted
//     working copy: mixed (seatsIn / seatsOut give the counts).
//  3. Not wire, no row groups, has sbt:sect and sbt:seat circles -> template.
//  4. Everything else -> authoring.
func classifySVG(root *xmlNode) (d svgKind, seatsIn, seatsOut int) {
	price := findGroupByID(root, priceCategoryGroupID)
	legend := findGroupByID(root, legendGroupID)
	var hasWire, hasSect, hasSeat bool
	var walk func(n *xmlNode)
	walk = func(n *xmlNode) {
		if isSBTSeat(n) {
			hasWire = true
		}
		if sbtAttr(n, "sect") != "" {
			hasSect = true
		}
		if n.Name.Local == "circle" && sbtAttr(n, "seat") != "" {
			hasSeat = true
		}
		for _, ch := range n.Children {
			if ch.element != nil {
				walk(ch.element)
			}
		}
	}
	walk(root)
	if hasWire {
		return kindWire, 0, 0
	}

	rows := collectRowGroups(root, price, legend)
	if len(rows) > 0 {
		// Seats ImportSVG reads are the direct circle children of a row group.
		read := map[*xmlNode]bool{}
		for _, r := range rows {
			for _, ch := range r.Children {
				if ch.element != nil && ch.element.Name.Local == "circle" {
					read[ch.element] = true
				}
			}
		}
		var count func(n *xmlNode)
		count = func(n *xmlNode) {
			// ImportSVG never reads seats in these places.
			if n == price || n == legend || isGALabel(inkscapeLabel(n)) {
				return
			}
			if n.Name.Local == "circle" && sbtAttr(n, "seat") != "" {
				if read[n] {
					seatsIn++
				} else {
					seatsOut++
				}
			}
			for _, ch := range n.Children {
				if ch.element != nil {
					count(ch.element)
				}
			}
		}
		count(root)
		if seatsOut > 0 {
			return kindMixed, seatsIn, seatsOut
		}
		return kindAuthoring, seatsIn, 0
	}
	if hasSect && hasSeat {
		return kindTemplate, 0, 0
	}
	return kindAuthoring, 0, 0
}

// IsSBTSVG reports whether ImportAnySVG reads raw through the sbt importer:
// a wire plan (circles with sbt:id and sbt:state) or an editor template
// (sbt:sect and sbt:seat circles, no row groups). A file with authoring row
// groups is NOT sbt, even if it carries stale sbt:* attributes.
func IsSBTSVG(raw []byte) bool {
	root, err := parseXMLTree(raw)
	if err != nil {
		return false
	}
	d, _, _ := classifySVG(root)
	return d == kindWire || d == kindTemplate
}

// ImportAnySVG imports raw with the importer matching its dialect (see
// classifySVG). A half-converted file is refused with ErrMixedDialect and
// nothing is imported. An editor template gets no upstream facts: every seat
// and category ExternalID is 0 and no price is invented.
func ImportAnySVG(raw []byte) (Geometry, []ValidationError, ValidationErrors) {
	root, err := parseXMLTree(raw)
	if err != nil {
		return Geometry{}, nil, ValidationErrors{{Code: ErrInvalidSVG, Detail: err.Error()}}
	}
	d, in, out := classifySVG(root)
	switch d {
	case kindWire:
		plan, warnings, errs := ImportSBTSVG(raw)
		return plan.Geometry, warnings, errs
	case kindTemplate:
		adapted, ok := adaptTemplate(raw, true)
		if !ok {
			return ImportSVG(raw)
		}
		plan, warnings, errs := ImportSBTSVG(adapted)
		errs = collapseCategoryMissing(errs)
		g := plan.Geometry
		// The ids and names the adapter made up are not upstream facts.
		for i := range g.Categories {
			g.Categories[i].ExternalID = 0
		}
		for si := range g.Sections {
			for ri := range g.Sections[si].Rows {
				seats := g.Sections[si].Rows[ri].Seats
				for k := range seats {
					seats[k].ExternalID = 0
				}
			}
		}
		return g, warnings, errs
	case kindMixed:
		return Geometry{}, nil, ValidationErrors{{
			Code: ErrMixedDialect,
			Detail: fmt.Sprintf("half-converted plan: %d seats with sbt:seat are in row groups, %d are outside them",
				in, out),
		}}
	}
	return ImportSVG(raw)
}

// collapseCategoryMissing replaces the per-seat "no price category" errors of
// a template with one error that carries the number of such seats.
func collapseCategoryMissing(errs ValidationErrors) ValidationErrors {
	n := 0
	out := ValidationErrors{}
	for _, e := range errs {
		if e.Code == ErrSBTSeatCategoryMissing {
			n++
			continue
		}
		out = append(out, e)
	}
	if n == 0 {
		return errs
	}
	return append(out, ValidationError{
		Code:   ErrSBTSeatCategoryMissing,
		Detail: fmt.Sprintf("%d seats have no price category (sbt:cat missing or unknown)", n),
	})
}

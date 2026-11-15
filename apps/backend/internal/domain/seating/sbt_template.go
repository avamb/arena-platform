package seating

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// The Forzum / Bil24 editor saves a TEMPLATE: seats carry sbt:seat and sbt:cat
// but no sbt:id and no sbt:state, and categories may have no name and no price.
// ImportSBTSVG needs those, so AdaptTemplate makes a copy with them added.

var ordinalNames = []string{"First", "Second", "Third", "Fourth", "Fifth", "Sixth", "Seventh", "Eighth",
	"Ninth", "Tenth", "Eleventh", "Twelfth", "Thirteenth", "Fourteenth", "Fifteenth"}

var (
	seatPrefixRE  = regexp.MustCompile(`\s([\w.-]+):seat\s*=`)
	indexPrefixRE = regexp.MustCompile(`\s([\w.-]+):index\s*=`)
)

// isTemplatePlan reports whether root is an editor template: a group with
// sbt:sect, circles with sbt:seat, and no circle with both sbt:id and
// sbt:state.
func isTemplatePlan(root *xmlNode) bool {
	hasSect, hasSeat, hasBoth := false, false, false
	var walk func(n *xmlNode)
	walk = func(n *xmlNode) {
		if strings.TrimSpace(sbtAttr(n, "sect")) != "" {
			hasSect = true
		}
		if n.Name.Local == "circle" {
			if sbtAttr(n, "seat") != "" {
				hasSeat = true
			}
			if sbtAttr(n, "id") != "" && sbtAttr(n, "state") != "" {
				hasBoth = true
			}
		}
		for _, ch := range n.Children {
			if ch.element != nil {
				walk(ch.element)
			}
		}
	}
	walk(root)
	return hasSect && hasSeat && !hasBoth
}

type tagInsertion struct {
	pos  int
	text string
}

// AdaptTemplate returns a copy of an sbt editor template with the attributes
// ImportSBTSVG needs inserted into the start tags: a running sbt:id and
// sbt:state="1" on every seat, an ordinal sbt:name and sbt:price="0" on a
// category that has none. Every other byte is kept. The second result is
// false when source is not a template. The ids are made up: a caller that
// stores the result must not treat them as upstream ids (ImportAnySVG zeroes
// them).
func AdaptTemplate(source []byte) ([]byte, bool) {
	return adaptTemplate(source, false)
}

// adaptTemplate is AdaptTemplate. With forImport set it invents less: a
// category without a name gets "Category <index>" and no price is added, so a
// made-up "0" price never looks like a real one.
func adaptTemplate(source []byte, forImport bool) ([]byte, bool) {
	root, err := parseXMLTree(source)
	if err != nil || !isTemplatePlan(root) {
		return nil, false
	}
	dec := xml.NewDecoder(bytes.NewReader(source))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	isSBT := func(a xml.Attr, local string) bool {
		return a.Name.Local == local && (a.Name.Space == SBTNamespaceURI || a.Name.Space == "sbt")
	}
	var edits []tagInsertion
	var stack []bool
	sectDepth, nextID := 0, 1
	for {
		start := int(dec.InputOffset())
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, false
		}
		end := int(dec.InputOffset())
		switch t := tok.(type) {
		case xml.StartElement:
			own := false
			has := map[string]bool{}
			for _, a := range t.Attr {
				if isSBT(a, "sect") && strings.TrimSpace(a.Value) != "" {
					own = true
				}
				for _, l := range []string{"seat", "cat", "name", "price", "id", "state"} {
					if isSBT(a, l) {
						has[l] = true
					}
				}
			}
			if end > len(source) || start < 0 || start >= end {
				return nil, false
			}
			tag := source[start:end]
			nameEnd := bytes.IndexAny(tag, " \t\r\n/>")
			if nameEnd < 0 {
				return nil, false
			}
			switch {
			// For an import a seat without sbt:cat is adapted too, so the importer
			// reports it as "no price category" and not as "no seats".
			case t.Name.Local == "circle" && sectDepth > 0 && has["seat"] && (has["cat"] || forImport):
				prefix := "sbt"
				if m := seatPrefixRE.FindSubmatch(tag); m != nil {
					prefix = string(m[1])
				}
				text := ""
				if !has["id"] {
					text += fmt.Sprintf(` %s:id="%d"`, prefix, nextID)
				}
				if !has["state"] {
					text += fmt.Sprintf(` %s:state="1"`, prefix)
				}
				nextID++
				if text != "" {
					edits = append(edits, tagInsertion{start + nameEnd, text})
				}
			case t.Name.Local == "category" && (t.Name.Space == SBTNamespaceURI || t.Name.Space == "sbt"):
				prefix := "sbt"
				if m := indexPrefixRE.FindSubmatch(tag); m != nil {
					prefix = string(m[1])
				}
				idx := 0
				for _, a := range t.Attr {
					if isSBT(a, "index") {
						idx, _ = strconv.Atoi(strings.TrimSpace(a.Value))
					}
				}
				text := ""
				if !has["name"] {
					if forImport && idx >= 1 {
						text += fmt.Sprintf(` %s:name="Category %d"`, prefix, idx)
					} else if idx >= 1 && idx <= len(ordinalNames) {
						text += fmt.Sprintf(` %s:name="%s"`, prefix, ordinalNames[idx-1])
					}
				}
				if !has["price"] && !forImport {
					text += fmt.Sprintf(` %s:price="0"`, prefix)
				}
				if text != "" {
					edits = append(edits, tagInsertion{start + nameEnd, text})
				}
			}
			if own {
				sectDepth++
			}
			stack = append(stack, own)
		case xml.EndElement:
			if len(stack) > 0 {
				if stack[len(stack)-1] {
					sectDepth--
				}
				stack = stack[:len(stack)-1]
			}
		}
	}
	var out bytes.Buffer
	last := 0
	for _, e := range edits {
		out.Write(source[last:e.pos])
		out.WriteString(e.text)
		last = e.pos
	}
	out.Write(source[last:])
	return out.Bytes(), true
}

package seating

import (
	"bytes"
	"encoding/xml"
	"strings"
	"unicode"
)

// The decor of a plan is drawing only: shapes, text, gradients, styles and
// embedded raster pictures. The writer of decor_svg (writeElement) leaves out
// every element and attribute that runs code or loads another document, so
// the stored decor and every answer built from it hold drawing only:
//
//   - the elements script, foreignObject, iframe, embed, object, handler,
//     set and every animate* element, with their whole subtree;
//   - every event attribute (on*);
//   - an href (xlink:href is written as href) that is not a reference inside
//     the document ("#id"), except a data:image/png|jpeg|gif|webp picture on
//     <image>;
//   - any attribute whose value holds "javascript:" once whitespace is
//     removed, in any letter case.
//
// A plan without such content is written byte-identically, so the geometry
// checksum of an existing plan does not move.

// decorDroppedElements are the lower-case local names left out of decor.
var decorDroppedElements = map[string]bool{
	"script":        true,
	"foreignobject": true,
	"iframe":        true,
	"embed":         true,
	"object":        true,
	"handler":       true,
	"set":           true,
}

// decorDataImagePrefixes are the picture types allowed in an href of <image>.
var decorDataImagePrefixes = []string{
	"data:image/png;", "data:image/png,",
	"data:image/jpeg;", "data:image/jpeg,",
	"data:image/gif;", "data:image/gif,",
	"data:image/webp;", "data:image/webp,",
}

// decorElementDropped reports whether n and its subtree are left out of decor.
func decorElementDropped(n *xmlNode) bool {
	local := strings.ToLower(n.Name.Local)
	return decorDroppedElements[local] || strings.HasPrefix(local, "animate")
}

// decorAttrDropped reports whether attribute a of element n is left out of
// decor.
func decorAttrDropped(n *xmlNode, a xml.Attr) bool {
	local := strings.ToLower(a.Name.Local)
	if strings.HasPrefix(local, "on") {
		return true
	}
	compact := strings.ToLower(strings.Map(func(r rune) rune {
		if r <= ' ' || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, a.Value))
	if strings.Contains(compact, "javascript:") {
		return true
	}
	if local == "href" {
		if strings.HasPrefix(compact, "#") {
			return false
		}
		if strings.ToLower(n.Name.Local) == "image" {
			for _, p := range decorDataImagePrefixes {
				if strings.HasPrefix(compact, p) {
					return false
				}
			}
		}
		return true
	}
	return false
}

// decorFragmentRoot wraps a stored decor fragment so it parses as one
// document. The known prefixes are resolved by resolveKnownPrefixes, so the
// wrapper declares nothing.
const (
	decorFragmentOpen  = "<svg>"
	decorFragmentClose = "</svg>"
)

// SanitizeDecorSVG reads a stored decor_svg fragment again and writes it
// through the decor writer, so decor stored before the writer left out
// code and outside references is served without them. A fragment the
// writer produced is returned unchanged. A fragment that does not parse
// gives an empty decor.
func SanitizeDecorSVG(fragment string) string {
	if strings.TrimSpace(fragment) == "" {
		return fragment
	}
	root, err := parseXMLTreeTokens([]byte(decorFragmentOpen+fragment+decorFragmentClose), true)
	if err != nil || root == nil {
		return ""
	}
	var buf bytes.Buffer
	for _, ch := range root.Children {
		writeChild(&buf, ch, nil)
	}
	return buf.String()
}

// resolveKnownPrefixes maps the prefixes the decor writer emits (svg,
// inkscape, sodipodi) back to their namespaces, so a name read by
// RawToken is written again exactly as before. Any other prefix stays as
// the namespace and is dropped by qname, as on import.
func resolveKnownPrefixes(se xml.StartElement) xml.StartElement {
	se.Name = resolveKnownPrefix(se.Name)
	attrs := make([]xml.Attr, len(se.Attr))
	for i, a := range se.Attr {
		a.Name = resolveKnownPrefix(a.Name)
		attrs[i] = a
	}
	se.Attr = attrs
	return se
}

func resolveKnownPrefix(name xml.Name) xml.Name {
	if name.Space == "" {
		return name
	}
	for ns, prefix := range knownNamespacePrefixes {
		if prefix == name.Space {
			name.Space = ns
			return name
		}
	}
	return name
}

package seating

import (
	"bytes"
	"encoding/xml"
	"strings"
	"unicode"
)

// The decor of a plan is drawing only. The writer of decor_svg
// (writeElement) keeps an ALLOWLIST and leaves everything else out, so the
// stored decor and every answer built from it hold drawing only:
//
//   - elements: the SVG drawing elements of decorAllowedElements, in the SVG
//     namespace or with no namespace, plus any element of the Inkscape and
//     Sodipodi namespaces (editor metadata the browser does not render). Any
//     other element, an HTML one included, is left out with its subtree. A
//     <style> element is left out too: no plan uses one;
//   - attributes: the names of decorAllowedAttrs, plus any attribute of the
//     Inkscape and Sodipodi namespaces. An event attribute (on*) is never
//     kept;
//   - href (xlink:href is written as href) only on the elements of
//     decorHrefElements and only as a reference inside the document
//     ("#id"), or as a data:image/png|jpeg|gif|webp picture on <image>;
//   - an attribute whose value holds "javascript:" once whitespace is
//     removed (any letter case), a url() that is not url(#id), "@import" or
//     a backslash is left out.
//
// The lists cover every element and attribute the decor of the repo
// fixtures uses, so a plan without other content is written
// byte-identically and its geometry checksum does not move.

// decorAllowedElements are the SVG elements kept in decor (exact case, SVG
// names are case-sensitive).
var decorAllowedElements = setOf(
	"svg", "g", "defs", "symbol", "use", "title", "desc", "metadata",
	"path", "rect", "circle", "ellipse", "line", "polyline", "polygon",
	"text", "tspan", "textPath",
	"linearGradient", "radialGradient", "stop", "pattern",
	"clipPath", "mask", "marker", "image",
)

// decorAllowedAttrs are the attribute names kept in decor, by local name.
// "space" is xml:space and the last five are attributes of
// sodipodi:namedview that Inkscape writes without a prefix.
var decorAllowedAttrs = setOf(
	"id", "class", "style", "transform", "lang", "space",
	"d", "x", "y", "x1", "y1", "x2", "y2", "cx", "cy", "r", "rx", "ry",
	"width", "height", "points", "pathLength", "viewBox", "preserveAspectRatio",
	"fill", "fill-opacity", "fill-rule", "stroke", "stroke-width", "stroke-opacity",
	"stroke-linecap", "stroke-linejoin", "stroke-miterlimit", "stroke-dasharray",
	"stroke-dashoffset", "opacity", "color", "display", "visibility", "overflow",
	"clip-path", "clip-rule", "mask", "paint-order", "vector-effect",
	"shape-rendering", "text-rendering", "image-rendering", "mix-blend-mode",
	"isolation", "transform-origin",
	"font-family", "font-size", "font-weight", "font-style", "font-variant",
	"font-stretch", "letter-spacing", "word-spacing", "text-anchor",
	"dominant-baseline", "alignment-baseline", "baseline-shift", "text-decoration",
	"writing-mode", "direction", "dx", "dy", "rotate", "textLength", "lengthAdjust",
	"startOffset", "offset", "stop-color", "stop-opacity",
	"gradientUnits", "gradientTransform", "spreadMethod", "fx", "fy", "fr",
	"patternUnits", "patternContentUnits", "patternTransform",
	"clipPathUnits", "maskUnits", "maskContentUnits",
	"markerWidth", "markerHeight", "markerUnits", "refX", "refY", "orient",
	"marker-start", "marker-mid", "marker-end", "href",
	"bordercolor", "borderopacity", "pagecolor", "showgrid", "showguides",
)

// decorHrefElements are the elements that may keep an href.
var decorHrefElements = setOf("use", "image", "textPath", "linearGradient", "radialGradient", "pattern")

// decorDataImagePrefixes are the picture types allowed in an href of <image>.
var decorDataImagePrefixes = []string{
	"data:image/png;", "data:image/png,",
	"data:image/jpeg;", "data:image/jpeg,",
	"data:image/gif;", "data:image/gif,",
	"data:image/webp;", "data:image/webp,",
}

const (
	nsSVG      = "http://www.w3.org/2000/svg"
	nsInkscape = "http://www.inkscape.org/namespaces/inkscape"
	nsSodipodi = "http://sodipodi.sourceforge.net/DTD/sodipodi-0.dtd"
)

func setOf(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func isEditorNamespace(ns string) bool {
	return ns == nsInkscape || ns == nsSodipodi
}

// decorElementDropped reports whether n and its subtree are left out of decor.
func decorElementDropped(n *xmlNode) bool {
	if isEditorNamespace(n.Name.Space) {
		return false
	}
	if n.Name.Space != "" && n.Name.Space != nsSVG {
		return true
	}
	return !decorAllowedElements[n.Name.Local]
}

// decorAttrDropped reports whether attribute a of element n is left out of
// decor.
func decorAttrDropped(n *xmlNode, a xml.Attr) bool {
	local := a.Name.Local
	if strings.HasPrefix(strings.ToLower(local), "on") {
		return true
	}
	compact := strings.ToLower(strings.Map(func(r rune) rune {
		if r <= ' ' || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, a.Value))
	if strings.Contains(compact, "javascript:") || strings.Contains(compact, "@import") ||
		strings.Contains(compact, `\`) || !onlyLocalURLs(compact) {
		return true
	}
	if isEditorNamespace(a.Name.Space) {
		return false
	}
	if !decorAllowedAttrs[local] {
		return true
	}
	if local == "href" {
		if !decorHrefElements[n.Name.Local] {
			return true
		}
		if strings.HasPrefix(compact, "#") {
			return false
		}
		if n.Name.Local == "image" {
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

// onlyLocalURLs reports whether every url( in the compacted, lower-case
// value s points inside the document: url(#id), quoted or not.
func onlyLocalURLs(s string) bool {
	for {
		i := strings.Index(s, "url(")
		if i < 0 {
			return true
		}
		s = s[i+len("url("):]
		arg := strings.TrimLeft(s, `"'`)
		if !strings.HasPrefix(arg, "#") {
			return false
		}
	}
}

// decorFragmentOpen and decorFragmentClose wrap a stored decor fragment so
// it parses as one document. The known prefixes are resolved by
// resolveKnownPrefixes, so the wrapper declares nothing.
const (
	decorFragmentOpen  = "<svg>"
	decorFragmentClose = "</svg>"
)

// SanitizeDecorSVG reads a stored decor_svg fragment again and writes it
// through the decor writer, so decor stored before the allowlist is served
// with drawing only. A fragment the writer produced is returned unchanged.
// A fragment that does not parse gives an empty decor.
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
// the namespace and is left out by the allowlist, as on import.
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

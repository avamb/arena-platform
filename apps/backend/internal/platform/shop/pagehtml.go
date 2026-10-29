package shop

// pagehtml.go — the sanitizer of a static storefront page. A page is
// operator-authored like an event description, but the real shops it is
// imported from carry pictures (events, partners) and tables (a privacy
// policy), which the rich-text subset of richtext.Sanitize drops. This
// sanitizer keeps the description's tags plus img and the table family, with
// a strict attribute list per tag; everything else is unwrapped (its text
// kept) or, for script-like elements, dropped with its content.

import (
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// pageTags are the tags a page keeps; b and i are written as strong and em.
var pageTags = map[atom.Atom]string{
	atom.P: "p", atom.Br: "br", atom.Strong: "strong", atom.B: "strong", atom.Em: "em", atom.I: "em",
	atom.U: "u", atom.A: "a", atom.Ul: "ul", atom.Ol: "ol", atom.Li: "li", atom.H3: "h3", atom.H4: "h4",
	atom.Blockquote: "blockquote", atom.Img: "img",
	atom.Table: "table", atom.Thead: "thead", atom.Tbody: "tbody", atom.Tfoot: "tfoot",
	atom.Tr: "tr", atom.Th: "th", atom.Td: "td",
}

// pageDropTags are removed together with their content.
var pageDropTags = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Iframe: true, atom.Object: true, atom.Embed: true,
	atom.Svg: true, atom.Math: true, atom.Template: true, atom.Noscript: true, atom.Title: true,
	atom.Head: true, atom.Meta: true, atom.Link: true, atom.Base: true, atom.Audio: true,
	atom.Video: true, atom.Canvas: true, atom.Applet: true, atom.Frame: true, atom.Frameset: true,
	atom.Noframes: true, atom.Noembed: true, atom.Select: true, atom.Textarea: true, atom.Button: true,
	atom.Input: true,
}

// pageVoid are written without a closing tag.
var pageVoid = map[string]bool{"br": true, "img": true}

// SanitizePageBody reduces a page's HTML to the page subset (see the file
// comment). A text over MaxPageBodyLen characters after sanitizing, or over
// four times that before, answers 422 shop.invalid_body_html.
func SanitizePageBody(in string) (string, error) {
	if strings.TrimSpace(in) == "" {
		return "", nil
	}
	tooLong := invalid("shop.invalid_body_html", "body_html is limited to 200000 characters of HTML")
	if utf8.RuneCountInString(in) > 4*MaxPageBodyLen {
		return "", tooLong
	}
	body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(in), body)
	if err != nil {
		e := invalid("shop.invalid_body_html", "body_html is not readable HTML")
		return "", e
	}
	var b strings.Builder
	for _, n := range nodes {
		writePageNode(&b, n)
	}
	out := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(out) > MaxPageBodyLen {
		return "", tooLong
	}
	return out, nil
}

func writePageNode(b *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.TextNode:
		b.WriteString(html.EscapeString(stripControls(n.Data)))
		return
	case html.ElementNode:
	case html.DocumentNode:
		writePageChildren(b, n)
		return
	default: // comments, doctypes
		return
	}
	if pageDropTags[n.DataAtom] {
		return
	}
	tag, ok := pageTags[n.DataAtom]
	if !ok {
		writePageChildren(b, n) // unwrap: the text stays, the tag goes
		return
	}
	attrs, keep := pageAttrs(tag, n.Attr)
	if !keep {
		return
	}
	b.WriteString("<" + tag)
	for _, a := range attrs {
		b.WriteString(" " + a[0] + `="` + html.EscapeString(a[1]) + `"`)
	}
	b.WriteString(">")
	if pageVoid[tag] {
		return
	}
	writePageChildren(b, n)
	b.WriteString("</" + tag + ">")
}

func writePageChildren(b *strings.Builder, n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		writePageNode(b, c)
	}
}

// pageAttrs keeps the attributes a tag may carry, in a fixed order. keep is
// false for an element that cannot stand without one (an img without a safe
// src).
func pageAttrs(tag string, in []html.Attribute) (out [][2]string, keep bool) {
	get := func(name string) (string, bool) {
		for _, a := range in {
			if a.Namespace == "" && strings.EqualFold(a.Key, name) {
				return strings.TrimSpace(a.Val), true
			}
		}
		return "", false
	}
	switch tag {
	case "a":
		if href, ok := get("href"); ok && (safeHref(href, "https", "mailto") || safeRootPath(href)) {
			out = append(out, [2]string{"href", href}, [2]string{"rel", "noopener noreferrer"})
		}
	case "img":
		src, ok := get("src")
		if !ok || !safeHref(src, "https") {
			return nil, false
		}
		out = append(out, [2]string{"src", src})
		if alt, ok := get("alt"); ok {
			out = append(out, [2]string{"alt", stripControls(alt)})
		}
		for _, k := range []string{"width", "height"} {
			if v, ok := get(k); ok && smallNumber(v, 10000) {
				out = append(out, [2]string{k, v})
			}
		}
		if v, ok := get("loading"); ok && (strings.EqualFold(v, "lazy") || strings.EqualFold(v, "eager")) {
			out = append(out, [2]string{"loading", strings.ToLower(v)})
		}
	case "td", "th":
		for _, k := range []string{"colspan", "rowspan"} {
			if v, ok := get(k); ok && smallNumber(v, 100) {
				out = append(out, [2]string{k, v})
			}
		}
	}
	return out, true
}

// safeHref accepts an absolute URL of one of the schemes (https with a host,
// mailto with an address); never data:, javascript: or a relative path.
func safeHref(raw string, schemes ...string) bool {
	if raw == "" || strings.ContainsAny(raw, " \t\r\n\x00") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	for _, s := range schemes {
		if !strings.EqualFold(u.Scheme, s) {
			continue
		}
		if s == "https" {
			return u.Host != ""
		}
		return u.Opaque != "" || u.Path != ""
	}
	return false
}

// safeRootPath accepts a link to another page of the same storefront: a path
// that starts with ONE slash ("/pages/contact", "/cart?x=1#top"). The theme's
// own buttons are written that way (a Shopify page body links "/pages/<handle>")
// and the storefront prefixes such a link with its own base (the organization
// segment on a platform host), so dropping it left a button that went nowhere.
// A protocol-relative address ("//host/x"), a backslash form and anything with
// whitespace or control characters stay refused.
func safeRootPath(raw string) bool {
	if len(raw) < 2 || raw[0] != '/' || raw[1] == '/' || raw[1] == '\\' {
		return false
	}
	for _, r := range raw {
		if r <= ' ' || r == '\\' || r == 0x7f {
			return false
		}
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "" && u.Host == "" && u.Opaque == ""
}

func smallNumber(v string, maxValue int) bool {
	n, err := strconv.Atoi(v)
	return err == nil && n >= 1 && n <= maxValue && strconv.Itoa(n) == v
}

// stripControls drops control, bidi and zero-width characters (the rich-text
// policy), keeping tabs and newlines.
func stripControls(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || (r >= 0x7f && r < 0xa0):
			return -1
		case r >= 0x200b && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0xfeff:
			return -1
		}
		return r
	}, s)
}

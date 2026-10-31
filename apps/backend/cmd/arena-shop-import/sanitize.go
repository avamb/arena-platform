package main

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// cleanOptions tune CleanHTML.
type cleanOptions struct {
	// BaseURL absolutizes relative links and pictures ("https://www.shop.com").
	// Protocol-relative addresses ("//cdn.shop.com/x.jpg") are always made https.
	BaseURL string
	// DropClasses removes an element whose class attribute contains any of
	// these words, with everything inside it (theme chrome such as banners).
	DropClasses []string
	// KeepImages false drops every <img>.
	KeepImages bool
}

// dropTags are removed together with their content.
var dropTags = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Link: true, atom.Meta: true, atom.Svg: true,
	atom.Noscript: true, atom.Form: true, atom.Button: true, atom.Input: true, atom.Select: true,
	atom.Textarea: true, atom.Iframe: true, atom.Template: true, atom.Head: true, atom.Object: true,
	atom.Embed: true, atom.Source: true, atom.Video: true, atom.Audio: true, atom.Canvas: true,
	atom.Dialog: true, atom.Nav: true, atom.Header: true, atom.Footer: true, atom.Label: true,
}

// keepTags are written out (attributes are rebuilt, never copied). Any other
// element is unwrapped: its text and children stay, its tag and attributes go.
var keepTags = map[atom.Atom]string{
	atom.P: "p", atom.Br: "br", atom.Hr: "hr",
	atom.H2: "h2", atom.H3: "h3", atom.H4: "h4", atom.H5: "h5", atom.H6: "h6",
	atom.Ul: "ul", atom.Ol: "ol", atom.Li: "li",
	atom.Strong: "strong", atom.B: "strong", atom.Em: "em", atom.I: "em", atom.U: "u",
	atom.Blockquote: "blockquote", atom.A: "a", atom.Img: "img",
	atom.Table: "table", atom.Thead: "thead", atom.Tbody: "tbody", atom.Tr: "tr", atom.Th: "th", atom.Td: "td",
}

// h1 is demoted: the storefront renders the product or page title as the
// page's one h1.
const demotedHeading = "h2"

// CleanHTML turns foreign HTML (a Shopify description, a rendered theme
// section) into the small, attribute-free subset the shop stores. It never
// returns script, style, event handlers, inline styles or classes.
func CleanHTML(src string, opt cleanOptions) string {
	ctx := &html.Node{Type: html.ElementNode, DataAtom: atom.Body, Data: "body"}
	nodes, err := html.ParseFragment(strings.NewReader(src), ctx)
	if err != nil {
		return strings.TrimSpace(html.EscapeString(src))
	}
	return cleanNodes(nodes, opt)
}

// cleanNodes renders already parsed nodes through the same filter.
func cleanNodes(nodes []*html.Node, opt cleanOptions) string {
	var b strings.Builder
	for _, n := range nodes {
		renderNode(&b, n, opt, 0)
	}
	return tidy(b.String())
}

func hasDropClass(n *html.Node, opt cleanOptions) bool {
	if len(opt.DropClasses) == 0 {
		return false
	}
	for _, a := range n.Attr {
		if a.Key != "class" {
			continue
		}
		for _, c := range strings.Fields(a.Val) {
			for _, d := range opt.DropClasses {
				if c == d {
					return true
				}
			}
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func renderNode(b *strings.Builder, n *html.Node, opt cleanOptions, depth int) {
	if depth > 100 {
		return
	}
	switch n.Type {
	case html.TextNode:
		b.WriteString(html.EscapeString(collapseSpace(n.Data)))
	case html.ElementNode:
		if dropTags[n.DataAtom] || hasDropClass(n, opt) {
			return
		}
		if n.DataAtom == atom.H1 {
			writeElement(b, n, demotedHeading, opt, depth)
			return
		}
		name, keep := keepTags[n.DataAtom]
		if !keep {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(b, c, opt, depth+1)
			}
			return
		}
		writeElement(b, n, name, opt, depth)
	case html.DocumentNode:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			renderNode(b, c, opt, depth+1)
		}
	}
}

func writeElement(b *strings.Builder, n *html.Node, name string, opt cleanOptions, depth int) {
	switch name {
	case "br", "hr":
		b.WriteString("<" + name + ">")
		return
	case "img":
		if !opt.KeepImages {
			return
		}
		src := resolveURL(attr(n, "src"), opt.BaseURL, false)
		if src == "" {
			return
		}
		fmt.Fprintf(b, `<img src="%s" alt="%s">`, html.EscapeString(src), html.EscapeString(strings.TrimSpace(attr(n, "alt"))))
		return
	}
	var inner strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		renderNode(&inner, c, opt, depth+1)
	}
	content := inner.String()
	switch name {
	case "a":
		href := resolveURL(attr(n, "href"), opt.BaseURL, true)
		if href == "" {
			b.WriteString(content)
			return
		}
		fmt.Fprintf(b, `<a href="%s" rel="noopener">%s</a>`, html.EscapeString(href), content)
		return
	case "p", "h2", "h3", "h4", "h5", "h6", "li", "ul", "ol", "blockquote", "strong", "em", "u":
		if isBlank(content) {
			return // an empty paragraph or heading carries nothing
		}
	}
	b.WriteString("<" + name + ">" + content + "</" + name + ">")
}

// resolveURL accepts http(s), mailto and tel (links only) and relative paths;
// anything else (javascript:, data:) is refused with "".
func resolveURL(raw, base string, link bool) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "#") {
		return ""
	}
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.String()
	case "mailto", "tel":
		if link {
			return u.String()
		}
		return ""
	case "":
		if base == "" {
			return u.String()
		}
		b, err := url.Parse(base)
		if err != nil {
			return ""
		}
		return b.ResolveReference(u).String()
	}
	return ""
}

func collapseSpace(s string) string {
	var out strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) || r == ' ' {
			space = true
			continue
		}
		if space {
			out.WriteByte(' ')
			space = false
		}
		out.WriteRune(r)
	}
	if space {
		out.WriteByte(' ')
	}
	return out.String()
}

// isBlank reports content with no visible text and no picture.
func isBlank(content string) bool {
	if strings.Contains(content, "<img") {
		return false
	}
	var text strings.Builder
	inTag := false
	for _, r := range content {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag && !unicode.IsSpace(r):
			text.WriteRune(r)
		}
	}
	return text.String() == ""
}

// emptySection is a heading that is followed by nothing but links before the
// next heading or the end: what is left of a theme section whose product
// grid was dropped. At least one link must follow, so a heading that ends
// the text is kept.
var emptySection = regexp.MustCompile(`<h[2-6]>[^<]*</h[2-6]>(?:\s*<a [^>]*>[^<]*</a>)+\s*(<h[2-6]>|$)`)

// emptyHeadings match a heading directly followed by another of the same
// level (the first one headed a grid that was dropped).
var emptyHeadings = func() []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, l := range []string{"2", "3", "4", "5", "6"} {
		out = append(out, regexp.MustCompile(`<h`+l+`>[^<]*</h`+l+`>\s*(<h`+l+`>)`))
	}
	return out
}()

var blockSpace = regexp.MustCompile(`\s*(</?(?:p|ul|ol|li|h[2-6]|blockquote|table|thead|tbody|tr|th|td|hr)>)\s*`)

// tidy trims the result, removes the whitespace around block tags and drops
// leading/trailing and tripled <br>.
func tidy(s string) string {
	s = blockSpace.ReplaceAllString(strings.TrimSpace(s), "$1")
	for prev := ""; prev != s; {
		prev = s
		s = emptySection.ReplaceAllString(s, "$1")
		for _, re := range emptyHeadings {
			s = re.ReplaceAllString(s, "$1")
		}
	}
	for strings.Contains(s, "<br><br><br>") {
		s = strings.ReplaceAll(s, "<br><br><br>", "<br><br>")
	}
	for strings.HasPrefix(s, "<br>") {
		s = strings.TrimSpace(strings.TrimPrefix(s, "<br>"))
	}
	for strings.HasSuffix(s, "<br>") {
		s = strings.TrimSpace(strings.TrimSuffix(s, "<br>"))
	}
	return s
}

// PlainText returns the visible text of an HTML fragment (for titles and
// reports).
func PlainText(src string) string {
	nodes, err := html.ParseFragment(strings.NewReader(src), &html.Node{Type: html.ElementNode, DataAtom: atom.Body, Data: "body"})
	if err != nil {
		return strings.TrimSpace(src)
	}
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && (dropTags[n.DataAtom]) {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	return strings.TrimSpace(collapseSpace(b.String()))
}

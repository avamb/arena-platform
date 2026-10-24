package richtext

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// isStructureTag says whether a tag shows that the author laid the text out
// with markup: the block tags of the white list and of the sanitizer's
// boundary set, the headings and br. A text that has one keeps its layout.
func isStructureTag(a atom.Atom) bool {
	switch a {
	case atom.Br, atom.P, atom.Ul, atom.Ol, atom.Li, atom.Blockquote,
		atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		return true
	}
	return boundaryTags[a]
}

// isKnownTag says whether a tag name belongs to the white list (with the
// aliases b and i) or is div, span or br: a name that shows the text was
// written as HTML.
func isKnownTag(a atom.Atom) bool {
	switch a {
	case atom.Div, atom.Span, atom.Br, atom.B, atom.I,
		atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		return true
	}
	return ProfileDescription.has(a.String())
}

// scanTags walks the start and end tags of s with the HTML tokenizer (so
// comments, raw text elements and "a < b" are not tags) and stops at the first
// tag for which stop returns true.
func scanTags(s string, stop func(atom.Atom) bool) bool {
	z := html.NewTokenizer(strings.NewReader(s))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return false
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			if stop(atom.Lookup(name)) {
				return true
			}
		}
	}
}

// LooksLikeHTML reports whether text holds at least one start or end tag that
// is on the description's white list (b, i and the headings h1 to h6 included)
// or is div, span or br.
// It is the test the Bil24 gateway uses for a row still marked "text" that an
// HTML-sending site wrote: such a text is shown as ART. A "<" that is not a
// tag ("a < b") does not count.
func LooksLikeHTML(text string) bool {
	if !strings.Contains(text, "<") {
		return false
	}
	return scanTags(text, isKnownTag)
}

// blankRunRE is a blank line (or several): two newlines with only blanks
// between them, together with the blanks around them.
var blankRunRE = regexp.MustCompile(`(?:[ \t\f]*\n){2,}[ \t\f]*`)

// lineBreakRE is a single newline with the blanks around it.
var lineBreakRE = regexp.MustCompile(`[ \t\f]*\n[ \t\f]*`)

// AutoParagraph gives the layout of WordPress's wpautop to an HTML text that
// has none: WordPress stores post_content with paragraphs separated by blank
// lines and no <p>, and adds the tags only when it shows the text. If html
// holds no block tag (p, ul, ol, li, blockquote, headings, div and the other
// block elements the sanitizer knows) and no <br>, a blank line becomes a
// paragraph boundary (<p>...</p>) and a single newline becomes <br>; the tags
// the text has (strong, a ...) are kept as they are. Otherwise, and for a text
// that has no newline, html is returned unchanged. The result is still
// untrusted HTML: Sanitize decides what is kept.
func AutoParagraph(htmlText string) string {
	if !strings.Contains(htmlText, "\n") && !strings.Contains(htmlText, "\r") {
		return htmlText
	}
	if scanTags(htmlText, isStructureTag) {
		return htmlText
	}
	src := strings.ReplaceAll(htmlText, "\r\n", "\n")
	src = strings.ReplaceAll(src, "\r", "\n")
	src = strings.TrimSpace(src)
	if src == "" || !strings.Contains(src, "\n") {
		return htmlText
	}
	var sb strings.Builder
	sb.WriteString("<p>")
	z := html.NewTokenizer(strings.NewReader(src))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		raw := string(z.Raw())
		if tt != html.TextToken {
			sb.WriteString(raw) // tags and comments keep their bytes
			continue
		}
		// A blank line ends the paragraph, a newline is a line break. The
		// blank runs are split first so that their newlines are not seen
		// as single ones.
		for i, part := range blankRunRE.Split(raw, -1) {
			if i > 0 {
				sb.WriteString("</p><p>")
			}
			sb.WriteString(lineBreakRE.ReplaceAllString(part, "<br>"))
		}
	}
	sb.WriteString("</p>")
	return sb.String()
}

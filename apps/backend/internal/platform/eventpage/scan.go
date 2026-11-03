package eventpage

import (
	"bytes"
	"strings"

	"golang.org/x/net/html"
)

// scanned is what one pass over a page's HTML collects.
type scanned struct {
	lang        string
	title       string
	meta        map[string]string // og:title, og:description, og:image, description, twitter:image…
	jsonLD      []string          // the bodies of <script type="application/ld+json">
	visibleText string
}

// maxJSONLDBlocks bounds how many JSON-LD blocks are read from one page.
const maxJSONLDBlocks = 20

// maxVisibleRunes is how much visible text is kept for the model.
const maxVisibleRunes = 20_000

// skipText are elements whose text a reader never sees as page content.
var skipText = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true, "svg": true,
	"head": true, "nav": true, "footer": true, "iframe": true, "canvas": true,
	"select": true,
}

// A form (ASP.NET wraps the whole page in one), a button and an option
// outside a select are not skipped: their end tag may never come, and the
// rest of the page would be lost.

// blockElements end a line of visible text.
var blockElements = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true, "h1": true, "h2": true,
	"h3": true, "h4": true, "h5": true, "h6": true, "section": true, "article": true,
	"header": true, "main": true, "table": true, "ul": true, "ol": true, "dl": true,
	"dt": true, "dd": true, "blockquote": true, "pre": true, "hr": true, "figure": true,
	"figcaption": true, "address": true, "time": false,
}

// scanHTML tokenizes the page once. It never fails: broken HTML yields
// whatever could be read.
func scanHTML(doc []byte) scanned {
	out := scanned{meta: map[string]string{}}
	z := html.NewTokenizer(bytes.NewReader(doc))
	var text strings.Builder
	textRunes := 0
	skipDepth := 0 // >0 while inside a skipped element
	var skipTag string
	inTitle := false
	inJSONLD := false
	var jsonBuf strings.Builder
	newline := func() {
		if text.Len() > 0 && !strings.HasSuffix(text.String(), "\n") {
			text.WriteByte('\n')
		}
	}
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			out.visibleText = tidyText(text.String())
			return out
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)
			attrs := map[string]string{}
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				attrs[string(k)] = string(v)
			}
			switch tag {
			case "body":
				// The end of <head> may be left out (legal HTML5): <body> ends it.
				if skipTag == "head" {
					skipDepth = 0
				}
			case "html":
				if l := strings.TrimSpace(attrs["lang"]); l != "" && out.lang == "" {
					out.lang = l
				}
			case "title":
				if tt == html.StartTagToken {
					inTitle = true
				}
			case "meta":
				key := strings.ToLower(strings.TrimSpace(attrs["property"]))
				if key == "" {
					key = strings.ToLower(strings.TrimSpace(attrs["name"]))
				}
				if key != "" {
					if _, seen := out.meta[key]; !seen {
						out.meta[key] = strings.TrimSpace(attrs["content"])
					}
				}
			case "script":
				typ := strings.ToLower(strings.TrimSpace(attrs["type"]))
				if typ == "application/ld+json" && tt == html.StartTagToken && len(out.jsonLD) < maxJSONLDBlocks {
					inJSONLD = true
					jsonBuf.Reset()
				}
			}
			if tt == html.StartTagToken && skipText[tag] {
				if skipDepth == 0 {
					skipTag = tag
				}
				if tag == skipTag {
					skipDepth++
				}
				continue
			}
			if blockElements[tag] && skipDepth == 0 {
				newline()
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)
			switch tag {
			case "title":
				inTitle = false
			case "script":
				if inJSONLD {
					out.jsonLD = append(out.jsonLD, jsonBuf.String())
					inJSONLD = false
				}
			}
			if skipDepth > 0 && tag == skipTag {
				skipDepth--
				continue
			}
			if blockElements[tag] && skipDepth == 0 {
				newline()
			}
		case html.TextToken:
			raw := z.Text()
			if inJSONLD {
				jsonBuf.Write(raw)
				continue
			}
			if inTitle {
				if out.title == "" {
					out.title = strings.TrimSpace(string(raw))
				}
				continue
			}
			if skipDepth > 0 || textRunes >= maxVisibleRunes {
				continue
			}
			s := collapseSpaces(string(raw))
			if strings.TrimSpace(s) == "" {
				if text.Len() > 0 && !strings.HasSuffix(text.String(), " ") && !strings.HasSuffix(text.String(), "\n") {
					text.WriteByte(' ')
				}
				continue
			}
			text.WriteString(s)
			textRunes += len([]rune(s))
		}
	}
}

// collapseSpaces turns every run of white space into one space.
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ") + trailingSpace(s)
}

func trailingSpace(s string) string {
	if s != "" && strings.ContainsAny(s[len(s)-1:], " \t\r\n") && strings.TrimSpace(s) != "" {
		return " "
	}
	return ""
}

// tidyText trims each line, drops empty lines and cuts the text.
func tidyText(s string) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return clipRunes(strings.Join(lines, "\n"), maxVisibleRunes)
}

func clipRunes(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

package legaldocs

import (
	"fmt"
	"html"
	"sort"
	"strings"
)

// placeholderKind tells how a placeholder may be used: a text value is
// printed with {{key}} and tested with {{#if key}}; a flag is only tested.
type placeholderKind int

const (
	kindText placeholderKind = iota
	kindFlag
)

// placeholders is the ALLOWLIST of keys a template may name. Anything else
// is refused when a version is created or published (Parse). BuildValues
// fills every one of them (a test keeps the two in step).
var placeholders = map[string]placeholderKind{
	"document.version": kindText,

	"seller.legal_name":          kindText,
	"seller.legal_form":          kindText,
	"seller.registration_number": kindText,
	"seller.tax_id":              kindText,
	"seller.ids_line":            kindText,
	"seller.address":             kindText,
	"seller.country":             kindText,
	"seller.phone":               kindText,
	"seller.is_platform":         kindFlag,

	"promoter.name":     kindText,
	"promoter.legal_id": kindText,
	"promoter.address":  kindText,
	"promoter.website":  kindText,

	"refund.mode":            kindText,
	"refund.mode_text":       kindText,
	"refund.no_refund":       kindFlag,
	"refund.until_sales_end": kindFlag,
	"refund.by_law":          kindFlag,

	"law.country":       kindText,
	"law.country_code":  kindText,
	"law.no_withdrawal": kindFlag,
}

// Placeholders lists every allowed key, sorted: the vocabulary a template
// author (the superadmin screen, P3b) may use.
func Placeholders() []string {
	out := make([]string, 0, len(placeholders))
	for k := range placeholders {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// maxNesting bounds {{#if}} nesting: a template is legal prose, not a program.
const maxNesting = 8

type node interface{ isNode() }

type textNode struct{ s string }
type varNode struct{ key string }
type ifNode struct {
	key        string
	then, els  []node
	inElse     bool
	openedLine int
}

func (textNode) isNode() {}
func (varNode) isNode()  {}
func (*ifNode) isNode()  {}

// Template is a parsed terms template.
type Template struct {
	nodes []node
}

// ParseError lists every problem of a template, each with its line number.
type ParseError struct {
	Problems []string
}

func (e *ParseError) Error() string {
	return "legaldocs: invalid template: " + strings.Join(e.Problems, "; ")
}

// Parse reads a template. The syntax is {{key}}, {{#if key}}, {{else}} and
// {{/if}}; keys come from the allowlist only. It reports every problem at
// once (*ParseError), never a partial template.
func Parse(src string) (*Template, error) {
	var problems []string
	root := &ifNode{}
	stack := []*ifNode{root}
	add := func(n node) {
		top := stack[len(stack)-1]
		if top.inElse {
			top.els = append(top.els, n)
		} else {
			top.then = append(top.then, n)
		}
	}
	lineAt := func(off int) int { return strings.Count(src[:off], "\n") + 1 }

	pos := 0
	for pos < len(src) {
		open := strings.Index(src[pos:], "{{")
		if open < 0 {
			add(textNode{src[pos:]})
			break
		}
		open += pos
		if open > pos {
			add(textNode{src[pos:open]})
		}
		end := strings.Index(src[open+2:], "}}")
		if end < 0 {
			problems = append(problems, fmt.Sprintf("line %d: \"{{\" is never closed with \"}}\"", lineAt(open)))
			break
		}
		end += open + 2
		inner := strings.TrimSpace(src[open+2 : end])
		line := lineAt(open)
		pos = end + 2

		switch {
		case strings.HasPrefix(inner, "#if ") || inner == "#if":
			key := strings.TrimSpace(strings.TrimPrefix(inner, "#if"))
			if _, ok := placeholders[key]; !ok {
				problems = append(problems, fmt.Sprintf("line %d: unknown placeholder %q in {{#if}}", line, key))
			}
			if len(stack) > maxNesting {
				problems = append(problems, fmt.Sprintf("line %d: {{#if}} nested deeper than %d", line, maxNesting))
			}
			n := &ifNode{key: key, openedLine: line}
			add(n)
			stack = append(stack, n)
		case inner == "else":
			top := stack[len(stack)-1]
			if len(stack) == 1 {
				problems = append(problems, fmt.Sprintf("line %d: {{else}} outside {{#if}}", line))
			} else if top.inElse {
				problems = append(problems, fmt.Sprintf("line %d: a second {{else}} in one {{#if}}", line))
			} else {
				top.inElse = true
			}
		case inner == "/if":
			if len(stack) == 1 {
				problems = append(problems, fmt.Sprintf("line %d: {{/if}} without {{#if}}", line))
			} else {
				stack = stack[:len(stack)-1]
			}
		default:
			kind, ok := placeholders[inner]
			switch {
			case !ok:
				problems = append(problems, fmt.Sprintf("line %d: unknown placeholder %q", line, inner))
			case kind == kindFlag:
				problems = append(problems, fmt.Sprintf("line %d: %q is a yes/no value, use it only in {{#if}}", line, inner))
			default:
				add(varNode{inner})
			}
		}
	}
	for _, n := range stack[1:] {
		problems = append(problems, fmt.Sprintf("line %d: {{#if %s}} is never closed with {{/if}}", n.openedLine, n.key))
	}
	if strings.TrimSpace(src) == "" {
		problems = append(problems, "the template is empty")
	}
	if len(problems) > 0 {
		return nil, &ParseError{Problems: problems}
	}
	return &Template{nodes: root.then}, nil
}

// Values are the resolved placeholder values: a string for a text key, a
// bool for a flag. A missing key is "" (or false).
type Values map[string]any

func (v Values) text(key string) string {
	s, _ := v[key].(string)
	return s
}

func (v Values) truthy(key string) bool {
	switch x := v[key].(type) {
	case bool:
		return x
	case string:
		return strings.TrimSpace(x) != ""
	}
	return false
}

// segment is one piece of an evaluated template: trusted template text, an
// untrusted value, or a marker left where a {{#if}} block opened or closed.
type segment struct {
	kind segKind
	s    string
}

type segKind int

const (
	segText segKind = iota
	segValue
	segMarker
)

func evaluate(nodes []node, v Values, out []segment) []segment {
	for _, n := range nodes {
		switch x := n.(type) {
		case textNode:
			out = append(out, segment{segText, x.s})
		case varNode:
			out = append(out, segment{segValue, v.text(x.key)})
		case *ifNode:
			out = append(out, segment{kind: segMarker})
			if v.truthy(x.key) {
				out = evaluate(x.then, v, out)
			} else {
				out = evaluate(x.els, v, out)
			}
			out = append(out, segment{kind: segMarker})
		}
	}
	return out
}

// Render substitutes the values into the template and returns HTML without
// scripts: the markup is decided from the template text only (a value can
// never start a heading or a list item), and every piece is escaped.
//
// Lines: "## " starts a heading (h2), "### " a smaller one (h3), "- " a list
// item; other lines are joined into paragraphs, and an empty line ends a
// paragraph. A line that holds nothing but {{#if}} / {{else}} / {{/if}}
// tags is dropped, so a block that prints nothing leaves no gap.
func Render(t *Template, v Values, locale string) string {
	segs := evaluate(t.nodes, v, nil)

	// Split into lines on the newlines of template text only.
	var lines [][]segment
	cur := []segment{}
	for _, s := range segs {
		if s.kind != segText {
			cur = append(cur, s)
			continue
		}
		parts := strings.Split(s.s, "\n")
		for i, p := range parts {
			if i > 0 {
				lines = append(lines, cur)
				cur = []segment{}
			}
			if p != "" {
				cur = append(cur, segment{segText, p})
			}
		}
	}
	lines = append(lines, cur)

	var b strings.Builder
	dir := "ltr"
	if locale == "he" {
		dir = "rtl"
	}
	fmt.Fprintf(&b, "<article class=\"legal-document\" lang=\"%s\" dir=\"%s\">\n", html.EscapeString(locale), dir)
	var para []string
	var items []string
	flushPara := func() {
		if len(para) > 0 {
			b.WriteString("<p>" + strings.Join(para, " ") + "</p>\n")
			para = nil
		}
	}
	flushList := func() {
		if len(items) > 0 {
			b.WriteString("<ul>\n")
			for _, it := range items {
				b.WriteString("<li>" + it + "</li>\n")
			}
			b.WriteString("</ul>\n")
			items = nil
		}
	}
	for _, line := range lines {
		kind, content := classify(line)
		switch kind {
		case lineSkip:
			continue
		case lineBlank:
			flushPara()
			flushList()
		case lineH2, lineH3:
			flushPara()
			flushList()
			tag := "h2"
			if kind == lineH3 {
				tag = "h3"
			}
			if content != "" {
				b.WriteString("<" + tag + ">" + content + "</" + tag + ">\n")
			}
		case lineItem:
			flushPara()
			if content != "" {
				items = append(items, content)
			}
		default:
			flushList()
			if content != "" {
				para = append(para, content)
			}
		}
	}
	flushPara()
	flushList()
	b.WriteString("</article>\n")
	return b.String()
}

type lineKind int

const (
	lineSkip lineKind = iota
	lineBlank
	lineH2
	lineH3
	lineItem
	lineText
)

// classify decides one line's markup from its template text and renders its
// escaped inline content.
func classify(line []segment) (lineKind, string) {
	hasMarker, hasContent := false, false
	for _, s := range line {
		switch s.kind {
		case segMarker:
			hasMarker = true
		case segValue:
			hasContent = true
		case segText:
			if strings.TrimSpace(s.s) != "" {
				hasContent = true
			}
		}
	}
	if !hasContent {
		if hasMarker {
			return lineSkip, ""
		}
		return lineBlank, ""
	}

	// Drop markers and leading blank text; the markup prefix may only come
	// from template text at the start of the line.
	var rest []segment
	for _, s := range line {
		if s.kind != segMarker {
			rest = append(rest, s)
		}
	}
	for len(rest) > 0 && rest[0].kind == segText && strings.TrimSpace(rest[0].s) == "" {
		rest = rest[1:]
	}
	kind := lineText
	if len(rest) > 0 && rest[0].kind == segText {
		first := strings.TrimLeft(rest[0].s, " \t\r")
		for _, p := range []struct {
			prefix string
			kind   lineKind
		}{{"### ", lineH3}, {"## ", lineH2}, {"- ", lineItem}} {
			if strings.HasPrefix(first, p.prefix) {
				kind = p.kind
				first = first[len(p.prefix):]
				break
			}
		}
		rest = append([]segment{{segText, first}}, rest[1:]...)
	}

	var b strings.Builder
	for _, s := range rest {
		switch s.kind {
		case segText:
			b.WriteString(html.EscapeString(strings.ReplaceAll(s.s, "\r", "")))
		case segValue:
			b.WriteString(html.EscapeString(strings.Join(strings.Fields(s.s), " ")))
		}
	}
	return kind, strings.Join(strings.Fields(b.String()), " ")
}

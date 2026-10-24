package richtext

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// schemeWordRE finds a scheme that can run code or load a local or opaque
// resource, or the plain-http scheme, as a word of the text: the strictness
// of the old email note (eventnote.go).
var schemeWordRE = regexp.MustCompile(`(?i)\b(?:javascript|vbscript|data|file|blob|about|http):\S`)

// dropTags are removed together with their content.
var dropTags = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Img: true, atom.Iframe: true,
	atom.Svg: true, atom.Object: true, atom.Embed: true, atom.Noscript: true,
	atom.Template: true, atom.Title: true, atom.Head: true, atom.Meta: true,
	atom.Link: true, atom.Base: true, atom.Audio: true, atom.Video: true,
	atom.Canvas: true, atom.Math: true, atom.Applet: true, atom.Frame: true,
	atom.Frameset: true, atom.Noframes: true, atom.Noembed: true,
	atom.Param: true, atom.Source: true, atom.Track: true, atom.Map: true,
	atom.Area: true,
}

// boundaryTags are block tags that end and start a paragraph; their text is
// kept.
var boundaryTags = map[atom.Atom]bool{
	atom.Div: true, atom.Section: true, atom.Article: true, atom.Aside: true,
	atom.Header: true, atom.Footer: true, atom.Nav: true, atom.Main: true,
	atom.Figure: true, atom.Figcaption: true, atom.Address: true,
	atom.Table: true, atom.Thead: true, atom.Tbody: true, atom.Tfoot: true,
	atom.Tr: true, atom.Td: true, atom.Th: true, atom.Caption: true,
	atom.Dl: true, atom.Dt: true, atom.Dd: true, atom.Pre: true, atom.Hr: true,
	atom.Fieldset: true, atom.Form: true, atom.Details: true, atom.Summary: true,
	atom.Center: true, atom.Menu: true, atom.Dir: true, atom.Li: true,
	atom.Ul: true, atom.Ol: true, atom.Blockquote: true, atom.P: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true,
	atom.H6: true, atom.Body: true, atom.Html: true, atom.Legend: true,
}

// Sanitize reduces input, any HTML, to ART for the profile: the canonical
// serialisation of the white-listed tags, so that
// Sanitize(Sanitize(x, p), p) == Sanitize(x, p). An empty result means "no
// text". It refuses control, bidi and zero-width characters and texts over
// the profile's limits, with a typed *Error.
func Sanitize(input string, p Profile) (string, error) {
	d, err := convert(input, p, true)
	if err != nil {
		return "", err
	}
	out, links := d.html(writeOpts{})
	if p.MaxVisible > 0 && d.visibleLen() > p.MaxVisible {
		return "", ErrTooLong
	}
	if p.MaxLinks > 0 && links > p.MaxLinks {
		return "", ErrTooManyLinks
	}
	if p.StrictSchemeWords && schemeWordRE.MatchString(d.visibleText()) {
		return "", ErrSchemeWord
	}
	if p.MaxHTML > 0 && utf8.RuneCountInString(out) > p.MaxHTML {
		return "", ErrHTMLTooLong
	}
	return out, nil
}

type containerKind int

const (
	cRoot containerKind = iota
	cQuote
	cList
	cItem
)

type container struct {
	kind  containerKind
	quote *block
	list  *list
	item  *item
	alias bool // a flattened list that adds its items to the parent list
}

type marks struct {
	strong, em, u bool
	href          string
	linkID        int
}

type conv struct {
	p        Profile
	strict   bool
	err      error
	root     []*block
	stack    []*container
	cur      []chunk
	curKind  blockKind
	nextLink int
}

// convert parses input and builds the document. In strict mode a forbidden
// character is an error; otherwise (reading) it is dropped.
func convert(input string, p Profile, strict bool) (*doc, error) {
	if !utf8.ValidString(input) {
		if strict {
			return nil, ErrBadEncoding
		}
		input = strings.ToValidUTF8(input, "")
	}
	if len(input) > maxInputBytes {
		return nil, ErrHTMLTooLong
	}
	if strict {
		for _, r := range input {
			if isForbidden(r) {
				return nil, ErrControlChars
			}
		}
	}
	nodes, err := html.ParseFragment(strings.NewReader(input), &html.Node{
		Type: html.ElementNode, Data: "body", DataAtom: atom.Body,
	})
	if err != nil {
		return nil, ErrHTMLTooLong
	}
	c := &conv{p: p, strict: strict, stack: []*container{{kind: cRoot}}}
	for _, n := range nodes {
		c.walk(n, marks{}, 0)
	}
	c.flush()
	if c.err != nil {
		return nil, c.err
	}
	return &doc{blocks: c.root}, nil
}

func (c *conv) top() *container { return c.stack[len(c.stack)-1] }

func (c *conv) push(k *container) { c.stack = append(c.stack, k) }

func (c *conv) pop() { c.stack = c.stack[:len(c.stack)-1] }

func (c *conv) quoteDepth() int {
	n := 0
	for _, k := range c.stack {
		if k.kind == cQuote {
			n++
		}
	}
	return n
}

func (c *conv) listDepth() int {
	n := 0
	for _, k := range c.stack {
		if k.kind == cList && !k.alias {
			n++
		}
	}
	return n
}

func (c *conv) fail(err error) {
	if c.err == nil {
		c.err = err
	}
}

// text adds a run of decoded text.
func (c *conv) text(s string, m marks) {
	if s == "" {
		return
	}
	var sb strings.Builder
	for _, r := range s {
		if isForbidden(r) {
			if c.strict {
				c.fail(ErrControlChars)
				return
			}
			continue
		}
		sb.WriteRune(r)
	}
	c.cur = append(c.cur, chunk{
		text: sb.String(), strong: m.strong, em: m.em, u: m.u,
		href: m.href, linkID: m.linkID,
	})
}

// flush closes the pending run into the current container.
func (c *conv) flush() {
	chunks := finish(c.cur)
	kind := c.curKind
	c.cur, c.curKind = nil, bPara
	if len(chunks) == 0 {
		return
	}
	top := c.top()
	switch top.kind {
	case cRoot:
		c.root = append(c.root, &block{kind: kind, inl: chunks})
	case cQuote:
		top.quote.paras = append(top.quote.paras, chunks)
	case cList:
		// Text between <ul> and <li>: it becomes an item of its own.
		top.list.items = append(top.list.items, &item{inl: chunks})
	case cItem:
		if len(top.item.inl) > 0 {
			top.item.inl = append(top.item.inl, chunk{br: true})
		}
		top.item.inl = append(top.item.inl, chunks...)
	}
}

func (c *conv) openQuote() bool {
	if !c.p.has("blockquote") || c.top().kind != cRoot {
		return false
	}
	b := &block{kind: bQuote}
	c.root = append(c.root, b)
	c.push(&container{kind: cQuote, quote: b})
	return true
}

func (c *conv) openList(ordered bool) bool {
	if !c.p.has("ul") || c.quoteDepth() > 0 {
		return false
	}
	top := c.top()
	switch top.kind {
	case cRoot:
		l := &list{ordered: ordered}
		c.root = append(c.root, &block{kind: bList, list: l})
		c.push(&container{kind: cList, list: l})
	case cItem:
		if c.listDepth() >= 2 {
			// Too deep: its items join the enclosing list.
			c.push(&container{kind: cList, list: c.enclosingList(), alias: true})
			return true
		}
		l := &list{ordered: ordered}
		top.item.subs = append(top.item.subs, l)
		c.push(&container{kind: cList, list: l})
	case cList:
		// A list directly in a list belongs to the item before it.
		if n := len(top.list.items); n > 0 && c.listDepth() < 2 {
			l := &list{ordered: ordered}
			last := top.list.items[n-1]
			last.subs = append(last.subs, l)
			c.push(&container{kind: cList, list: l})
			return true
		}
		c.push(&container{kind: cList, list: top.list, alias: true})
	default:
		return false
	}
	return true
}

// enclosingList is the nearest list below the top of the stack.
func (c *conv) enclosingList() *list {
	for i := len(c.stack) - 1; i >= 0; i-- {
		if c.stack[i].kind == cList {
			return c.stack[i].list
		}
	}
	return nil
}

func (c *conv) walk(n *html.Node, m marks, depth int) {
	if c.err != nil {
		return
	}
	if depth > maxDepth {
		c.fail(ErrTooDeep)
		return
	}
	switch n.Type {
	case html.TextNode:
		c.text(n.Data, m)
		return
	case html.ElementNode:
	default:
		return // comments, doctype
	}
	a := n.DataAtom
	if dropTags[a] {
		return
	}
	children := func(m marks) {
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			c.walk(ch, m, depth+1)
		}
	}
	switch a {
	case atom.Br:
		c.cur = append(c.cur, chunk{br: true})
	case atom.B, atom.Strong:
		if c.p.has("strong") {
			m.strong = true
		}
		children(m)
	case atom.I, atom.Em:
		if c.p.has("em") {
			m.em = true
		}
		children(m)
	case atom.U:
		if c.p.has("u") {
			m.u = true
		}
		children(m)
	case atom.A:
		if c.p.has("a") && m.href == "" {
			if href, ok := c.linkTarget(n); ok {
				c.nextLink++
				m.href, m.linkID = href, c.nextLink
			}
		}
		children(m)
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		c.flush()
		level := bH3
		if a == atom.H4 || a == atom.H5 || a == atom.H6 {
			level = bH4
		}
		if c.p.has("h3") && c.top().kind == cRoot {
			c.curKind = level
		} else if c.p.has("strong") {
			m.strong = true // a heading outside the description is a bold line
		}
		children(m)
		c.flush()
	case atom.Blockquote:
		c.flush()
		pushed := c.openQuote()
		children(m)
		c.flush()
		if pushed {
			c.pop()
		}
	case atom.Ul, atom.Ol:
		c.flush()
		pushed := c.openList(a == atom.Ol)
		children(m)
		c.flush()
		if pushed {
			c.pop()
		}
	case atom.Li:
		c.flush()
		if c.top().kind == cList {
			it := &item{}
			c.top().list.items = append(c.top().list.items, it)
			c.push(&container{kind: cItem, item: it})
			children(m)
			c.flush()
			c.pop()
			return
		}
		// An <li> outside a list is a paragraph.
		children(m)
		c.flush()
	default:
		if boundaryTags[a] {
			c.flush()
			children(m)
			c.flush()
			return
		}
		children(m) // any other tag is unwrapped
	}
}

// linkTarget reads the href of an <a>: it must be valid for the profile.
func (c *conv) linkTarget(n *html.Node) (string, bool) {
	for _, at := range n.Attr {
		if at.Namespace == "" && strings.EqualFold(at.Key, "href") {
			return cleanHref(at.Val, c.p)
		}
	}
	return "", false
}

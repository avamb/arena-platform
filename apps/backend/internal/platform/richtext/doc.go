package richtext

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// The internal model of a text: blocks of inline chunks. Sanitize builds it
// from foreign HTML, FromLegacy from the old plain text, and every output
// (HTML, plain text, legacy, Telegram) is written from it, so they cannot
// disagree about what a text contains.

// chunk is a run of text with its marks, or a line break.
type chunk struct {
	text          string
	br            bool
	strong, em, u bool
	href          string // canonical link target, "" for no link
	linkID        int    // identifies the <a> element a chunk came from
}

type blockKind int

const (
	bPara blockKind = iota
	bH3
	bH4
	bQuote
	bList
)

type block struct {
	kind  blockKind
	inl   []chunk   // bPara, bH3, bH4
	paras [][]chunk // bQuote
	list  *list     // bList
}

type list struct {
	ordered bool
	items   []*item
}

type item struct {
	inl  []chunk
	subs []*list
}

type doc struct {
	blocks []*block
}

func isSpaceText(s string) bool {
	for _, r := range s {
		if !isWS(r) {
			return false
		}
	}
	return true
}

// finish normalises the chunks of one paragraph, heading or item: a link
// that holds only spaces is unwrapped, runs of spaces collapse into one,
// spaces at the borders and around line breaks go, and neighbours with the
// same marks merge. A result with no text and no break is nil.
func finish(in []chunk) []chunk {
	// A link with no visible text is no link.
	linkText := map[int]bool{}
	for _, c := range in {
		if c.href != "" && !c.br && !isSpaceText(c.text) {
			linkText[c.linkID] = true
		}
	}
	// Merging appends to the text of the last chunk, so the texts are kept in
	// byte buffers beside out and turned into strings at the end: a text of
	// many small nodes costs linear time.
	out := make([]chunk, 0, len(in))
	bufs := make([][]byte, 0, len(in))
	lastSpace := true // at the start and after a break a space is dropped
	trimTail := func() {
		for len(out) > 0 {
			n := len(out) - 1
			if out[n].br {
				return
			}
			bufs[n] = bytes.TrimRight(bufs[n], " ")
			if len(bufs[n]) > 0 {
				return
			}
			out, bufs = out[:n], bufs[:n]
		}
	}
	for _, c := range in {
		if c.br {
			trimTail()
			if len(out) > 0 { // a break at the start is dropped
				out = append(out, chunk{br: true})
				bufs = append(bufs, nil)
			}
			lastSpace = true
			continue
		}
		if c.href != "" && !linkText[c.linkID] {
			c.href, c.linkID = "", 0
		}
		var text []byte
		n := len(out)
		merge := n > 0 && !out[n-1].br && sameMarks(out[n-1], c)
		if merge {
			text = bufs[n-1]
		}
		before := len(text)
		for _, r := range c.text {
			if isWS(r) {
				if lastSpace {
					continue
				}
				text = append(text, ' ')
				lastSpace = true
				continue
			}
			text = utf8.AppendRune(text, r)
			lastSpace = false
		}
		if len(text) == before {
			continue
		}
		if merge {
			bufs[n-1] = text
			continue
		}
		c.text = ""
		out = append(out, c)
		bufs = append(bufs, text)
	}
	trimTail()
	for len(out) > 0 && out[len(out)-1].br {
		out = out[:len(out)-1]
		bufs = bufs[:len(bufs)-1]
	}
	if len(out) == 0 {
		return nil
	}
	for i := range out {
		if !out[i].br {
			out[i].text = string(bufs[i])
		}
	}
	return out
}

func sameMarks(a, b chunk) bool {
	return a.strong == b.strong && a.em == b.em && a.u == b.u && a.href == b.href
}

// ---- serialisation ----

type writeOpts struct {
	// blank adds rel="noopener noreferrer" target="_blank" to every link.
	blank bool
	// tg writes the tags of Telegram's parse_mode=HTML (b, i, u, a) and a
	// line break as a newline.
	tg bool
}

type mark struct {
	kind byte // 'a', 's', 'e', 'u'
	href string
}

func marksOf(c chunk) []mark {
	var m []mark
	if c.href != "" {
		m = append(m, mark{'a', c.href})
	}
	if c.strong {
		m = append(m, mark{kind: 's'})
	}
	if c.em {
		m = append(m, mark{kind: 'e'})
	}
	if c.u {
		m = append(m, mark{kind: 'u'})
	}
	return m
}

func escapeText(s string) string {
	if !strings.ContainsAny(s, "&<>") {
		return s
	}
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func escapeAttr(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// writeInline writes chunks with a minimal, canonical nesting: link, strong,
// em, underline, outermost first. Marks never span a line break.
func writeInline(sb *strings.Builder, chunks []chunk, o writeOpts, links *int) {
	var open []mark
	closeTo := func(n int) {
		for len(open) > n {
			switch open[len(open)-1].kind {
			case 'a':
				sb.WriteString("</a>")
			case 's':
				sb.WriteString(o.tag("</strong>", "</b>"))
			case 'e':
				sb.WriteString(o.tag("</em>", "</i>"))
			case 'u':
				sb.WriteString("</u>")
			}
			open = open[:len(open)-1]
		}
	}
	for _, c := range chunks {
		if c.br {
			closeTo(0)
			sb.WriteString(o.tag("<br>", "\n"))
			continue
		}
		want := marksOf(c)
		n := 0
		for n < len(open) && n < len(want) && open[n] == want[n] {
			n++
		}
		closeTo(n)
		for _, m := range want[n:] {
			switch m.kind {
			case 'a':
				sb.WriteString(`<a href="`)
				sb.WriteString(escapeAttr(m.href))
				sb.WriteString(`"`)
				if o.blank {
					sb.WriteString(` rel="noopener noreferrer" target="_blank"`)
				}
				sb.WriteString(">")
				*links++
			case 's':
				sb.WriteString(o.tag("<strong>", "<b>"))
			case 'e':
				sb.WriteString(o.tag("<em>", "<i>"))
			case 'u':
				sb.WriteString("<u>")
			}
			open = append(open, m)
		}
		sb.WriteString(escapeText(c.text))
	}
	closeTo(0)
}

func itemEmpty(it *item) bool {
	if len(it.inl) > 0 {
		return false
	}
	for _, s := range it.subs {
		if !listEmpty(s) {
			return false
		}
	}
	return true
}

func listEmpty(l *list) bool {
	for _, it := range l.items {
		if !itemEmpty(it) {
			return false
		}
	}
	return true
}

func writeList(sb *strings.Builder, l *list, o writeOpts, links *int) {
	if listEmpty(l) {
		return
	}
	tag := "ul"
	if l.ordered {
		tag = "ol"
	}
	sb.WriteString("<" + tag + ">")
	for _, it := range l.items {
		if itemEmpty(it) {
			continue
		}
		sb.WriteString("<li>")
		writeInline(sb, it.inl, o, links)
		for _, s := range it.subs {
			writeList(sb, s, o, links)
		}
		sb.WriteString("</li>")
	}
	sb.WriteString("</" + tag + ">")
}

// html serialises the document and counts its links.
func (d *doc) html(o writeOpts) (string, int) {
	var sb strings.Builder
	links := 0
	for _, b := range d.blocks {
		switch b.kind {
		case bPara:
			if len(b.inl) == 0 {
				continue
			}
			sb.WriteString("<p>")
			writeInline(&sb, b.inl, o, &links)
			sb.WriteString("</p>")
		case bH3, bH4:
			if len(b.inl) == 0 {
				continue
			}
			tag := "h3"
			if b.kind == bH4 {
				tag = "h4"
			}
			sb.WriteString("<" + tag + ">")
			writeInline(&sb, b.inl, o, &links)
			sb.WriteString("</" + tag + ">")
		case bQuote:
			if len(b.paras) == 0 {
				continue
			}
			sb.WriteString("<blockquote>")
			for _, p := range b.paras {
				sb.WriteString("<p>")
				writeInline(&sb, p, o, &links)
				sb.WriteString("</p>")
			}
			sb.WriteString("</blockquote>")
		case bList:
			writeList(&sb, b.list, o, &links)
		}
	}
	return sb.String(), links
}

// units calls fn for every line-like unit of the text in reading order:
// paragraphs, headings, quote paragraphs and list items.
func (d *doc) units(fn func(chunks []chunk)) {
	d.numberedUnits(func(_ int, chunks []chunk) { fn(chunks) })
}

// numberedUnits is units that also tells the visible number of an item of an
// ordered list (1, 2, ... as PlainText writes it), and 0 for every other unit.
func (d *doc) numberedUnits(fn func(num int, chunks []chunk)) {
	var walkList func(l *list)
	walkList = func(l *list) {
		n := 0
		for _, it := range l.items {
			if itemEmpty(it) {
				continue
			}
			if len(it.inl) > 0 {
				n++
				if l.ordered {
					fn(n, it.inl)
				} else {
					fn(0, it.inl)
				}
			}
			for _, s := range it.subs {
				walkList(s)
			}
		}
	}
	for _, b := range d.blocks {
		switch b.kind {
		case bPara, bH3, bH4:
			if len(b.inl) > 0 {
				fn(0, b.inl)
			}
		case bQuote:
			for _, p := range b.paras {
				fn(0, p)
			}
		case bList:
			walkList(b.list)
		}
	}
}

func chunksText(chunks []chunk) string {
	var sb strings.Builder
	for _, c := range chunks {
		if c.br {
			sb.WriteByte('\n')
			continue
		}
		sb.WriteString(c.text)
	}
	return sb.String()
}

// visibleLen counts the visible characters: the text of every unit and one
// character for every break between units or lines.
func (d *doc) visibleLen() int {
	n, units := 0, 0
	d.units(func(chunks []chunk) {
		units++
		n += utf8.RuneCountInString(chunksText(chunks))
	})
	if units > 1 {
		n += units - 1
	}
	return n
}

// visibleText is the text the way a reader sees it, units separated by line
// breaks, for the scheme-word check.
func (d *doc) visibleText() string {
	var parts []string
	d.units(func(chunks []chunk) { parts = append(parts, chunksText(chunks)) })
	return strings.Join(parts, "\n")
}

// tag picks the HTML tag or its Telegram form.
func (o writeOpts) tag(html, tg string) string {
	if o.tg {
		return tg
	}
	return html
}

package richtext

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// trailingPunct is cut off the end of an auto-linked address: it belongs to
// the sentence (the same set as eventnote.go).
const trailingPunct = ".,;:!?)]}»”’"

// FromLegacy turns the old plain-text form of a text into ART: a blank line
// is a paragraph, a single line break is <br>, **x** is strong, _x_ at word
// borders is em, a block of two or more lines that start with "- ", "* " or
// "• " is a list, and so is a block of numbered lines "1. ", "2. ". Anything
// else is text, HTML-escaped: a "<b>" the organizer typed stays "<b>". For a
// profile with AutoLink, bare https:// addresses (up to the profile's link
// limit) become links. The result is canonical: Sanitize leaves it as it is.
func FromLegacy(text string, p Profile) string {
	out, _ := legacyDoc(text, p).html(writeOpts{})
	return out
}

// legacyConv holds the state of one FromLegacy run.
type legacyConv struct {
	p        Profile
	budget   int // auto-links left
	nextLink int
}

func legacyDoc(text string, p Profile) *doc {
	text = strings.ToValidUTF8(text, "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lc := &legacyConv{p: p}
	if p.AutoLink && p.has("a") {
		lc.budget = p.MaxLinks
	}
	d := &doc{}
	var block []string
	flushBlock := func() {
		if len(block) > 0 {
			lc.block(d, block)
			block = nil
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimFunc(line, func(r rune) bool { return isWS(r) })
		if line == "" {
			flushBlock()
			continue
		}
		block = append(block, line)
	}
	flushBlock()
	return d
}

type lineKind int

const (
	lText lineKind = iota
	lBullet
	lNumber
)

// classify tells the list marker of a trimmed line and the text after it.
func classify(line string) (lineKind, string, int) {
	r, size := utf8.DecodeRuneInString(line)
	if (r == '-' || r == '*' || r == '•') && size < len(line) {
		if rest := line[size:]; startsWithWS(rest) {
			if body := strings.TrimLeftFunc(rest, isWS); body != "" {
				return lBullet, body, 0
			}
		}
	}
	n := 0
	for n < len(line) && n < 3 && line[n] >= '0' && line[n] <= '9' {
		n++
	}
	if n > 0 && n+1 < len(line) && line[n] == '.' && startsWithWS(line[n+1:]) {
		if body := strings.TrimLeftFunc(line[n+1:], isWS); body != "" {
			num := 0
			for _, d := range line[:n] {
				num = num*10 + int(d-'0')
			}
			return lNumber, body, num
		}
	}
	return lText, line, 0
}

// block converts the lines of one blank-line-separated block.
func (lc *legacyConv) block(d *doc, lines []string) {
	kinds := make([]lineKind, len(lines))
	bodies := make([]string, len(lines))
	nums := make([]int, len(lines))
	for i, l := range lines {
		kinds[i], bodies[i], nums[i] = classify(l)
	}
	var para []chunk
	flushPara := func() {
		if fin := finish(para); len(fin) > 0 {
			d.blocks = append(d.blocks, &block{kind: bPara, inl: fin})
		}
		para = nil
	}
	addText := func(s string) {
		if len(para) > 0 {
			para = append(para, chunk{br: true})
		}
		para = append(para, lc.inline(s)...)
	}
	for i := 0; i < len(lines); {
		k := kinds[i]
		if k == lBullet || (k == lNumber && nums[i] == 1) {
			j := i + 1
			for j < len(lines) && kinds[j] == k {
				j++
			}
			if j-i >= 2 {
				flushPara()
				l := &list{ordered: k == lNumber}
				for x := i; x < j; x++ {
					l.items = append(l.items, &item{inl: finish(lc.inline(bodies[x]))})
				}
				d.blocks = append(d.blocks, &block{kind: bList, list: l})
				i = j
				continue
			}
		}
		addText(lines[i])
		i++
	}
	flushPara()
}

// inline converts one line: **bold** first, then _italic_ and links inside
// each part.
func (lc *legacyConv) inline(s string) []chunk {
	var out []chunk
	last := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '*' || i+1 >= len(s) || s[i+1] != '*' {
			continue
		}
		j, ok := boldEnd(s, i)
		if !ok {
			continue
		}
		out = append(out, lc.italics(s[last:i], false)...)
		out = append(out, lc.italics(s[i+2:j], true)...)
		i = j + 1
		last = j + 2
	}
	return append(out, lc.italics(s[last:], false)...)
}

// boldEnd finds the "**" that closes the one opened at s[i:i+2]: the text
// between starts and ends on a non-space.
func boldEnd(s string, i int) (int, bool) {
	c := i + 2
	if c >= len(s) {
		return 0, false
	}
	if r, _ := utf8.DecodeRuneInString(s[c:]); unicode.IsSpace(r) {
		return 0, false
	}
	for j := c + 1; j+1 < len(s); j++ {
		if s[j] == '*' && s[j+1] == '*' {
			if r, _ := utf8.DecodeLastRuneInString(s[:j]); !unicode.IsSpace(r) {
				return j, true
			}
		}
	}
	return 0, false
}

const (
	italicOpenAfter   = "([{\"“‘'«„"
	italicCloseBefore = ".,;:!?)]}\"”’'»…"
)

// italics converts _italic_ at word borders. A border is the start or end of
// the text, a space or one of the punctuation marks above, so "snake_case"
// and "https://x/a_b_c" stay intact and Latin, Cyrillic and Hebrew behave the
// same.
func (lc *legacyConv) italics(s string, strong bool) []chunk {
	var out []chunk
	last := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '_' || !italicOpensAt(s, i) {
			continue
		}
		j := strings.IndexByte(s[i+1:], '_')
		if j < 0 {
			continue
		}
		j += i + 1
		body := s[i+1 : j]
		if body == "" || !italicBody(body) || !italicClosesAt(s, j) {
			continue
		}
		out = append(out, lc.links(s[last:i], strong, false)...)
		out = append(out, lc.links(body, strong, true)...)
		i = j
		last = j + 1
	}
	return append(out, lc.links(s[last:], strong, false)...)
}

func italicOpensAt(s string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return unicode.IsSpace(r) || strings.ContainsRune(italicOpenAfter, r)
}

func italicClosesAt(s string, j int) bool {
	if j+1 >= len(s) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(s[j+1:])
	return unicode.IsSpace(r) || strings.ContainsRune(italicCloseBefore, r)
}

func italicBody(b string) bool {
	f, _ := utf8.DecodeRuneInString(b)
	l, _ := utf8.DecodeLastRuneInString(b)
	return !unicode.IsSpace(f) && !unicode.IsSpace(l)
}

// links splits s into text and, when the profile auto-links, https links.
func (lc *legacyConv) links(s string, strong, em bool) []chunk {
	if s == "" {
		return nil
	}
	plain := func(t string) chunk { return chunk{text: t, strong: strong, em: em} }
	if lc.budget <= 0 {
		return []chunk{plain(s)}
	}
	var out []chunk
	last := 0
	for i := 0; i+8 <= len(s) && lc.budget > 0; {
		if !hasPrefixFold(s[i:], "https://") {
			i++
			continue
		}
		end := i + 8
		for end < len(s) {
			r, size := utf8.DecodeRuneInString(s[end:])
			if unicode.IsSpace(r) || isWS(r) || strings.ContainsRune("<>\"'`", r) {
				break
			}
			end += size
		}
		for end > i {
			r, size := utf8.DecodeLastRuneInString(s[i:end])
			if r == utf8.RuneError || !strings.ContainsRune(trailingPunct, r) {
				break
			}
			end -= size
		}
		href, ok := cleanHref(s[i:end], lc.p)
		if !ok {
			i += 8
			continue
		}
		if i > last {
			out = append(out, plain(s[last:i]))
		}
		lc.nextLink++
		lc.budget--
		out = append(out, chunk{text: s[i:end], strong: strong, em: em, href: href, linkID: lc.nextLink})
		i, last = end, end
	}
	if last < len(s) {
		out = append(out, plain(s[last:]))
	}
	return out
}

func hasPrefixFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != prefix[i] {
			return false
		}
	}
	return true
}

func startsWithWS(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return isWS(r)
}

package richtext

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Normalize is the single write point: it returns the ART to store for input
// in the given format. "text" (and an empty format) is the old plain text and
// goes through FromLegacy, "art" is HTML and goes through Sanitize, anything
// else is an error. The server never guesses the format from the tags.
// Normalize(Normalize(x, f, p), "art", p) == Normalize(x, f, p).
func Normalize(input, format string, p Profile) (string, error) {
	switch format {
	case "", FormatText:
		if !utf8.ValidString(input) {
			return "", ErrBadEncoding
		}
		if len(input) > maxInputBytes {
			return "", ErrHTMLTooLong
		}
		return Sanitize(FromLegacy(input, p), p)
	case FormatART:
		return Sanitize(input, p)
	}
	return "", ErrInvalidFormat
}

// Render is the single read point: the safe HTML of a stored text for HTML
// consumers. ART is sanitised again, text is converted by FromLegacy first,
// and every link gets rel="noopener noreferrer" target="_blank". Limits are
// not enforced (a text stored under an older limit must still show) and
// forbidden characters are dropped. It never returns unsafe HTML, even if
// stored is hostile: on any failure it returns "".
func Render(stored, format string, p Profile) string {
	src := stored
	if format != FormatART {
		src = FromLegacy(stored, p)
	}
	d, err := convert(src, p.readOnly(), false)
	if err != nil {
		return ""
	}
	out, _ := d.html(writeOpts{blank: true})
	return out
}

// parseART reads a stored ART for the outputs below. They take no profile, so
// the most permissive one is used. A text that cannot be read gives an empty
// document.
func parseART(art string) *doc {
	d, err := convert(art, anyProfile(), false)
	if err != nil {
		return &doc{}
	}
	return d
}

// VisibleLen is the length of the visible text of art, in characters: what
// the profile limits count.
func VisibleLen(art string) int { return parseART(art).visibleLen() }

// runs splits chunks into runs that share one link (or have none).
func runs(chunks []chunk) [][]chunk {
	var out [][]chunk
	for _, c := range chunks {
		if n := len(out); n > 0 && !c.br && !out[n-1][0].br &&
			out[n-1][0].href == c.href && out[n-1][0].linkID == c.linkID {
			out[n-1] = append(out[n-1], c)
			continue
		}
		out = append(out, []chunk{c})
	}
	return out
}

func runText(run []chunk, br string) string {
	var sb strings.Builder
	for _, c := range run {
		if c.br {
			sb.WriteString(br)
			continue
		}
		sb.WriteString(c.text)
	}
	return sb.String()
}

// displayHref is a link target as a reader should see it.
func displayHref(href string) string { return strings.TrimPrefix(href, "mailto:") }

// sameLink reports whether a link's text just repeats its target.
func sameLink(text, href string) bool {
	t := strings.TrimRight(text, "/")
	return strings.EqualFold(t, strings.TrimRight(href, "/")) ||
		strings.EqualFold(t, strings.TrimRight(displayHref(href), "/"))
}

// plainInline writes chunks as text: a link as "text (https://…)" unless the
// text is the address itself.
func plainInline(chunks []chunk, br string) string {
	var sb strings.Builder
	for _, run := range runs(chunks) {
		text := runText(run, br)
		sb.WriteString(text)
		if h := run[0].href; h != "" && !sameLink(text, h) {
			sb.WriteString(" (" + displayHref(h) + ")")
		}
	}
	return sb.String()
}

func plainList(sb *[]string, l *list, depth int, inline func([]chunk) string) {
	n := 0
	for _, it := range l.items {
		if itemEmpty(it) {
			continue
		}
		if len(it.inl) > 0 {
			n++
			marker := "• "
			if l.ordered {
				marker = strconv.Itoa(n) + ". "
			}
			*sb = append(*sb, strings.Repeat("  ", depth)+marker+inline(it.inl))
		}
		for _, s := range it.subs {
			plainList(sb, s, depth+1, inline)
		}
	}
}

// PlainText is the text of art without markup, for the text part of letters,
// <meta> and counters: paragraphs, headings and quotes are separated by a
// blank line, list items are "• x" or "1. x" lines and a link is
// "text (https://…)" unless its text is the address.
func PlainText(art string) string {
	d := parseART(art)
	var parts []string
	for _, b := range d.blocks {
		switch b.kind {
		case bPara, bH3, bH4:
			if len(b.inl) > 0 {
				parts = append(parts, plainInline(b.inl, "\n"))
			}
		case bQuote:
			for _, p := range b.paras {
				parts = append(parts, plainInline(p, "\n"))
			}
		case bList:
			var lines []string
			plainList(&lines, b.list, 0, func(c []chunk) string { return plainInline(c, " ") })
			if len(lines) > 0 {
				parts = append(parts, strings.Join(lines, "\n"))
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// ---- legacy ----

// wrapMark puts marker around s, moving the spaces at its borders outside, so
// "**a **" never appears.
func wrapMark(s, marker string) string {
	core := strings.TrimFunc(s, unicode.IsSpace)
	if core == "" {
		return s
	}
	i := strings.Index(s, core)
	return s[:i] + marker + core + marker + s[i+len(core):]
}

func legacyEm(chunks []chunk, br string) string {
	var sb strings.Builder
	for i := 0; i < len(chunks); {
		j := i
		for j < len(chunks) && chunks[j].em == chunks[i].em && !chunks[j].br == !chunks[i].br {
			j++
		}
		t := runText(chunks[i:j], br)
		if chunks[i].em && !chunks[i].br {
			t = wrapMark(t, "_")
		}
		sb.WriteString(t)
		i = j
	}
	return sb.String()
}

func legacyInline(chunks []chunk, br string) string {
	var sb strings.Builder
	for _, run := range runs(chunks) {
		var inner strings.Builder
		for i := 0; i < len(run); {
			j := i
			for j < len(run) && run[j].strong == run[i].strong && !run[j].br == !run[i].br {
				j++
			}
			t := legacyEm(run[i:j], br)
			if run[i].strong && !run[i].br {
				t = wrapMark(t, "**")
			}
			inner.WriteString(t)
			i = j
		}
		h := run[0].href
		switch {
		case h == "":
			sb.WriteString(inner.String())
		case sameLink(runText(run, br), h):
			sb.WriteString(inner.String())
		default:
			sb.WriteString(inner.String() + " (" + displayHref(h) + ")")
		}
	}
	return sb.String()
}

// ToLegacy is the reverse of FromLegacy for the old forms that cannot show
// markup: strong is **x**, em is _x_, lists are "- " and "1. " lines, a link
// is "text (url)" (or just the url), underline is dropped, and headings and
// quotes are paragraphs of their own.
func ToLegacy(art string) string {
	d := parseART(art)
	var parts []string
	for _, b := range d.blocks {
		switch b.kind {
		case bPara, bH3, bH4:
			if len(b.inl) > 0 {
				parts = append(parts, legacyInline(b.inl, "\n"))
			}
		case bQuote:
			for _, p := range b.paras {
				parts = append(parts, legacyInline(p, "\n"))
			}
		case bList:
			var lines []string
			plainListLegacy(&lines, b.list, 0)
			if len(lines) > 0 {
				parts = append(parts, strings.Join(lines, "\n"))
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func plainListLegacy(sb *[]string, l *list, depth int) {
	n := 0
	for _, it := range l.items {
		if itemEmpty(it) {
			continue
		}
		if len(it.inl) > 0 {
			n++
			marker := "- "
			if l.ordered {
				marker = strconv.Itoa(n) + ". "
			}
			*sb = append(*sb, strings.Repeat("  ", depth)+marker+legacyInline(it.inl, " "))
		}
		for _, s := range it.subs {
			plainListLegacy(sb, s, depth+1)
		}
	}
}

// ---- Telegram ----

// tgChunks prepares a line for Telegram: a heading is bold, a mailto link is
// text (Telegram links addresses itself), and breaks may be turned to spaces.
func tgChunks(in []chunk, bold, brSpace bool) []chunk {
	var out []chunk
	for _, run := range runs(in) {
		h := run[0].href
		mail := strings.HasPrefix(h, "mailto:")
		for _, c := range run {
			if c.br && brSpace {
				c = chunk{text: " "}
			}
			if bold {
				c.strong = true
			}
			if mail {
				c.href, c.linkID = "", 0
			}
			out = append(out, c)
		}
		if mail && !sameLink(runText(run, " "), h) {
			out = append(out, chunk{text: " (" + displayHref(h) + ")"})
		}
	}
	return out
}

func tgLine(chunks []chunk, bold, brSpace bool) string {
	var sb strings.Builder
	links := 0
	writeInline(&sb, tgChunks(chunks, bold, brSpace), writeOpts{tg: true}, &links)
	return sb.String()
}

func tgList(sb *[]string, l *list, depth int) {
	n := 0
	for _, it := range l.items {
		if itemEmpty(it) {
			continue
		}
		if len(it.inl) > 0 {
			n++
			marker := "• "
			if l.ordered {
				marker = strconv.Itoa(n) + ". "
			}
			*sb = append(*sb, strings.Repeat("  ", depth)+marker+tgLine(it.inl, false, true))
		}
		for _, s := range it.subs {
			tgList(sb, s, depth+1)
		}
	}
}

// ToTelegramHTML writes art in the markup Telegram's parse_mode=HTML accepts:
// b, i, u, a href and blockquote only. Paragraphs are separated by a blank
// line, a heading is a bold line, lists are "• " and "1. " lines, and the
// text is escaped (&lt; &gt; &amp;).
func ToTelegramHTML(art string) string {
	d := parseART(art)
	var parts []string
	for _, b := range d.blocks {
		switch b.kind {
		case bPara:
			if len(b.inl) > 0 {
				parts = append(parts, tgLine(b.inl, false, false))
			}
		case bH3, bH4:
			if len(b.inl) > 0 {
				parts = append(parts, tgLine(b.inl, true, false))
			}
		case bQuote:
			var lines []string
			for _, p := range b.paras {
				lines = append(lines, tgLine(p, false, false))
			}
			if len(lines) > 0 {
				parts = append(parts, "<blockquote>"+strings.Join(lines, "\n")+"</blockquote>")
			}
		case bList:
			var lines []string
			tgList(&lines, b.list, 0)
			if len(lines) > 0 {
				parts = append(parts, strings.Join(lines, "\n"))
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// Words is the sequence of words of the visible text of art: NFC-normalised
// and split on Unicode spaces and punctuation, so list markers, empty items
// and the markup itself add nothing. It is what the AI check compares to
// prove that no word changed.
func Words(art string) []string {
	d := parseART(art)
	var sb strings.Builder
	d.units(func(chunks []chunk) {
		for _, c := range chunks {
			if c.br {
				sb.WriteByte(' ')
				continue
			}
			sb.WriteString(c.text)
		}
		sb.WriteByte(' ')
	})
	s := norm.NFC.String(sb.String())
	return strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
}

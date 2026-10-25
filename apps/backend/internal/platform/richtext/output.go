package richtext

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/text/unicode/norm"
)

// Normalize is the single write point: it returns the ART to store for input
// in the given format. "text" (and an empty format) is the old plain text and
// goes through FromLegacy, "art" is HTML and goes through Sanitize, anything
// else is an error. The server never guesses the format from the tags. A
// "text" longer (in characters) than the profile's HTML limit is refused
// before any work is done, because it cannot fit; Sanitize does the same for
// "art" at four times the limit. An "art" input that has no block tag and no
// <br> first gets its paragraphs from AutoParagraph (WordPress texts).
// Normalize(Normalize(x, f, p), "art", p) == Normalize(x, f, p).
func Normalize(input, format string, p Profile) (string, error) {
	switch format {
	case "", FormatText:
		if p.MaxHTML > 0 && utf8.RuneCountInString(input) > p.MaxHTML {
			return "", ErrHTMLTooLong
		}
		if !utf8.ValidString(input) {
			return "", ErrBadEncoding
		}
		if len(input) > maxInputBytes {
			return "", ErrHTMLTooLong
		}
		return Sanitize(FromLegacy(input, p), p)
	case FormatART:
		if len(input) <= maxInputBytes {
			// WordPress texts have no <p>: blank lines are the paragraphs.
			input = AutoParagraph(input)
		}
		return Sanitize(input, p)
	}
	return "", ErrInvalidFormat
}

// Render is the single read point: the safe HTML of a stored text for HTML
// consumers. ART is sanitised again, text is converted by FromLegacy first,
// and every link gets rel="noopener noreferrer" target="_blank". Limits are
// not enforced (a text stored under an older limit must still show) and
// forbidden characters are dropped. It never returns unsafe HTML, even if
// stored is hostile, and never an empty string for a text that has content:
// when the text cannot be read (too large, nested too deeply) it returns its
// visible text, escaped, together with the error, so the caller can count or
// log the failure and the reader still sees the words.
func Render(stored, format string, p Profile) (string, error) {
	if len(stored) > maxInputBytes {
		return fallbackHTML(stored, format), ErrHTMLTooLong
	}
	var src string
	if format == FormatART {
		src = AutoParagraph(stored)
	} else {
		src = FromLegacy(stored, p)
	}
	d, err := convert(src, p.readOnly(), false)
	if err != nil {
		return fallbackHTML(stored, format), err
	}
	out, _ := d.html(writeOpts{blank: true})
	return out, nil
}

// fallbackHTML is the escaped visible text of a stored text that could not be
// converted: the text nodes of ART (or the plain text itself), forbidden
// characters removed, lines kept as <br>. It holds no markup of the author's.
func fallbackHTML(stored, format string) string {
	if len(stored) > maxInputBytes {
		stored = stored[:maxInputBytes]
	}
	stored = strings.ToValidUTF8(stored, "")
	text := stored
	if format == FormatART {
		text = markupText(stored)
		if strings.TrimSpace(text) == "" {
			text = stored // nothing readable: show the source as it is
		}
	}
	var lines []string
	var sb strings.Builder
	for _, r := range text {
		switch charPolicy(r) {
		case charRefuse, charStrip:
			continue
		case charLineSep, charParaSep:
			r = '\n'
		}
		sb.WriteRune(r)
	}
	for _, line := range strings.Split(strings.ReplaceAll(sb.String(), "\r", "\n"), "\n") {
		if line = strings.TrimFunc(line, isWS); line != "" {
			lines = append(lines, escapeText(line))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "<p>" + strings.Join(lines, "<br>") + "</p>"
}

// markupText is the text of the text nodes of markup: a <br> or a block tag
// becomes a line break and the content of dropped elements (script, style ...)
// is skipped. It uses the tokenizer, so it has no nesting limit.
func markupText(s string) string {
	z := html.NewTokenizer(strings.NewReader(s))
	var sb strings.Builder
	skip := "" // the dropped element whose content is being skipped
	for {
		switch z.Next() {
		case html.ErrorToken:
			return sb.String()
		case html.TextToken:
			if skip == "" {
				sb.Write(z.Text())
			}
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			tt := z.Token()
			a := atom.Lookup([]byte(tt.Data))
			switch {
			case skip != "":
				if tt.Type == html.EndTagToken && tt.Data == skip {
					skip = ""
				}
			case dropTags[a] && tt.Type == html.StartTagToken:
				skip = tt.Data
			case a == atom.Br || boundaryTags[a]:
				sb.WriteByte('\n')
			}
		}
	}
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

// Words is the sequence of tokens of the visible text of art, what the AI
// check compares to prove that no word and no sign changed. The text is
// NFC-normalised, then a token is a maximal run of letters, numbers and
// marks, and EVERY other character that is not a space (punctuation and
// symbols alike) is a token of its own, so "-20%" and "20%", "10,5" and "10 5"
// or a moved comma differ. Spaces, line breaks and the markup add nothing,
// except that an item of an ordered list adds the number and the dot it is
// shown with ("N", "."), so numbering cannot appear or vanish unseen. The
// items of a bulleted list add nothing.
func Words(art string) []string {
	d := parseART(art)
	var sb strings.Builder
	d.numberedUnits(func(num int, chunks []chunk) {
		if num > 0 {
			sb.WriteString(strconv.Itoa(num))
			sb.WriteString(". ")
		}
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
	words := []string{}
	start := -1 // start of the current run of letters, numbers and marks
	for i, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r):
			if start < 0 {
				start = i
			}
			continue
		case unicode.IsSpace(r):
			if start >= 0 {
				words = append(words, s[start:i])
				start = -1
			}
		default:
			if start >= 0 {
				words = append(words, s[start:i])
				start = -1
			}
			words = append(words, string(r))
		}
	}
	if start >= 0 {
		words = append(words, s[start:])
	}
	return words
}

// StripRefused removes the characters Render drops when it reads a stored text
// (control characters, direction overrides, tag characters, invisible
// operators). A writer of the old plain text uses it so that such a text is
// cleaned like Render would clean it, instead of being refused: older clients
// sent these characters (the tag characters of the England flag, a direction
// mark copied from Hebrew text) and were never told no. The line break family
// and the characters Sanitize keeps or strips on its own are left alone.
func StripRefused(s string) string {
	refused := false
	for _, r := range s {
		if charPolicy(r) == charRefuse {
			refused = true
			break
		}
	}
	if !refused {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		if charPolicy(r) != charRefuse {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

package eventbot

// textfmt.go — the pure part of "write the text as usual, the bot formats it
// and shows what came out" (spec 44, RT-05 and RT-06). The organizer types
// plain text; arena-api asks a model to add markup and proves in code that no
// word changed (RT-04); the bot shows the result in Telegram's HTML and the
// organizer decides. Nothing here talks to Telegram or to the API: the dialog
// that does is textfmt_dialog.go.
//
// The model's answer is a hint, never a fact. The route already checks it, and
// the bot checks again before it shows or stores anything (tfDecide): the same
// words, and no link that the organizer did not write. Whatever fails, the
// organizer still gets a valid text: the plain conversion of what they typed.

import (
	"html"
	"regexp"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

// tfKind is which text is being formatted; it picks the profile (the tags, the
// limits) and the name the API knows the kind by.
type tfKind string

const (
	tfKindDescription tfKind = "description"
	tfKindEmailNote   tfKind = "email_note"
)

func (k tfKind) profile() richtext.Profile {
	if k == tfKindEmailNote {
		return richtext.ProfileEmailNote
	}
	return richtext.ProfileDescription
}

// tfRefusal is a typed text the bot will not send to the model: the text key
// to answer with, and the numbers that key names.
type tfRefusal struct {
	Key   string
	Limit int
	Links int
}

// tfCheck cleans a typed text and converts it the plain way. clean is what is
// sent to the model (the old notation: **bold**, _italic_, "- " lists); plain
// is its ART, what "Without formatting" stores and what the model's words are
// compared with. A text the server would refuse comes back as a refusal, so
// the organizer hears it before a model call is spent.
func tfCheck(text string, kind tfKind) (clean, plain string, ref *tfRefusal) {
	p := kind.profile()
	clean = strings.TrimSpace(stripBidiEmbedding(text))
	if clean == "" {
		return "", "", &tfRefusal{Key: "bot.tf.err_generic"}
	}
	plain, err := richtext.Normalize(clean, richtext.FormatText, p)
	if err != nil {
		switch richtext.ReasonOf(err) {
		case richtext.ReasonTooLong, richtext.ReasonHTMLTooLong:
			return clean, "", &tfRefusal{Key: "bot.tf.err_too_long", Limit: p.MaxVisible}
		case richtext.ReasonTooManyLinks:
			return clean, "", &tfRefusal{Key: "bot.tf.err_links", Links: p.MaxLinks}
		case richtext.ReasonSchemeWord:
			return clean, "", &tfRefusal{Key: "bot.tf.err_scheme"}
		case richtext.ReasonControlChars, richtext.ReasonBadEncoding:
			return clean, "", &tfRefusal{Key: "bot.tf.err_chars"}
		}
		return clean, "", &tfRefusal{Key: "bot.tf.err_generic"}
	}
	if strings.TrimSpace(plain) == "" || richtext.VisibleLen(plain) == 0 {
		return clean, "", &tfRefusal{Key: "bot.tf.err_generic"}
	}
	return clean, plain, nil
}

// tfRefusalForReason maps the reason of a 422 the server answered when a text
// was stored (error.details.reason) to the same texts.
func tfRefusalForReason(reason string, kind tfKind) tfRefusal {
	p := kind.profile()
	switch richtext.Reason(reason) {
	case richtext.ReasonTooLong, richtext.ReasonHTMLTooLong:
		return tfRefusal{Key: "bot.tf.err_too_long", Limit: p.MaxVisible}
	case richtext.ReasonTooManyLinks:
		return tfRefusal{Key: "bot.tf.err_links", Links: p.MaxLinks}
	case richtext.ReasonSchemeWord:
		return tfRefusal{Key: "bot.tf.err_scheme"}
	case richtext.ReasonControlChars, richtext.ReasonBadEncoding:
		return tfRefusal{Key: "bot.tf.err_chars"}
	}
	return tfRefusal{Key: "bot.tf.err_generic"}
}

// tfOutcome is what the organizer is shown and may keep.
type tfOutcome struct {
	// Art is the text "Save as shown" stores: ART, formatted when Applied.
	Art string
	// Applied says Art carries the model's markup. When false Art is Plain and
	// the organizer is told, without error wording, that it is shown as typed.
	Applied bool
	// Reason is why the model's answer was not used (log only).
	Reason string
}

var hrefRE = regexp.MustCompile(`href="([^"]*)"`)

// artHrefs lists the link targets of an ART text, decoded.
func artHrefs(art string) []string {
	var out []string
	for _, m := range hrefRE.FindAllStringSubmatch(art, -1) {
		out = append(out, html.UnescapeString(m[1]))
	}
	return out
}

// tfDecide turns the route's answer into the text to show. plain is the ART of
// the typed text (tfCheck), typed the cleaned text the model was sent. A call
// that failed (err), or whose answer the route did not keep, or whose answer
// does not hold up here, gives the plain text: valid ART in every case.
func tfDecide(typed, plain string, res FormattedText, err error, kind tfKind) tfOutcome {
	fallback := func(reason string) tfOutcome { return tfOutcome{Art: plain, Reason: reason} }
	if err != nil {
		return fallback("unavailable")
	}
	if !res.AIApplied {
		if res.Reason != "" {
			return fallback(res.Reason)
		}
		return fallback("not_applied")
	}
	art, nerr := richtext.Normalize(res.HTML, richtext.FormatART, kind.profile())
	if nerr != nil {
		return fallback("invalid_answer")
	}
	// The route proved this already; the bot does not take its word for it.
	if !slices.Equal(richtext.Words(art), richtext.Words(plain)) {
		return fallback("words_changed")
	}
	for _, href := range artHrefs(art) {
		if !strings.Contains(typed, strings.TrimPrefix(href, "mailto:")) {
			return fallback("link_not_typed")
		}
	}
	return tfOutcome{Art: art, Applied: true}
}

// ─── Telegram messages ────────────────────────────────────────────────────────

// tgMessageMax is the longest text the bot puts in one message: Telegram
// counts 4096 characters after the markup is read, and a margin is kept.
const tgMessageMax = 4000

// tgParsedLen is the length Telegram counts for an HTML text: the characters
// outside the tags, an entity as one, in UTF-16 units.
func tgParsedLen(s string) int {
	n := 0
	for i := 0; i < len(s); {
		switch s[i] {
		case '<':
			j := strings.IndexByte(s[i:], '>')
			if j < 0 {
				return n + len(utf16.Encode([]rune(s[i:])))
			}
			i += j + 1
		case '&':
			if j := strings.IndexByte(s[i:], ';'); j > 0 && j <= 8 {
				n++
				i += j + 1
			} else {
				n++
				i++
			}
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			if r >= 0x10000 {
				n += 2
			} else {
				n++
			}
			i += size
		}
	}
	return n
}

// tgSplit cuts Telegram HTML into messages of at most max characters, between
// paragraphs (a blank line), so a tag is never cut in half: the markup of
// richtext.ToTelegramHTML never spans a blank line. A paragraph longer than
// max is cut between its lines, and a line longer than that as plain text.
func tgSplit(s string, max int) []string {
	if tgParsedLen(s) <= max {
		return []string{s}
	}
	var out []string
	cur := ""
	flush := func() {
		if cur != "" {
			out = append(out, cur)
			cur = ""
		}
	}
	for _, block := range strings.Split(s, "\n\n") {
		if tgParsedLen(block) > max {
			flush()
			out = append(out, tgSplitBlock(block, max)...)
			continue
		}
		if cur != "" && tgParsedLen(cur)+2+tgParsedLen(block) > max {
			flush()
		}
		if cur == "" {
			cur = block
		} else {
			cur += "\n\n" + block
		}
	}
	flush()
	return out
}

// tgSplitBlock cuts one long paragraph (a list, a quotation) between its lines.
func tgSplitBlock(block string, max int) []string {
	open, shut := "", ""
	if strings.HasPrefix(block, "<blockquote>") && strings.HasSuffix(block, "</blockquote>") {
		open, shut = "<blockquote>", "</blockquote>"
		block = strings.TrimSuffix(strings.TrimPrefix(block, open), shut)
	}
	var out []string
	cur := ""
	flush := func() {
		if cur != "" {
			out = append(out, open+cur+shut)
			cur = ""
		}
	}
	for _, line := range strings.Split(block, "\n") {
		if tgParsedLen(line) > max {
			flush()
			for _, piece := range tgHardCut(line, max) {
				out = append(out, open+piece+shut)
			}
			continue
		}
		if cur != "" && tgParsedLen(cur)+1+tgParsedLen(line) > max {
			flush()
		}
		if cur == "" {
			cur = line
		} else {
			cur += "\n" + line
		}
	}
	flush()
	return out
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

// tgHardCut cuts one very long line into plain pieces of at most max
// characters: its tags are dropped (a cut tag would make Telegram refuse the
// message), its entities stay whole.
func tgHardCut(line string, max int) []string {
	s := tagRE.ReplaceAllString(line, "")
	var out []string
	start, n := 0, 0
	for i := 0; i < len(s); {
		size, width := 1, 1
		if s[i] == '&' {
			if j := strings.IndexByte(s[i:], ';'); j > 0 && j <= 8 {
				size = j + 1
			}
		} else {
			r, sz := utf8.DecodeRuneInString(s[i:])
			size = sz
			if r >= 0x10000 {
				width = 2
			}
		}
		if n+width > max {
			out = append(out, s[start:i])
			start, n = i, 0
		}
		n += width
		i += size
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// tfScreens lays out the preview: the texts to send first, then the one
// message that carries the buttons. The preview, the note and the question go
// together in one message while that fits; a longer preview goes in messages of
// its own and the buttons follow in a short message after it, so no message
// passes Telegram's limit (spec 44, RT-05). note may be empty.
func tfScreens(previewHTML, note, question string) (texts []string, last string) {
	last = note + question
	if tgParsedLen(previewHTML+"\n\n"+last) <= tgMessageMax {
		return nil, previewHTML + "\n\n" + last
	}
	return tgSplit(previewHTML, tgMessageMax), last
}

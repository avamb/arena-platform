// Package eventnote is the organizer's own text of an event that the buyer
// letters print (EC-31, task 7-1): a few paragraphs the organizer wants every
// buyer of the event to read ("come 30 minutes early", "the cloakroom is
// free").
//
// The text is plain text. It is stored in event_email_notes (migration 0161)
// after Normalize, which is the ONLY server-side filter: no HTML, no control
// characters, at most MaxLinks links and every link an https:// address. The
// letter templates get the note through Parse as paragraphs of text and link
// segments, never as markup, so a link can only reach a letter as an <a href>
// that html/template has checked.
package eventnote

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxRunes is the longest note, in characters. The table's CHECK mirrors
	// it, and a test reads the migration to keep the two equal.
	MaxRunes = 2000
	// MaxLinks is how many links one note may carry.
	MaxLinks = 5
	// MaxLinkLength bounds one link.
	MaxLinkLength = 500
)

// Reasons a note is refused. Handlers map every one of them to the same
// 400/422 code and put Reason into the error details.
var (
	ErrTooLong      = errors.New("the note is longer than 2000 characters")
	ErrControlChars = errors.New("the note contains control characters")
	ErrBadEncoding  = errors.New("the note is not valid UTF-8")
	ErrTooManyLinks = errors.New("the note has more than 5 links")
	ErrUnsafeLink   = errors.New("a link must start with https:// and name a site")
	ErrScheme       = errors.New("the note contains a link scheme that is not allowed")
)

// Reason is a short stable word for err, for the API's error details.
func Reason(err error) string {
	switch {
	case errors.Is(err, ErrTooLong):
		return "too_long"
	case errors.Is(err, ErrControlChars):
		return "control_characters"
	case errors.Is(err, ErrBadEncoding):
		return "bad_encoding"
	case errors.Is(err, ErrTooManyLinks):
		return "too_many_links"
	case errors.Is(err, ErrUnsafeLink):
		return "unsafe_link"
	case errors.Is(err, ErrScheme):
		return "scheme_not_allowed"
	}
	return "invalid"
}

var (
	// tagRE matches a tag, a comment/doctype or a processing instruction. A
	// lone "<" in prose ("a < b") is not a tag and stays.
	tagRE = regexp.MustCompile(`</?[A-Za-z][^<>]*>|<![^<>]*>|<\?[^<>]*>`)
	// schemeRE finds a scheme that can run code or load a local or opaque
	// resource, or the plain-http scheme. The character after the colon must
	// be a non-space, so "Metadata: bring a pass" is not a hit.
	schemeRE = regexp.MustCompile(`(?i)\b(?:javascript|vbscript|data|file|blob|about|http):\S`)
	// linkRE finds an https link. Trailing punctuation is trimmed afterwards.
	linkRE = regexp.MustCompile(`(?i)https://[^\s<>"'` + "`" + `]*`)
	// manyBlankRE folds three or more line breaks into one blank line.
	manyBlankRE = regexp.MustCompile(`\n{3,}`)
)

// trailingPunct is cut off the end of a link: it belongs to the sentence.
const trailingPunct = ".,;:!?)]}»”’"

// Normalize filters raw and returns the text to store. An empty result means
// "no note" (the caller clears the stored one). It is idempotent:
// Normalize(Normalize(x)) == Normalize(x).
func Normalize(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", ErrBadEncoding
	}
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\t", " ")
	for _, r := range s {
		if r == '\n' {
			continue
		}
		if unicode.IsControl(r) || isBidiControl(r) || r == 0x2028 || r == 0x2029 {
			return "", ErrControlChars
		}
	}
	// No HTML: remove tags until nothing more is removed (a removal can join
	// the two halves of a new tag).
	for {
		next := tagRE.ReplaceAllString(s, "")
		if next == s {
			break
		}
		s = next
	}
	if schemeRE.MatchString(s) {
		return "", ErrScheme
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	s = strings.TrimSpace(strings.Join(lines, "\n"))
	s = manyBlankRE.ReplaceAllString(s, "\n\n")
	if utf8.RuneCountInString(s) > MaxRunes {
		return "", ErrTooLong
	}
	links := findLinks(s)
	if len(links) > MaxLinks {
		return "", ErrTooManyLinks
	}
	for _, l := range links {
		if !safeLink(l.url) {
			return "", ErrUnsafeLink
		}
	}
	return s, nil
}

func isBidiControl(r rune) bool {
	return (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) ||
		r == 0x200e || r == 0x200f || r == 0xfeff
}

type linkSpan struct {
	start, end int // byte offsets in the searched string
	url        string
}

// findLinks locates the https links of s, with trailing punctuation cut off.
func findLinks(s string) []linkSpan {
	var out []linkSpan
	for _, m := range linkRE.FindAllStringIndex(s, -1) {
		end := m[1]
		// Cut by rune: some closing quotes are multi-byte.
		for end > m[0] {
			r, size := utf8.DecodeLastRuneInString(s[m[0]:end])
			if r == utf8.RuneError || !strings.ContainsRune(trailingPunct, r) {
				break
			}
			end -= size
		}
		out = append(out, linkSpan{start: m[0], end: end, url: s[m[0]:end]})
	}
	return out
}

func safeLink(raw string) bool {
	if len(raw) > MaxLinkLength {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil {
		return false
	}
	host := u.Hostname()
	return host != "" && !strings.ContainsAny(host, " \\")
}

// Segment is a run of a paragraph: plain text, or a link when URL is set
// (Text is then the link's visible text, the address itself).
type Segment struct {
	Text string
	URL  string
}

// Paragraph is the segments of one paragraph, in order.
type Paragraph []Segment

// Block is a note ready for the letter templates. The zero Block prints
// nothing: templates guard it with {{with .EventNote.Paragraphs}}.
type Block struct {
	// Paragraphs is the HTML part: split at blank lines.
	Paragraphs []Paragraph
	// Plain is the text part: the stored note as it is.
	Plain string
}

// Empty reports whether the block prints nothing.
func (b Block) Empty() bool { return len(b.Paragraphs) == 0 }

// Parse turns a stored note into a Block. It never fails: the note was
// filtered when it was stored, and anything that does not look like a safe
// link is left as text. An empty or blank note gives the zero Block.
func Parse(note string) Block {
	note = strings.TrimSpace(note)
	if note == "" {
		return Block{}
	}
	var paras []Paragraph
	for _, chunk := range strings.Split(note, "\n\n") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		paras = append(paras, segments(chunk))
	}
	if len(paras) == 0 {
		return Block{}
	}
	return Block{Paragraphs: paras, Plain: note}
}

func segments(s string) Paragraph {
	var out Paragraph
	pos := 0
	n := 0
	for _, l := range findLinks(s) {
		if n >= MaxLinks || !safeLink(l.url) {
			continue
		}
		if l.start > pos {
			out = append(out, Segment{Text: s[pos:l.start]})
		}
		out = append(out, Segment{Text: l.url, URL: l.url})
		pos = l.end
		n++
	}
	if pos < len(s) {
		out = append(out, Segment{Text: s[pos:]})
	}
	return out
}

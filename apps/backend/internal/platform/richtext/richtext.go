// Package richtext is the platform's one filter and converter for formatted
// texts (spec 44, RT-01): the description of an event, the organizer's own
// text of the buyer letters and the message about a moved or cancelled
// session.
//
// The stored format is "text Arena" (ART): a small HTML subset (p, br, strong,
// em, u, a[href], ul, ol, li and, for the description only, h3, h4 and
// blockquote). Sanitize is the ONLY way to produce it from foreign HTML,
// FromLegacy turns the old "**bold**, _italic_, - list" plain text into it,
// Normalize is the single write point that picks one of the two by the
// format flag, and Render is the single read point. PlainText, ToLegacy,
// ToTelegramHTML and Words read ART for the other consumers.
//
// The profile data (tags, schemes, limits) lives in the profiles table below,
// so the PHP and JS ports can mirror it.
package richtext

import (
	"errors"
	"slices"
)

// Format flags of a stored text. The server never guesses the format from the
// presence of tags.
const (
	FormatText = "text"
	FormatART  = "art"
)

// MaxLinkLength bounds the href of one link.
const MaxLinkLength = 500

// maxInputBytes bounds the raw input of Sanitize, so a hostile megabyte of
// markup cannot cost much.
const maxInputBytes = 1 << 20

// maxDepth bounds the element nesting the sanitizer walks.
const maxDepth = 200

// Profile says what one kind of text may contain. The zero limits mean "no
// limit" and are used for reading only.
type Profile struct {
	// Name is the stable name of the profile, used by the golden corpus.
	Name string
	// Tags is the white list of output tags. b and i are accepted as input
	// aliases of strong and em whenever those are listed.
	Tags []string
	// Schemes is the list of link schemes: "https" and, for the description,
	// "mailto".
	Schemes []string
	// MaxVisible is the longest visible text, in characters.
	MaxVisible int
	// MaxHTML is the longest stored HTML, in characters.
	MaxHTML int
	// MaxLinks is how many links the text may carry.
	MaxLinks int
	// AutoLink makes FromLegacy turn bare https:// addresses into links (at
	// most MaxLinks of them).
	AutoLink bool
	// StrictSchemeWords rejects a visible text that holds a "javascript:",
	// "data:", "http:" ... word, as the old email note did.
	StrictSchemeWords bool
}

const (
	// ProfileNameDescription is the description of an event.
	ProfileNameDescription = "description"
	// ProfileNameEmailNote is the organizer's own text in buyer letters.
	ProfileNameEmailNote = "email_note"
	// ProfileNameSessionMessage is the message about a moved or cancelled
	// session.
	ProfileNameSessionMessage = "session_message"
)

// The profiles table. Keep it in step with the PHP and JS ports.
var (
	// ProfileDescription is the description of an event.
	ProfileDescription = Profile{
		Name:       ProfileNameDescription,
		Tags:       []string{"p", "br", "strong", "em", "u", "a", "ul", "ol", "li", "h3", "h4", "blockquote"},
		Schemes:    []string{"https", "mailto"},
		MaxVisible: 4000,
		MaxHTML:    16000,
		MaxLinks:   20,
	}
	// ProfileEmailNote is the organizer's own text of the buyer letters.
	ProfileEmailNote = Profile{
		Name:              ProfileNameEmailNote,
		Tags:              []string{"p", "br", "strong", "em", "u", "a", "ul", "ol", "li"},
		Schemes:           []string{"https"},
		MaxVisible:        2000,
		MaxHTML:           8000,
		MaxLinks:          5,
		AutoLink:          true,
		StrictSchemeWords: true,
	}
	// ProfileSessionMessage is the message about a moved or cancelled session.
	ProfileSessionMessage = Profile{
		Name:       ProfileNameSessionMessage,
		Tags:       []string{"p", "br", "strong", "em", "u", "a", "ul", "ol", "li"},
		Schemes:    []string{"https"},
		MaxVisible: 1000,
		MaxHTML:    4000,
		MaxLinks:   3,
	}
)

// ProfileByName returns the predefined profile with that name.
func ProfileByName(name string) (Profile, bool) {
	switch name {
	case ProfileNameDescription:
		return ProfileDescription, true
	case ProfileNameEmailNote:
		return ProfileEmailNote, true
	case ProfileNameSessionMessage:
		return ProfileSessionMessage, true
	}
	return Profile{}, false
}

func (p Profile) has(tag string) bool { return slices.Contains(p.Tags, tag) }

func (p Profile) allowsScheme(s string) bool { return slices.Contains(p.Schemes, s) }

// readOnly is p without its limits: Render must show a stored text even when
// it is longer than a limit that was set later.
func (p Profile) readOnly() Profile {
	p.MaxVisible, p.MaxHTML, p.MaxLinks = 0, 0, 0
	p.StrictSchemeWords = false
	return p
}

// anyProfile is the most permissive profile, for the readers of ART that get
// no profile (PlainText, ToLegacy, ToTelegramHTML, Words).
func anyProfile() Profile {
	p := ProfileDescription
	p.Name = ""
	p.MaxVisible, p.MaxHTML, p.MaxLinks = 0, 0, 0
	return p
}

// Reason is a short stable word for why a text is refused, for the API's
// error details.
type Reason string

// The reasons.
const (
	ReasonTooLong       Reason = "too_long"
	ReasonHTMLTooLong   Reason = "html_too_long"
	ReasonTooManyLinks  Reason = "too_many_links"
	ReasonControlChars  Reason = "control_characters"
	ReasonBadEncoding   Reason = "bad_encoding"
	ReasonSchemeWord    Reason = "scheme_not_allowed"
	ReasonTooDeep       Reason = "too_deep"
	ReasonInvalidFormat Reason = "invalid_format"
)

// Error is a refusal with its Reason.
type Error struct {
	Reason Reason
}

func (e *Error) Error() string {
	switch e.Reason {
	case ReasonTooLong:
		return "richtext: the visible text is too long"
	case ReasonHTMLTooLong:
		return "richtext: the stored HTML is too long"
	case ReasonTooManyLinks:
		return "richtext: too many links"
	case ReasonControlChars:
		return "richtext: the text contains control, bidi or zero-width characters"
	case ReasonBadEncoding:
		return "richtext: the text is not valid UTF-8"
	case ReasonSchemeWord:
		return "richtext: the text contains a link scheme that is not allowed"
	case ReasonTooDeep:
		return "richtext: the markup is nested too deeply"
	case ReasonInvalidFormat:
		return "richtext: the format must be text or art"
	}
	return "richtext: invalid text"
}

// Is makes errors.Is(err, ErrTooLong) work for any *Error with that reason.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Reason == e.Reason
}

// Sentinels for errors.Is.
var (
	ErrTooLong       = &Error{ReasonTooLong}
	ErrHTMLTooLong   = &Error{ReasonHTMLTooLong}
	ErrTooManyLinks  = &Error{ReasonTooManyLinks}
	ErrControlChars  = &Error{ReasonControlChars}
	ErrBadEncoding   = &Error{ReasonBadEncoding}
	ErrSchemeWord    = &Error{ReasonSchemeWord}
	ErrTooDeep       = &Error{ReasonTooDeep}
	ErrInvalidFormat = &Error{ReasonInvalidFormat}
)

// ReasonOf is the short stable word for err, "invalid" for a foreign error.
func ReasonOf(err error) Reason {
	var e *Error
	if errors.As(err, &e) {
		return e.Reason
	}
	return "invalid"
}

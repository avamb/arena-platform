package textformat

import (
	"errors"
	"slices"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

// Reason says why the answer of the model was not used. The values are part
// of the API (the reason of a fallback answer) and of the logs.
type Reason string

// The reasons Verify gives. The route adds not_configured, quota, timeout and
// model_error for the failures that happen before there is an answer.
const (
	// ReasonEmpty: the answer holds no text.
	ReasonEmpty Reason = "empty_output"
	// ReasonInvalidMarkup: the answer cannot be cleaned (forbidden
	// characters, markup nested too deeply, a scheme that is not allowed).
	ReasonInvalidMarkup Reason = "invalid_markup"
	// ReasonTextChanged: the visible words, numbers or signs differ from the
	// organizer's text.
	ReasonTextChanged Reason = "text_changed"
	// ReasonLimits: the answer passes the checks but not the limits of the
	// profile (too long, too many links).
	ReasonLimits Reason = "limits"
)

// maxMarksPerParagraph is how many emphasised fragments one paragraph keeps.
const maxMarksPerParagraph = 3

// Verify is the code that decides whether the model's answer may be used. It
// returns the ART to use and an empty Reason, or an empty string and the
// Reason the caller falls back for.
//
//  1. html is cleaned by richtext.Sanitize for the profile: markup outside
//     the white list is gone, a link outside the profile's schemes loses its
//     tag, forbidden characters refuse the answer.
//  2. The words, numbers and signs of the result equal those of the input as
//     the old notation reads it (richtext.FromLegacy: the **bold** markers are
//     markup, not text). Not one word, digit, minus, comma or symbol may
//     move, and a bulleted list cannot become a numbered one (the numbers
//     are part of the words).
//  3. Every link target appears in the input and the visible text of the link
//     is that address; a link that does not is unwrapped and its text stays.
//  4. At most three emphasised fragments stay in a paragraph (the rest is
//     unwrapped), and an anchor without text is no anchor.
//  5. The result is cleaned once more, so the limits of the profile hold.
func Verify(input, html string, p richtext.Profile) (string, Reason) {
	if strings.TrimSpace(html) == "" {
		return "", ReasonEmpty
	}
	sanitized, err := richtext.Sanitize(html, p)
	if err != nil {
		return "", sanitizeReason(err)
	}
	if sanitized == "" {
		return "", ReasonEmpty
	}
	want := richtext.Words(richtext.FromLegacy(input, p))
	if !slices.Equal(richtext.Words(sanitized), want) {
		return "", ReasonTextChanged
	}
	fixed, err := richtext.Constrain(sanitized, p, richtext.ConstrainOptions{
		MaxMarksPerUnit: maxMarksPerParagraph,
		KeepLink:        linkKeeper(input),
	})
	if err != nil {
		return "", sanitizeReason(err)
	}
	// Unwrapping only drops tags; the words are checked again so that no
	// future change of Constrain can slip a change of text through.
	if fixed == "" || !slices.Equal(richtext.Words(fixed), want) {
		return "", ReasonTextChanged
	}
	return fixed, ""
}

func sanitizeReason(err error) Reason {
	if errors.Is(err, richtext.ErrTooLong) || errors.Is(err, richtext.ErrHTMLTooLong) || errors.Is(err, richtext.ErrTooManyLinks) {
		return ReasonLimits
	}
	return ReasonInvalidMarkup
}

// linkKeeper keeps a link only when its visible text is its own address and
// that address is written in the organizer's text. The case of the address is
// not compared: the host of a canonical link is lower case, and the text of
// the link was already proven to be the organizer's by the words check. An
// address ending in a sentence sign is a sentence, not a link.
func linkKeeper(input string) func(text, href string) bool {
	lower := strings.ToLower(input)
	return func(text, href string) bool {
		addr := strings.TrimPrefix(href, "mailto:")
		if addr == "" || strings.ContainsAny(addr[len(addr)-1:], ".,;:!?") {
			return false
		}
		if !strings.EqualFold(text, addr) {
			return false
		}
		return strings.Contains(lower, strings.ToLower(addr))
	}
}

// The reasons of a fallback that happens before there is an answer to verify.
// The route owns them; they live here so that the API's list of reasons is in
// one place.
const (
	// ReasonNotConfigured: the server holds no model key.
	ReasonNotConfigured Reason = "not_configured"
	// ReasonQuota: the person or the organization used its formats of the day.
	ReasonQuota Reason = "quota"
	// ReasonTimeout: the model did not answer in time.
	ReasonTimeout Reason = "timeout"
	// ReasonModelError: the model answered with an error or with nothing usable.
	ReasonModelError Reason = "model_error"
	// ReasonNoText: the text is blank, so there is nothing to format.
	ReasonNoText Reason = "no_text"
)

package textformat

import (
	"errors"
	"slices"
	"strings"

	"golang.org/x/text/unicode/norm"

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
	// ReasonLinksLost: the profile turns bare https:// addresses into links
	// (the buyer letters), and the answer has fewer links than the plain
	// conversion of the text, so using it would make an address unclickable.
	ReasonLinksLost Reason = "links_lost"
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
//  3. A link is kept only when it is one of the addresses written in the
//     input (the https:// scanner of FromLegacy and an e-mail scanner, see
//     richtext.LinkCandidates): its target is exactly the canonical target of
//     that address, its visible text is that address byte for byte (only the
//     case of the scheme and the host may differ), and the characters around
//     it do not make it a piece of a longer address (richtext.AddressBoundary).
//     Any other link is unwrapped and its text stays.
//  4. At most three emphasised fragments stay in a paragraph (the rest is
//     unwrapped), and an anchor without text is no anchor.
//  5. The result is cleaned once more, so the limits of the profile hold.
//  6. For a profile that links bare https:// addresses itself the result must
//     keep at least as many links as FromLegacy makes, else ReasonLinksLost.
//
// The answer is NFC-normalised first, so what is stored does not depend on
// the normalisation form the model happened to write.
//
// Known, accepted risk: Words does not compare where the items of a bulleted
// list begin and end, so a model may join or split the items of a "- " list
// ("- 20" and "- 30 people" as one paragraph "20 30 people") without the
// check seeing it. Spec 44 accepts this; the words themselves never change.
func Verify(input, html string, p richtext.Profile) (string, Reason) {
	html = norm.NFC.String(html)
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
	legacy := richtext.FromLegacy(input, p)
	want := richtext.Words(legacy)
	if !slices.Equal(richtext.Words(sanitized), want) {
		return "", ReasonTextChanged
	}
	fixed, err := richtext.Constrain(sanitized, p, richtext.ConstrainOptions{
		MaxMarksPerUnit: maxMarksPerParagraph,
		KeepLink:        linkKeeper(input, p),
	})
	if err != nil {
		return "", sanitizeReason(err)
	}
	// Unwrapping only drops tags; the words are checked again so that no
	// future change of Constrain can slip a change of text through.
	if fixed == "" || !slices.Equal(richtext.Words(fixed), want) {
		return "", ReasonTextChanged
	}
	if p.AutoLink && richtext.LinkCount(fixed) < richtext.LinkCount(legacy) {
		return "", ReasonLinksLost
	}
	return fixed, ""
}

func sanitizeReason(err error) Reason {
	if errors.Is(err, richtext.ErrTooLong) || errors.Is(err, richtext.ErrHTMLTooLong) || errors.Is(err, richtext.ErrTooManyLinks) {
		return ReasonLimits
	}
	return ReasonInvalidMarkup
}

// linkKeeper keeps a link only when it is one of the addresses written in the
// organizer's text: href is exactly the canonical target of the address, the
// visible text of the link is the address byte for byte (the scheme and the
// host are compared without case, the path, the query and the mailbox name
// are not: bit.ly/AbC and bit.ly/abc are two addresses), and the characters
// next to the link do not continue the address.
func linkKeeper(input string, p richtext.Profile) func(text, href, before, after string) bool {
	cands := richtext.LinkCandidates(input, p)
	return func(text, href, before, after string) bool {
		if !richtext.AddressBoundary(before, after) {
			return false
		}
		for _, c := range cands {
			if href == c.Href && sameAddress(text, norm.NFC.String(c.Text)) {
				return true
			}
		}
		return false
	}
}

// sameAddress compares two written addresses: equal bytes, except that the
// scheme and the host may differ in case.
func sameAddress(a, b string) bool {
	return foldHead(a) == foldHead(b)
}

// foldHead lower-cases the scheme and the host of a written address ("https://"
// up to the first "/", "?" or "#", or what follows the "@" of an e-mail
// address) and leaves the rest as it is.
func foldHead(s string) string {
	if len(s) >= 8 && strings.EqualFold(s[:8], "https://") {
		end := strings.IndexAny(s[8:], "/?#")
		if end < 0 {
			return strings.ToLower(s)
		}
		return strings.ToLower(s[:8+end]) + s[8+end:]
	}
	if at := strings.LastIndexByte(s, '@'); at >= 0 {
		return s[:at+1] + strings.ToLower(s[at+1:])
	}
	return s
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

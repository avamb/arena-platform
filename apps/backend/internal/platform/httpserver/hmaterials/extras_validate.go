package hmaterials

import (
	"math"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventmaterials"
)

// NormalizePartnerURL checks the address of a "where else to buy" link and
// returns it ready to store. It accepts only an absolute https address with
// a host that contains a dot and no user name or password; whitespace at
// either end is trimmed (a pasted link often carries it), whitespace or a
// control character inside is refused, and so is http, javascript:, data:
// and everything else. A scheme written in capitals is lowered, because the
// database CHECK compares the prefix case-sensitively.
func NormalizePartnerURL(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || utf8.RuneCountInString(s) > eventmaterials.MaxURLLength {
		return "", false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", false
		}
	}
	const scheme = "https://"
	if len(s) <= len(scheme) || !strings.EqualFold(s[:len(scheme)], scheme) {
		return "", false
	}
	s = scheme + s[len(scheme):]
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" {
		return "", false
	}
	host := strings.TrimSuffix(u.Hostname(), ".")
	if host == "" || !strings.Contains(host, ".") || strings.HasPrefix(host, ".") {
		return "", false
	}
	return s, true
}

// SellerNameFromURL derives the seller shown beside a partner link from its
// address: the host in lower case without a leading "www." (for example
// "ticketportal.cz"). It returns "" when the address has no host.
func SellerNameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	host = strings.TrimPrefix(host, "www.")
	return truncateRunes(host, eventmaterials.MaxSellerNameRunes)
}

// NormalizeSellerName trims a seller name the client typed and collapses
// inner runs of whitespace. ok is false for a blank name, one over the limit
// or one carrying a control character.
func NormalizeSellerName(raw string) (string, bool) {
	s := strings.Join(strings.Fields(raw), " ")
	if s == "" || utf8.RuneCountInString(s) > eventmaterials.MaxSellerNameRunes || hasControl(s) {
		return "", false
	}
	return s, true
}

// NormalizeCaption trims a photo caption. A blank caption is "no caption"
// (nil). ok is false for one over the limit or with a control character.
func NormalizeCaption(raw string) (*string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, true
	}
	if utf8.RuneCountInString(s) > eventmaterials.MaxCaptionRunes || hasControl(s) {
		return nil, false
	}
	return &s, true
}

// NormalizeMapLabel trims the label of the map point; blank means no label.
func NormalizeMapLabel(raw string) (*string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, true
	}
	if utf8.RuneCountInString(s) > eventmaterials.MaxMapLabelRunes || hasControl(s) {
		return nil, false
	}
	return &s, true
}

// ValidMapPoint reports whether the coordinates are finite and inside the
// range of a real place.
func ValidMapPoint(lat, lon float64) bool {
	if math.IsNaN(lat) || math.IsInf(lat, 0) || math.IsNaN(lon) || math.IsInf(lon, 0) {
		return false
	}
	return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

// coordString renders a coordinate with the six decimals of numeric(9,6).
func coordString(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

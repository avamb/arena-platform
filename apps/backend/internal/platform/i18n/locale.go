// Package i18n provides locale negotiation and message translation helpers for
// the arena_new platform. It implements the "Accept-Language → ?lang= →
// user.preferred_locale → default" resolution chain described in app_spec.txt.
//
// For this milestone the supported locales are "en" and "ru". The package is
// designed to scale: callers pass the supported set explicitly so adding a new
// locale requires only a catalog entry, not a code change here.
package i18n

import (
	"strings"
)

// SupportedLocales is the baseline set used when the caller does not supply
// an explicit list. It matches the ACTIVE_LOCALES default in config.go.
var SupportedLocales = []string{"en", "ru"}

// DefaultLocale is used as the final fallback when no supported locale can be
// matched. It must appear in SupportedLocales.
const DefaultLocale = "en"

// NegotiateLocale resolves the best-match locale using the following priority
// chain (first non-empty match wins):
//
//  1. Accept-Language HTTP header (RFC 7231 §5.3.5 with quality factors).
//  2. lang query parameter (caller must pass it pre-extracted).
//  3. preferred (caller-supplied user preference, e.g. from a JWT claim or DB).
//  4. defaultLocale (the configured DEFAULT_LOCALE environment variable).
//
// Only locales present in the supported slice are considered; all other tags
// are silently ignored. If no match is found from any source, defaultLocale is
// returned, guaranteeing a non-empty result.
func NegotiateLocale(acceptLang, lang, preferred, defaultLocale string, supported []string) string {
	if len(supported) == 0 {
		supported = SupportedLocales
	}
	if defaultLocale == "" {
		defaultLocale = DefaultLocale
	}

	// 1. Accept-Language header
	if acceptLang != "" {
		if l := fromAcceptLanguage(acceptLang, supported); l != "" {
			return l
		}
	}

	// 2. ?lang= query parameter
	if lang != "" {
		if l := canonicalize(strings.TrimSpace(lang), supported); l != "" {
			return l
		}
	}

	// 3. User preferred locale
	if preferred != "" {
		if l := canonicalize(strings.TrimSpace(preferred), supported); l != "" {
			return l
		}
	}

	// 4. Configured default
	if l := canonicalize(defaultLocale, supported); l != "" {
		return l
	}

	// Absolute fallback — should never be reached in a correctly configured
	// deployment but prevents an empty string from escaping the package.
	return DefaultLocale
}

// FromRequest is a convenience wrapper that reads the Accept-Language header
// and ?lang= parameter from an HTTP request, then calls NegotiateLocale.
// preferred is optional (e.g. from the authenticated actor's stored locale).
func FromRequest(acceptLang, lang, preferred, defaultLocale string, supported []string) string {
	return NegotiateLocale(acceptLang, lang, preferred, defaultLocale, supported)
}

// fromAcceptLanguage parses the Accept-Language header value and returns the
// highest-quality supported locale. Returns "" when no supported locale is
// found.
//
// The parser handles the common "lang;q=0.9, lang2;q=0.8" shape. Quality
// factors are honoured; full BCP-47 sub-tags (e.g. "ru-RU") are reduced to
// their primary language subtag ("ru").
func fromAcceptLanguage(header string, supported []string) string {
	best := ""
	bestQ := -1.0

	for _, raw := range strings.Split(header, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		segs := strings.Split(raw, ";")
		tag := strings.ToLower(strings.TrimSpace(segs[0]))
		// Reduce "en-US" → "en", "zh-Hant-TW" → "zh".
		if dash := strings.IndexByte(tag, '-'); dash > 0 {
			tag = tag[:dash]
		}

		q := 1.0
		for _, p := range segs[1:] {
			p = strings.TrimSpace(p)
			if strings.HasPrefix(p, "q=") {
				if v, err := parseQuality(p[2:]); err == nil {
					q = v
				}
			}
		}

		if !inSupported(tag, supported) {
			continue
		}
		if q > bestQ {
			best = tag
			bestQ = q
		}
	}
	return best
}

// canonicalize reduces a locale tag to its primary subtag and checks whether
// it is in the supported set. Returns "" when not found.
func canonicalize(tag string, supported []string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if dash := strings.IndexByte(tag, '-'); dash > 0 {
		tag = tag[:dash]
	}
	if inSupported(tag, supported) {
		return tag
	}
	return ""
}

func inSupported(tag string, supported []string) bool {
	for _, s := range supported {
		if strings.ToLower(strings.TrimSpace(s)) == tag {
			return true
		}
	}
	return false
}

// parseQuality parses a q-value string (e.g. "0.9", "1", "0.123"). Returns an
// error for strings that do not conform to the RFC 7231 quality syntax.
func parseQuality(s string) (float64, error) {
	s = strings.TrimSpace(s)
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		switch s {
		case "0":
			return 0, nil
		case "1":
			return 1, nil
		}
		return 0, &qualityError{s}
	}
	var whole, frac int
	var scale = 1
	for _, r := range s[:dot] {
		if r < '0' || r > '9' {
			return 0, &qualityError{s}
		}
		whole = whole*10 + int(r-'0')
	}
	for _, r := range s[dot+1:] {
		if r < '0' || r > '9' {
			return 0, &qualityError{s}
		}
		frac = frac*10 + int(r-'0')
		scale *= 10
	}
	return float64(whole) + float64(frac)/float64(scale), nil
}

type qualityError struct{ raw string }

func (e *qualityError) Error() string { return "invalid q-value: " + e.raw }

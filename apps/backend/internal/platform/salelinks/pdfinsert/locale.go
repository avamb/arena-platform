package pdfinsert

// locale.go — the one localized line of the insert and the languages it
// exists in.

import (
	"strings"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// SupportedLocales are the languages of the tagline (spec 43 section 6.5):
// the key below exists in every one of these catalogs.
var SupportedLocales = []string{"en", "ru", "es", "cs", "he"}

// DefaultLocale is used when nothing names a supported language.
const DefaultLocale = "en"

// TaglineKey is the catalog key of the "scan and buy tickets" line.
const TaglineKey = "salelinks.insert.scan_and_buy"

// taglineFallback is the English text used when no bundle is wired (unit
// tests, a server built without catalogs).
const taglineFallback = "Scan and buy tickets"

// NormalizeLocale folds a tag to one of SupportedLocales: case and region
// are dropped (cs-CZ -> cs), anything unknown is DefaultLocale.
func NormalizeLocale(locale string) string {
	tag := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		tag = tag[:i]
	}
	for _, s := range SupportedLocales {
		if s == tag {
			return s
		}
	}
	return DefaultLocale
}

// Tagline returns the "scan and buy tickets" line in locale. A nil bundle or
// a missing key gives the English text, never an empty string.
func Tagline(bundle *i18n.Bundle, locale string) string {
	if bundle == nil {
		return taglineFallback
	}
	msg, err := bundle.LocalizerFor(NormalizeLocale(locale)).Localize(&goi18n.LocalizeConfig{MessageID: TaglineKey})
	if err != nil || strings.TrimSpace(msg) == "" {
		return taglineFallback
	}
	return msg
}

package hfeed

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
)

// The buyer's locale arrives from an embedded widget on someone else's site.
// It must be treated as untrusted input that can only ever change which
// language an e-mail renders in — never as something that can fail a sale.

func TestNormalizeBuyerLocale_AcceptsEveryShippedLocale(t *testing.T) {
	// Whatever templates the repo ships, this must accept — otherwise a
	// language with working templates silently renders in English.
	for _, locale := range templates.SupportedLocales {
		if got := normalizeBuyerLocale(locale); got != locale {
			t.Errorf("normalizeBuyerLocale(%q) = %q; want %q — templates exist for it", locale, got, locale)
		}
	}
}

func TestNormalizeBuyerLocale_FoldsCaseAndRegionSubtags(t *testing.T) {
	// A browser reports the full BCP-47 tag; the templates are per-language.
	cases := map[string]string{
		"cs":      "cs",
		"CS":      "cs",
		"cs-CZ":   "cs",
		"cs_CZ":   "cs",
		"CS-cz":   "cs",
		"  ru  ":  "ru",
		"ru-RU":   "ru",
		"en-GB":   "en",
		"he-IL":   "he",
		"de-AT":   "de",
		"es-419":  "es",
		" EN-us ": "en",
	}
	for in, want := range cases {
		if got := normalizeBuyerLocale(in); got != want {
			t.Errorf("normalizeBuyerLocale(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestNormalizeBuyerLocale_DropsAnythingUnshipped(t *testing.T) {
	// Every one of these must yield "" — NOT an error, and never a value
	// that reaches the renderer. English is the right fallback; refusing
	// the checkout would trade a cosmetic miss for a lost sale.
	for _, in := range []string{
		"",
		"   ",
		"fr",       // real language, no templates shipped
		"zz",       // not a language
		"english",  // a name, not a tag
		"e",        // too short
		"-cs",      // leading separator: no language part at all
		"_ru",      //
		"../../en", // path traversal shaped
		"en;DROP TABLE tickets",
		"<script>alert(1)</script>",
		"cs\x00",
	} {
		if got := normalizeBuyerLocale(in); got != "" {
			t.Errorf("normalizeBuyerLocale(%q) = %q; want empty", in, got)
		}
	}
}

func TestNormalizeBuyerLocale_NeverReturnsAnUnsupportedValue(t *testing.T) {
	// Belt and braces: whatever comes out must be either empty or a locale
	// the renderer actually has templates for. This is the invariant the
	// column's nullability depends on.
	supported := map[string]bool{}
	for _, l := range templates.SupportedLocales {
		supported[l] = true
	}
	for _, in := range []string{"cs-CZ", "fr-FR", "", "RU", "xx-YY", "he"} {
		got := normalizeBuyerLocale(in)
		if got != "" && !supported[got] {
			t.Errorf("normalizeBuyerLocale(%q) = %q, which is not a shipped locale", in, got)
		}
	}
}

func TestLocalePtr_EmptyBecomesNullNotEmptyString(t *testing.T) {
	// "not stated" must land in the column as NULL. An empty string would
	// force every reader to special-case two spellings of the same thing.
	if got := localePtr(""); got != nil {
		t.Errorf("localePtr(\"\") = %q; want nil so the column stores NULL", *got)
	}
	got := localePtr("cs")
	if got == nil {
		t.Fatal("localePtr(\"cs\") = nil; want a pointer to cs")
	}
	if *got != "cs" {
		t.Errorf("localePtr(\"cs\") = %q; want cs", *got)
	}
}

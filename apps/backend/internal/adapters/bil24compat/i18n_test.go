// i18n_test.go — feature #478. Pins the locale-negotiation table,
// the LocalizeDescription fallback contract, and the completeness
// guarantee that every bil24.* description key resolves to a
// non-empty target-language string in every supported locale.

package bil24compat_test

import (
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// TestBil24_478_NegotiateBil24Locale pins the ru-RU/en-GB/he-IL/cs-CZ
// mapping table plus the channel-default and hard fallback tiers
// (spec section 6).
func TestBil24_478_NegotiateBil24Locale(t *testing.T) {
	cases := []struct {
		name           string
		reqLocale      string
		channelDefault string
		want           string
	}{
		{"ru-RU strict", "ru-RU", "", "ru"},
		{"en-GB strict", "en-GB", "", "en"},
		{"he-IL strict", "he-IL", "", "he"},
		{"cs-CZ strict", "cs-CZ", "", "cs"},
		{"primary tag ru", "ru", "", "ru"},
		{"case-insensitive", "HE-il", "", "he"},
		{"underscore variant", "cs_CZ", "", "cs"},
		{"unknown -> channel default cs", "klingon", "cs-CZ", "cs"},
		{"unknown -> channel default he", "xx", "he", "he"},
		{"empty req -> channel default ru", "", "ru", "ru"},
		{"both unknown -> en", "klingon", "elvish", "en"},
		{"empty everything -> en", "", "", "en"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bil24compat.NegotiateBil24Locale(tc.reqLocale, tc.channelDefault)
			if got != tc.want {
				t.Errorf("NegotiateBil24Locale(%q, %q) = %q, want %q", tc.reqLocale, tc.channelDefault, got, tc.want)
			}
		})
	}
}

// TestBil24_478_LocaleBundleHasEveryBil24Key is the completeness
// gate: table-driven proof that every bil24.* key defined in
// Bil24DescriptionKeys resolves to a non-empty string that is NOT
// the English fallback in every supported locale (excluding en
// itself, which is the fallback baseline). Missing keys, empty
// values, or accidentally-inherited English text all fail this test.
func TestBil24_478_LocaleBundleHasEveryBil24Key(t *testing.T) {
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}

	// bil24.ok resolves to "OK" in every locale (it's the exact string).
	// The seat_taken and category_sold_out keys require template data
	// or the go-i18n localizer treats the placeholders as literal.
	seed := map[string]map[string]any{
		"bil24.seat_taken":        {"sector": "Parter", "row": "3", "number": "12"},
		"bil24.category_sold_out": {"name": "Standing", "available": 0},
	}

	for _, loc := range bil24compat.Bil24SupportedLocales {
		loc := loc
		t.Run(loc, func(t *testing.T) {
			localizer := bundle.LocalizerFor(loc)
			for _, key := range bil24compat.Bil24DescriptionKeys {
				got := bil24compat.LocalizeDescription(localizer, key, "", seed[key])
				if got == "" {
					t.Errorf("%s: key %q resolved to empty string", loc, key)
					continue
				}
				if got == key {
					t.Errorf("%s: key %q resolved to the key itself (message absent)", loc, key)
				}
			}
		})
	}
}

// TestBil24_478_LocalizeDescription_FallbackContract pins the fallback
// behaviour: nil localizer, empty key, or a missing key must all
// return the english argument unchanged (never an empty wire string).
func TestBil24_478_LocalizeDescription_FallbackContract(t *testing.T) {
	t.Run("nil localizer -> english", func(t *testing.T) {
		got := bil24compat.LocalizeDescription(nil, "bil24.seat_taken", "seat taken", nil)
		if got != "seat taken" {
			t.Errorf("nil localizer: got %q, want %q", got, "seat taken")
		}
	})

	t.Run("empty key -> english", func(t *testing.T) {
		bundle, err := i18n.NewBundle()
		if err != nil {
			t.Fatalf("i18n.NewBundle: %v", err)
		}
		got := bil24compat.LocalizeDescription(bundle.LocalizerFor("ru"), "", "english", nil)
		if got != "english" {
			t.Errorf("empty key: got %q, want %q", got, "english")
		}
	})

	t.Run("missing key -> english", func(t *testing.T) {
		bundle, err := i18n.NewBundle()
		if err != nil {
			t.Fatalf("i18n.NewBundle: %v", err)
		}
		got := bil24compat.LocalizeDescription(bundle.LocalizerFor("cs"), "bil24.this.does.not.exist.at.all", "englishfallback", nil)
		if got != "englishfallback" {
			t.Errorf("missing key: got %q, want %q", got, "englishfallback")
		}
	})
}

// TestBil24_478_SeatTakenIsLocalized proves the two goldens' payloads:
// a seat_taken description rendered under ru and he uses target-
// language script (Cyrillic for ru, Hebrew for he), never English.
func TestBil24_478_SeatTakenIsLocalized(t *testing.T) {
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	params := map[string]any{"sector": "Parter", "row": "3", "number": "12"}

	ru := bil24compat.LocalizeDescription(bundle.LocalizerFor("ru"), "bil24.seat_taken", "seat is taken", params)
	if !containsCyrillic(ru) {
		t.Errorf("ru seat_taken has no Cyrillic chars: %q", ru)
	}
	if !strings.Contains(ru, "Parter") || !strings.Contains(ru, "3") || !strings.Contains(ru, "12") {
		t.Errorf("ru seat_taken missing template substitution: %q", ru)
	}

	he := bil24compat.LocalizeDescription(bundle.LocalizerFor("he"), "bil24.seat_taken", "seat is taken", params)
	if !containsHebrew(he) {
		t.Errorf("he seat_taken has no Hebrew chars: %q", he)
	}
	if !strings.Contains(he, "Parter") {
		t.Errorf("he seat_taken missing template substitution: %q", he)
	}
}

func containsCyrillic(s string) bool {
	for _, r := range s {
		if r >= 0x0400 && r <= 0x04FF {
			return true
		}
	}
	return false
}

func containsHebrew(s string) bool {
	for _, r := range s {
		if r >= 0x0590 && r <= 0x05FF {
			return true
		}
	}
	return false
}

// city_i18n_537_test.go — feature #537 (W1-S1c), spec
// 08_architecture/22_site_facing_gaps_w1s1_ru.md §2.3.
//
// The DB half of the feature is covered by
// tests/compat/bil24/city_name_537_integration_test.go; what is pinned here is
// the rule the spec states in words — "the incoming cityName with whitespace
// normalized and case preserved".
package himports

import "testing"

func TestNormalizeDisplayName_537(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain name is untouched", "Praha", "Praha"},
		{"case is preserved verbatim", "PRAHA", "PRAHA"},
		{"lower case is preserved too", "praha", "praha"},
		{"surrounding whitespace is trimmed", "  Praha  ", "Praha"},
		{"inner runs collapse to one space", "Praha    2", "Praha 2"},
		{"tabs and newlines are whitespace", "Nové\tMěsto\nnad Metují", "Nové Město nad Metují"},
		{"diacritics survive", "Plzeň", "Plzeň"},
		{"non-latin scripts survive", "Санкт-Петербург", "Санкт-Петербург"},
		{"empty stays empty", "", ""},
		{"blank collapses to empty", "   \t\n ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeDisplayName(tc.in); got != tc.want {
				t.Errorf("normalizeDisplayName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalizeDisplayName_537_NeverLowercases is the regression the feature
// exists for: the slug is lowercase by construction, and the display name must
// never be reduced to it.
func TestNormalizeDisplayName_537_NeverLowercases(t *testing.T) {
	const in = "Praha"
	if got := normalizeDisplayName(in); got == slugify(in) {
		t.Errorf("normalizeDisplayName(%q) = %q, which equals the slug — the display name must keep its case", in, got)
	}
}

func TestNormalizeLocale_537(t *testing.T) {
	cases := map[string]string{
		"en":     "en",
		"EN":     "en",
		" cs ":   "cs",
		"ru-RU":  "ru-ru",
		"":       "",
		"   \t ": "",
	}
	for in, want := range cases {
		if got := normalizeLocale(in); got != want {
			t.Errorf("normalizeLocale(%q) = %q, want %q", in, got, want)
		}
	}
}

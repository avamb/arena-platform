package hcatalog

import (
	"reflect"
	"testing"
)

func TestNormalizePromoterName(t *testing.T) {
	cases := map[string]string{
		"  Partner   Agency  ": "Partner Agency",
		"Lampyris s.r.o.":      "Lampyris s.r.o.",
		"\t\n":                 "",
		"ООО  Ромашка":         "ООО Ромашка",
	}
	for in, want := range cases {
		if got := NormalizePromoterName(in); got != want {
			t.Errorf("NormalizePromoterName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeOptionalText(t *testing.T) {
	blank := "   "
	if normalizeOptionalText(&blank) != nil {
		t.Error("a blank value must be stored as NULL")
	}
	v := "  +420 123  "
	if got := normalizeOptionalText(&v); got == nil || *got != "+420 123" {
		t.Errorf("got %v", got)
	}
	if normalizeOptionalText(nil) != nil {
		t.Error("nil stays nil")
	}
}

func TestCitySlugCandidates(t *testing.T) {
	got := CitySlugCandidates("Guardamar del Segura", "ES", 4)
	want := []string{"guardamar-del-segura", "guardamar-del-segura-es", "guardamar-del-segura-es-2", "guardamar-del-segura-es-3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("latin name: got %v, want %v", got, want)
	}

	// Nothing transliterable (Hebrew) → a country-scoped placeholder slug.
	got = CitySlugCandidates("אשדוד", "IL", 3)
	want = []string{"city-il", "city-il-2", "city-il-3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hebrew name: got %v, want %v", got, want)
	}

	got = CitySlugCandidates("Москва", "", 2)
	want = []string{"moskva", "moskva-2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("no country code: got %v, want %v", got, want)
	}
}

func TestNormalizeCityLocale(t *testing.T) {
	cases := map[string]string{
		"":      "en",
		"cs":    "cs",
		"cs-CZ": "cs",
		"RU":    "ru",
		"he_IL": "he",
		"x":     "en",
		"12":    "en",
	}
	for in, want := range cases {
		if got := normalizeCityLocale(in); got != want {
			t.Errorf("normalizeCityLocale(%q) = %q, want %q", in, got, want)
		}
	}
}

package generate

import "testing"

func TestCategoryName(t *testing.T) {
	for locale, want := range map[string]string{
		"en": "Standard", "ru": "Стандарт", "es": "Estándar", "RU": "Стандарт", "ru-RU": "Стандарт",
		"es_ES": "Estándar", "": "Standard", "de": "Standard", "  ": "Standard",
	} {
		if got := CategoryName(locale); got != want {
			t.Errorf("CategoryName(%q) = %q, want %q", locale, got, want)
		}
	}
}

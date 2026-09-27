package geoslug

import "testing"

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Tel Aviv":             "tel-aviv",
		"  Tel   Aviv  ":       "tel-aviv",
		"Tel-Aviv":             "tel-aviv",
		"Москва":               "moskva",
		"Санкт-Петербург":      "sankt-peterburg",
		"Hradec Králové":       "hradec-kralove",
		"Ústí nad Labem":       "usti-nad-labem",
		"Kraków":               "krakow",
		"Győr":                 "gyor",
		"Guardamar del Segura": "guardamar-del-segura",
		"Zürich":               "zurich",
		"A Coruña":             "a-coruna",
		"תל אביב":              "",
		"!!!":                  "",
		"":                     "",
		"Brno 2":               "brno-2",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"  Tel   Aviv ": "tel aviv",
		"PRAHA":         "praha",
		"Praha\t\nWest": "praha west",
		"Москва":        "москва",
		"":              "",
	}
	for in, want := range cases {
		if got := NormalizeName(in); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

package geotz

import "testing"

func TestSearchCities_SpainByLatinPrefix(t *testing.T) {
	got := SearchCities("ES", "barc")
	if len(got) == 0 {
		t.Fatal("no match for barc in ES")
	}
	if got[0].Name != "Barcelona" || got[0].Timezone != "Europe/Madrid" {
		t.Errorf("first match = %+v, want Barcelona in Europe/Madrid", got[0])
	}
}

func TestSearchCities_CyrillicQueryFindsTheLatinPlace(t *testing.T) {
	got := SearchCities("es", "  Мадр ")
	if len(got) == 0 || got[0].Name != "Madrid" || got[0].Timezone != "Europe/Madrid" {
		t.Fatalf("got %+v, want Madrid first", got)
	}
}

func TestSearchCities_ManyZoneCountryKeepsItsOwnZone(t *testing.T) {
	got := SearchCities("ES", "las palmas")
	if len(got) == 0 || got[0].Timezone != "Atlantic/Canary" {
		t.Fatalf("got %+v, want a place in Atlantic/Canary first", got)
	}
}

func TestSearchCities_SingleZoneCountryUsesTopList(t *testing.T) {
	got := SearchCities("CZ", "pra")
	if len(got) == 0 || got[0].Name != "Prague" || got[0].Timezone != "Europe/Prague" {
		t.Fatalf("got %+v, want Prague in Europe/Prague", got)
	}
}

func TestSearchCities_LimitBlankAndUnknown(t *testing.T) {
	if got := SearchCities("ES", "a"); len(got) != SearchLimit {
		t.Errorf("a one-letter query returned %d places, want the cap %d", len(got), SearchLimit)
	}
	if got := SearchCities("ES", "   "); got != nil {
		t.Errorf("blank query gave %v", got)
	}
	if got := SearchCities("ZZ", "mad"); got != nil {
		t.Errorf("unknown country gave %v", got)
	}
}

func TestSingleZone(t *testing.T) {
	if got := SingleZone("cz"); got != "Europe/Prague" {
		t.Errorf("CZ: %q", got)
	}
	if got := SingleZone("ES"); got != "" {
		t.Errorf("ES has several zones, got %q", got)
	}
}

func TestCountriesOfCity(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Valencia", "valencia", "Валенсия"} {
		got := CountriesOfCity(name)
		if len(got) == 0 || !containsCC(got, "ES") {
			t.Errorf("CountriesOfCity(%q) = %v, want ES among them", name, got)
		}
		if containsCC(got, "LV") {
			t.Errorf("CountriesOfCity(%q) = %v names Latvia", name, got)
		}
	}
	if got := CountriesOfCity("Madrid"); len(got) == 0 || got[0] != "ES" {
		t.Errorf("CountriesOfCity(Madrid) = %v, want ES first", got)
	}
	for _, name := range []string{"", "   ", "Nowhereville Xyz"} {
		if got := CountriesOfCity(name); got != nil {
			t.Errorf("CountriesOfCity(%q) = %v, want nil", name, got)
		}
	}
}

func containsCC(list []string, cc string) bool {
	for _, c := range list {
		if c == cc {
			return true
		}
	}
	return false
}

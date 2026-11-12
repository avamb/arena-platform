package citymatch

import (
	"testing"

	"github.com/google/uuid"
)

func prague() Existing {
	return Existing{ID: uuid.New(), Slug: "prague", Names: []string{"Prague", "Прага"}}
}

func TestMatch_PrahaFindsPragueByAnyName(t *testing.T) {
	p := prague()
	for _, name := range []string{"Praha", "praha", " PRAHA ", "Prague", "Прага", "прага"} {
		got, ok := Match("CZ", name, []Existing{p})
		if !ok || got.ID != p.ID {
			t.Errorf("Match(CZ, %q) = %v, %v; want the Prague row", name, got, ok)
		}
	}
}

func TestMatch_PrahaFindsRowKnownOnlyByRussianName(t *testing.T) {
	p := Existing{ID: uuid.New(), Slug: "city-cz", Names: []string{"Прага"}}
	if _, ok := Match("CZ", "Praha", []Existing{p}); !ok {
		t.Fatal("Praha must find a row whose only name is Прага")
	}
}

func TestMatch_AliasesApplyOnlyInTheirOwnCountry(t *testing.T) {
	rome := Existing{ID: uuid.New(), Slug: "rome", Names: []string{"Rome"}}
	if got, ok := Match("US", "Roma", []Existing{rome}); ok {
		t.Fatalf("Roma in US must not match Rome: %v", got)
	}
	if _, ok := Match("IT", "Roma", []Existing{rome}); !ok {
		t.Fatal("Roma in IT must match Rome")
	}
	if _, ok := Match("", "Roma", []Existing{rome}); ok {
		t.Fatal("an empty iso2 applies no alias")
	}
	if _, ok := Match("CZ", "Praha", []Existing{prague()}); !ok {
		t.Fatal("Praha in CZ must still match Prague")
	}
}

func TestMatch_NewCityIsNotMatched(t *testing.T) {
	if got, ok := Match("CZ", "Ostrava", []Existing{prague()}); ok {
		t.Fatalf("Ostrava matched %v", got)
	}
}

func TestMatch_SlugWinsAndDiacriticsFold(t *testing.T) {
	brno := Existing{ID: uuid.New(), Slug: "brno", Names: []string{"Brno"}}
	if _, ok := Match("CZ", "Brnó", []Existing{prague(), brno}); !ok {
		t.Fatal("accent must fold")
	}
	if got, _ := Match("CZ", "Brno", []Existing{prague(), brno}); got.ID != brno.ID {
		t.Fatalf("slug match picked %v", got)
	}
}

func TestMatch_EmptyCountryAndBlankName(t *testing.T) {
	if _, ok := Match("CZ", "Praha", nil); ok {
		t.Fatal("no cities, no match")
	}
	if _, ok := Match("CZ", "  ", []Existing{prague()}); ok {
		t.Fatal("blank name must not match")
	}
}

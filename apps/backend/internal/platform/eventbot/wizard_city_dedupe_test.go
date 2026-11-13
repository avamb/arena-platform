package eventbot

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/geotz"
)

// langRefs answers the reference cities per language, the way the API does.
type langRefs struct {
	*fakeRefs
	byLang map[string][]RefItem
}

func (l *langRefs) Cities(_ context.Context, _ string, _ string, lang string) ([]RefItem, error) {
	return l.byLang[NormalizeLocale(lang)], nil
}

func (l *langRefs) VenueDirectory(_ context.Context, _ string, _ uuid.UUID, _ string, _ string, _ string, _ string) (VenueDir, error) {
	return VenueDir{Cities: []DirCity{{ID: "c-prg", Name: "Прага", ISO2: "CZ", Venues: 23}}}, nil
}

func cityButtons(t *testing.T, refs RefIO) []string {
	t.Helper()
	r := newWizardRun(t, refs)
	s := r.d.session()
	s.CountryID, s.CountryISO2, s.CountryName = "cz", "CZ", "Czechia"
	r.d.goTo(stSCity)
	scr, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var out []string
	for _, row := range scr.Buttons {
		for _, b := range row {
			out = append(out, b.Label)
		}
	}
	return out
}

func countLabels(labels []string, subs ...string) int {
	n := 0
	for _, l := range labels {
		for _, s := range subs {
			if strings.Contains(strings.ToLower(l), strings.ToLower(s)) {
				n++
				break
			}
		}
	}
	return n
}

// Prague in three spellings is one button: the platform's.
func TestRenderCity_PragueInThreeSpellingsIsOneButton(t *testing.T) {
	refs := &langRefs{fakeRefs: &fakeRefs{}, byLang: map[string][]RefItem{
		"ru": {{ID: "c-prg", Name: "Прага", ISO2: "CZ"}},
		"en": {{ID: "c-prg", Name: "Prague", ISO2: "CZ"}},
	}}
	labels := cityButtons(t, refs)
	if got := countLabels(labels, "Прага", "Prague", "Praha"); got != 1 {
		t.Fatalf("Prague spelled %d times, want 1: %v", got, labels)
	}
	if !strings.Contains(strings.Join(labels, "|"), "Прага · 23") {
		t.Fatalf("the platform city must stay: %v", labels)
	}
}

// Even without the English names, geotz knows that «Брно» and «Brno» are one
// place (it carries the aliases of a place in several scripts).
func TestRenderCity_SameGeotzPlaceIsDropped(t *testing.T) {
	refs := &langRefs{fakeRefs: &fakeRefs{}, byLang: map[string][]RefItem{
		"ru": {{ID: "c-brn", Name: "Брно", ISO2: "CZ"}},
	}}
	labels := cityButtons(t, refs)
	if got := countLabels(labels, "Брно", "Brno"); got != 2 { // «Брно» + the district «Brno střed»
		t.Fatalf("want the platform city and the district only, got %d: %v", got, labels)
	}
	for _, l := range labels {
		if l == "Brno" {
			t.Fatalf("the suggestion «Brno» repeats the platform city: %v", labels)
		}
	}
}

// A suggestion of a city the reference lacks still shows.
func TestRenderCity_SuggestionOfUnknownCityStays(t *testing.T) {
	refs := &langRefs{fakeRefs: &fakeRefs{}, byLang: map[string][]RefItem{
		"ru": {{ID: "c-prg", Name: "Прага", ISO2: "CZ"}},
	}}
	top := geotz.TopCities("CZ")
	if len(top) < 2 {
		t.Skip("no top cities for CZ in the data")
	}
	labels := cityButtons(t, refs)
	other := 0
	for _, n := range top {
		if !strings.EqualFold(geotz.Normalize(n), "prague") && !strings.EqualFold(geotz.Normalize(n), "praha") {
			if countLabels(labels, n) > 0 {
				other++
			}
		}
	}
	if other == 0 {
		t.Fatalf("no suggestion of another city is left: top=%v labels=%v", top, labels)
	}
}

// A platform city is never dropped, even when it equals a suggestion.
func TestRenderCity_PlatformCityNeverDropped(t *testing.T) {
	refs := &langRefs{fakeRefs: &fakeRefs{}, byLang: map[string][]RefItem{
		"ru": {{ID: "c-prg", Name: "Прага", ISO2: "CZ"}, {ID: "c-brn", Name: "Brno", ISO2: "CZ"}, {ID: "c-brs", Name: "Brno střed", ISO2: "CZ"}},
	}}
	labels := cityButtons(t, refs)
	for _, want := range []string{"Прага · 23", "Brno", "Brno střed"} {
		if countLabels(labels, want) == 0 {
			t.Fatalf("platform city %q is missing: %v", want, labels)
		}
	}
}

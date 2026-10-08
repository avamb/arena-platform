package eventbot

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// manyCountries builds a long list spread over several regions, the shape the
// platform has since migration 0127.
func manyCountries() []RefItem {
	out := []RefItem{}
	add := func(region string, n int, prefix string) {
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("%s%d", prefix, i)
			out = append(out, RefItem{ID: id, Name: strings.ToUpper(id), ISO2: strings.ToUpper(id[:2]), Currency: "EUR", Region: region})
		}
	}
	add("europe", 8, "eu")
	add("latin_america", 5, "la")
	add("asia", 2, "as")
	return out
}

func countryButtons(t *testing.T, r *wizardRun) []Button {
	t.Helper()
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	flat := []Button{}
	for _, row := range screen.Buttons {
		flat = append(flat, row...)
	}
	return flat
}

func hasData(bs []Button, data string) bool {
	for _, b := range bs {
		if b.Data == data {
			return true
		}
	}
	return false
}

// A long country list opens on regions, not on every country at once; a
// region shows only its own countries, with a way back.
func TestWizard_CountryQuestionGoesThroughRegions(t *testing.T) {
	refs := newFakeRefs()
	refs.countries = manyCountries()
	refs.cities["eu3"] = []RefItem{{ID: "town", Name: "Town", ISO2: "EU"}}
	r := newWizardRun(t, refs)
	r.eventHead()
	r.text("15.12.2026", stSTime)
	r.press("default", stSCountry)

	// Region buttons only: no country button among them.
	bs := countryButtons(t, r)
	for _, region := range []string{"europe", "latin_america", "asia"} {
		if !hasData(bs, "region:"+region) {
			t.Errorf("region button %q missing in %+v", region, bs)
		}
	}
	for _, b := range bs {
		if strings.HasPrefix(b.Data, "country:") {
			t.Fatalf("a country button %q on the region screen", b.Data)
		}
	}
	if hasData(bs, "region:africa") {
		t.Error("a region with no countries must not be offered")
	}

	// Pressing a region stays on the step and lists that region's countries.
	r.press("region:latin_america", stSCountry)
	bs = countryButtons(t, r)
	if !hasData(bs, "country:la0") || !hasData(bs, "country:la4") {
		t.Errorf("Latin America countries missing: %+v", bs)
	}
	if hasData(bs, "country:eu0") || hasData(bs, "country:as0") {
		t.Errorf("countries of other regions leaked in: %+v", bs)
	}
	if !hasData(bs, "region:") {
		t.Error("no way back to the regions")
	}

	// Back to the regions, into Europe, and pick a country: the dialog moves
	// on to the city and forgets the region.
	r.press("region:", stSCountry)
	r.press("region:europe", stSCountry)
	r.press("country:eu3", stSCity)
	if r.d.Scratch.CountryRegion != "" {
		t.Errorf("region not cleared after a country was picked: %q", r.d.Scratch.CountryRegion)
	}
}

// A short list (a young installation) needs no extra step, and a made-up
// region key from an old button is ignored.
func TestWizard_ShortCountryListStaysFlat(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.eventHead()
	r.text("15.12.2026", stSTime)
	r.press("default", stSCountry)
	bs := countryButtons(t, r)
	if !hasData(bs, "country:cz") || !hasData(bs, "country:es") {
		t.Fatalf("a short list should show the countries directly: %+v", bs)
	}
	r.press("region:nowhere", stSCountry)
	if r.d.Scratch.CountryRegion != "" {
		t.Errorf("an unknown region was accepted: %q", r.d.Scratch.CountryRegion)
	}
}

func TestRegionHelpers(t *testing.T) {
	t.Parallel()
	cs := []RefItem{{ID: "a", Region: "asia"}, {ID: "b", Region: "europe"}, {ID: "c"}, {ID: "d", Region: "mars"}}
	got := presentRegions(cs)
	if strings.Join(got, ",") != "europe,asia,other" {
		t.Errorf("presentRegions = %v", got)
	}
	if regionOf(cs[2]) != "other" || regionOf(cs[3]) != "other" {
		t.Error("a missing or unknown region must read as other")
	}
	if len(inRegion(cs, "other")) != 2 {
		t.Error("inRegion(other) should hold the two unclassified countries")
	}
}

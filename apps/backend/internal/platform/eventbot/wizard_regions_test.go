package eventbot

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// manyCountries builds a long list spread over several regions, the shape the
// platform has since migration 0127. Every code is unique.
func manyCountries() []RefItem {
	out := []RefItem{}
	add := func(region string, n int, prefix string) {
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("%s%d", prefix, i)
			out = append(out, RefItem{ID: id, Name: strings.ToUpper(id), ISO2: strings.ToUpper(prefix) + string(rune('A'+i)), Currency: "EUR", Region: region})
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

// The country question opens on the quick pick and the regions, not on every
// country at once; a region shows only its own countries, with a way back.
func TestWizard_CountryQuestionGoesThroughRegions(t *testing.T) {
	refs := newFakeRefs()
	refs.countries = manyCountries()
	refs.cities["eu3"] = []RefItem{{ID: "town", Name: "Town", ISO2: "EUD"}}
	r := newWizardRun(t, refs)
	r.eventHead()
	r.text("15.12.2026", stSTime)
	r.press("default", stSCountry)

	// Region buttons only: no country button among them (nobody has venues).
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
	if hasData(bs, "region:africa") || hasData(bs, "region:other") {
		t.Error("a region with no countries, or other, must not be offered")
	}

	// Pressing a region stays on the step and lists that region's countries.
	r.press("region:latin_america", stSCountry)
	bs = countryButtons(t, r)
	if !hasData(bs, "country:LAA") || !hasData(bs, "country:LAE") {
		t.Errorf("Latin America countries missing: %+v", bs)
	}
	if hasData(bs, "country:EUA") || hasData(bs, "country:ASA") {
		t.Errorf("countries of other regions leaked in: %+v", bs)
	}
	if !hasData(bs, "region:") {
		t.Error("no way back to the regions")
	}

	// Back to the regions, into Europe, and pick a country: the dialog moves
	// on to the city and forgets the region.
	r.press("region:", stSCountry)
	r.press("region:europe", stSCountry)
	r.press("country:EUD", stSCity)
	if r.d.Scratch.CountryRegion != "" {
		t.Errorf("region not cleared after a country was picked: %q", r.d.Scratch.CountryRegion)
	}
	if got := r.d.session(); got.CountryISO2 != "EUD" || got.CountryID != "eu3" {
		t.Errorf("session country = %+v", got)
	}
}

// A made-up region key from an old button is ignored.
func TestWizard_UnknownRegionIsIgnored(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.eventHead()
	r.text("15.12.2026", stSTime)
	r.press("default", stSCountry)
	r.press("region:nowhere", stSCountry)
	if r.d.Scratch.CountryRegion != "" {
		t.Errorf("an unknown region was accepted: %q", r.d.Scratch.CountryRegion)
	}
}

// A region with more countries than a page holds is paged, and the countries
// that already have venues come first with their venue count in the label.
func TestWizard_LongRegionIsPaged(t *testing.T) {
	refs := newFakeRefs()
	refs.countries = nil
	for i := 0; i < 23; i++ {
		code := fmt.Sprintf("X%c", 'A'+i)
		refs.countries = append(refs.countries, RefItem{ID: "id" + code, Name: "Country " + code, ISO2: code, Currency: "EUR", Region: "asia"})
	}
	// Two venues in XW and one in XB: they lead the region.
	refs.venues = []VenueRef{
		{ID: "v1", Name: "A", CountryISO2: "XW"}, {ID: "v2", Name: "B", CountryISO2: "XW"}, {ID: "v3", Name: "C", CountryISO2: "XB"},
	}
	r := newWizardRun(t, refs)
	r.eventHead()
	r.text("15.12.2026", stSTime)
	r.press("default", stSCountry)
	r.press("region:asia", stSCountry)

	labels := func() []string {
		var out []string
		for _, b := range countryButtons(t, r) {
			if strings.HasPrefix(b.Data, "country:") {
				out = append(out, b.Label)
			}
		}
		return out
	}
	first := labels()
	if len(first) != countryPageSize {
		t.Fatalf("page 1 has %d countries, want %d", len(first), countryPageSize)
	}
	if first[0] != "Country XW · 2" || first[1] != "Country XB · 1" {
		t.Errorf("countries with venues should lead with their count: %v", first[:3])
	}
	bs := countryButtons(t, r)
	if !hasData(bs, "page:2") || hasData(bs, "page:1") {
		t.Errorf("page 1 needs a next button only: %+v", bs)
	}
	r.press("page:3", stSCountry)
	if last := labels(); len(last) != 3 {
		t.Errorf("page 3 has %d countries, want 3: %v", len(last), last)
	}
	// A page number out of range lands on the last page, never on nothing.
	r.press("page:99", stSCountry)
	if last := labels(); len(last) != 3 {
		t.Errorf("page 99 should show the last page: %v", last)
	}
}

func worldSample() []WorldCountry {
	return []WorldCountry{
		{ISO2: "MY", ISO3: "MYS", Name: "Малайзия", NameEn: "Malaysia", Region: "asia"},
		{ISO2: "ES", ISO3: "ESP", Name: "Испания", NameEn: "Spain", Region: "europe"},
		{ISO2: "MV", ISO3: "MDV", Name: "Мальдивы", NameEn: "Maldives", Region: "asia"},
		{ISO2: "ML", ISO3: "MLI", Name: "Мали", NameEn: "Mali", Region: "africa"},
		{ISO2: "PS", ISO3: "PSE", Name: "Палестина", NameEn: "Palestine, State of", Region: "middle_east"},
		{ISO2: "DM", ISO3: "DMA", Name: "Доминика", NameEn: "Dominica", Region: "latin_america"},
		{ISO2: "DO", ISO3: "DOM", Name: "Доминиканская Республика", NameEn: "Dominican Republic", Region: "latin_america"},
	}
}

func codesOf(cs []WorldCountry) string { return strings.Join(isoCodes(cs), ",") }

// A typed country name offers its matches: beginnings first, then parts, a
// code before both; any letter case, in the request language and in English.
func TestMatchCountries(t *testing.T) {
	t.Parallel()
	world := worldSample()
	cases := []struct {
		typed, want string
	}{
		{"malay", "MY"},
		{"MALAY", "MY"},
		{"Малайз", "MY"},
		{"малайзия", "MY"},
		{"ESP", "ES"},
		{"es", "ES,MV,PS"},  // the code, then the names that hold it
		{"Мал", "MY,ML,MV"}, // beginnings, by name
		{"ali", "ML"},       // a part of "Mali"
		{"republic", "DO"},  // a part, English name
		{"domini", "DM,DO"}, // beginnings, in order
		{"Dominican", "DO"}, // English beginning
		{"state", "PS"},     // a part of "Palestine, State of"
		{"x", ""},           // one letter matches nothing
		{"Atlantis", ""},    // no match
		{"  spain  ", "ES"}, // spaces
	}
	for _, c := range cases {
		got := codesOf(matchCountries(world, c.typed, maxCountryMatches))
		if got != c.want {
			t.Errorf("match %q = %q, want %q", c.typed, got, c.want)
		}
	}
	// At most the limit.
	many := make([]WorldCountry, 0, 20)
	for i := 0; i < 20; i++ {
		many = append(many, WorldCountry{ISO2: fmt.Sprintf("Z%c", 'A'+i), Name: fmt.Sprintf("Zeta %d", i), NameEn: fmt.Sprintf("Zeta %d", i)})
	}
	if got := matchCountries(many, "zeta", maxCountryMatches); len(got) != maxCountryMatches {
		t.Errorf("matches not capped: %d", len(got))
	}
}

// A typed name on the country question offers buttons and picking one goes on;
// a name nobody has says so and keeps the screen.
func TestWizard_TypedCountryOffersMatches(t *testing.T) {
	refs := newFakeRefs()
	refs.world = worldSample()
	r := newWizardRun(t, refs)
	r.eventHead()
	r.text("15.12.2026", stSTime)
	r.press("default", stSCountry)

	r.text("Малайз", stSCountry)
	bs := countryButtons(t, r)
	if !hasData(bs, "country:MY") || hasData(bs, "country:ES") {
		t.Fatalf("matches of Малайз wrong: %+v", bs)
	}
	if !hasData(bs, "region:") {
		t.Error("the matches screen needs a way back to the regions")
	}
	note := r.text("Atlantis", stSCountry)
	if !strings.Contains(note, "Atlantis") {
		t.Errorf("a name nobody has should be named in the refusal: %q", note)
	}
	if len(r.d.Scratch.CountryMatches) != 0 {
		t.Errorf("a refused name left matches behind: %v", r.d.Scratch.CountryMatches)
	}

	r.text("malay", stSCountry)
	r.press("country:MY", stSCity)
	s := r.d.session()
	if s.CountryISO2 != "MY" || s.CountryID != "" || s.CountryName != "Малайзия" {
		t.Errorf("a country outside the reference keeps code and name only: %+v", s)
	}
}

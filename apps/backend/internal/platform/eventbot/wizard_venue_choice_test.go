package eventbot

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func screenButtons(t *testing.T, r *wizardRun) ([]Button, string) {
	t.Helper()
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatalf("Render at %s: %v", r.d.Step, err)
	}
	var flat []Button
	for _, row := range screen.Buttons {
		flat = append(flat, row...)
	}
	return flat, screen.Text
}

func labelOf(bs []Button, data string) string {
	for _, b := range bs {
		if b.Data == data {
			return b.Label
		}
	}
	return ""
}

// The city question leads with the directory's cities and their venue counts,
// and the quick pick of the country question is the directory's.
func TestWizardVenueChoice_CityLeadsWithDirectoryCounts(t *testing.T) {
	refs := newFakeRefs()
	refs.cities["cz"] = append(refs.cities["cz"], RefItem{ID: "brn", Name: "Brno", ISO2: "CZ"})
	r := newWizardRun(t, refs)
	r.eventHead()
	r.text("15.12.2026", stSTime)
	r.press("default", stSCountry)
	if bs, _ := screenButtons(t, r); !hasData(bs, "country:CZ") {
		t.Fatalf("the quick pick should hold the country with venues: %+v", bs)
	}
	r.press("country:CZ", stSCity)
	bs, _ := screenButtons(t, r)
	if got := labelOf(bs, "city:prg"); got != "Prague · 1" {
		t.Errorf("Prague label = %q", got)
	}
	if got := labelOf(bs, "city:brn"); got != "Brno" {
		t.Errorf("a city without venues has no count: %q", got)
	}
	// The directory's city comes before the one without venues.
	var order []string
	for _, b := range bs {
		if strings.HasPrefix(b.Data, "city:") && b.Data != "city:new" && !strings.HasPrefix(b.Data, "city:g:") {
			order = append(order, b.Data)
		}
	}
	if len(order) < 2 || order[0] != "city:prg" {
		t.Errorf("cities with venues must lead: %v", order)
	}
}

// A country outside the reference: the city is only a name, nothing is created
// before the venue, and the venue is made with the country and the city name in
// one call; its answer fills the draft.
func TestWizardVenueChoice_CountryOutsideReferenceNewVenue(t *testing.T) {
	refs := newFakeRefs()
	refs.world = worldSample()
	refs.world[0].Currency = "MYR"
	r := newWizardRun(t, refs)
	r.eventHead()
	r.text("15.12.2026", stSTime)
	r.press("default", stSCountry)
	r.press("region:asia", stSCountry)
	r.press("country:MY", stSCity)
	if s := r.d.session(); s.CountryID != "" || s.CountryISO2 != "MY" || s.Currency != "MYR" {
		t.Fatalf("country = %+v", s)
	}
	bs, text := screenButtons(t, r)
	if hasData(bs, "city:new") || !strings.Contains(text, "Малайзия") {
		t.Errorf("a country outside the reference has no platform cities (text %q): %+v", text, bs)
	}

	// The typed city waits in the draft; nothing is created.
	r.text("Kuala Lumpur", stSVenue)
	if s := r.d.session(); s.CityID != "" || s.CityName != "Kuala Lumpur" {
		t.Fatalf("waiting city = %+v", s)
	}
	if len(refs.created) != 0 {
		t.Fatalf("something was created before the venue: %v", refs.created)
	}
	bs, _ = screenButtons(t, r)
	if !hasData(bs, "venue:new") || len(bs) > 4 {
		t.Errorf("the venue question of a waiting city offers the new venue: %+v", bs)
	}

	r.press("venue:new", stVName)
	if nv := r.d.Scratch.NewVenue; nv.CityName != "Kuala Lumpur" || nv.CityID != "" || nv.CountryISO2 != "MY" {
		t.Fatalf("new venue form = %+v", nv)
	}
	r.text("Istana Budaya", stVAddress)
	r.press("skip", stVCapacity)
	r.text("300", stVTz)
	r.press("tz:Asia/Kuala_Lumpur", stSMore)
	if got := strings.Join(refs.created, ","); got != "venue-by-name:Istana Budaya@MY/Kuala Lumpur" {
		t.Fatalf("created = %q", got)
	}
	s := r.d.session()
	if s.CountryID != "country-my" || s.CityID != "city-kuala lumpur" || s.VenueID != "venue-istana budaya" || s.Timezone != "Asia/Kuala_Lumpur" {
		t.Fatalf("session after the venue = %+v", s)
	}
}

// A refused or abandoned country leaves no row: the draft only ever holds code,
// name and currency until a venue is made.
func TestWizardVenueChoice_BackFromWaitingCity(t *testing.T) {
	refs := newFakeRefs()
	refs.world = worldSample()
	r := newWizardRun(t, refs)
	r.eventHead()
	r.text("15.12.2026", stSTime)
	r.press("default", stSCountry)
	r.press("country:MY", stSCity)
	r.text("Kuala Lumpur", stSVenue)
	r.press("back", stSCity)
	r.press("back", stSCountry)
	if len(refs.created) != 0 {
		t.Fatalf("created = %v", refs.created)
	}
}

// The venues of a city are paged, marked, and typed text searches by name.
func TestWizardVenueChoice_VenuesPagedMarkedAndSearched(t *testing.T) {
	refs := newFakeRefs()
	refs.venues = nil
	for i := 0; i < 7; i++ {
		refs.venues = append(refs.venues, VenueRef{ID: fmt.Sprintf("v%d", i), Name: fmt.Sprintf("Hall %d", i), CityID: "prg", CountryISO2: "CZ", Timezone: "Europe/Prague", Capacity: 100})
	}
	refs.venues = append(refs.venues, VenueRef{ID: "club", Name: "Jazz Club", CityID: "prg", CountryISO2: "CZ"})
	refs.plans = map[string][]PlanRef{"v1": {{VersionID: "pv1", Name: "Main"}}}
	r := newWizardRun(t, refs)
	r.eventHead()
	r.text("15.12.2026", stSTime)
	r.press("default", stSCountry)
	r.press("country:CZ", stSCity)
	r.press("city:prg", stSVenue)

	venueButtons := func() []string {
		bs, _ := screenButtons(t, r)
		var out []string
		for _, b := range bs {
			if strings.HasPrefix(b.Data, "venue:") && b.Data != "venue:new" {
				out = append(out, b.Data)
			}
		}
		return out
	}
	if got := venueButtons(); len(got) != listPageSize {
		t.Fatalf("page 1 has %d venues, want %d", len(got), listPageSize)
	}
	bs, _ := screenButtons(t, r)
	if !hasData(bs, "page:2") || !hasData(bs, "venue:new") {
		t.Errorf("pager or new-venue button missing: %+v", bs)
	}
	if l := labelOf(bs, "venue:v1"); !strings.Contains(l, "схема зала") {
		t.Errorf("a venue with a plan should say so: %q", l)
	}
	r.press("page:2", stSVenue)
	if got := venueButtons(); len(got) != 3 {
		t.Errorf("page 2 has %d venues, want 3: %v", len(got), got)
	}

	// Typed text searches inside the city.
	r.text("jazz", stSVenue)
	if got := venueButtons(); len(got) != 1 || got[0] != "venue:club" {
		t.Errorf("search result = %v", got)
	}
	if bs, _ := screenButtons(t, r); !hasData(bs, "q:clear") {
		t.Error("a search needs a way back to every venue")
	}
	r.text("zzz", stSVenue)
	if got := venueButtons(); len(got) != 0 {
		t.Errorf("a search with no result lists venues: %v", got)
	}
	if bs, _ := screenButtons(t, r); !hasData(bs, "venue:new") {
		t.Error("an empty search must still offer the new venue")
	}
	r.press("q:clear", stSVenue)
	if got := venueButtons(); len(got) != listPageSize {
		t.Errorf("after clearing the search page 1 has %d venues", len(got))
	}

	// Picking a venue found by the search works.
	r.text("jazz", stSVenue)
	r.press("venue:club", stSCapacity)
	if s := r.d.session(); s.VenueID != "club" {
		t.Errorf("session = %+v", s)
	}
}

// A draft saved before the world list (a country by reference id, no extra
// scratch fields) still opens: the old payloads of its buttons keep working.
func TestWizardVenueChoice_OldPayloadsStillWork(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.eventHead()
	r.text("15.12.2026", stSTime)
	r.press("default", stSCountry)
	r.press("country:cz", stSCity) // the reference id, as a button of an older message carried it
	if s := r.d.session(); s.CountryISO2 != "CZ" || s.CountryID != "cz" {
		t.Fatalf("session = %+v", s)
	}
	r.press("city:prg", stSVenue)
	r.press("venue:akr", stSCapacity)
}

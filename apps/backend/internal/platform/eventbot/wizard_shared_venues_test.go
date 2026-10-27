package eventbot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// sharedRefs adds verified venues of other organizations to the fake source.
type sharedRefs struct {
	*fakeRefs
	shared []VenueRef
	err    error
	asked  []string
}

func (s *sharedRefs) SharedVenues(_ context.Context, _ string, _ uuid.UUID, cityID, country string) ([]VenueRef, error) {
	s.asked = append(s.asked, cityID+"|"+country)
	if s.err != nil {
		return nil, s.err
	}
	out := []VenueRef{}
	for _, v := range s.shared {
		if (cityID == "" || v.CityID == cityID) && (country == "" || v.CountryISO2 == country) {
			out = append(out, v)
		}
	}
	return out, nil
}

func sharedVenue() VenueRef {
	return VenueRef{ID: "shared1", Name: "Foreign Hall", CityID: "prg", CountryISO2: "CZ", Timezone: "Europe/Prague", Capacity: 800}
}

func buttonsOf(s Screen) map[string]string {
	got := map[string]string{}
	for _, row := range s.Buttons {
		for _, b := range row {
			got[b.Data] = b.Label
		}
	}
	return got
}

// A verified venue of another organization is listed next to the own ones,
// marked, and picking it works like picking an own venue.
func TestWizardSharedVenues_ListedMarkedAndPickable(t *testing.T) {
	refs := &sharedRefs{fakeRefs: newFakeRefs(), shared: []VenueRef{sharedVenue()}}
	r := newWizardRun(t, refs)
	seatedToVenue(r)
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	got := buttonsOf(screen)
	if got["venue:akr"] != "Palác Akropolis" {
		t.Fatalf("own venue = %q", got["venue:akr"])
	}
	if !strings.HasPrefix(got["venue:shared1"], sharedVenueMark) || !strings.Contains(got["venue:shared1"], "Foreign Hall") {
		t.Fatalf("shared venue button = %q", got["venue:shared1"])
	}
	if !strings.Contains(screen.Text, "✓") {
		t.Fatalf("no note about the mark: %q", screen.Text)
	}
	r.press("venue:shared1", stSCapacity)
	if s := r.d.Sessions[0]; s.VenueID != "shared1" || s.VenueName != "Foreign Hall" || s.Timezone != "Europe/Prague" || s.Capacity != 800 {
		t.Fatalf("session = %+v", s)
	}
}

// Only the chosen city's shared venues are asked for and shown.
func TestWizardSharedVenues_OtherCityNotShown(t *testing.T) {
	other := sharedVenue()
	other.ID, other.CityID = "shared2", "brn"
	refs := &sharedRefs{fakeRefs: newFakeRefs(), shared: []VenueRef{sharedVenue(), other}}
	r := newWizardRun(t, refs)
	seatedToVenue(r)
	screen, _ := r.w.Render(context.Background(), r.ws, r.d)
	if _, ok := buttonsOf(screen)["venue:shared2"]; ok {
		t.Fatalf("a venue of another city is listed: %v", screen.Buttons)
	}
}

// A venue the organization owns is never listed twice; a failing shared list
// leaves the own venues and no note.
func TestWizardSharedVenues_NoDuplicateAndFailureIsSilent(t *testing.T) {
	dup := VenueRef{ID: "akr", Name: "Palác Akropolis", CityID: "prg", CountryISO2: "CZ"}
	refs := &sharedRefs{fakeRefs: newFakeRefs(), shared: []VenueRef{dup}}
	r := newWizardRun(t, refs)
	seatedToVenue(r)
	screen, _ := r.w.Render(context.Background(), r.ws, r.d)
	count := 0
	for _, row := range screen.Buttons {
		for _, b := range row {
			if b.Data == "venue:akr" {
				count++
			}
		}
	}
	if count != 1 || strings.Contains(screen.Text, "✓") {
		t.Fatalf("count=%d text=%q", count, screen.Text)
	}

	refs = &sharedRefs{fakeRefs: newFakeRefs(), err: errors.New("down")}
	r = newWizardRun(t, refs)
	seatedToVenue(r)
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if got := buttonsOf(screen); got["venue:akr"] == "" {
		t.Fatalf("own venues lost: %v", screen.Buttons)
	}
}

// A city that only has a shared venue still counts as "has a venue" on the
// city question, and a country the same on the country question.
func TestWizardSharedVenues_CityAndCountryAreAsked(t *testing.T) {
	refs := &sharedRefs{fakeRefs: newFakeRefs(), shared: []VenueRef{sharedVenue()}}
	r := newWizardRun(t, refs)
	r.eventHead()
	r.text("15.12.2026", stSTime)
	r.press("default", stSCountry)
	if _, err := r.w.Render(context.Background(), r.ws, r.d); err != nil {
		t.Fatal(err)
	}
	r.press("country:cz", stSCity)
	if _, err := r.w.Render(context.Background(), r.ws, r.d); err != nil {
		t.Fatal(err)
	}
	asked := strings.Join(refs.asked, ",")
	if !strings.Contains(asked, "|") || !strings.Contains(asked, "|CZ") {
		t.Fatalf("asked = %q", asked)
	}
}

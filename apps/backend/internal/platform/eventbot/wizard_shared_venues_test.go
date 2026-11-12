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

// VenueDirectory is the fake's directory plus the verified venues of other
// organizations that match the filters, as the server lists them.
func (s *sharedRefs) VenueDirectory(ctx context.Context, jwt string, org uuid.UUID, country, cityID, q, lang string) (VenueDir, error) {
	dir, err := s.fakeRefs.VenueDirectory(ctx, jwt, org, country, cityID, q, lang)
	if err != nil {
		return dir, err
	}
	s.countShared(&dir, country)
	if country == "" && cityID == "" && q == "" {
		return dir, nil
	}
	for _, v := range s.shared {
		if (cityID != "" && v.CityID != cityID) || (country != "" && v.CountryISO2 != country) || hasDirVenue(dir.Venues, v.ID) {
			continue
		}
		dir.Venues = append(dir.Venues, DirVenue{ID: v.ID, Name: v.Name, CityID: v.CityID, CountryISO2: v.CountryISO2,
			Timezone: v.Timezone, Capacity: v.Capacity, Verified: true})
	}
	return dir, nil
}

// countShared adds the shared venues to the counts the directory shows on the
// country and city questions, as the server's directory does.
func (s *sharedRefs) countShared(dir *VenueDir, country string) {
	for _, v := range s.shared {
		own := false
		for _, o := range s.venues {
			own = own || o.ID == v.ID
		}
		if own {
			continue
		}
		found := false
		for i := range dir.Countries {
			if dir.Countries[i].ISO2 == v.CountryISO2 {
				dir.Countries[i].Venues++
				found = true
			}
		}
		if !found {
			dir.Countries = append(dir.Countries, DirCountry{ISO2: v.CountryISO2, Venues: 1})
			dir.QuickPick = append(dir.QuickPick, v.CountryISO2)
		}
		if country == "" || country != v.CountryISO2 {
			continue
		}
		found = false
		for i := range dir.Cities {
			if dir.Cities[i].ID == v.CityID {
				dir.Cities[i].Venues++
				found = true
			}
		}
		if !found {
			name := v.CityID
			for _, c := range s.cities[s.countryID(country)] {
				if c.ID == v.CityID {
					name = c.Name
				}
			}
			dir.Cities = append(dir.Cities, DirCity{ID: v.CityID, Name: name, ISO2: country, Venues: 1})
		}
	}
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

	// Without the directory a failing shared list leaves the own venues.
	refs = &sharedRefs{fakeRefs: newFakeRefs(), err: errors.New("down")}
	refs.dirErr = errors.New("directory down")
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

// When the venue directory is down the venue question still lists the own
// venues and the verified shared ones of the chosen city, as it did before the
// directory existed.
func TestWizardSharedVenues_FallbackWhenDirectoryIsDown(t *testing.T) {
	refs := &sharedRefs{fakeRefs: newFakeRefs(), shared: []VenueRef{sharedVenue()}}
	refs.dirErr = errors.New("directory down")
	r := newWizardRun(t, refs)
	r.eventHead()
	r.text("15.12.2026", stSTime)
	r.press("default", stSCountry)
	r.press("country:CZ", stSCity)
	r.press("city:prg", stSVenue)
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	got := buttonsOf(screen)
	if got["venue:akr"] == "" || !strings.HasPrefix(got["venue:shared1"], sharedVenueMark) {
		t.Fatalf("own and shared venues expected: %v", screen.Buttons)
	}
	if asked := strings.Join(refs.asked, ","); !strings.Contains(asked, "prg|") {
		t.Fatalf("asked = %q", asked)
	}
}

// A city that only has a shared venue still counts as "has a venue" on the
// city question, and its country is asked for on the country question.
func TestWizardSharedVenues_CityAndCountryAreAsked(t *testing.T) {
	other := sharedVenue()
	other.ID, other.CityID = "shared2", "brn"
	refs := &sharedRefs{fakeRefs: newFakeRefs(), shared: []VenueRef{other}}
	refs.cities["cz"] = append(refs.cities["cz"], RefItem{ID: "brn", Name: "Brno", ISO2: "CZ"})
	r := newWizardRun(t, refs)
	r.eventHead()
	r.text("15.12.2026", stSTime)
	r.press("default", stSCountry)
	if bs, _ := screenButtons(t, r); !hasData(bs, "country:CZ") {
		t.Fatalf("the country question lost the country with venues: %+v", bs)
	}
	r.press("country:CZ", stSCity)
	bs, _ := screenButtons(t, r)
	if got := labelOf(bs, "city:brn"); got != "Brno · 1" {
		t.Fatalf("a city with only a shared venue should count it, got %q in %+v", got, bs)
	}
	r.press("city:brn", stSVenue)
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if got := buttonsOf(screen); !strings.HasPrefix(got["venue:shared2"], sharedVenueMark) {
		t.Fatalf("the shared venue of the city is not offered: %v", screen.Buttons)
	}
}

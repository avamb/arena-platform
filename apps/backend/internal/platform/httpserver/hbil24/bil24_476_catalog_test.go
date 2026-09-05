// bil24_476_catalog_test.go — feature #476 (W1-A2b) slice 15 pins the
// GET_ALL_ACTIONS countryList / cityList aggregation shape (spec §7.1)
// against buildCountryCityLists. The tests use a Handler with a nil
// compatDB so the id-emitter fallback returns UUID strings (matches the
// pre-W1 unit-test contract); an integration test in a follow-up slice
// will pin the int64 wire form end-to-end against Docker PG.
package hbil24

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestBil24_476_BuildCountryCityLists_GroupsByCountryAndCity pins the
// happy path: two venues in one city, plus a second city under the same
// country, produce one country entry, two city entries, and a nested
// venueList that accumulates the first city's two venues in the SQL
// ORDER BY sequence.
func TestBil24_476_BuildCountryCityLists_GroupsByCountryAndCity(t *testing.T) {
	h := &Handler{}
	ctx := context.Background()

	czID := uuid.New()
	pragueID := uuid.New()
	brnoID := uuid.New()
	venuePragueA := uuid.New()
	venuePragueB := uuid.New()
	venueBrno := uuid.New()
	cz := "Czechia"
	prague := "Praha"
	brno := "Brno"
	iso2 := "CZ"

	rows := []gen.ActionVenueRow{
		{
			VenueID: venuePragueA, VenueName: "Palác Akropolis",
			CityID: &pragueID, CityName: &prague,
			CountryID: &czID, CountryIso2: &iso2, CountryName: &cz,
		},
		{
			VenueID: venuePragueB, VenueName: "Lucerna",
			CityID: &pragueID, CityName: &prague,
			CountryID: &czID, CountryIso2: &iso2, CountryName: &cz,
		},
		{
			VenueID: venueBrno, VenueName: "Sono Music Club",
			CityID: &brnoID, CityName: &brno,
			CountryID: &czID, CountryIso2: &iso2, CountryName: &cz,
		},
	}

	countries, cities := h.buildCountryCityLists(ctx, rows)

	if got, want := len(countries), 1; got != want {
		t.Fatalf("countryList length = %d, want %d (%+v)", got, want, countries)
	}
	if got, want := countries[0]["countryName"], cz; got != want {
		t.Errorf("countryName = %q, want %q", got, want)
	}
	// Fallback path (nil compatDB) emits UUID strings.
	if got := countries[0]["countryId"]; got != czID.String() {
		t.Errorf("countryId = %v, want %q", got, czID.String())
	}

	if got, want := len(cities), 2; got != want {
		t.Fatalf("cityList length = %d, want %d (%+v)", got, want, cities)
	}
	// First city preserves SQL order (Prague before Brno in the input).
	if got, want := cities[0]["cityName"], prague; got != want {
		t.Errorf("cities[0].cityName = %q, want %q", got, want)
	}
	pragueVenues := cities[0]["venueList"].([]map[string]any)
	if got, want := len(pragueVenues), 2; got != want {
		t.Fatalf("prague venueList length = %d, want %d (%+v)", got, want, pragueVenues)
	}
	if got, want := pragueVenues[0]["venueName"], "Palác Akropolis"; got != want {
		t.Errorf("prague venueList[0].venueName = %q, want %q", got, want)
	}
	if got, want := pragueVenues[1]["venueName"], "Lucerna"; got != want {
		t.Errorf("prague venueList[1].venueName = %q, want %q", got, want)
	}
	if got, want := cities[0]["countryId"], czID.String(); got != want {
		t.Errorf("cities[0].countryId = %v, want %q", got, want)
	}

	if got, want := cities[1]["cityName"], brno; got != want {
		t.Errorf("cities[1].cityName = %q, want %q", got, want)
	}
	brnoVenues := cities[1]["venueList"].([]map[string]any)
	if got, want := len(brnoVenues), 1; got != want {
		t.Fatalf("brno venueList length = %d, want %d", got, want)
	}
}

// TestBil24_476_BuildCountryCityLists_SkipsNilCityAndCountry pins the
// nil-safety contract: a row whose city_id is nil is omitted from the
// cityList (no reference to attach), and a row whose country_id is nil
// is omitted from countryList — matching the SQL projection's LEFT JOIN
// semantics for venues without a city_id / recognised country ISO2.
func TestBil24_476_BuildCountryCityLists_SkipsNilCityAndCountry(t *testing.T) {
	h := &Handler{}
	ctx := context.Background()

	rows := []gen.ActionVenueRow{
		{VenueID: uuid.New(), VenueName: "Orphan Venue"}, // no city, no country
	}

	countries, cities := h.buildCountryCityLists(ctx, rows)
	if len(countries) != 0 {
		t.Errorf("countryList = %+v, want empty (nil country_id must not surface)", countries)
	}
	if len(cities) != 0 {
		t.Errorf("cityList = %+v, want empty (nil city_id must not surface)", cities)
	}
}

// TestBil24_476_BuildCountryCityLists_VenueAddressAndGeo pins the
// slice-16 addition (spec §7.1): venueList entries carry `address`,
// `geoLat`, and `geoLon` when the underlying venues row has them.
// Rows without an address are emitted without the key (omit rather than
// empty string), and geo coordinates require BOTH lat AND lng — a lone
// coordinate never surfaces because the site plugin cannot render half
// a pin on the venue map.
func TestBil24_476_BuildCountryCityLists_VenueAddressAndGeo(t *testing.T) {
	h := &Handler{}
	ctx := context.Background()

	czID := uuid.New()
	pragueID := uuid.New()
	fullVenue := uuid.New()
	partialVenue := uuid.New()
	bareVenue := uuid.New()
	cz := "Czechia"
	prague := "Praha"
	iso2 := "CZ"
	addr := "Kubelíkova 27"
	lat := 50.0806
	lng := 14.4508
	lonelyLat := 49.1951

	rows := []gen.ActionVenueRow{
		{
			VenueID: fullVenue, VenueName: "Palác Akropolis",
			Address: &addr, GeoLat: &lat, GeoLng: &lng,
			CityID: &pragueID, CityName: &prague,
			CountryID: &czID, CountryIso2: &iso2, CountryName: &cz,
		},
		{
			// Address present but only latitude → geo pair MUST NOT surface.
			VenueID: partialVenue, VenueName: "Lucerna",
			Address: &addr, GeoLat: &lonelyLat,
			CityID: &pragueID, CityName: &prague,
			CountryID: &czID, CountryIso2: &iso2, CountryName: &cz,
		},
		{
			// No address, no geo — keys MUST be absent, not empty.
			VenueID: bareVenue, VenueName: "Bare Stage",
			CityID: &pragueID, CityName: &prague,
			CountryID: &czID, CountryIso2: &iso2, CountryName: &cz,
		},
	}

	_, cities := h.buildCountryCityLists(ctx, rows)
	if len(cities) != 1 {
		t.Fatalf("cityList length = %d, want 1 (all rows in one city)", len(cities))
	}
	venues := cities[0]["venueList"].([]map[string]any)
	if len(venues) != 3 {
		t.Fatalf("venueList length = %d, want 3", len(venues))
	}

	// full venue: address + geoLat + geoLon all present.
	if got, want := venues[0]["address"], addr; got != want {
		t.Errorf("venues[0].address = %v, want %q", got, want)
	}
	if got, want := venues[0]["geoLat"], lat; got != want {
		t.Errorf("venues[0].geoLat = %v, want %v", got, want)
	}
	if got, want := venues[0]["geoLon"], lng; got != want {
		t.Errorf("venues[0].geoLon = %v, want %v", got, want)
	}

	// partial venue: address present, geo pair suppressed (half-coordinate).
	if got, want := venues[1]["address"], addr; got != want {
		t.Errorf("venues[1].address = %v, want %q", got, want)
	}
	if _, has := venues[1]["geoLat"]; has {
		t.Errorf("venues[1].geoLat MUST be absent when GeoLng is nil, got %v", venues[1]["geoLat"])
	}
	if _, has := venues[1]["geoLon"]; has {
		t.Errorf("venues[1].geoLon MUST be absent when GeoLng is nil, got %v", venues[1]["geoLon"])
	}

	// bare venue: neither address nor geo keys present (omit rather than empty).
	if _, has := venues[2]["address"]; has {
		t.Errorf("venues[2].address MUST be absent when Address is nil, got %v", venues[2]["address"])
	}
	if _, has := venues[2]["geoLat"]; has {
		t.Errorf("venues[2].geoLat MUST be absent when GeoLat is nil, got %v", venues[2]["geoLat"])
	}
	if _, has := venues[2]["geoLon"]; has {
		t.Errorf("venues[2].geoLon MUST be absent when GeoLng is nil, got %v", venues[2]["geoLon"])
	}
}

// TestBil24_476_BuildCountryCityLists_EmptyInput pins the empty-input
// contract: no rows → both lists are empty but non-nil so the JSON
// envelope emits `[]` not `null`.
func TestBil24_476_BuildCountryCityLists_EmptyInput(t *testing.T) {
	h := &Handler{}
	ctx := context.Background()

	countries, cities := h.buildCountryCityLists(ctx, nil)
	if countries == nil {
		t.Errorf("countryList is nil; want non-nil empty slice for stable JSON []")
	}
	if cities == nil {
		t.Errorf("cityList is nil; want non-nil empty slice for stable JSON []")
	}
	if len(countries) != 0 || len(cities) != 0 {
		t.Errorf("expected both lists empty, got countries=%+v cities=%+v", countries, cities)
	}
}

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
	"time"

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

// TestBil24_476_BuildActionEntry_LastEventDateAndAge pins the slice-17
// spec §7.1 additions on the actionList entry body: lastEventDate is
// projected from EventRow.LastSessionAt (RFC3339 UTC), and age is
// projected from EventRow.AgeRating with the "NR" sentinel normalised
// to the empty string per spec (`age` — `events.age_rating` (`NR` → `""`)).
// Both keys are OMITTED (not emitted as empty) when the underlying
// column is nil / empty — the WP plugin treats an absent key the same
// as an empty value and omitempty wire-bytes are cheaper.
func TestBil24_476_BuildActionEntry_LastEventDateAndAge(t *testing.T) {
	h := &Handler{}
	ctx := context.Background()
	last := time.Date(2026, 4, 27, 19, 0, 0, 0, time.UTC)
	first := time.Date(2026, 4, 26, 19, 0, 0, 0, time.UTC)
	age := "12+"
	nrAge := "NR"

	// Full row: both dates present, age populated → all keys emitted.
	full := gen.EventRow{
		ID:             uuid.New(),
		Name:           "Full",
		Status:         "published",
		FirstSessionAt: &first,
		LastSessionAt:  &last,
		AgeRating:      &age,
	}
	entry := h.buildActionEntry(ctx, full, 0, "", catalogAction{})
	// Feature #497 (spec §7.1): the catalog dates are DD.MM.YYYY, not
	// RFC3339. With no projected sessions the helper falls back to the
	// first_session_at / last_session_at caches, formatted in UTC.
	if got, want := entry["lastEventDate"], last.UTC().Format("02.01.2006"); got != want { // allow:timeformat: spec §7.1
		t.Errorf("lastEventDate = %v, want %q", got, want)
	}
	if got, want := entry["firstEventDate"], first.UTC().Format("02.01.2006"); got != want { // allow:timeformat: spec §7.1
		t.Errorf("firstEventDate = %v, want %q", got, want)
	}
	if got, want := entry["age"], "12+"; got != want {
		t.Errorf("age = %v, want %q", got, want)
	}

	// NR age must be normalised to "" and then OMITTED (empty string is
	// not the same as an absent key — spec expects the latter).
	nr := gen.EventRow{
		ID:        uuid.New(),
		Name:      "NR Event",
		Status:    "published",
		AgeRating: &nrAge,
	}
	entry = h.buildActionEntry(ctx, nr, 0, "", catalogAction{})
	if _, has := entry["age"]; has {
		t.Errorf("age MUST be absent when AgeRating='NR' (normalised to empty), got %v", entry["age"])
	}

	// Bare row: no sessions, no rating → keys absent for lastEventDate,
	// firstEventDate, age (omit rather than empty).
	bare := gen.EventRow{
		ID:     uuid.New(),
		Name:   "Bare",
		Status: "published",
	}
	entry = h.buildActionEntry(ctx, bare, 0, "", catalogAction{})
	if _, has := entry["lastEventDate"]; has {
		t.Errorf("lastEventDate MUST be absent when LastSessionAt is nil, got %v", entry["lastEventDate"])
	}
	if _, has := entry["firstEventDate"]; has {
		t.Errorf("firstEventDate MUST be absent when FirstSessionAt is nil, got %v", entry["firstEventDate"])
	}
	if _, has := entry["age"]; has {
		t.Errorf("age MUST be absent when AgeRating is nil, got %v", entry["age"])
	}
	// Baseline invariants for the bare row: actionId + actionName always
	// present regardless of optional column state.
	if entry["actionName"] != "Bare" {
		t.Errorf("actionName = %v, want %q", entry["actionName"], "Bare")
	}
	if entry["actionId"] != bare.ID.String() {
		t.Errorf("actionId (nil-compatDB fallback) = %v, want %q", entry["actionId"], bare.ID.String())
	}
}

// TestBil24_476_BuildActionEntry_PosterPreference pins the slice-18
// spec §7.1 poster preference contract: when events.poster_media_id is
// set the wire URL is /v1/media-files/{uuid} (AB-47b canonical media
// host, matching hfeed.mediaFileURL); when only the legacy image_url is
// set the value passes through verbatim; when neither is set both
// bigPosterUrl and smallPosterUrl keys are OMITTED (not empty strings).
func TestBil24_476_BuildActionEntry_PosterPreference(t *testing.T) {
	h := &Handler{}
	ctx := context.Background()

	posterID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	legacy := "https://legacy.example.com/poster.jpg"

	// Preference path: poster_media_id present overrides image_url.
	both := gen.EventRow{
		ID:            uuid.New(),
		Name:          "Both",
		Status:        "published",
		PosterMediaID: &posterID,
		ImageURL:      &legacy,
	}
	entry := h.buildActionEntry(ctx, both, 0, "", catalogAction{})
	wantMedia := "/v1/media-files/" + posterID.String()
	if got := entry["bigPosterUrl"]; got != wantMedia {
		t.Errorf("bigPosterUrl = %v, want %q (poster_media_id must win over image_url)", got, wantMedia)
	}
	if got := entry["smallPosterUrl"]; got != wantMedia {
		t.Errorf("smallPosterUrl = %v, want %q", got, wantMedia)
	}

	// Fallback path: legacy image_url only.
	legacyOnly := gen.EventRow{
		ID:       uuid.New(),
		Name:     "Legacy",
		Status:   "published",
		ImageURL: &legacy,
	}
	entry = h.buildActionEntry(ctx, legacyOnly, 0, "", catalogAction{})
	if got := entry["bigPosterUrl"]; got != legacy {
		t.Errorf("bigPosterUrl = %v, want %q (legacy image_url passthrough)", got, legacy)
	}
	if got := entry["smallPosterUrl"]; got != legacy {
		t.Errorf("smallPosterUrl = %v, want %q", got, legacy)
	}

	// Empty legacy URL is treated as absent — no keys emitted.
	empty := ""
	emptyRow := gen.EventRow{
		ID:       uuid.New(),
		Name:     "Empty",
		Status:   "published",
		ImageURL: &empty,
	}
	entry = h.buildActionEntry(ctx, emptyRow, 0, "", catalogAction{})
	if _, has := entry["bigPosterUrl"]; has {
		t.Errorf("bigPosterUrl MUST be absent when image_url is empty and poster_media_id nil, got %v", entry["bigPosterUrl"])
	}
	if _, has := entry["smallPosterUrl"]; has {
		t.Errorf("smallPosterUrl MUST be absent when image_url is empty and poster_media_id nil, got %v", entry["smallPosterUrl"])
	}

	// Bare row: no artwork at all → both keys absent.
	bare := gen.EventRow{ID: uuid.New(), Name: "Bare", Status: "published"}
	entry = h.buildActionEntry(ctx, bare, 0, "", catalogAction{})
	if _, has := entry["bigPosterUrl"]; has {
		t.Errorf("bigPosterUrl MUST be absent when no poster source set, got %v", entry["bigPosterUrl"])
	}
	if _, has := entry["smallPosterUrl"]; has {
		t.Errorf("smallPosterUrl MUST be absent when no poster source set, got %v", entry["smallPosterUrl"])
	}
}

// TestBil24_498_BuildActionEntry_SessionPosterPreference pins the feature
// #498 widening of the cover resolution: the action's first projected
// session's OWN poster override (ae.posterMediaID, sessions.poster_media_id
// per migration 0082) wins over BOTH the event's poster_media_id and its
// legacy image_url. This is a strict superset of
// TestBil24_476_BuildActionEntry_PosterPreference, which only covers the
// two event-level sources.
func TestBil24_498_BuildActionEntry_SessionPosterPreference(t *testing.T) {
	h := &Handler{}
	ctx := context.Background()

	sessionPosterID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	eventPosterID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	legacy := "https://legacy.example.com/poster.jpg"

	// Session override present alongside BOTH event-level sources: session
	// wins.
	row := gen.EventRow{
		ID:            uuid.New(),
		Name:          "Session wins",
		Status:        "published",
		PosterMediaID: &eventPosterID,
		ImageURL:      &legacy,
	}
	entry := h.buildActionEntry(ctx, row, 0, "", catalogAction{posterMediaID: &sessionPosterID})
	wantMedia := "/v1/media-files/" + sessionPosterID.String()
	if got := entry["bigPosterUrl"]; got != wantMedia {
		t.Errorf("bigPosterUrl = %v, want %q (session poster must win over event poster/image_url)", got, wantMedia)
	}
	if got := entry["smallPosterUrl"]; got != wantMedia {
		t.Errorf("smallPosterUrl = %v, want %q", got, wantMedia)
	}

	// No session override: falls through to the event-level chain exactly
	// as feature #476 pinned it (event poster_media_id wins over image_url).
	entry = h.buildActionEntry(ctx, row, 0, "", catalogAction{})
	wantEventMedia := "/v1/media-files/" + eventPosterID.String()
	if got := entry["bigPosterUrl"]; got != wantEventMedia {
		t.Errorf("bigPosterUrl = %v, want %q (fallback to event poster_media_id)", got, wantEventMedia)
	}
}

// TestBil24_476_BuildActionEntry_Organizer pins the slice-19 spec §7.1
// additions on the actionList entry body: organizerId is projected from
// organizations.display_number (int64, migration 0072) and organizerName
// from organizations.name. Both keys are OMITTED when the pair is not
// available (organizerID == 0 / empty organizerName) — the WP plugin
// treats an absent key as an unset organizer chip and 0 is not a valid
// display_number so this cannot mask real data.
func TestBil24_476_BuildActionEntry_Organizer(t *testing.T) {
	h := &Handler{}
	ctx := context.Background()

	row := gen.EventRow{
		ID:     uuid.New(),
		Name:   "With Organizer",
		Status: "published",
	}

	// Full organizer context → both keys emitted with the exact values.
	entry := h.buildActionEntry(ctx, row, 7, "Lampyris Events s.r.o.", catalogAction{})
	if got, want := entry["organizerId"], int64(7); got != want {
		t.Errorf("organizerId = %v (%T), want %d (int64)", got, got, want)
	}
	if got, want := entry["organizerName"], "Lampyris Events s.r.o."; got != want {
		t.Errorf("organizerName = %v, want %q", got, want)
	}

	// No organizer context (unauthed / lookup error) → both keys absent.
	entry = h.buildActionEntry(ctx, row, 0, "", catalogAction{})
	if _, has := entry["organizerId"]; has {
		t.Errorf("organizerId MUST be absent when organizerID=0, got %v", entry["organizerId"])
	}
	if _, has := entry["organizerName"]; has {
		t.Errorf("organizerName MUST be absent when organizerName='', got %v", entry["organizerName"])
	}

	// Partial: numeric id present but name blank → id present, name absent.
	// Same guard the other direction: name present but id 0 → name present,
	// id absent. Both defensive against a rare partial fetch that leaves
	// half the pair populated.
	entry = h.buildActionEntry(ctx, row, 7, "", catalogAction{})
	if entry["organizerId"] != int64(7) {
		t.Errorf("organizerId = %v, want 7 (id must survive blank name)", entry["organizerId"])
	}
	if _, has := entry["organizerName"]; has {
		t.Errorf("organizerName MUST be absent when name is blank, got %v", entry["organizerName"])
	}
	entry = h.buildActionEntry(ctx, row, 0, "OnlyName", catalogAction{})
	if _, has := entry["organizerId"]; has {
		t.Errorf("organizerId MUST be absent when id=0, got %v", entry["organizerId"])
	}
	if entry["organizerName"] != "OnlyName" {
		t.Errorf("organizerName = %v, want %q (name must survive zero id)", entry["organizerName"], "OnlyName")
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

// TestBil24_476_SeatListCurrency_FirstNonEmpty pins the spec §7.2
// top-level currency projection (feature #476 slice 21): the helper
// returns the first tier's currency; multiple tiers all sharing one
// currency yield that currency; an empty tier slice yields "" so the
// caller can OMIT the key rather than emit an empty string; a leading
// tier with an empty currency (should never happen in production but
// defensive against a partial scan) is skipped in favor of the next
// non-empty entry.
func TestBil24_476_SeatListCurrency_FirstNonEmpty(t *testing.T) {
	cases := []struct {
		name  string
		tiers []gen.TicketTierRow
		want  string
	}{
		{
			name:  "empty slice",
			tiers: nil,
			want:  "",
		},
		{
			name: "single tier",
			tiers: []gen.TicketTierRow{
				{Currency: "CZK"},
			},
			want: "CZK",
		},
		{
			name: "multiple tiers share currency",
			tiers: []gen.TicketTierRow{
				{Currency: "EUR"},
				{Currency: "EUR"},
				{Currency: "EUR"},
			},
			want: "EUR",
		},
		{
			name: "leading empty skipped",
			tiers: []gen.TicketTierRow{
				{Currency: ""},
				{Currency: "USD"},
			},
			want: "USD",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := seatListCurrency(tc.tiers); got != tc.want {
				t.Errorf("seatListCurrency=%q want %q", got, tc.want)
			}
		})
	}
}

// TestBil24_476_BuildGASeatEntry_CategoryPriceNameRename pins the
// spec §7.2 wire-shape contract for a single GA-branch seatList entry
// (feature #476 W1-A2b slice 22). The per-entry name key is
// `categoryPriceName` — the legacy `categoryName` was pre-slice-22
// vocabulary that had no partner (the id key already used
// `categoryPriceId`) and diverged from spec §7.2 / the WP plugin.
//
// The test also pins the neighbouring invariants so a future edit does
// not re-rename or drop them by accident:
//   - categoryPriceId is passed through as-is (int64 in prod, UUID
//     string in unit tests).
//   - price is the EFFECTIVE amount (post-priceresolve), not the tier's
//     base amount.
//   - currency / pricingMode surface verbatim from the tier row.
//   - availableCount is emitted ONLY when Capacity is non-nil (the
//     uncapped case must omit rather than emit 0 or -1 — the WP plugin
//     treats an absent key as "unlimited" and 0 as "sold out").
//   - legacy `categoryName` is NEVER present on the map (regression
//     guard against a partial revert of the rename).
func TestBil24_476_BuildGASeatEntry_CategoryPriceNameRename(t *testing.T) {
	tierID := uuid.MustParse("00000000-0000-0000-0000-000000000c22")
	cap32 := int32(120)
	tier := gen.TicketTierRow{
		ID:          tierID,
		Name:        "Standing",
		PriceAmount: 300,
		Currency:    "CZK",
		PricingMode: "fixed",
		Capacity:    &cap32,
	}

	t.Run("capped tier emits all keys and availableCount", func(t *testing.T) {
		entry := buildGASeatEntry(tierID.String(), tier, 350)

		if _, ok := entry["categoryName"]; ok {
			t.Fatalf("legacy `categoryName` key must not be emitted anymore; got entry=%#v", entry)
		}
		if got, want := entry["categoryPriceName"], "Standing"; got != want {
			t.Errorf("categoryPriceName=%v want %v", got, want)
		}
		if got, want := entry["categoryPriceId"], tierID.String(); got != want {
			t.Errorf("categoryPriceId=%v want %v", got, want)
		}
		if got, want := entry["price"], int64(350); got != want {
			t.Errorf("price=%v want %v (must be the effective amount, not the base)", got, want)
		}
		if got, want := entry["currency"], "CZK"; got != want {
			t.Errorf("currency=%v want %v", got, want)
		}
		if got, want := entry["pricingMode"], "fixed"; got != want {
			t.Errorf("pricingMode=%v want %v", got, want)
		}
		if got, want := entry["availableCount"], int32(120); got != want {
			t.Errorf("availableCount=%v want %v", got, want)
		}
	})

	t.Run("uncapped tier omits availableCount", func(t *testing.T) {
		uncapped := tier
		uncapped.Capacity = nil
		entry := buildGASeatEntry(int64(1000000021), uncapped, 300)

		if _, ok := entry["availableCount"]; ok {
			t.Errorf("availableCount must be OMITTED when Capacity is nil; got %#v", entry["availableCount"])
		}
		if got, want := entry["categoryPriceId"], int64(1000000021); got != want {
			t.Errorf("categoryPriceId=%v want %v (int64 wire form on prod path)", got, want)
		}
		if got, want := entry["categoryPriceName"], "Standing"; got != want {
			t.Errorf("categoryPriceName=%v want %v", got, want)
		}
		if _, ok := entry["categoryName"]; ok {
			t.Fatalf("legacy `categoryName` key must not be emitted anymore")
		}
	})
}

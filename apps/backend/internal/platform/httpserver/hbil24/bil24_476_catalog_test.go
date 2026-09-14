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

// TestBil24_499_SeatListAvailability_QuotaModel pins spec §7.2's
// per-category `availability` rule under the GA-quota model (plan
// 08_architecture/23 step 5, migration 0101).
//
// A category OWNS its places, so the number the WordPress site renders as
// "N tickets left" is simply how many of its OWN places are free. The
// pre-0101 shared-pool arithmetic (add the free NULL-tier pool, cap it by
// the declared capacity minus what the category already took) is gone.
//
// A CLOSED category and one outside its sale window both report 0: the
// wire has no "closed" flag, so decision 4 of the plan renders them as
// sold out. Fallbacks survive only for a category that owns no place at
// all — a ledger-only session an import never materialised.
//
// The clamp at zero is part of the contract: an oversold ledger must not
// put a negative count on the wire, which legacy clients render as
// garbage rather than as "sold out".
func TestBil24_499_SeatListAvailability_QuotaModel(t *testing.T) {
	tierID := uuid.MustParse("00000000-0000-0000-0000-000000000c22")
	cap32 := int32(120)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	tier := gen.TicketTierRow{ID: tierID, Name: "Standing", Capacity: &cap32, IsOpen: true}

	ledgerCap := int32(50)
	ledgers := map[uuid.UUID]gen.InventoryLedgerRow{
		tierID: {TierID: &tierID, CapacityTotal: &ledgerCap, CapacitySold: 8, CapacityHeld: 2},
	}

	t.Run("the category's own free places win over ledger and capacity", func(t *testing.T) {
		own := map[uuid.UUID]tierUnitStats{tierID: {seats: 30, available: 7}}
		if got := seatListAvailability(tier, own, ledgers, now); got != 7 {
			t.Errorf("availability=%d want 7 (count of the category's available places)", got)
		}
	})

	// The pool bug this replaces: before migration 0101 a category that had
	// sold two tickets owned exactly two (stamped) rows and reported 0 while
	// 56 free pool units sat next to it. Now those 56 belong to a category.
	t.Run("a category that sold some places still sells the rest", func(t *testing.T) {
		sold := map[uuid.UUID]tierUnitStats{tierID: {gaUnits: 58, available: 56}}
		if got := seatListAvailability(tier, sold, nil, now); got != 56 {
			t.Errorf("availability=%d want 56 (its own free places)", got)
		}
	})

	t.Run("a closed category reports zero", func(t *testing.T) {
		closed := tier
		closed.IsOpen = false
		own := map[uuid.UUID]tierUnitStats{tierID: {gaUnits: 60, available: 56}}
		if got := seatListAvailability(closed, own, nil, now); got != 0 {
			t.Errorf("availability=%d want 0 (closed reads as sold out — decision 4)", got)
		}
	})

	t.Run("a category outside its sale window reports zero", func(t *testing.T) {
		own := map[uuid.UUID]tierUnitStats{tierID: {gaUnits: 60, available: 56}}

		notYet := tier
		start := now.Add(24 * time.Hour)
		notYet.SaleWindowStart = &start
		if got := seatListAvailability(notYet, own, nil, now); got != 0 {
			t.Errorf("availability=%d want 0 (sale has not opened — decision 5)", got)
		}

		over := tier
		ended := now.Add(-24 * time.Hour)
		over.SaleWindowEnd = &ended
		if got := seatListAvailability(over, own, nil, now); got != 0 {
			t.Errorf("availability=%d want 0 (sale window closed — decision 5)", got)
		}

		open := tier
		open.SaleWindowStart = &ended
		later := now.Add(24 * time.Hour)
		open.SaleWindowEnd = &later
		if got := seatListAvailability(open, own, nil, now); got != 56 {
			t.Errorf("availability=%d want 56 (inside the window)", got)
		}
	})

	t.Run("ledger used when the category owns no place", func(t *testing.T) {
		if got := seatListAvailability(tier, nil, ledgers, now); got != 40 {
			t.Errorf("availability=%d want 40 (capacity_total 50 - sold 8 - held 2)", got)
		}
	})

	t.Run("session-level ledger used when nothing else exists", func(t *testing.T) {
		sessCap := int32(50)
		sessLedger := map[uuid.UUID]gen.InventoryLedgerRow{
			uuid.Nil: {CapacityTotal: &sessCap, CapacitySold: 5, CapacityHeld: 1},
		}
		uncapped := tier
		uncapped.Capacity = nil
		if got := seatListAvailability(uncapped, nil, sessLedger, now); got != 44 {
			t.Errorf("availability=%d want 44 (session ledger 50 - sold 5 - held 1)", got)
		}
	})

	t.Run("category capacity used when no ledger and no places exist", func(t *testing.T) {
		if got := seatListAvailability(tier, nil, nil, now); got != 120 {
			t.Errorf("availability=%d want 120 (the category's own capacity)", got)
		}
	})

	t.Run("unlimited ledger falls through to category capacity", func(t *testing.T) {
		unlimited := map[uuid.UUID]gen.InventoryLedgerRow{
			tierID: {TierID: &tierID, CapacityTotal: nil, CapacitySold: 3},
		}
		if got := seatListAvailability(tier, nil, unlimited, now); got != 120 {
			t.Errorf("availability=%d want 120 (nil capacity_total means unlimited, not zero)", got)
		}
	})

	t.Run("uncapped category with no ledger reports zero", func(t *testing.T) {
		uncapped := tier
		uncapped.Capacity = nil
		if got := seatListAvailability(uncapped, nil, nil, now); got != 0 {
			t.Errorf("availability=%d want 0", got)
		}
	})

	t.Run("oversold ledger clamps at zero", func(t *testing.T) {
		uncapped := tier
		uncapped.Capacity = nil
		oversold := map[uuid.UUID]gen.InventoryLedgerRow{
			tierID: {TierID: &tierID, CapacityTotal: &ledgerCap, CapacitySold: 60, CapacityHeld: 5},
		}
		if got := seatListAvailability(uncapped, nil, oversold, now); got != 0 {
			t.Errorf("availability=%d want 0 (never negative on the wire)", got)
		}
	})
}

// TestBil24_GAAllActionsCategoryAvailability pins GET_ALL_ACTIONS'
// categoryLimitList count under the quota model: a category's own free
// places, 0 when it is closed or outside its sale window, and the
// session-level count only for a category that owns no place at all.
func TestBil24_GAAllActionsCategoryAvailability(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	c50, c10 := int32(50), int32(10)
	std := gen.ActionEventTierRow{
		Tier: gen.TicketTierRow{Name: "Standard", Capacity: &c50, IsOpen: true},
		IsGA: true, GAUnitsTotal: 50, GAUnitsAvailable: 48,
	}
	vip := gen.ActionEventTierRow{
		Tier: gen.TicketTierRow{Name: "VIP", Capacity: &c10, IsOpen: true},
		IsGA: true, GAUnitsTotal: 10, GAUnitsAvailable: 8,
	}

	// The staging shape that produced the pre-0101 bug: 60 places split
	// 50/10, two sold from each. Both categories must offer what is left.
	if got := gaCategoryAvailability(std, 56, now); got != 48 {
		t.Errorf("Standard=%d want 48 (its own free places)", got)
	}
	if got := gaCategoryAvailability(vip, 56, now); got != 8 {
		t.Errorf("VIP=%d want 8 (its own free places)", got)
	}

	soldOut := vip
	soldOut.GAUnitsAvailable = 0
	if got := gaCategoryAvailability(soldOut, 56, now); got != 0 {
		t.Errorf("sold-out category=%d want 0", got)
	}

	closed := std
	closed.Tier.IsOpen = false
	if got := gaCategoryAvailability(closed, 56, now); got != 0 {
		t.Errorf("closed category=%d want 0 (decision 4)", got)
	}

	ended := now.Add(-time.Hour)
	offSale := std
	offSale.Tier.SaleWindowEnd = &ended
	if got := gaCategoryAvailability(offSale, 56, now); got != 0 {
		t.Errorf("off-sale category=%d want 0 (decision 5)", got)
	}

	noPlaces := gen.ActionEventTierRow{
		Tier: gen.TicketTierRow{Name: "Extra", IsOpen: true}, IsGA: true,
	}
	if got := gaCategoryAvailability(noPlaces, 12, now); got != 12 {
		t.Errorf("category without places=%d want 12 (session count)", got)
	}
}

// TestBil24_SessionAvailability_SumsOpenCategories pins GET_ALL_ACTIONS'
// session-level `availability` under the quota model: the sum of the free
// places of the OPEN, on-sale categories, seated and GA alike. A session
// whose remaining stock all sits in a closed category advertises 0 rather
// than counting rows nobody may buy.
func TestBil24_SessionAvailability_SumsOpenCategories(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	sess := gen.ActionEventRow{SeatsTotal: 60, SeatsAvailable: 56, LedgerAvailable: 99}

	ga := gen.ActionEventTierRow{
		Tier: gen.TicketTierRow{Name: "Standing", IsOpen: true},
		IsGA: true, GAUnitsTotal: 50, GAUnitsAvailable: 48,
	}
	seated := gen.ActionEventTierRow{
		Tier:       gen.TicketTierRow{Name: "Parter", IsOpen: true},
		SeatsTotal: 10, SeatsAvailable: 8,
	}
	if got := sessionAvailability(sess, []gen.ActionEventTierRow{ga, seated}, now); got != 56 {
		t.Errorf("availability=%d want 56 (48 GA places + 8 seats)", got)
	}

	closed := ga
	closed.Tier.IsOpen = false
	if got := sessionAvailability(sess, []gen.ActionEventTierRow{closed, seated}, now); got != 8 {
		t.Errorf("availability=%d want 8 (the closed category contributes nothing)", got)
	}

	// No category owns a place: keep the pre-0101 session-level answer.
	noPlaces := []gen.ActionEventTierRow{{Tier: gen.TicketTierRow{Name: "Extra", IsOpen: true}, IsGA: true}}
	if got := sessionAvailability(sess, noPlaces, now); got != 56 {
		t.Errorf("availability=%d want 56 (free session_seats fallback)", got)
	}
	ledgerOnly := gen.ActionEventRow{SeatsTotal: 0, LedgerAvailable: 42}
	if got := sessionAvailability(ledgerOnly, noPlaces, now); got != 42 {
		t.Errorf("availability=%d want 42 (session ledger fallback)", got)
	}
	oversold := gen.ActionEventRow{SeatsTotal: 0, LedgerAvailable: -3}
	if got := sessionAvailability(oversold, nil, now); got != 0 {
		t.Errorf("availability=%d want 0 (never negative on the wire)", got)
	}
}

// TestBil24_499_SeatListPlacement_TriState pins spec §7.2's TRI-STATE
// `placement` flag (feature #499). The three states are semantically
// distinct and the WordPress plugin branches on all three:
//
//	nil   — pure general-admission session: there is no seating plan, so
//	        "is this category placed?" has no answer and the KEY IS ABSENT
//	        from the JSON (the *bool carries `omitempty`).
//	false — a GA category living inside a plan (the standing-room block of
//	        a hybrid session, INCLUDING one added by hand to a session that
//	        used to be assigned_seats — decision 10 makes every such
//	        category GA): placed inventory exists, this category just is
//	        not part of it.
//	true  — a seated category.
//
// Collapsing nil into false is the tempting bug: it would make a pure-GA
// session claim it has a seat map.
func TestBil24_499_SeatListPlacement_TriState(t *testing.T) {
	t.Run("pure GA session omits the key", func(t *testing.T) {
		if got := seatListPlacement("general_admission", tierUnitStats{gaUnits: 50}); got != nil {
			t.Errorf("placement=%v want nil (key absent on a pure-GA session)", *got)
		}
	})

	t.Run("hybrid GA-only category is false", func(t *testing.T) {
		got := seatListPlacement("hybrid", tierUnitStats{gaUnits: 40})
		if got == nil || *got {
			t.Errorf("placement=%v want false (GA category inside a seating plan)", got)
		}
	})

	t.Run("hybrid seated category is true", func(t *testing.T) {
		got := seatListPlacement("hybrid", tierUnitStats{seats: 84})
		if got == nil || !*got {
			t.Errorf("placement=%v want true (seated category)", got)
		}
	})

	t.Run("assigned_seats category with no units yet is true", func(t *testing.T) {
		got := seatListPlacement("assigned_seats", tierUnitStats{})
		if got == nil || !*got {
			t.Errorf("placement=%v want true (a placed session's categories default to placed)", got)
		}
	})
}

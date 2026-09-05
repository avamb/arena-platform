//go:build integration

package gen_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestListActionVenuesByOrg_LiveDB is the round-trip DB test for the
// Bil24 compat GET_ALL_ACTIONS aggregation query landed by feature #476
// W1-A2b slice 14 (spec §7.1). The test verifies:
//
//  1. A published event's venue surfaces in the projection with the
//     city + country reference data attached via the city_id chain.
//  2. A venue attached only via venues.country (no city_id) still yields
//     country fields via the co_vn fallback join.
//  3. A draft-event-only venue is filtered out (published-only rule).
//  4. Cross-org isolation: venues owned by a different org do not leak.
//  5. Locale fallback picks the requested locale from i18n_text, and the
//     'en' row when the requested locale is missing.
//
// The full compat handler wiring is added in a later slice; this test
// pins the SQL contract the handler will build on.
func TestListActionVenuesByOrg_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, mustPoolConfig(t, dsn, 4))
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	f := createActionVenuesFixture(t, ctx, pool)
	defer f.cleanup()

	q := gen.New(pool)

	rows, err := q.ListActionVenuesByOrg(ctx, f.orgID, "ru")
	if err != nil {
		t.Fatalf("ListActionVenuesByOrg: %v", err)
	}

	// Only the two venues with published-event sessions surface; the
	// draft-only venue and the other-org venue are filtered out.
	byID := map[uuid.UUID]gen.ActionVenueRow{}
	for _, r := range rows {
		if r.VenueID == f.venueWithCity || r.VenueID == f.venueWithoutCity {
			byID[r.VenueID] = r
		}
		if r.VenueID == f.draftOnlyVenue {
			t.Errorf("draft-only venue leaked into results")
		}
		if r.VenueID == f.otherOrgVenue {
			t.Errorf("other-org venue leaked into results")
		}
	}
	if len(byID) != 2 {
		t.Fatalf("want 2 in-scope venues, got %d (rows=%+v)", len(byID), rows)
	}

	// Venue-with-city: country resolved via cities.country_id, city_name
	// via i18n_text 'ru' row, country_name via 'ru' row.
	vc, ok := byID[f.venueWithCity]
	if !ok {
		t.Fatalf("venueWithCity missing from projection")
	}
	if vc.CityID == nil || *vc.CityID != f.cityID {
		t.Errorf("venueWithCity.CityID = %v; want %v", vc.CityID, f.cityID)
	}
	if vc.CountryIso2 == nil || *vc.CountryIso2 != "HU" {
		t.Errorf("venueWithCity.CountryIso2 = %v; want HU", vc.CountryIso2)
	}
	if vc.CityName == nil || *vc.CityName != "Будапешт" {
		t.Errorf("venueWithCity.CityName = %v; want ru-localized Будапешт", vc.CityName)
	}
	if vc.CountryName == nil || *vc.CountryName != "Венгрия" {
		t.Errorf("venueWithCity.CountryName = %v; want ru-localized Венгрия", vc.CountryName)
	}

	// Venue-without-city: country resolved via venues.country (co_vn); no
	// city_id / city_name; locale 'ru' has no country row for CZ so the
	// English fallback wins.
	vn, ok := byID[f.venueWithoutCity]
	if !ok {
		t.Fatalf("venueWithoutCity missing from projection")
	}
	if vn.CityID != nil {
		t.Errorf("venueWithoutCity.CityID = %v; want nil", vn.CityID)
	}
	if vn.CountryIso2 == nil || *vn.CountryIso2 != "CZ" {
		t.Errorf("venueWithoutCity.CountryIso2 = %v; want CZ", vn.CountryIso2)
	}
	if vn.CountryName == nil || *vn.CountryName != "Czechia" {
		t.Errorf("venueWithoutCity.CountryName = %v; want en-fallback Czechia", vn.CountryName)
	}
}

type actionVenuesFixture struct {
	t                *testing.T
	pool             *pgxpool.Pool
	orgID            uuid.UUID
	otherOrgID       uuid.UUID
	hungaryID        uuid.UUID
	czechiaID        uuid.UUID
	cityID           uuid.UUID
	venueWithCity    uuid.UUID
	venueWithoutCity uuid.UUID
	draftOnlyVenue   uuid.UUID
	otherOrgVenue    uuid.UUID
	publishedEventID uuid.UUID
	draftEventID     uuid.UUID
	otherEventID     uuid.UUID
	sessionA         uuid.UUID
	sessionB         uuid.UUID
	draftSession     uuid.UUID
	otherSession     uuid.UUID
	// The i18n_text and countries/cities rows may be pre-seeded; track
	// which ones this fixture created so cleanup only touches its own.
	insertedHungary bool
	insertedCzechia bool
	insertedCity    bool
	i18nKeys        []struct{ ns, key, locale string }
}

func createActionVenuesFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *actionVenuesFixture {
	suffix := uuid.NewString()[:8]
	f := &actionVenuesFixture{
		t: t, pool: pool,
		orgID:            uuid.New(),
		otherOrgID:       uuid.New(),
		venueWithCity:    uuid.New(),
		venueWithoutCity: uuid.New(),
		draftOnlyVenue:   uuid.New(),
		otherOrgVenue:    uuid.New(),
		publishedEventID: uuid.New(),
		draftEventID:     uuid.New(),
		otherEventID:     uuid.New(),
		sessionA:         uuid.New(),
		sessionB:         uuid.New(),
		draftSession:     uuid.New(),
		otherSession:     uuid.New(),
	}

	// countries — reuse when already seeded, insert otherwise so the test
	// works against a bare CI migrate as well as a fully seeded local DB.
	if err := pool.QueryRow(ctx,
		`SELECT id FROM countries WHERE iso2 = 'HU'`).Scan(&f.hungaryID); err != nil {
		f.hungaryID = uuid.New()
		if _, err := pool.Exec(ctx,
			`INSERT INTO countries (id, iso2, iso3, slug, currency)
			 VALUES ($1, 'HU', 'HUN', 'hungary-`+suffix+`', 'HUF')`,
			f.hungaryID); err != nil {
			t.Fatalf("insert HU: %v", err)
		}
		f.insertedHungary = true
	}
	if err := pool.QueryRow(ctx,
		`SELECT id FROM countries WHERE iso2 = 'CZ'`).Scan(&f.czechiaID); err != nil {
		f.czechiaID = uuid.New()
		if _, err := pool.Exec(ctx,
			`INSERT INTO countries (id, iso2, iso3, slug, currency)
			 VALUES ($1, 'CZ', 'CZE', 'czechia-`+suffix+`', 'CZK')`,
			f.czechiaID); err != nil {
			t.Fatalf("insert CZ: %v", err)
		}
		f.insertedCzechia = true
	}

	// city — unique slug per test run.
	f.cityID = uuid.New()
	citySlug := "budapest-" + suffix
	if _, err := pool.Exec(ctx,
		`INSERT INTO cities (id, country_id, slug) VALUES ($1, $2, $3)`,
		f.cityID, f.hungaryID, citySlug); err != nil {
		t.Fatalf("insert city: %v", err)
	}
	f.insertedCity = true

	// i18n rows — city + country localized names.
	i18nRows := []struct {
		ns, key, locale, value string
	}{
		{"geo.cities", citySlug, "ru", "Будапешт"},
		{"geo.cities", citySlug, "en", "Budapest"},
		{"geo.countries", "HU", "ru", "Венгрия"},
		{"geo.countries", "HU", "en", "Hungary"},
		// CZ has no 'ru' row on purpose — the projection must fall back
		// to the 'en' row 'Czechia'.
		{"geo.countries", "CZ", "en", "Czechia"},
	}
	for _, r := range i18nRows {
		if _, err := pool.Exec(ctx,
			`INSERT INTO i18n_text (namespace, key, locale, value)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (namespace, key, locale) DO NOTHING`,
			r.ns, r.key, r.locale, r.value); err != nil {
			t.Fatalf("insert i18n_text %s/%s/%s: %v", r.ns, r.key, r.locale, err)
		}
		f.i18nKeys = append(f.i18nKeys,
			struct{ ns, key, locale string }{r.ns, r.key, r.locale})
	}

	// orgs
	for _, o := range []struct {
		id   uuid.UUID
		name string
		slug string
	}{
		{f.orgID, "AV Org " + suffix, "av-" + suffix},
		{f.otherOrgID, "AV Other " + suffix, "av-other-" + suffix},
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			o.id, o.name, o.slug); err != nil {
			t.Fatalf("insert org: %v", err)
		}
	}

	// venues
	if _, err := pool.Exec(ctx,
		`INSERT INTO venues (id, org_id, city_id, name)
		 VALUES ($1, $2, $3, $4)`,
		f.venueWithCity, f.orgID, f.cityID, "AV VenueCity "+suffix); err != nil {
		t.Fatalf("insert venueWithCity: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO venues (id, org_id, name, country)
		 VALUES ($1, $2, $3, 'CZ')`,
		f.venueWithoutCity, f.orgID, "AV VenueNoCity "+suffix); err != nil {
		t.Fatalf("insert venueWithoutCity: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO venues (id, org_id, name)
		 VALUES ($1, $2, $3)`,
		f.draftOnlyVenue, f.orgID, "AV VenueDraft "+suffix); err != nil {
		t.Fatalf("insert draftOnlyVenue: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO venues (id, org_id, name)
		 VALUES ($1, $2, $3)`,
		f.otherOrgVenue, f.otherOrgID, "AV VenueOther "+suffix); err != nil {
		t.Fatalf("insert otherOrgVenue: %v", err)
	}

	// events: one published for the org, one draft for the org, one
	// published for the other org.
	if _, err := pool.Exec(ctx,
		`INSERT INTO events (id, org_id, name, status, visibility)
		 VALUES ($1, $2, $3, 'published', 'public')`,
		f.publishedEventID, f.orgID, "AV Ev Pub "+suffix); err != nil {
		t.Fatalf("insert publishedEvent: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO events (id, org_id, name, status, visibility)
		 VALUES ($1, $2, $3, 'draft', 'private')`,
		f.draftEventID, f.orgID, "AV Ev Draft "+suffix); err != nil {
		t.Fatalf("insert draftEvent: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO events (id, org_id, name, status, visibility)
		 VALUES ($1, $2, $3, 'published', 'public')`,
		f.otherEventID, f.otherOrgID, "AV Ev Other "+suffix); err != nil {
		t.Fatalf("insert otherEvent: %v", err)
	}

	// sessions
	sessions := []struct {
		id      uuid.UUID
		event   uuid.UUID
		venue   uuid.UUID
		startAt string
	}{
		{f.sessionA, f.publishedEventID, f.venueWithCity, "+30 days"},
		{f.sessionB, f.publishedEventID, f.venueWithoutCity, "+31 days"},
		{f.draftSession, f.draftEventID, f.draftOnlyVenue, "+32 days"},
		{f.otherSession, f.otherEventID, f.otherOrgVenue, "+33 days"},
	}
	for _, s := range sessions {
		if _, err := pool.Exec(ctx, `
			INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
			    capacity_total, status, admission_mode, currency, currency_source)
			VALUES ($1, $2, $3, now() + $4::interval,
			    now() + $4::interval + interval '2 hours',
			    50, 'scheduled', 'general_admission', 'EUR', 'override')`,
			s.id, s.event, s.venue, s.startAt); err != nil {
			t.Fatalf("insert session %s: %v", s.id, err)
		}
	}

	return f
}

func (f *actionVenuesFixture) cleanup() {
	ctx := context.Background()

	// order matters: sessions -> events -> venues -> orgs -> city -> countries
	for _, id := range []uuid.UUID{f.sessionA, f.sessionB, f.draftSession, f.otherSession} {
		if _, err := f.pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id); err != nil {
			f.t.Logf("cleanup session %s: %v", id, err)
		}
	}
	for _, id := range []uuid.UUID{f.publishedEventID, f.draftEventID, f.otherEventID} {
		if _, err := f.pool.Exec(ctx, `DELETE FROM events WHERE id = $1`, id); err != nil {
			f.t.Logf("cleanup event %s: %v", id, err)
		}
	}
	for _, id := range []uuid.UUID{f.venueWithCity, f.venueWithoutCity, f.draftOnlyVenue, f.otherOrgVenue} {
		if _, err := f.pool.Exec(ctx, `DELETE FROM venues WHERE id = $1`, id); err != nil {
			f.t.Logf("cleanup venue %s: %v", id, err)
		}
	}
	for _, id := range []uuid.UUID{f.orgID, f.otherOrgID} {
		if _, err := f.pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, id); err != nil {
			f.t.Logf("cleanup org %s: %v", id, err)
		}
	}
	if f.insertedCity {
		if _, err := f.pool.Exec(ctx, `DELETE FROM cities WHERE id = $1`, f.cityID); err != nil {
			f.t.Logf("cleanup city: %v", err)
		}
	}
	for _, r := range f.i18nKeys {
		if _, err := f.pool.Exec(ctx,
			`DELETE FROM i18n_text
			 WHERE namespace = $1 AND key = $2 AND locale = $3`,
			r.ns, r.key, r.locale); err != nil {
			f.t.Logf("cleanup i18n_text %s/%s/%s: %v", r.ns, r.key, r.locale, err)
		}
	}
	if f.insertedHungary {
		if _, err := f.pool.Exec(ctx, `DELETE FROM countries WHERE id = $1`, f.hungaryID); err != nil {
			f.t.Logf("cleanup HU: %v", err)
		}
	}
	if f.insertedCzechia {
		if _, err := f.pool.Exec(ctx, `DELETE FROM countries WHERE id = $1`, f.czechiaID); err != nil {
			f.t.Logf("cleanup CZ: %v", err)
		}
	}
}

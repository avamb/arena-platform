//go:build integration

package httpserver

// world_country_integration_test.go — spec 64 section 2.1.1, "a new country
// comes with a venue": POST /v1/organizations/{org_id}/venues with
// country_iso2 + city_name creates the country from the world table, the city
// and the venue in one transaction and queues ONE "new country" notification;
// an API key never creates a country; a refused venue leaves no country; the
// geo helpers answer for a country only the world table has.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
)

// wcAbsent makes sure the reference has no row of iso2 (deleting a leftover
// one without cities) and removes whatever the test adds for it afterwards.
func wcAbsent(t *testing.T, e *vpnEnv, iso2 string) {
	t.Helper()
	ctx := context.Background()
	pool := e.srv.pgxPool
	purge := func() error {
		var id *uuid.UUID
		_ = pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2 = $1`, iso2).Scan(&id)
		if id == nil {
			return nil
		}
		orgs := []uuid.UUID{e.orgA, e.orgB}
		stmts := []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM worker_jobs WHERE job_type = 'venue_plan.notify' AND payload->>'country_id' = $1`, []any{id.String()}},
			{`DELETE FROM venue_plan_notify_log WHERE subject_id = $1`, []any{*id}},
			{`DELETE FROM audit_events WHERE resource_type = 'country' AND resource_id = $1`, []any{id.String()}},
			{`DELETE FROM venues WHERE org_id = ANY($2) AND city_id IN (SELECT id FROM cities WHERE country_id = $1)`, []any{*id, orgs}},
			{`DELETE FROM i18n_text WHERE namespace = 'geo.cities' AND key IN (SELECT slug FROM cities WHERE country_id = $1)`, []any{*id}},
			{`DELETE FROM cities WHERE country_id = $1`, []any{*id}},
			{`DELETE FROM countries WHERE id = $1`, []any{*id}},
		}
		for _, s := range stmts {
			if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
				return fmt.Errorf("%s: %w", s.sql, err)
			}
		}
		_, err := pool.Exec(ctx, `DELETE FROM i18n_text WHERE namespace = 'geo.countries' AND key = $1`, iso2)
		return err
	}
	if err := purge(); err != nil {
		t.Skipf("the database already uses %s and it cannot be removed: %v", iso2, err)
	}
	t.Cleanup(func() {
		if err := purge(); err != nil {
			t.Logf("cleanup of %s: %v", iso2, err)
		}
	})
}

func (e *vpnEnv) countryRows(t *testing.T, iso2 string) int {
	return e.count(t, `SELECT count(*) FROM countries WHERE iso2 = $1`, iso2)
}

func TestWorldCountry_VenueCreatesCountryCityAndOneNotification(t *testing.T) {
	e := newVPNEnv(t)
	wcAbsent(t, e, "MN")
	wcAbsent(t, e, "LA")
	org := "/v1/organizations/" + e.orgA.String()
	ctx := context.Background()

	// An organization API key never creates a country, and nothing is written.
	creator := e.user(t, "wc-keyowner")
	_, key, err := apikeys.Issue(ctx, apikeys.NewStoreFromQueries(e.q), apikeys.IssueInput{
		OrgID: e.orgA, Name: "wc-" + uuid.NewString()[:6], Scopes: []string{"venue.create", "venue.read"}, CreatedBy: creator,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}
	keyBody := `{"name":"WC Key Hall","timezone":"Asia/Ulaanbaatar","country_iso2":"MN","city_name":"Ulaanbaatar"}`
	if r := e.jsonDo(t, http.MethodPost, org+"/venues", key, "", keyBody); r.status != http.StatusForbidden || r.code() != "venue.country_create_forbidden" {
		t.Fatalf("API key: %d %s", r.status, r.body)
	}
	if e.countryRows(t, "MN") != 0 || e.count(t, `SELECT count(*) FROM venues WHERE org_id = $1 AND name = 'WC Key Hall'`, e.orgA) != 0 {
		t.Fatal("the API key left a country or a venue")
	}

	// Malformed requests.
	cityID := uuid.NewString()
	for body, code := range map[string]string{
		`{"name":"X","timezone":"Europe/Madrid","city_id":"` + cityID + `","country_iso2":"MN","city_name":"Y"}`: "venue.city_conflict",
		`{"name":"X","timezone":"Europe/Madrid","country_iso2":"ZZ","city_name":"Y"}`:                            "venue.unknown_country",
		`{"name":"X","timezone":"Europe/Madrid","country_iso2":"MN"}`:                                            "venue.city_name_required",
		`{"name":"X","timezone":"Europe/Madrid","city_name":"Y"}`:                                                "venue.country_required",
	} {
		if r := e.jsonDo(t, http.MethodPost, org+"/venues", e.organizerToken, "", body); r.status != http.StatusBadRequest || r.code() != code {
			t.Errorf("%s: %d %s, want 400 %s", body, r.status, r.body, code)
		}
	}

	// The geo helpers answer for a country only the world table has.
	if r := e.jsonDo(t, http.MethodGet, "/v1/geo/countries/LA/currency", "", "", ""); r.status != http.StatusOK || r.json(t)["currency"] != "LAK" {
		t.Errorf("LA currency: %d %s", r.status, r.body)
	}
	if r := e.jsonDo(t, http.MethodGet, "/v1/geo/countries/MN/timezones", "", "", ""); r.status != http.StatusOK || !strings.Contains(string(r.body), "Asia/Ulaanbaatar") {
		t.Errorf("MN zones: %d %s", r.status, r.body)
	}
	// A city by country code is refused for a country outside the reference.
	if r := e.jsonDo(t, http.MethodPost, org+"/cities", e.organizerToken, "", `{"country_iso2":"MN","name":"Ulaanbaatar"}`); r.status != http.StatusConflict || r.code() != "city.country_not_in_reference" {
		t.Errorf("city in a new country: %d %s", r.status, r.body)
	}

	// The organizer creates the venue: country, names, city and venue appear.
	name := "WC Steppe Hall " + uuid.NewString()[:8]
	body := fmt.Sprintf(`{"name":%q,"timezone":"Asia/Ulaanbaatar","country_iso2":"mn","city_name":"Ulaanbaatar"}`, name)
	r := e.jsonDo(t, http.MethodPost, org+"/venues?lang=ru", e.organizerToken, "", body)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	var created struct {
		Venue struct {
			ID      string  `json:"id"`
			CityID  *string `json:"city_id"`
			Country *string `json:"country"`
		} `json:"venue"`
		City struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			CountryIso2 string `json:"country_iso2"`
		} `json:"city"`
		CreatedCountry *struct {
			ID       string `json:"id"`
			Iso2     string `json:"iso2"`
			Name     string `json:"name"`
			Currency string `json:"currency"`
			Region   string `json:"region"`
		} `json:"created_country"`
	}
	if err := json.Unmarshal(r.body, &created); err != nil {
		t.Fatal(err)
	}
	cc := created.CreatedCountry
	if cc == nil || cc.Iso2 != "MN" || cc.Name != "Монголия" || cc.Currency != "MNT" || cc.Region != "asia" {
		t.Fatalf("created_country = %+v (%s)", cc, r.body)
	}
	if created.City.CountryIso2 != "MN" || created.City.Name != "Ulaanbaatar" || created.Venue.CityID == nil || *created.Venue.CityID != created.City.ID {
		t.Fatalf("city / venue = %s", r.body)
	}
	if created.Venue.Country == nil || *created.Venue.Country != "MN" {
		t.Errorf("venue.country = %v, want MN", created.Venue.Country)
	}
	var iso3, slug, currency, region string
	if err := e.srv.pgxPool.QueryRow(ctx, `SELECT iso3, slug, btrim(currency), region FROM countries WHERE iso2 = 'MN'`).Scan(&iso3, &slug, &currency, &region); err != nil {
		t.Fatalf("country row: %v", err)
	}
	if iso3 != "MNG" || slug != "mongolia" || currency != "MNT" || region != "asia" {
		t.Errorf("country row = %s %s %s %s", iso3, slug, currency, region)
	}
	if n := e.count(t, `SELECT count(*) FROM i18n_text WHERE namespace = 'geo.countries' AND key = 'MN' AND
		((locale = 'en' AND value = 'Mongolia') OR (locale = 'ru' AND value = 'Монголия'))`); n != 2 {
		t.Errorf("country names = %d rows, want 2", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_events WHERE action = 'v1.country.create' AND resource_id = $1`, cc.ID); n != 1 {
		t.Errorf("country audit rows = %d, want 1", n)
	}

	// One venue_created and ONE country_created job; the country message goes
	// to every administrator once, also when the job runs twice.
	e.job(t, "venue_created", "venue_id", created.Venue.ID)
	payload := e.job(t, "country_created", "country_iso2", "MN")
	title := "Новая страна: Монголия, площадка " + name + ", организация " + e.orgName
	details, _ := e.run(t, payload, title, "Площадка: "+name, "Страна: Монголия (MN)", "Валюта: MNT", "Город: Ulaanbaatar", vpnAppURL+"/geo")
	if !strings.Contains(details, "Регион: Азия") {
		t.Errorf("no region in the message:\n%s", details)
	}

	// A second venue in the same country creates no country and no message.
	r = e.jsonDo(t, http.MethodPost, org+"/venues", e.organizerToken, "", `{"name":"WC Second Hall","timezone":"Asia/Ulaanbaatar","country_iso2":"MN","city_name":"ulaanbaatar"}`)
	if r.status != http.StatusCreated || strings.Contains(string(r.body), "created_country") {
		t.Fatalf("second venue: %d %s", r.status, r.body)
	}
	if got := r.json(t)["city"].(map[string]any)["id"]; got != created.City.ID {
		t.Errorf("the second venue got another city %v, want %s", got, created.City.ID)
	}
	e.job(t, "country_created", "country_iso2", "MN") // still exactly one
	// Now the city route takes the code.
	if r := e.jsonDo(t, http.MethodPost, org+"/cities", e.organizerToken, "", `{"country_iso2":"MN","name":"Darkhan"}`); r.status != http.StatusCreated {
		t.Errorf("city by code in a reference country: %d %s", r.status, r.body)
	}

	// The world list marks the reference country and the one outside it.
	w := e.jsonDo(t, http.MethodGet, "/v1/geo/world-countries?locale=ru", "", "", "")
	if w.status != http.StatusOK {
		t.Fatalf("world countries: %d %s", w.status, w.body)
	}
	var world struct {
		Regions   []string `json:"regions"`
		Countries []struct {
			Iso2        string  `json:"iso2"`
			Name        string  `json:"name"`
			InReference bool    `json:"in_reference"`
			CountryID   *string `json:"country_id"`
		} `json:"countries"`
	}
	if err := json.Unmarshal(w.body, &world); err != nil {
		t.Fatal(err)
	}
	if len(world.Regions) != 7 || len(world.Countries) < 245 {
		t.Fatalf("world list: %d regions, %d countries", len(world.Regions), len(world.Countries))
	}
	for _, c := range world.Countries {
		switch c.Iso2 {
		case "MN":
			if !c.InReference || c.CountryID == nil || *c.CountryID != cc.ID || c.Name != "Монголия" {
				t.Errorf("MN in the world list: %+v", c)
			}
		case "LA":
			if c.InReference || c.CountryID != nil {
				t.Errorf("LA in the world list: %+v", c)
			}
		}
	}
}

// A refused venue (a duplicate name) leaves no country and no city behind.
func TestWorldCountry_RefusedVenueLeavesNoCountry(t *testing.T) {
	e := newVPNEnv(t)
	wcAbsent(t, e, "LA")
	org := "/v1/organizations/" + e.orgA.String()
	name := "WC Dup Hall " + uuid.NewString()[:8]
	if r := e.jsonDo(t, http.MethodPost, org+"/venues", e.organizerToken, "", fmt.Sprintf(`{"name":%q,"timezone":"Europe/Madrid"}`, name)); r.status != http.StatusCreated {
		t.Fatalf("first venue: %d %s", r.status, r.body)
	}
	r := e.jsonDo(t, http.MethodPost, org+"/venues", e.organizerToken, "", fmt.Sprintf(`{"name":%q,"timezone":"Asia/Vientiane","country_iso2":"LA","city_name":"Vientiane"}`, name))
	if r.status != http.StatusConflict || r.code() != "venue.duplicate" {
		t.Fatalf("duplicate: %d %s", r.status, r.body)
	}
	if e.countryRows(t, "LA") != 0 {
		t.Fatal("a refused venue left its country")
	}
	if n := e.count(t, `SELECT count(*) FROM i18n_text WHERE namespace = 'geo.countries' AND key = 'LA'`); n != 0 {
		t.Errorf("a refused venue left %d country names", n)
	}
	e.noJob(t, "country_created", "country_iso2", "LA")
}

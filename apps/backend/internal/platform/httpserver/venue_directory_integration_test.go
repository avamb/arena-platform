//go:build integration

package httpserver

// venue_directory_integration_test.go — GET /v1/organizations/{org_id}/venue-directory
// (spec 64 section 2.1.1): the organization sees its own venues that are not
// archived and the verified venues of other organizations, never an
// unverified foreign venue and never a contact; counters, filters and the
// plan marks are right; another organization's caller is refused like on
// shared-venues.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type vdirAnswer struct {
	QuickPick []string `json:"quick_pick"`
	Countries []struct {
		Iso2      string  `json:"iso2"`
		CountryID *string `json:"country_id"`
		Name      string  `json:"name"`
		Region    string  `json:"region"`
		Venues    int64   `json:"venues"`
		OwnVenues int64   `json:"own_venues"`
	} `json:"countries"`
	Cities []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		CountryIso2 string `json:"country_iso2"`
		Venues      int64  `json:"venues"`
		OwnVenues   int64  `json:"own_venues"`
	} `json:"cities"`
	Venues []struct {
		ID           string   `json:"id"`
		Name         string   `json:"name"`
		CityID       *string  `json:"city_id"`
		CityName     *string  `json:"city_name"`
		CountryIso2  *string  `json:"country_iso2"`
		AddressLine1 *string  `json:"address_line1"`
		GeoLat       *float64 `json:"geo_lat"`
		Own          bool     `json:"own"`
		ReviewStatus string   `json:"review_status"`
		HasPlan      bool     `json:"has_plan"`
		HasOwnPlan   bool     `json:"has_own_plan"`
	} `json:"venues"`
	Total int64 `json:"total"`
}

func TestVenueDirectory_ScopeCountersFiltersAndPlans(t *testing.T) {
	e := newSec1Env(t)
	ctx := context.Background()
	pool := e.srv.pgxPool
	orgA, orgB, orgC := e.org(t, "VDIR-A"), e.org(t, "VDIR-B"), e.org(t, "VDIR-C")
	ua, ub, uc := e.user(t, "vdir-a"), e.user(t, "vdir-b"), e.user(t, "vdir-c")
	e.member(t, ua, orgA, "org_admin")
	e.member(t, ub, orgB, "org_admin")
	e.member(t, uc, orgC, "org_admin")
	tokA, tokB, tokC := e.token(t, ua), e.token(t, ub), e.token(t, uc)
	s := strings.ToLower(uuid.NewString()[:8])

	// A city of Spain only this test uses.
	cityID := uuid.New()
	citySlug := "vdir-city-" + s
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	exec(`INSERT INTO cities (id, country_id, slug) SELECT $1, id, $2 FROM countries WHERE iso2 = 'ES'`, cityID, citySlug)
	exec(`INSERT INTO i18n_text (namespace, key, locale, value) VALUES ('geo.cities', $1, 'en', $2)`, citySlug, "Vdir City "+s)
	t.Cleanup(func() {
		c := context.Background()
		orgs := []uuid.UUID{orgA, orgB, orgC}
		all := `(SELECT id FROM seating_plans WHERE owner_org_id = ANY($1))`
		for _, stmt := range []string{
			`DELETE FROM audit_events WHERE resource_type = 'seating_plan' AND resource_id IN (SELECT id::text FROM seating_plans WHERE owner_org_id = ANY($1))`,
			`UPDATE seating_plans SET current_version_id = NULL WHERE id IN ` + all,
			`DELETE FROM seating_plan_versions WHERE seating_plan_id IN ` + all,
			`DELETE FROM seating_plans WHERE owner_org_id = ANY($1)`,
			`DELETE FROM venues WHERE org_id = ANY($1)`,
		} {
			if _, err := pool.Exec(c, stmt, orgs); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
		_, _ = pool.Exec(c, `DELETE FROM i18n_text WHERE namespace = 'geo.cities' AND key = $1`, citySlug)
		_, _ = pool.Exec(c, `DELETE FROM cities WHERE id = $1`, cityID)
	})

	venue := func(org uuid.UUID, name string, city *uuid.UUID, country, status, review string) uuid.UUID {
		t.Helper()
		id := uuid.New()
		exec(`INSERT INTO venues (id, org_id, name, city_id, country, timezone, status, review_status, contact_email, address_line1, geo_lat, geo_lng)
		      VALUES ($1, $2, $3, $4, NULLIF($5, ''), 'Europe/Madrid', $6, $7, 'secret-`+s+`@owner.test', 'Calle 1', 40.4, -3.7)`,
			id, org, name, city, country, status, review)
		return id
	}
	alpha := venue(orgA, "VDir Alpha "+s, &cityID, "", "active", "unverified")
	venue(orgA, "VDir Archived "+s, &cityID, "", "archived", "unverified")
	venue(orgA, "VDir Gamma "+s, nil, "DE", "active", "unverified")
	beta := venue(orgB, "VDir Beta "+s, &cityID, "", "active", "verified")
	hidden := venue(orgB, "VDir Hidden "+s, &cityID, "", "active", "unverified")
	delta := venue(orgB, "VDir Delta "+s, &cityID, "", "active", "verified")

	// Plans: A has its own plan at Alpha; B's plan at Beta is a verified
	// template; B's plan at Delta is only B's.
	plan := func(venueID, org uuid.UUID, token string) uuid.UUID {
		t.Helper()
		st, out := e.do(t, http.MethodPost, "/v1/venues/"+venueID.String()+"/seating-plans", token, "",
			fmt.Sprintf(`{"owner_org_id":%q,"name":"VDir plan %s","plan_type":"assigned_seats","status":"active"}`, org, uuid.NewString()[:6]))
		if st != http.StatusCreated {
			t.Fatalf("plan: %d %s", st, out)
		}
		id := uuid.MustParse(spvJSON(t, out)["seating_plan"].(map[string]any)["id"].(string))
		svg, _ := json.Marshal(spvSVG4)
		if st, out := e.do(t, http.MethodPost, "/v1/seating-plans/"+id.String()+"/versions", token, "", `{"svg":`+string(svg)+`}`); st != http.StatusCreated {
			t.Fatalf("plan version: %d %s", st, out)
		}
		return id
	}
	plan(alpha, orgA, tokA)
	template := plan(beta, orgB, tokB)
	exec(`UPDATE seating_plans SET visibility = 'operator_verified', verification_state = 'verified', verified_at = now() WHERE id = $1`, template)
	plan(delta, orgB, tokB)

	get := func(org uuid.UUID, token, query string) (int, string, vdirAnswer) {
		t.Helper()
		st, out := e.do(t, http.MethodGet, "/v1/organizations/"+org.String()+"/venue-directory"+query, token, "", "")
		var a vdirAnswer
		if st == http.StatusOK {
			if err := json.Unmarshal([]byte(out), &a); err != nil {
				t.Fatalf("decode: %v %s", err, out)
			}
		}
		return st, out, a
	}

	// Without a filter: counters only.
	st, raw, a := get(orgA, tokA, "")
	if st != http.StatusOK || len(a.Venues) != 0 || a.Total != 0 || len(a.Cities) != 0 {
		t.Fatalf("no filter: %d %s", st, raw)
	}
	own := map[string]int64{}
	for _, c := range a.Countries {
		own[c.Iso2] = c.OwnVenues
		if c.Iso2 == "ES" && (c.CountryID == nil || c.Region != "europe" || c.Name == "" || c.Venues < 3) {
			t.Errorf("ES counter: %+v", c)
		}
	}
	if own["ES"] != 1 || own["DE"] != 1 {
		t.Errorf("own venues by country = %v, want ES 1 (archived left out) and DE 1", own)
	}
	if len(a.QuickPick) < 2 || a.QuickPick[0] != "DE" || a.QuickPick[1] != "ES" || len(a.QuickPick) > 8 {
		t.Errorf("quick_pick = %v, want DE, ES first (own venues), at most 8", a.QuickPick)
	}
	if strings.Contains(raw, "secret-"+s) || strings.Contains(raw, "contact_") {
		t.Fatalf("the directory leaks a contact: %s", raw)
	}

	// Country + q (any letter case): own first, then by name; B's unverified
	// venue and A's archived one are not there.
	st, raw, a = get(orgA, tokA, "?country=es&q="+url.QueryEscape(" "+strings.ToUpper(s))+"&lang=ru")
	if st != http.StatusOK {
		t.Fatalf("country filter: %d %s", st, raw)
	}
	names := []string{}
	for _, v := range a.Venues {
		names = append(names, v.Name)
	}
	want := []string{"VDir Alpha " + s, "VDir Beta " + s, "VDir Delta " + s}
	if strings.Join(names, "|") != strings.Join(want, "|") || a.Total != 3 {
		t.Fatalf("venues = %v (total %d), want %v: %s", names, a.Total, want, raw)
	}
	marks := map[string][3]bool{}
	for _, v := range a.Venues {
		marks[v.ID] = [3]bool{v.Own, v.HasOwnPlan, v.HasPlan}
		if v.CityID == nil || *v.CityID != cityID.String() || v.CityName == nil || *v.CityName != "Vdir City "+s ||
			v.CountryIso2 == nil || *v.CountryIso2 != "ES" || v.AddressLine1 == nil || v.GeoLat == nil {
			t.Errorf("venue fields: %+v", v)
		}
	}
	if marks[alpha.String()] != [3]bool{true, true, true} {
		t.Errorf("Alpha own/has_own_plan/has_plan = %v, want all true", marks[alpha.String()])
	}
	if marks[beta.String()] != [3]bool{false, false, true} {
		t.Errorf("Beta (verified template) = %v, want false/false/true", marks[beta.String()])
	}
	if marks[delta.String()] != [3]bool{false, false, false} {
		t.Errorf("Delta (B's own plan only) = %v, want all false", marks[delta.String()])
	}
	if strings.Contains(raw, hidden.String()) {
		t.Fatal("A sees B's unverified venue")
	}
	var city *struct{ venues, own int64 }
	for _, c := range a.Cities {
		if c.ID == cityID.String() {
			city = &struct{ venues, own int64 }{c.Venues, c.OwnVenues}
		}
	}
	if city == nil || city.venues != 3 || city.own != 1 {
		t.Errorf("city counter = %+v, want 3 venues, 1 own (%s)", city, raw)
	}

	// city_id filter and limit.
	if st, raw, a = get(orgA, tokA, "?city_id="+cityID.String()+"&limit=2"); st != http.StatusOK || len(a.Venues) != 2 || a.Total != 3 {
		t.Errorf("city filter with limit: %d %s", st, raw)
	}
	// B sees its own unverified venue but not A's unverified one.
	st, raw, _ = get(orgB, tokB, "?city_id="+cityID.String())
	if st != http.StatusOK || !strings.Contains(raw, hidden.String()) || strings.Contains(raw, alpha.String()) {
		t.Errorf("B's view: %d %s", st, raw)
	}
	// A third organization sees the two verified venues only.
	if st, raw, a = get(orgC, tokC, "?city_id="+cityID.String()); st != http.StatusOK || a.Total != 2 {
		t.Errorf("C's view: %d %s", st, raw)
	}

	// Errors and the organization check of shared-venues.
	for q, code := range map[string]string{"?country=ESP": "venue.invalid_country", "?city_id=x": "venue.invalid_city_id", "?country=ES&limit=0": "venue.invalid_limit"} {
		if st, raw, _ := get(orgA, tokA, q); st != http.StatusBadRequest || sec1Code(raw) != code {
			t.Errorf("%s: %d %s, want 400 %s", q, st, raw, code)
		}
	}
	stShared, _ := e.do(t, http.MethodGet, "/v1/organizations/"+orgA.String()+"/shared-venues", tokB, "", "")
	stDir, rawDir, _ := get(orgA, tokB, "?country=ES")
	if stDir != http.StatusForbidden || stDir != stShared {
		t.Errorf("another organization's caller: directory %d %s, shared-venues %d", stDir, rawDir, stShared)
	}
}

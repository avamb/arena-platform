//go:build integration

// admin_seating_plans_list_integration_test.go — GET /v1/admin/seating-plans
// in one request: plans with a NULL verification state (filter none), the
// filters org_id, country, city_id and q, the page and its total, the counts
// per state, and no archived or deleted plan or venue. Every request goes
// through the real router; the test counts only rows of its own organization.
//
//	DATABASE_URL=... go test -tags integration -count=1 -p 1 \
//	    ./apps/backend/internal/platform/httpserver/ -run AdminSeatingPlansList
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type splEnv struct {
	*spvEnv
	sfx                      string
	cityID                   uuid.UUID
	cityIso2                 string
	v1, v2, vArchived, vGone uuid.UUID
	vB                       uuid.UUID
	pNone, pUnv, pFix, pVer  uuid.UUID
	pArchived, pDeleted      uuid.UUID
	pArchVenue, pGoneVenue   uuid.UUID
	pOrgB                    uuid.UUID
	cityNameEn, cityNameRu   string
	ownerToken, superToken   string
	orgAString, orgBString   string
}

func newSPLEnv(t *testing.T) *splEnv {
	t.Helper()
	e := &splEnv{spvEnv: newSPVEnv(t), sfx: strings.ReplaceAll(uuid.NewString()[:8], "-", "")}
	e.ownerToken, e.superToken = e.adminA, e.super
	e.orgAString, e.orgBString = e.orgA.String(), e.orgB.String()
	ctx := context.Background()
	db := e.srv.pgxPool

	var countryID uuid.UUID
	if err := db.QueryRow(ctx, `SELECT id, iso2 FROM countries WHERE iso2 <> 'QZ' ORDER BY iso2 LIMIT 1`).
		Scan(&countryID, &e.cityIso2); err != nil {
		t.Fatalf("pick a country: %v", err)
	}
	slug := "spl-city-" + e.sfx
	e.cityNameEn, e.cityNameRu = "SPL City "+e.sfx, "Город SPL "+e.sfx
	if err := db.QueryRow(ctx, `INSERT INTO cities (country_id, slug) VALUES ($1, $2) RETURNING id`, countryID, slug).
		Scan(&e.cityID); err != nil {
		t.Fatalf("city fixture: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO i18n_text (namespace, key, locale, value) VALUES
		('geo.cities', $1, 'en', $2), ('geo.cities', $1, 'ru', $3)`, slug, e.cityNameEn, e.cityNameRu); err != nil {
		t.Fatalf("city names: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		all := `(SELECT id FROM seating_plans WHERE owner_org_id = ANY($1))`
		orgs := []uuid.UUID{e.orgA, e.orgB}
		for _, stmt := range []string{
			`UPDATE seating_plans SET current_version_id = NULL WHERE id IN ` + all,
			`DELETE FROM seating_plan_versions WHERE seating_plan_id IN ` + all,
			`DELETE FROM seating_plans WHERE owner_org_id = ANY($1)`,
			`DELETE FROM venues WHERE org_id = ANY($1)`,
		} {
			if _, err := db.Exec(ctx, stmt, orgs); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
		if _, err := db.Exec(ctx, `DELETE FROM i18n_text WHERE namespace = 'geo.cities' AND key = $1`, slug); err != nil {
			t.Logf("cleanup city names: %v", err)
		}
		if _, err := db.Exec(ctx, `DELETE FROM cities WHERE id = $1`, e.cityID); err != nil {
			t.Logf("cleanup city: %v", err)
		}
	})

	venue := func(org uuid.UUID, name string, cityID *uuid.UUID, country *string, status string, deleted bool) uuid.UUID {
		t.Helper()
		id := uuid.New()
		var deletedAt *time.Time
		if deleted {
			now := time.Now()
			deletedAt = &now
		}
		if _, err := db.Exec(ctx, `INSERT INTO venues (id, org_id, name, timezone, city_id, country, status, deleted_at)
			VALUES ($1, $2, $3, 'Europe/Prague', $4, $5, $6, $7)`, id, org, name, cityID, country, status, deletedAt); err != nil {
			t.Fatalf("venue %s: %v", name, err)
		}
		return id
	}
	str := func(s string) *string { return &s }
	e.v1 = venue(e.orgA, "SPL Alpha Hall "+e.sfx, &e.cityID, nil, "active", false)
	e.v2 = venue(e.orgA, "SPL Beta Club "+e.sfx, nil, str("QZ"), "active", false)
	e.vArchived = venue(e.orgA, "SPL Archived Venue "+e.sfx, &e.cityID, nil, "archived", false)
	e.vGone = venue(e.orgA, "SPL Deleted Venue "+e.sfx, &e.cityID, nil, "active", true)
	e.vB = venue(e.orgB, "SPL Gamma Org B "+e.sfx, nil, str("QZ"), "active", false)

	base := time.Now().Add(-time.Hour)
	plan := func(org, venueID uuid.UUID, name, status string, state *string, minutesAgo int, deleted bool) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		var deletedAt *time.Time
		if deleted {
			now := time.Now()
			deletedAt = &now
		}
		if err := db.QueryRow(ctx, `INSERT INTO seating_plans
			(venue_id, owner_org_id, name, plan_type, status, verification_state, created_at, updated_at, deleted_at)
			VALUES ($1, $2, $3, 'assigned_seats', $4, $5, $6, $6, $7) RETURNING id`,
			venueID, org, name, status, state, base.Add(-time.Duration(minutesAgo)*time.Minute), deletedAt).Scan(&id); err != nil {
			t.Fatalf("plan %s: %v", name, err)
		}
		return id
	}

	// pNone is made through the API so it has a real version (3 seats).
	out := e.call(t, http.StatusCreated, "", "POST", "/v1/venues/"+e.v1.String()+"/seating-plans", e.adminA, "",
		fmt.Sprintf(`{"owner_org_id":%q,"name":"SPL none","plan_type":"assigned_seats","status":"active"}`, e.orgA))
	e.pNone = uuid.MustParse(out["seating_plan"].(map[string]any)["id"].(string))
	svg, _ := json.Marshal(planUpSVG)
	e.call(t, http.StatusCreated, "", "POST", "/v1/seating-plans/"+e.pNone.String()+"/versions", e.adminA, "", `{"svg":`+string(svg)+`}`)
	if _, err := db.Exec(ctx, `UPDATE seating_plans SET created_at = $2, verification_state = NULL WHERE id = $1`,
		e.pNone, base.Add(-1*time.Minute)); err != nil {
		t.Fatalf("pNone time: %v", err)
	}

	e.pUnv = plan(e.orgA, e.v1, "SPL unverified draft", "draft", str("unverified"), 2, false)
	e.pFix = plan(e.orgA, e.v2, "SPL needs fix", "active", str("needs_fix"), 3, false)
	e.pVer = plan(e.orgA, e.v2, "SPL verified", "active", str("verified"), 4, false)
	e.pArchived = plan(e.orgA, e.v1, "SPL archived", "archived", str("unverified"), 5, false)
	e.pDeleted = plan(e.orgA, e.v1, "SPL deleted", "active", nil, 6, true)
	e.pArchVenue = plan(e.orgA, e.vArchived, "SPL at archived venue", "active", str("unverified"), 7, false)
	e.pGoneVenue = plan(e.orgA, e.vGone, "SPL at deleted venue", "active", nil, 8, false)
	e.pOrgB = plan(e.orgB, e.vB, "SPL org B", "active", str("unverified"), 9, false)
	return e
}

type splPage struct {
	ids    []string
	rows   map[string]map[string]any
	total  float64
	limit  float64
	offset float64
	counts map[string]float64
}

func (e *splEnv) list(t *testing.T, query url.Values) splPage {
	t.Helper()
	out := e.call(t, http.StatusOK, "", "GET", "/v1/admin/seating-plans?"+query.Encode(), e.superToken, "", "")
	p := splPage{rows: map[string]map[string]any{}, counts: map[string]float64{}}
	for _, it := range out["seating_plans"].([]any) {
		row := it.(map[string]any)
		id := row["plan_id"].(string)
		p.ids = append(p.ids, id)
		p.rows[id] = row
	}
	p.total, p.limit, p.offset = out["total"].(float64), out["limit"].(float64), out["offset"].(float64)
	for k, v := range out["counts"].(map[string]any) {
		p.counts[k] = v.(float64)
	}
	return p
}

func splIDs(us ...uuid.UUID) []string {
	out := []string{}
	for _, u := range us {
		out = append(out, u.String())
	}
	return out
}

func (e *splEnv) wantPage(t *testing.T, name string, query url.Values, want []string, total float64, counts map[string]float64) splPage {
	t.Helper()
	p := e.list(t, query)
	got := p.ids
	if got == nil {
		got = []string{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: plans %v, want %v", name, got, want)
	}
	if p.total != total {
		t.Errorf("%s: total %v, want %v", name, p.total, total)
	}
	if counts != nil && !reflect.DeepEqual(p.counts, counts) {
		t.Errorf("%s: counts %v, want %v", name, p.counts, counts)
	}
	return p
}

func TestAdminSeatingPlansList_FiltersPagingCounts(t *testing.T) {
	e := newSPLEnv(t)
	org := url.Values{"org_id": {e.orgAString}}
	with := func(kv ...string) url.Values {
		q := url.Values{"org_id": {e.orgAString}}
		for i := 0; i+1 < len(kv); i += 2 {
			q.Set(kv[i], kv[i+1])
		}
		return q
	}
	all := map[string]float64{"none": 1, "unverified": 1, "needs_fix": 1, "verified": 1}

	// Every state, newest first; archived and deleted plans and plans of
	// archived or deleted venues are absent from the list and the counts.
	p := e.wantPage(t, "all states", org, splIDs(e.pNone, e.pUnv, e.pFix, e.pVer), 4, all)
	if p.limit != 50 || p.offset != 0 {
		t.Errorf("default page: limit %v offset %v", p.limit, p.offset)
	}
	for _, gone := range []uuid.UUID{e.pArchived, e.pDeleted, e.pArchVenue, e.pGoneVenue} {
		if _, ok := p.rows[gone.String()]; ok {
			t.Errorf("plan %s must not be listed", gone)
		}
	}

	// Each state filter; the counts ignore it. The archived unverified plan
	// is not under unverified (the bug this closes), the draft plan is.
	e.wantPage(t, "none", with("verification_state", "none"), splIDs(e.pNone), 1, all)
	unv := e.wantPage(t, "unverified", with("verification_state", "unverified"), splIDs(e.pUnv), 1, all)
	if row := unv.rows[e.pUnv.String()]; row == nil || row["status"] != "draft" {
		t.Errorf("draft plan row: %v", row)
	}
	e.wantPage(t, "needs_fix", with("verification_state", "needs_fix"), splIDs(e.pFix), 1, all)
	e.wantPage(t, "verified", with("verification_state", "verified"), splIDs(e.pVer), 1, all)

	// Country: the city's country, else the venue's address country.
	cityCounts := map[string]float64{"none": 1, "unverified": 1, "needs_fix": 0, "verified": 0}
	addrCounts := map[string]float64{"none": 0, "unverified": 0, "needs_fix": 1, "verified": 1}
	e.wantPage(t, "country of city", with("country", strings.ToLower(e.cityIso2)), splIDs(e.pNone, e.pUnv), 2, cityCounts)
	e.wantPage(t, "country of address", with("country", "QZ"), splIDs(e.pFix, e.pVer), 2, addrCounts)
	e.wantPage(t, "country + state", with("country", "QZ", "verification_state", "verified"), splIDs(e.pVer), 1, addrCounts)
	e.wantPage(t, "city", with("city_id", e.cityID.String()), splIDs(e.pNone, e.pUnv), 2, cityCounts)
	e.wantPage(t, "q any case", with("q", "alpha HALL"), splIDs(e.pNone, e.pUnv), 2, cityCounts)
	e.wantPage(t, "q other venue", with("q", "beta club "+e.sfx), splIDs(e.pFix, e.pVer), 2, addrCounts)
	none := map[string]float64{"none": 0, "unverified": 0, "needs_fix": 0, "verified": 0}
	e.wantPage(t, "q wildcard is a letter", with("q", "SPL%Hall"), []string{}, 0, none)
	e.wantPage(t, "q not a plan name", with("q", "SPL none"), []string{}, 0, none)

	// Paging: total counts every matching plan, not the page.
	e.wantPage(t, "page 1", with("limit", "2"), splIDs(e.pNone, e.pUnv), 4, all)
	e.wantPage(t, "page 2", with("limit", "2", "offset", "2"), splIDs(e.pFix, e.pVer), 4, all)
	e.wantPage(t, "page 3", with("limit", "2", "offset", "4"), []string{}, 4, all)

	// Without org_id the superadmin sees every organization.
	cross := e.list(t, url.Values{"q": {e.sfx}, "limit": {"200"}})
	if _, ok := cross.rows[e.pOrgB.String()]; !ok {
		t.Errorf("without org_id the plan of organization B is missing: %v", cross.ids)
	}
	if _, ok := cross.rows[e.pNone.String()]; !ok {
		t.Errorf("without org_id the plan of organization A is missing: %v", cross.ids)
	}
	if cross.total != 5 || cross.counts["unverified"] != 2 {
		t.Errorf("without org_id: total %v counts %v", cross.total, cross.counts)
	}
	e.wantPage(t, "org B", url.Values{"org_id": {e.orgBString}}, splIDs(e.pOrgB), 1,
		map[string]float64{"none": 0, "unverified": 1, "needs_fix": 0, "verified": 0})

	// Names, capacity and the version of each row.
	r := p.rows[e.pNone.String()]
	if r["capacity_seated"] != float64(3) || r["capacity_standing"] != float64(0) || r["version_number"] != float64(1) ||
		r["current_version_id"] == nil || r["verification_state"] != nil ||
		r["venue_id"] != e.v1.String() || r["venue_name"] != "SPL Alpha Hall "+e.sfx ||
		r["city_id"] != e.cityID.String() || r["city_name"] != e.cityNameEn || r["country_iso2"] != e.cityIso2 ||
		r["owner_org_id"] != e.orgAString || r["owner_org_name"] == "" || r["created_at"] == "" || r["updated_at"] == "" {
		t.Errorf("row of the plan with a version: %v", r)
	}
	r = p.rows[e.pUnv.String()]
	if r["capacity_seated"] != float64(0) || r["capacity_standing"] != float64(0) || r["version_number"] != nil || r["current_version_id"] != nil {
		t.Errorf("row of the plan without a version: %v", r)
	}
	r = p.rows[e.pFix.String()]
	if r["city_id"] != nil || r["city_name"] != nil || r["country_iso2"] != "QZ" || r["venue_name"] != "SPL Beta Club "+e.sfx {
		t.Errorf("row of the venue without a city: %v", r)
	}
	ru := e.list(t, with("lang", "ru", "verification_state", "none"))
	if row := ru.rows[e.pNone.String()]; row == nil || row["city_name"] != e.cityNameRu {
		t.Errorf("city name in ru: %v", row)
	}
	loc := e.list(t, with("locale", "ru", "verification_state", "none"))
	if row := loc.rows[e.pNone.String()]; row == nil || row["city_name"] != e.cityNameRu {
		t.Errorf("city name with locale=ru: %v", row)
	}

	// Bad parameters.
	for _, c := range []struct{ query, code string }{
		{"verification_state=approved", "seating_plan.invalid_verification_state"},
		{"org_id=x", "seating_plan.invalid_org_id"},
		{"country=ESP", "seating_plan.invalid_country"},
		{"city_id=x", "seating_plan.invalid_city_id"},
		{"q=" + strings.Repeat("a", 101), "seating_plan.invalid_query"},
		{"limit=201", "seating_plan.invalid_limit"},
		{"offset=-1", "seating_plan.invalid_offset"},
	} {
		e.call(t, http.StatusBadRequest, c.code, "GET", "/v1/admin/seating-plans?"+c.query, e.superToken, "", "")
	}
}

// Access is the platform permission seating_plan.verify of a superadmin;
// org_id in the query never grants it.
func TestAdminSeatingPlansList_Access(t *testing.T) {
	e := newSPLEnv(t)
	path := "/v1/admin/seating-plans?org_id=" + e.orgAString
	for _, c := range []struct {
		name, bearer string
		want         int
	}{
		{"organization owner", e.ownerToken, http.StatusForbidden},
		{"organization API key", e.key(t, e.orgA, "seating_plan.read"), http.StatusForbidden},
		{"no token", "", http.StatusUnauthorized},
	} {
		st, body := e.do(t, "GET", path, c.bearer, "", "")
		if st != c.want {
			t.Errorf("%s: got %d %s, want %d", c.name, st, body, c.want)
		}
		if strings.Contains(body, e.pNone.String()) || strings.Contains(body, "seating_plans") {
			t.Errorf("%s: the answer carries data: %s", c.name, body)
		}
	}
	e.call(t, http.StatusOK, "", "GET", path, e.superToken, "", "")
}

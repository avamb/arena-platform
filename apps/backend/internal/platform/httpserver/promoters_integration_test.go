//go:build integration

// promoters_integration_test.go — live-PostgreSQL coverage for migration
// 0113: the organization's promoters, the event ↔ promoter link, the
// event-bundle's action.promoterId and the organization-side city create.
//
// Every request goes through the REAL router (s.Router().ServeHTTP via an
// httptest server) authenticated with organization API keys, so the
// permission gate (applyAuth + the key's scopes), the org binding of a key
// and the handlers are exercised together — no handler is called directly.
//
// Run against a FRESH migrated database (AGENTS.md CI-Integration recipe):
//
//	DATABASE_URL=... go test -tags integration -p 1 \
//	    ./apps/backend/internal/platform/httpserver/ -run TestPromoters0113
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
)

type prom0113Fixture struct {
	ts     *httptest.Server
	client *http.Client
	q      *gen.Queries
	userID uuid.UUID
}

func newProm0113Fixture(t *testing.T) *prom0113Fixture {
	t.Helper()
	srv, _ := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	q := gen.New(srv.pgxPool)
	user, err := q.InsertUser(context.Background(), "prom0113-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	return &prom0113Fixture{ts: ts, client: ts.Client(), q: q, userID: user.ID}
}

func (f *prom0113Fixture) org(t *testing.T, label string) uuid.UUID {
	t.Helper()
	suffix := uuid.NewString()[:8]
	org, err := f.q.InsertOrganization(context.Background(), "Prom0113 "+label+" "+suffix,
		"prom0113-"+strings.ToLower(label)+"-"+suffix, "EE", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	return org.ID
}

func (f *prom0113Fixture) key(t *testing.T, orgID uuid.UUID, scopes ...string) string {
	t.Helper()
	_, raw, err := apikeys.Issue(context.Background(), apikeys.NewStoreFromQueries(f.q), apikeys.IssueInput{
		OrgID:     orgID,
		Name:      "prom0113-" + uuid.NewString()[:6],
		Scopes:    scopes,
		CreatedBy: f.userID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue(%v): %v", scopes, err)
	}
	return raw
}

func (f *prom0113Fixture) do(t *testing.T, method, path, key, body string) (int, map[string]any) {
	t.Helper()
	resp := integDoRequest(t, f.client, method, f.ts.URL+path, key, body)
	raw := integReadBody(t, resp)
	out := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	return resp.StatusCode, out
}

func prom0113Code(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		if c, ok := e["code"].(string); ok {
			return c
		}
	}
	if c, ok := m["code"].(string); ok {
		return c
	}
	return ""
}

func prom0113Field(t *testing.T, m map[string]any, obj, field string) any {
	t.Helper()
	o, ok := m[obj].(map[string]any)
	if !ok {
		t.Fatalf("response has no %q object: %v", obj, m)
	}
	return o[field]
}

func TestPromoters0113_CRUDAndOrgIsolation(t *testing.T) {
	f := newProm0113Fixture(t)
	orgA, orgB := f.org(t, "A"), f.org(t, "B")
	keyA := f.key(t, orgA, "promoter.read", "promoter.manage", "event.read", "event.update")
	keyEventsOnly := f.key(t, orgA, "event.read", "event.create", "event.update")
	keyB := f.key(t, orgB, "promoter.read", "promoter.manage", "event.update")
	base := "/v1/organizations/" + orgA.String()

	// A key with only event.* scopes cannot create or list promoters.
	if st, _ := f.do(t, http.MethodPost, base+"/promoters", keyEventsOnly, `{"name":"Nope"}`); st != http.StatusForbidden {
		t.Fatalf("event-only key POST promoters: status %d, want 403", st)
	}
	if st, _ := f.do(t, http.MethodGet, base+"/promoters", keyEventsOnly, ""); st != http.StatusForbidden {
		t.Fatalf("event-only key GET promoters: status %d, want 403", st)
	}

	// Create, with name normalization.
	st, body := f.do(t, http.MethodPost, base+"/promoters", keyA,
		`{"name":"  Partner   Agency ","legal_id":"12345678","phone":" ","email":"office@partner.example"}`)
	if st != http.StatusCreated {
		t.Fatalf("create promoter: status %d body %v", st, body)
	}
	promA := prom0113Field(t, body, "promoter", "id").(string)
	if got := prom0113Field(t, body, "promoter", "name"); got != "Partner Agency" {
		t.Errorf("name = %v, want normalized %q", got, "Partner Agency")
	}
	if got := prom0113Field(t, body, "promoter", "phone"); got != nil {
		t.Errorf("blank phone should be stored as null, got %v", got)
	}

	// Duplicate (case/whitespace-insensitive) and blank names.
	if st, body := f.do(t, http.MethodPost, base+"/promoters", keyA, `{"name":"partner agency"}`); st != http.StatusConflict || prom0113Code(body) != "promoter.duplicate_name" {
		t.Fatalf("duplicate: status %d code %q", st, prom0113Code(body))
	}
	if st, _ := f.do(t, http.MethodPost, base+"/promoters", keyA, `{"name":"   "}`); st != http.StatusBadRequest {
		t.Fatalf("blank name: status %d, want 400", st)
	}

	// Org B's promoter.
	st, body = f.do(t, http.MethodPost, "/v1/organizations/"+orgB.String()+"/promoters", keyB, `{"name":"B Promoter"}`)
	if st != http.StatusCreated {
		t.Fatalf("create org B promoter: status %d body %v", st, body)
	}
	promB := prom0113Field(t, body, "promoter", "id").(string)

	// Isolation: a key bound to org B cannot reach org A at all, and org A
	// cannot PATCH org B's promoter through its own org path.
	if st, _ := f.do(t, http.MethodGet, base+"/promoters", keyB, ""); st != http.StatusForbidden {
		t.Fatalf("org B key on org A: status %d, want 403", st)
	}
	if st, body := f.do(t, http.MethodPatch, base+"/promoters/"+promB, keyA, `{"name":"Hijack"}`); st != http.StatusNotFound {
		t.Fatalf("patch another org's promoter: status %d body %v, want 404", st, body)
	}

	// PATCH tri-state: phone set, legal_id cleared, email kept.
	st, body = f.do(t, http.MethodPatch, base+"/promoters/"+promA, keyA, `{"phone":"+420 1","legal_id":null}`)
	if st != http.StatusOK {
		t.Fatalf("patch: status %d body %v", st, body)
	}
	if prom0113Field(t, body, "promoter", "phone") != "+420 1" || prom0113Field(t, body, "promoter", "legal_id") != nil ||
		prom0113Field(t, body, "promoter", "email") != "office@partner.example" {
		t.Errorf("tri-state PATCH result wrong: %v", body["promoter"])
	}

	// Event link.
	ev, err := f.q.InsertEvent(context.Background(), orgA, "Prom0113 Event", nil, "draft", "public", nil)
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	evPath := base + "/events/" + ev.ID.String() + "/promoter"
	st, body = f.do(t, http.MethodPut, evPath, keyA, fmt.Sprintf(`{"promoter_id":%q}`, promA))
	if st != http.StatusOK || body["promoter_name"] != "Partner Agency" {
		t.Fatalf("set event promoter: status %d body %v", st, body)
	}
	assertEventPromoterInList(t, f, base, keyA, ev.ID.String(), promA, "Partner Agency")

	// Another org's promoter → 422.
	if st, body := f.do(t, http.MethodPut, evPath, keyA, fmt.Sprintf(`{"promoter_id":%q}`, promB)); st != http.StatusUnprocessableEntity || prom0113Code(body) != "event.invalid_promoter" {
		t.Fatalf("foreign promoter: status %d code %q", st, prom0113Code(body))
	}
	// An event-only key may set the link (event.update) — but never with a foreign id.
	if st, _ := f.do(t, http.MethodPut, evPath, keyEventsOnly, fmt.Sprintf(`{"promoter_id":%q}`, promA)); st != http.StatusOK {
		t.Fatalf("event.update key PUT promoter: status %d, want 200", st)
	}

	// Archive: hidden from the default list, refused for new links.
	if st, body := f.do(t, http.MethodPatch, base+"/promoters/"+promA, keyA, `{"archived":true}`); st != http.StatusOK || prom0113Field(t, body, "promoter", "archived") != true {
		t.Fatalf("archive: status %d body %v", st, body)
	}
	_, body = f.do(t, http.MethodGet, base+"/promoters", keyA, "")
	if n := len(body["promoters"].([]any)); n != 0 {
		t.Errorf("default list after archive has %d promoters, want 0", n)
	}
	_, body = f.do(t, http.MethodGet, base+"/promoters?include_archived=true", keyA, "")
	if n := len(body["promoters"].([]any)); n != 1 {
		t.Errorf("include_archived list has %d promoters, want 1", n)
	}
	if st, _ := f.do(t, http.MethodPut, evPath, keyA, fmt.Sprintf(`{"promoter_id":%q}`, promA)); st != http.StatusUnprocessableEntity {
		t.Fatalf("archived promoter link: status %d, want 422", st)
	}
	// A new promoter may take the archived one's name.
	if st, body := f.do(t, http.MethodPost, base+"/promoters", keyA, `{"name":"Partner Agency"}`); st != http.StatusCreated {
		t.Fatalf("reuse archived name: status %d body %v", st, body)
	}
	// Restoring the archived one now collides.
	if st, _ := f.do(t, http.MethodPatch, base+"/promoters/"+promA, keyA, `{"archived":false}`); st != http.StatusConflict {
		t.Fatalf("restore into a name collision: status %d, want 409", st)
	}

	// Clear the link → the organization itself.
	st, body = f.do(t, http.MethodPut, evPath, keyA, `{"promoter_id":null}`)
	if st != http.StatusOK || body["promoter_id"] != nil {
		t.Fatalf("clear link: status %d body %v", st, body)
	}
	assertEventPromoterInList(t, f, base, keyA, ev.ID.String(), "", "")

	// Another org's event → 404.
	evB, err := f.q.InsertEvent(context.Background(), orgB, "Prom0113 B Event", nil, "draft", "public", nil)
	if err != nil {
		t.Fatalf("InsertEvent B: %v", err)
	}
	if st, _ := f.do(t, http.MethodPut, base+"/events/"+evB.ID.String()+"/promoter", keyA, `{"promoter_id":null}`); st != http.StatusNotFound {
		t.Fatalf("foreign event: status %d, want 404", st)
	}
}

func assertEventPromoterInList(t *testing.T, f *prom0113Fixture, base, key, eventID, wantID, wantName string) {
	t.Helper()
	st, body := f.do(t, http.MethodGet, base+"/events", key, "")
	if st != http.StatusOK {
		t.Fatalf("list events: status %d body %v", st, body)
	}
	for _, raw := range body["events"].([]any) {
		e := raw.(map[string]any)
		if e["id"] != eventID {
			continue
		}
		if wantID == "" {
			if e["promoter_id"] != nil || e["promoter_name"] != nil {
				t.Errorf("event promoter = %v/%v, want null", e["promoter_id"], e["promoter_name"])
			}
			return
		}
		if e["promoter_id"] != wantID || e["promoter_name"] != wantName {
			t.Errorf("event promoter = %v/%v, want %s/%s", e["promoter_id"], e["promoter_name"], wantID, wantName)
		}
		return
	}
	t.Fatalf("event %s not in list", eventID)
}

func TestPromoters0113_EventBundlePromoterID(t *testing.T) {
	f := newProm0113Fixture(t)
	orgA, orgB := f.org(t, "BundleA"), f.org(t, "BundleB")
	keyA := f.key(t, orgA, "promoter.manage", "import.bil24_session")
	keyB := f.key(t, orgB, "promoter.manage")
	base := "/v1/organizations/" + orgA.String()

	_, body := f.do(t, http.MethodPost, base+"/promoters", keyA, `{"name":"Bundle Partner"}`)
	promA := prom0113Field(t, body, "promoter", "id").(string)
	_, body = f.do(t, http.MethodPost, "/v1/organizations/"+orgB.String()+"/promoters", keyB, `{"name":"Other Org Partner"}`)
	promB := prom0113Field(t, body, "promoter", "id").(string)

	ref := "prom0113-" + uuid.NewString()[:8]
	bundle := func(promoter string) string {
		return fmt.Sprintf(`{
			"source": "arena",
			"externalRef": %q,
			"action": {"actionName": "Prom0113 Bundle %s"%s},
			"actionEvent": {"day": "01.06.2027", "time": "19:00", "currency": "EUR"},
			"venue": {"venueName": "Prom0113 Venue %s", "cityName": "Tallinn", "countryName": "EE", "timezone": "Europe/Tallinn"},
			"categoryList": [{"categoryPriceName": "Standard", "price": 15.00, "availability": 10}]
		}`, ref, ref, promoter, ref)
	}
	linked := func(eventID string) string {
		var name string
		err := f.q.DB().QueryRow(context.Background(),
			`SELECT p.name FROM event_promoters ep JOIN org_promoters p ON p.id = ep.promoter_id WHERE ep.event_id = $1`,
			eventID).Scan(&name)
		if err != nil {
			return ""
		}
		return name
	}

	st, body := f.do(t, http.MethodPost, base+"/imports/event-bundle", keyA, bundle(fmt.Sprintf(`, "promoterId": %q`, promA)))
	if st != http.StatusOK {
		t.Fatalf("import with promoterId: status %d body %v", st, body)
	}
	eventID, _ := body["event_id"].(string)
	if got := linked(eventID); got != "Bundle Partner" {
		t.Fatalf("after import, linked promoter = %q, want %q", got, "Bundle Partner")
	}

	// Absent keeps.
	if st, body := f.do(t, http.MethodPost, base+"/imports/event-bundle", keyA, bundle("")); st != http.StatusOK {
		t.Fatalf("re-import without promoterId: status %d body %v", st, body)
	}
	if got := linked(eventID); got != "Bundle Partner" {
		t.Fatalf("absent promoterId must keep the link, got %q", got)
	}

	// Another org's promoter → 422, nothing changes.
	st, body = f.do(t, http.MethodPost, base+"/imports/event-bundle", keyA, bundle(fmt.Sprintf(`, "promoterId": %q`, promB)))
	if st != http.StatusUnprocessableEntity || prom0113Code(body) != "import.invalid_promoter" {
		t.Fatalf("foreign promoterId: status %d body %v", st, body)
	}
	if got := linked(eventID); got != "Bundle Partner" {
		t.Fatalf("a refused import must not touch the link, got %q", got)
	}

	// "" removes the link.
	if st, body := f.do(t, http.MethodPost, base+"/imports/event-bundle", keyA, bundle(`, "promoterId": ""`)); st != http.StatusOK {
		t.Fatalf("import with empty promoterId: status %d body %v", st, body)
	}
	if got := linked(eventID); got != "" {
		t.Fatalf("empty promoterId must remove the link, still %q", got)
	}
}

func TestPromoters0113_OrgCityCreate(t *testing.T) {
	f := newProm0113Fixture(t)
	orgA := f.org(t, "City")
	key := f.key(t, orgA, "city.create", "venue.create")
	keyNoCity := f.key(t, orgA, "venue.create")
	path := "/v1/organizations/" + orgA.String() + "/cities"

	ee, err := f.q.GetCountryByISO2(context.Background(), "EE")
	if err != nil {
		t.Fatalf("country EE: %v", err)
	}
	country := ee.ID.String()

	if st, _ := f.do(t, http.MethodPost, path, keyNoCity, fmt.Sprintf(`{"country_id":%q,"name":"X"}`, country)); st != http.StatusForbidden {
		t.Fatalf("key without city.create: status %d, want 403", st)
	}

	// An existing seeded city is found, not duplicated.
	st, body := f.do(t, http.MethodPost, path, key, fmt.Sprintf(`{"country_id":%q,"name":"  TALLINN "}`, country))
	if st != http.StatusOK || prom0113Field(t, body, "city", "slug") != "tallinn" || body["created"] != false {
		t.Fatalf("existing city: status %d body %v", st, body)
	}

	// A new city, named in Czech with diacritics.
	suffix := uuid.NewString()[:6]
	name := "Nové Město " + suffix
	st, body = f.do(t, http.MethodPost, path, key, fmt.Sprintf(`{"country_id":%q,"name":%q,"locale":"cs-CZ"}`, country, name))
	if st != http.StatusCreated || body["created"] != true {
		t.Fatalf("create city: status %d body %v", st, body)
	}
	slug := prom0113Field(t, body, "city", "slug").(string)
	cityID := prom0113Field(t, body, "city", "id").(string)
	if slug != "nove-mesto-"+suffix {
		t.Errorf("slug = %q, want %q", slug, "nove-mesto-"+suffix)
	}
	if got := prom0113Field(t, body, "city", "name"); got != name {
		t.Errorf("name = %v, want %q", got, name)
	}

	// Same name, different case and spacing, other locale → the same city.
	st, body = f.do(t, http.MethodPost, path, key, fmt.Sprintf(`{"country_id":%q,"name":%q}`, country, "  nové   MĚSTO "+suffix))
	if st != http.StatusOK || prom0113Field(t, body, "city", "id") != cityID {
		t.Fatalf("idempotent create: status %d body %v", st, body)
	}
	var n int
	if err := f.q.DB().QueryRow(context.Background(), `SELECT count(*) FROM cities WHERE slug LIKE $1`, "nove-mesto-"+suffix+"%").Scan(&n); err != nil || n != 1 {
		t.Fatalf("cities with the slug = %d (%v), want 1", n, err)
	}

	// The same name in ANOTHER country gets its own, disambiguated slug.
	lv, err := f.q.GetCountryByISO2(context.Background(), "LV")
	if err == nil {
		st, body = f.do(t, http.MethodPost, path, key, fmt.Sprintf(`{"country_id":%q,"name":%q}`, lv.ID.String(), name))
		if st != http.StatusCreated || prom0113Field(t, body, "city", "slug") != slug+"-lv" {
			t.Fatalf("same name in another country: status %d body %v", st, body)
		}
	}

	// Unknown country and blank name.
	if st, body := f.do(t, http.MethodPost, path, key, fmt.Sprintf(`{"country_id":%q,"name":"Nowhere"}`, uuid.NewString())); st != http.StatusUnprocessableEntity {
		t.Fatalf("unknown country: status %d body %v", st, body)
	}
	if st, _ := f.do(t, http.MethodPost, path, key, fmt.Sprintf(`{"country_id":%q,"name":" "}`, country)); st != http.StatusBadRequest {
		t.Fatalf("blank name: status %d, want 400", st)
	}
}

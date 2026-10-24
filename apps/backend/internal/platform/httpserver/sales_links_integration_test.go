//go:build integration

package httpserver

// sales_links_integration_test.go — QL-4 (spec 43 section 4.2): the
// organization routes of tracked sales links, through the real router.
// Run against a migrated and seeded database (AGENTS.md CI-Integration recipe).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const (
	slTicketsBase = "https://tickets.sl4.example.test"
	slShortBase   = "https://go.sl4.example.test"
)

type sl4Fixture struct {
	ts     *httptest.Server
	pool   *pgxpool.Pool
	q      *gen.Queries
	secret string

	orgA, orgB uuid.UUID
	orgASlug   string
	ownerA     string
	managerA   string
	agentA     string
	ownerB     string

	venue, event, session, promoter uuid.UUID
	eventSlug, promoterSlug         string
	eventB, sessionB, promoterB     uuid.UUID
	channel                         uuid.UUID
	keyA                            string // an API key of org A holding sales_link.read and .manage
	users                           []uuid.UUID
}

func newSL4Fixture(t *testing.T) *sl4Fixture {
	t.Helper()
	srv, secret := productionIntegrationServerCfg(t, func(c *config.Config) {
		c.PublicTicketsBaseURL = slTicketsBase
		c.ShortLinkBaseURL = slShortBase
	})
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	f := &sl4Fixture{ts: ts, pool: srv.pgxPool, q: gen.New(srv.pgxPool), secret: secret}
	ctx := context.Background()

	suffix := uuid.NewString()[:8]
	f.orgASlug = "sl4-a-" + suffix
	orgA, err := f.q.InsertOrganization(ctx, "SL4 A "+suffix, f.orgASlug, "CZ", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization A: %v", err)
	}
	orgB, err := f.q.InsertOrganization(ctx, "SL4 B "+suffix, "sl4-b-"+suffix, "CZ", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization B: %v", err)
	}
	f.orgA, f.orgB = orgA.ID, orgB.ID

	mkUser := func(org uuid.UUID, role string) string {
		u, err := f.q.InsertUser(ctx, "sl4-"+role+"-"+uuid.NewString()+"@example.test", "x", "en")
		if err != nil {
			t.Fatalf("InsertUser: %v", err)
		}
		f.users = append(f.users, u.ID)
		if _, err := f.q.InsertMembership(ctx, u.ID, org, role); err != nil {
			t.Fatalf("InsertMembership(%s): %v", role, err)
		}
		tok, _, err := auth.IssueJWT(secret, u.ID, nil, nil, "arena-api", "arena-api", time.Hour)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		return tok
	}
	f.ownerA, f.managerA, f.agentA = mkUser(f.orgA, "org_admin"), mkUser(f.orgA, "organizer"), mkUser(f.orgA, "agent")
	f.ownerB = mkUser(f.orgB, "org_admin")

	keyOwner, err := f.q.InsertUser(ctx, "sl4-keyowner-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	f.users = append(f.users, keyOwner.ID)
	_, f.keyA, err = apikeys.Issue(ctx, apikeys.NewStoreFromQueries(f.q), apikeys.IssueInput{
		OrgID: f.orgA, Name: "sl4-" + uuid.NewString()[:6], Scopes: []string{"sales_link.manage", "sales_link.read"}, CreatedBy: keyOwner.ID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}

	f.venue, f.event, f.session, f.promoter = uuid.New(), uuid.New(), uuid.New(), uuid.New()
	f.eventB, f.sessionB, f.promoterB, f.channel = uuid.New(), uuid.New(), uuid.New(), uuid.New()
	f.eventSlug, f.promoterSlug = "sl4-show-"+suffix, "sl4-promo-"+suffix
	venueB := uuid.New()
	start := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Hour)
	f.exec(t, `INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'SL4 Hall', 'Europe/Prague')`, f.venue, f.orgA)
	f.exec(t, `INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'SL4 Hall B', 'Europe/Prague')`, venueB, f.orgB)
	f.exec(t, `INSERT INTO events (id, org_id, name, status, visibility, slug) VALUES ($1, $2, 'SL4 Show', 'published', 'public', $3)`, f.event, f.orgA, f.eventSlug)
	f.exec(t, `INSERT INTO events (id, org_id, name, status, visibility, slug) VALUES ($1, $2, 'SL4 Show B', 'published', 'public', $3)`, f.eventB, f.orgB, "sl4-showb-"+suffix)
	for _, s := range []struct{ id, event, venue uuid.UUID }{{f.session, f.event, f.venue}, {f.sessionB, f.eventB, venueB}} {
		f.exec(t, `INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		           VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 50, 'scheduled', 'EUR', 'override')`, s.id, s.event, s.venue, start)
	}
	f.exec(t, `INSERT INTO org_promoters (id, org_id, name, slug) VALUES ($1, $2, 'SL4 Promoter', $3)`, f.promoter, f.orgA, f.promoterSlug)
	f.exec(t, `INSERT INTO org_promoters (id, org_id, name, slug) VALUES ($1, $2, 'SL4 Promoter B', $3)`, f.promoterB, f.orgB, "sl4-promob-"+suffix)
	f.exec(t, `INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, 'SL4 Channel')`, f.channel, f.orgA)

	t.Cleanup(f.cleanup)
	return f
}

func (f *sl4Fixture) cleanup() {
	c := context.Background()
	for _, st := range []string{
		`DELETE FROM sales_link_hits WHERE sales_link_id IN (SELECT id FROM sales_links WHERE org_id = ANY($1))`,
		`DELETE FROM tickets WHERE session_id IN (SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = ANY($1))`,
		`DELETE FROM orders WHERE org_id = ANY($1)`,
		`DELETE FROM sales_links WHERE org_id = ANY($1)`,
		`DELETE FROM checkout_sessions WHERE org_id = ANY($1)`,
		`DELETE FROM reservations WHERE org_id = ANY($1)`,
		`DELETE FROM ticket_tiers WHERE session_id IN (SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = ANY($1))`,
		`DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = ANY($1))`,
		`DELETE FROM events WHERE org_id = ANY($1)`,
		`DELETE FROM org_promoters WHERE org_id = ANY($1)`,
		`DELETE FROM api_keys WHERE org_id = ANY($1)`,
		`DELETE FROM sales_channels WHERE org_id = ANY($1)`,
		`DELETE FROM venues WHERE org_id = ANY($1)`,
		`DELETE FROM memberships WHERE org_id = ANY($1)`,
		`DELETE FROM organizations WHERE id = ANY($1)`,
	} {
		if _, err := f.pool.Exec(c, st, []uuid.UUID{f.orgA, f.orgB}); err != nil {
			fmt.Printf("sl4 cleanup %q: %v\n", st, err)
		}
	}
	if _, err := f.pool.Exec(c, `DELETE FROM users WHERE id = ANY($1)`, f.users); err != nil {
		fmt.Printf("sl4 cleanup users: %v\n", err)
	}
}

func (f *sl4Fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("fixture %q: %v", sql, err)
	}
}

func (f *sl4Fixture) do(t *testing.T, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	resp := integDoRequest(t, f.ts.Client(), method, f.ts.URL+path, token, body)
	raw := integReadBody(t, resp)
	out := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return resp.StatusCode, out
}

// doHeader is do with extra request headers.
func (f *sl4Fixture) doHeader(t *testing.T, method, path, token string, hdr map[string]string, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, f.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := f.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	out := map[string]any{}
	_ = json.Unmarshal([]byte(integReadBody(t, resp)), &out)
	return resp.StatusCode, out
}

func (f *sl4Fixture) base() string { return "/v1/organizations/" + f.orgA.String() + "/sales-links" }

// create posts a link as the owner and returns the answer.
func (f *sl4Fixture) create(t *testing.T, body string) map[string]any {
	t.Helper()
	st, out := f.do(t, http.MethodPost, f.base(), f.ownerA, body)
	if st != http.StatusCreated {
		t.Fatalf("create %s: status %d %v", body, st, out)
	}
	return out
}

func slErrCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func slStr(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func slMap(m map[string]any, k string) map[string]any { v, _ := m[k].(map[string]any); return v }

func TestSalesLinks_CreateOnEveryTargetKind(t *testing.T) {
	f := newSL4Fixture(t)

	// event: defaults, short_url, destination.
	ev := f.create(t, fmt.Sprintf(`{"target":{"kind":"event","event_id":%q},"label":"Poster at the door"}`, f.event))
	code := slStr(ev, "code")
	if len(code) != 7 {
		t.Fatalf("code %q is not 7 characters", code)
	}
	if got := slStr(ev, "short_url"); got != slShortBase+"/"+code {
		t.Errorf("short_url = %q, want %q", got, slShortBase+"/"+code)
	}
	utm := slMap(ev, "utm")
	want := map[string]string{"utm_source": "qr", "utm_medium": "offline", "utm_campaign": f.eventSlug, "utm_content": code}
	for k, v := range want {
		if utm[k] != v {
			t.Errorf("default utm[%s] = %v, want %q", k, utm[k], v)
		}
	}
	wantDest := slTicketsBase + "/" + f.orgASlug + "/" + f.eventSlug
	if got := slStr(ev, "destination_url"); got != wantDest {
		t.Errorf("destination_url = %q, want %q", got, wantDest)
	}
	if ev["destination_degraded"] != false {
		t.Errorf("destination_degraded = %v, want false", ev["destination_degraded"])
	}
	var host string
	if err := f.pool.QueryRow(context.Background(), `SELECT short_host FROM sales_links WHERE id = $1`, slStr(ev, "id")).Scan(&host); err != nil || host != "go.sl4.example.test" {
		t.Errorf("stored short_host = %q (%v), want the host of SHORT_LINK_BASE_URL", host, err)
	}
	if slStr(ev, "status") != "active" || slStr(ev, "placement_kind") != "poster" {
		t.Errorf("status/placement = %v/%v", ev["status"], ev["placement_kind"])
	}
	if ev["created_by"] == nil {
		t.Error("created_by must be the acting user for a JWT caller")
	}
	// A declared client channel is recorded.
	st, viaOut := f.doHeader(t, http.MethodPost, f.base(), f.ownerA, map[string]string{"X-Client-Channel": "telegram_bot"},
		`{"target":{"kind":"org_page"},"label":"From the bot"}`)
	if st != http.StatusCreated || slStr(viaOut, "created_via") != "telegram_bot" {
		t.Errorf("created_via with X-Client-Channel: status %d via %v, want telegram_bot", st, viaOut["created_via"])
	}
	if slStr(ev, "created_via") != "api" {
		t.Errorf("created_via = %v, want api", ev["created_via"])
	}

	// session: event_id is filled from the session.
	se := f.create(t, fmt.Sprintf(`{"target":{"kind":"session","session_id":%q},"label":"Date flyer","placement_kind":"flyer"}`, f.session))
	tg := slMap(se, "target")
	if tg["event_id"] != f.event.String() || tg["session_id"] != f.session.String() {
		t.Errorf("session target = %v, want event_id %s", tg, f.event)
	}
	if got := slStr(se, "destination_url"); got != wantDest+"?session="+f.session.String() {
		t.Errorf("session destination_url = %q", got)
	}

	// promoter, org page, url.
	pr := f.create(t, fmt.Sprintf(`{"target":{"kind":"promoter_page","promoter_id":%q},"label":"Promoter sign"}`, f.promoter))
	if got := slStr(pr, "destination_url"); got != slTicketsBase+"/"+f.promoterSlug {
		t.Errorf("promoter destination_url = %q", got)
	}
	if got := slMap(pr, "utm")["utm_campaign"]; got != f.promoterSlug {
		t.Errorf("promoter utm_campaign = %v", got)
	}
	org := f.create(t, `{"target":{"kind":"org_page"},"label":"Business card","placement_kind":"social"}`)
	if got := slStr(org, "destination_url"); got != slTicketsBase+"/"+f.orgASlug {
		t.Errorf("org destination_url = %q", got)
	}
	if utm := slMap(org, "utm"); utm["utm_source"] != "social" || utm["utm_medium"] != "social" || utm["utm_campaign"] != f.orgASlug {
		t.Errorf("social org utm = %v", utm)
	}
	ul := f.create(t, `{"target":{"kind":"url","url":"https://merch.example.test/shop"},"label":"Merch","utm":{"utm_source":"print","utm_campaign":"spring"}}`)
	if got := slStr(ul, "destination_url"); got != "https://merch.example.test/shop" {
		t.Errorf("url destination_url = %q", got)
	}
	if utm := slMap(ul, "utm"); utm["utm_source"] != "print" || utm["utm_campaign"] != "spring" || len(utm) != 2 {
		t.Errorf("explicit utm kept as given, got %v", utm)
	}

	// An API key creates links too, and has no created_by.
	if st, out := f.do(t, http.MethodPost, f.base(), f.keyA, `{"target":{"kind":"org_page"},"label":"By key"}`); st != http.StatusCreated || out["created_by"] != nil {
		t.Errorf("api key create: status %d created_by %v", st, out["created_by"])
	}

	// The audit row of the first link.
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE action = 'v1.sales_link.create' AND resource_id = $1`, slStr(ev, "id")).Scan(&n); err != nil || n != 1 {
		t.Errorf("v1.sales_link.create audit rows = %d (%v), want 1", n, err)
	}
}

func TestSalesLinks_Validation(t *testing.T) {
	f := newSL4Fixture(t)
	ownEvent := fmt.Sprintf(`"target":{"kind":"event","event_id":%q}`, f.event)
	cases := []struct {
		name, body string
		status     int
		code       string
	}{
		{"foreign event", fmt.Sprintf(`{"target":{"kind":"event","event_id":%q},"label":"x"}`, f.eventB), 422, "sales_link.invalid_target"},
		{"foreign session", fmt.Sprintf(`{"target":{"kind":"session","session_id":%q},"label":"x"}`, f.sessionB), 422, "sales_link.invalid_target"},
		{"foreign promoter", fmt.Sprintf(`{"target":{"kind":"promoter_page","promoter_id":%q},"label":"x"}`, f.promoterB), 422, "sales_link.invalid_target"},
		{"unknown event", fmt.Sprintf(`{"target":{"kind":"event","event_id":%q},"label":"x"}`, uuid.New()), 422, "sales_link.invalid_target"},
		{"session with foreign event", fmt.Sprintf(`{"target":{"kind":"session","session_id":%q,"event_id":%q},"label":"x"}`, f.session, f.eventB), 422, "sales_link.invalid_target"},
		{"unknown kind", `{"target":{"kind":"shop"},"label":"x"}`, 422, "sales_link.invalid_target"},
		{"event without id", `{"target":{"kind":"event"},"label":"x"}`, 422, "sales_link.invalid_target"},
		{"org page with an id", fmt.Sprintf(`{"target":{"kind":"org_page","event_id":%q},"label":"x"}`, f.event), 422, "sales_link.invalid_target"},
		{"label 121", `{` + ownEvent + `,"label":"` + strings.Repeat("a", 121) + `"}`, 400, "sales_link.invalid_label"},
		{"label blank", `{` + ownEvent + `,"label":"   "}`, 400, "sales_link.invalid_label"},
		{"placement", `{` + ownEvent + `,"label":"x","placement_kind":"billboard"}`, 400, "sales_link.invalid_placement"},
		{"utm bad char", `{` + ownEvent + `,"label":"x","utm":{"utm_source":"a b"}}`, 400, "sales_link.invalid_utm"},
		{"utm unknown key", `{` + ownEvent + `,"label":"x","utm":{"ref":"a"}}`, 400, "sales_link.invalid_utm"},
		{"utm too long", `{` + ownEvent + `,"label":"x","utm":{"utm_term":"` + strings.Repeat("a", 65) + `"}}`, 400, "sales_link.invalid_utm"},
		{"url missing", `{"target":{"kind":"url"},"label":"x"}`, 400, "sales_link.invalid_url"},
		{"url javascript", `{"target":{"kind":"url","url":"javascript:alert(1)"},"label":"x"}`, 400, "sales_link.invalid_url"},
		{"url credentials", `{"target":{"kind":"url","url":"https://u:p@example.test/"},"label":"x"}`, 400, "sales_link.invalid_url"},
		{"url too long", `{"target":{"kind":"url","url":"https://example.test/` + strings.Repeat("a", 2048) + `"},"label":"x"}`, 400, "sales_link.invalid_url"},
		{"unknown body field", `{` + ownEvent + `,"label":"x","bogus":1}`, 400, "sales_link.invalid_body"},
	}
	for _, c := range cases {
		st, out := f.do(t, http.MethodPost, f.base(), f.ownerA, c.body)
		if st != c.status || slErrCode(out) != c.code {
			t.Errorf("%s: status %d code %q, want %d %s", c.name, st, slErrCode(out), c.status, c.code)
		}
	}
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM sales_links WHERE org_id = $1`, f.orgA).Scan(&n); err != nil || n != 0 {
		t.Errorf("refused creates left %d rows (%v)", n, err)
	}
}

func TestSalesLinks_DestinationDegradesAndArchiveFilters(t *testing.T) {
	f := newSL4Fixture(t)
	ev := f.create(t, fmt.Sprintf(`{"target":{"kind":"event","event_id":%q},"label":"Poster"}`, f.event))
	id := slStr(ev, "id")
	one := f.base() + "/" + id

	f.exec(t, `UPDATE events SET status = 'draft' WHERE id = $1`, f.event)
	st, out := f.do(t, http.MethodGet, one, f.ownerA, "")
	if st != http.StatusOK || out["destination_degraded"] != true || slStr(out, "destination_url") != slTicketsBase+"/"+f.orgASlug {
		t.Errorf("draft event: status %d degraded %v dest %q", st, out["destination_degraded"], slStr(out, "destination_url"))
	}
	f.exec(t, `UPDATE events SET status = 'published' WHERE id = $1`, f.event)
	if _, out = f.do(t, http.MethodGet, one, f.ownerA, ""); out["destination_degraded"] != false {
		t.Errorf("republished event must not be degraded, got %v", out["destination_degraded"])
	}

	// Archive: listed under archived, not under active, and the code leads to the org page.
	st, out = f.do(t, http.MethodPatch, one, f.ownerA, `{"status":"archived"}`)
	if st != http.StatusOK || slStr(out, "status") != "archived" || out["destination_degraded"] != true {
		t.Fatalf("archive: status %d %v", st, out)
	}
	ids := func(status string) []string {
		_, l := f.do(t, http.MethodGet, f.base()+"?status="+status, f.ownerA, "")
		var got []string
		items, _ := l["items"].([]any)
		for _, it := range items {
			got = append(got, slStr(it.(map[string]any), "id"))
		}
		return got
	}
	if got := ids("active"); len(got) != 0 {
		t.Errorf("active list after archive = %v", got)
	}
	if got := ids("archived"); len(got) != 1 || got[0] != id {
		t.Errorf("archived list = %v", got)
	}
	if got := ids("all"); len(got) != 1 {
		t.Errorf("all list = %v", got)
	}
	if st, out := f.do(t, http.MethodGet, f.base()+"?status=gone", f.ownerA, ""); st != 400 || slErrCode(out) != "sales_link.invalid_status" {
		t.Errorf("status=gone: %d %q", st, slErrCode(out))
	}
	// Back to active writes the unarchive audit row.
	if st, _ := f.do(t, http.MethodPatch, one, f.ownerA, `{"status":"active"}`); st != http.StatusOK {
		t.Fatalf("unarchive: %d", st)
	}
	for _, action := range []string{"v1.sales_link.archive", "v1.sales_link.unarchive"} {
		var n int
		if err := f.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM audit_events WHERE action = $1 AND resource_id = $2`, action, id).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s audit rows = %d (%v), want 1", action, n, err)
		}
	}
}

func TestSalesLinks_PatchRetargetWritesAudit(t *testing.T) {
	f := newSL4Fixture(t)
	ev := f.create(t, fmt.Sprintf(`{"target":{"kind":"event","event_id":%q},"label":"Poster"}`, f.event))
	id := slStr(ev, "id")
	one := f.base() + "/" + id

	// A label-only change keeps the target and writes an update row, no retarget.
	st, out := f.do(t, http.MethodPatch, one, f.managerA, `{"label":"Poster, entrance"}`)
	if st != http.StatusOK || slStr(out, "label") != "Poster, entrance" || slMap(out, "target")["kind"] != "event" {
		t.Fatalf("label patch: %d %v", st, out)
	}
	// A foreign target is refused with 422 and the link does not move.
	st, out = f.do(t, http.MethodPatch, one, f.ownerA, fmt.Sprintf(`{"target":{"kind":"event","event_id":%q}}`, f.eventB))
	if st != 422 || slErrCode(out) != "sales_link.invalid_target" {
		t.Errorf("foreign retarget: %d %q", st, slErrCode(out))
	}
	// A real retarget moves it and audits from and to.
	st, out = f.do(t, http.MethodPatch, one, f.ownerA, fmt.Sprintf(`{"target":{"kind":"session","session_id":%q}}`, f.session))
	if st != http.StatusOK || slMap(out, "target")["kind"] != "session" || slMap(out, "target")["event_id"] != f.event.String() {
		t.Fatalf("retarget: %d %v", st, out)
	}
	var raw []byte
	if err := f.pool.QueryRow(context.Background(),
		`SELECT metadata FROM audit_events WHERE action = 'v1.sales_link.retarget' AND resource_id = $1`, id).Scan(&raw); err != nil {
		t.Fatalf("retarget audit row: %v", err)
	}
	var meta map[string]map[string]any
	_ = json.Unmarshal(raw, &meta)
	if meta["from"]["kind"] != "event" || meta["to"]["kind"] != "session" || meta["to"]["session_id"] != f.session.String() {
		t.Errorf("retarget audit metadata = %s", raw)
	}
	var updates int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = 'v1.sales_link.update' AND resource_id = $1`, id).Scan(&updates)
	if updates != 1 {
		t.Errorf("v1.sales_link.update rows = %d, want 1 (the label change)", updates)
	}
	// Same target again writes no second retarget row.
	if st, _ := f.do(t, http.MethodPatch, one, f.ownerA, fmt.Sprintf(`{"target":{"kind":"session","session_id":%q}}`, f.session)); st != http.StatusOK {
		t.Fatalf("repeat retarget: %d", st)
	}
	var rt int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = 'v1.sales_link.retarget' AND resource_id = $1`, id).Scan(&rt)
	if rt != 1 {
		t.Errorf("retarget audit rows after a repeat = %d, want 1", rt)
	}
	// Tri-state: utm null clears, an empty body is refused.
	if st, out := f.do(t, http.MethodPatch, one, f.ownerA, `{"utm":null}`); st != http.StatusOK || len(slMap(out, "utm")) != 0 {
		t.Errorf("utm null: %d %v", st, out["utm"])
	}
	if st, out := f.do(t, http.MethodPatch, one, f.ownerA, `{}`); st != 400 || slErrCode(out) != "sales_link.invalid_body" {
		t.Errorf("empty patch: %d %q", st, slErrCode(out))
	}
	if st, out := f.do(t, http.MethodPatch, one, f.ownerA, `{"label":null}`); st != 400 || slErrCode(out) != "sales_link.invalid_label" {
		t.Errorf("label null: %d %q", st, slErrCode(out))
	}
}

func TestSalesLinks_ForeignLinkIsNotFound(t *testing.T) {
	f := newSL4Fixture(t)
	ev := f.create(t, fmt.Sprintf(`{"target":{"kind":"event","event_id":%q},"label":"Poster"}`, f.event))
	id := slStr(ev, "id")
	// Org B's owner asks through its own path with org A's link id, and through A's path.
	own := "/v1/organizations/" + f.orgB.String() + "/sales-links/" + id
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, own, ""},
		{http.MethodGet, own + "/stats", ""},
		{http.MethodPatch, own, `{"status":"archived"}`},
		{http.MethodPatch, own, fmt.Sprintf(`{"target":{"kind":"event","event_id":%q}}`, f.event)},
	} {
		if st, out := f.do(t, c.method, c.path, f.ownerB, c.body); st != http.StatusNotFound || slErrCode(out) != "sales_link.not_found" {
			t.Errorf("%s %s as org B: %d %q, want 404 sales_link.not_found", c.method, c.path, st, slErrCode(out))
		}
	}
	// A is not a member of B's path.
	if st, _ := f.do(t, http.MethodGet, "/v1/organizations/"+f.orgA.String()+"/sales-links/"+id, f.ownerB, ""); st != http.StatusForbidden {
		t.Errorf("org B owner on A's path: %d, want 403", st)
	}
	// The link did not move.
	_, got := f.do(t, http.MethodGet, f.base()+"/"+id, f.ownerA, "")
	if slStr(got, "status") != "active" || slMap(got, "target")["event_id"] != f.event.String() {
		t.Errorf("link moved: %v", got)
	}
	// Org B's list is empty.
	if _, l := f.do(t, http.MethodGet, "/v1/organizations/"+f.orgB.String()+"/sales-links?status=all", f.ownerB, ""); len(l["items"].([]any)) != 0 {
		t.Errorf("org B sees links: %v", l["items"])
	}
}

func TestSalesLinks_ListCursorPaging(t *testing.T) {
	f := newSL4Fixture(t)
	var want []string
	for i := 0; i < 3; i++ {
		l := f.create(t, fmt.Sprintf(`{"target":{"kind":"org_page"},"label":"Link %d"}`, i))
		want = append([]string{slStr(l, "id")}, want...) // newest first
	}
	var got []string
	cursor := ""
	pages := 0
	for {
		path := f.base() + "?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		st, page := f.do(t, http.MethodGet, path, f.ownerA, "")
		if st != http.StatusOK {
			t.Fatalf("list: %d %v", st, page)
		}
		pages++
		for _, it := range page["items"].([]any) {
			got = append(got, slStr(it.(map[string]any), "id"))
		}
		cursor = slStr(page, "next_cursor")
		if cursor == "" {
			break
		}
		if pages > 5 {
			t.Fatal("cursor never ended")
		}
	}
	if pages != 2 || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("pages %d, got %v want %v", pages, got, want)
	}
	if st, out := f.do(t, http.MethodGet, f.base()+"?cursor=not-a-cursor", f.ownerA, ""); st != 400 || slErrCode(out) != "sales_link.invalid_cursor" {
		t.Errorf("bad cursor: %d %q", st, slErrCode(out))
	}
	// event_id filter: only event links.
	f.create(t, fmt.Sprintf(`{"target":{"kind":"event","event_id":%q},"label":"On the event"}`, f.event))
	_, l := f.do(t, http.MethodGet, f.base()+"?event_id="+f.event.String(), f.ownerA, "")
	if n := len(l["items"].([]any)); n != 1 {
		t.Errorf("event_id filter returned %d links, want 1", n)
	}
}

func TestSalesLinks_Stats(t *testing.T) {
	f := newSL4Fixture(t)
	ev := f.create(t, fmt.Sprintf(`{"target":{"kind":"event","event_id":%q},"label":"Poster"}`, f.event))
	id := uuid.MustParse(slStr(ev, "id"))
	today := time.Now().UTC().Truncate(24 * time.Hour)
	f.exec(t, `UPDATE sales_links SET scan_count = 5, suspect_scan_count = 1 WHERE id = $1`, id)
	f.exec(t, `INSERT INTO sales_link_hits (sales_link_id, day, country, hits, suspect_hits) VALUES ($1, $2, 'CZ', 3, 1), ($1, $3, '', 2, 0)`,
		id, today, today.AddDate(0, 0, -1))

	// One paid order of 2500 EUR with one ticket came through the link; a pending one does not count.
	tier, res, cs, ord, pending := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	f.exec(t, `INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, unit_seq, is_open)
	           VALUES ($1, $2, 'Standing', 'fixed', 2500, 'EUR', 50, 1, true)`, tier, f.session)
	f.exec(t, `INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	           VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, res, f.orgA, f.channel, f.session)
	f.exec(t, `INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`, cs, f.orgA, f.channel, res)
	f.exec(t, `INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                               source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, paid_at, sales_link_id)
	           VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 2500, 0, 0, 2500, 'SL4 Buyer', 'sl4-buyer@example.test', now(), $8)`,
		ord, f.orgA, f.channel, f.event, f.session, cs, res, id)
	f.exec(t, `INSERT INTO tickets (checkout_session_id, session_id, tier_id, holder_email, status, order_id, ordinal)
	           VALUES ($1, $2, $3, 'sl4-buyer@example.test', 'active', $4, 0)`, cs, f.session, tier, ord)
	// A second order that never got paid, same link.
	res2, cs2 := uuid.New(), uuid.New()
	f.exec(t, `INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at)
	           VALUES ($1, $2, $3, $4, 1, 'active', now() + interval '1 hour')`, res2, f.orgA, f.channel, f.session)
	f.exec(t, `INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'pricing_confirmed')`, cs2, f.orgA, f.channel, res2)
	f.exec(t, `INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                               source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, sales_link_id)
	           VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'pending_payment', 'EUR', 900, 0, 0, 900, 'SL4 Pending', 'sl4-pending@example.test', $8)`,
		pending, f.orgA, f.channel, f.event, f.session, cs2, res2, id)

	st, out := f.do(t, http.MethodGet, f.base()+"/"+id.String()+"/stats", f.managerA, "")
	if st != http.StatusOK {
		t.Fatalf("stats: %d %v", st, out)
	}
	if out["scans_total"] != float64(5) || out["suspect_total"] != float64(1) {
		t.Errorf("totals = %v/%v, want 5/1", out["scans_total"], out["suspect_total"])
	}
	days := out["by_day"].([]any)
	if len(days) != 2 {
		t.Fatalf("by_day = %v, want 2 days", days)
	}
	d0, d1 := days[0].(map[string]any), days[1].(map[string]any)
	if d0["day"] != today.AddDate(0, 0, -1).Format("2006-01-02") || d0["hits"] != float64(2) ||
		d1["day"] != today.Format("2006-01-02") || d1["hits"] != float64(3) || d1["suspect_hits"] != float64(1) {
		t.Errorf("by_day = %v", days)
	}
	countries := out["by_country"].([]any)
	if len(countries) != 2 || countries[0].(map[string]any)["country"] != "CZ" || countries[0].(map[string]any)["hits"] != float64(3) {
		t.Errorf("by_country = %v", countries)
	}
	orders := slMap(out, "orders")
	rev := orders["revenue"].([]any)
	if orders["count"] != float64(1) || orders["tickets"] != float64(1) || len(rev) != 1 ||
		rev[0].(map[string]any)["currency"] != "EUR" || rev[0].(map[string]any)["amount_minor"] != float64(2500) {
		t.Errorf("orders = %v", orders)
	}
	// The card carries the same order count.
	if _, card := f.do(t, http.MethodGet, f.base()+"/"+id.String(), f.ownerA, ""); card["orders_count"] != float64(1) || card["scan_count"] != float64(5) {
		t.Errorf("card orders_count/scan_count = %v/%v", card["orders_count"], card["scan_count"])
	}

	// Range rules.
	for _, q := range []string{"?from=2026-13-01", "?from=2026-02-01&to=2026-01-01", "?from=2020-01-01&to=2026-01-01"} {
		if st, out := f.do(t, http.MethodGet, f.base()+"/"+id.String()+"/stats"+q, f.ownerA, ""); st != 400 || slErrCode(out) != "sales_link.invalid_range" {
			t.Errorf("stats%s: %d %q", q, st, slErrCode(out))
		}
	}
	// An explicit range that excludes both hit days leaves the series empty but keeps the totals.
	_, out = f.do(t, http.MethodGet, f.base()+"/"+id.String()+"/stats?from=2020-01-01&to=2020-01-31", f.ownerA, "")
	if len(out["by_day"].([]any)) != 0 || out["scans_total"] != float64(5) {
		t.Errorf("old range: by_day %v totals %v", out["by_day"], out["scans_total"])
	}
}

func TestSalesLinks_RolesAndPermissions(t *testing.T) {
	f := newSL4Fixture(t)
	body := `{"target":{"kind":"org_page"},"label":"By role"}`
	// The owner and the manager (organizer) can create and read.
	for name, tok := range map[string]string{"owner": f.ownerA, "manager": f.managerA} {
		if st, out := f.do(t, http.MethodPost, f.base(), tok, body); st != http.StatusCreated {
			t.Errorf("%s create: %d %v", name, st, out)
		}
		if st, _ := f.do(t, http.MethodGet, f.base(), tok, ""); st != http.StatusOK {
			t.Errorf("%s list: %d", name, st)
		}
	}
	// An agent holds neither permission.
	if st, _ := f.do(t, http.MethodPost, f.base(), f.agentA, body); st != http.StatusForbidden {
		t.Errorf("agent create: %d, want 403", st)
	}
	if st, _ := f.do(t, http.MethodGet, f.base(), f.agentA, ""); st != http.StatusForbidden {
		t.Errorf("agent list: %d, want 403", st)
	}
	// No token at all.
	if st, _ := f.do(t, http.MethodGet, f.base(), "", ""); st != http.StatusUnauthorized {
		t.Errorf("anonymous list: %d, want 401", st)
	}
}

func TestSalesLinks_SoftDeletedTargetsAreInvalid(t *testing.T) {
	f := newSL4Fixture(t)
	f.exec(t, `UPDATE sessions SET deleted_at = now() WHERE id = $1`, f.session)
	st, out := f.do(t, http.MethodPost, f.base(), f.ownerA, fmt.Sprintf(`{"target":{"kind":"session","session_id":%q},"label":"x"}`, f.session))
	if st != 422 || slErrCode(out) != "sales_link.invalid_target" {
		t.Errorf("deleted session: %d %q, want 422 sales_link.invalid_target", st, slErrCode(out))
	}
	f.exec(t, `UPDATE events SET deleted_at = now() WHERE id = $1`, f.event)
	st, out = f.do(t, http.MethodPost, f.base(), f.ownerA, fmt.Sprintf(`{"target":{"kind":"event","event_id":%q},"label":"x"}`, f.event))
	if st != 422 || slErrCode(out) != "sales_link.invalid_target" {
		t.Errorf("deleted event: %d %q, want 422 sales_link.invalid_target", st, slErrCode(out))
	}
}

func TestSalesLinks_PatchRefusalsAndRoles(t *testing.T) {
	f := newSL4Fixture(t)
	ev := f.create(t, fmt.Sprintf(`{"target":{"kind":"event","event_id":%q},"label":"Poster"}`, f.event))
	id := slStr(ev, "id")
	one := f.base() + "/" + id

	for _, c := range []struct{ body, code string }{
		{`{"status":"deleted"}`, "sales_link.invalid_status"},
		{`{"status":null}`, "sales_link.invalid_status"},
		{`{"placement_kind":"billboard"}`, "sales_link.invalid_placement"},
		{`{"placement_kind":null}`, "sales_link.invalid_placement"},
		{`{"utm":{"utm_source":"a b"}}`, "sales_link.invalid_utm"},
		{`{"target":{"kind":"org_page","bogus":1}}`, "sales_link.invalid_body"},
		{`{"target":null}`, "sales_link.invalid_target"},
	} {
		st, out := f.do(t, http.MethodPatch, one, f.ownerA, c.body)
		want := 400
		if c.code == "sales_link.invalid_target" {
			want = 422
		}
		if st != want || slErrCode(out) != c.code {
			t.Errorf("PATCH %s: %d %q, want %d %s", c.body, st, slErrCode(out), want, c.code)
		}
	}

	// Repeating the stored label writes no update audit row.
	for i := 0; i < 2; i++ {
		if st, _ := f.do(t, http.MethodPatch, one, f.ownerA, `{"label":"Poster"}`); st != http.StatusOK {
			t.Fatalf("same label patch: %d", st)
		}
	}
	var n int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = 'v1.sales_link.update' AND resource_id = $1`, id).Scan(&n)
	if n != 0 {
		t.Errorf("unchanged label wrote %d update audit rows, want 0", n)
	}

	// An agent holds neither permission: PATCH and stats are 403.
	if st, _ := f.do(t, http.MethodPatch, one, f.agentA, `{"label":"Hack"}`); st != http.StatusForbidden {
		t.Errorf("agent PATCH: %d, want 403", st)
	}
	if st, _ := f.do(t, http.MethodGet, one+"/stats", f.agentA, ""); st != http.StatusForbidden {
		t.Errorf("agent stats: %d, want 403", st)
	}
	// An API key with the scopes reads the card and the stats and patches.
	if st, out := f.do(t, http.MethodGet, one, f.keyA, ""); st != http.StatusOK || slStr(out, "id") != id {
		t.Errorf("api key GET: %d %v", st, out)
	}
	if st, out := f.do(t, http.MethodGet, one+"/stats", f.keyA, ""); st != http.StatusOK || out["scans_total"] != float64(0) {
		t.Errorf("api key stats: %d %v", st, out)
	}
	if st, out := f.do(t, http.MethodPatch, one, f.keyA, `{"label":"By key"}`); st != http.StatusOK || slStr(out, "label") != "By key" {
		t.Errorf("api key PATCH: %d %v", st, out)
	}

	// A cursor that is valid base64url with a bad inner part is 400.
	for _, raw := range []string{"garbage", "2026-10-24T12:00:00Z|not-a-uuid", "not-a-time|" + uuid.NewString()} {
		cur := base64.RawURLEncoding.EncodeToString([]byte(raw))
		if st, out := f.do(t, http.MethodGet, f.base()+"?cursor="+cur, f.ownerA, ""); st != 400 || slErrCode(out) != "sales_link.invalid_cursor" {
			t.Errorf("cursor %q: %d %q", raw, st, slErrCode(out))
		}
	}
}

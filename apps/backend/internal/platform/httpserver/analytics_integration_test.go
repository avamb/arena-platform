//go:build integration

package httpserver

// analytics_integration_test.go — stage A1 of spec 50: the product analytics
// receivers through the REAL router, the buffered writer and a migrated
// database (run on a scratch one, never the shared stand):
//
//	DATABASE_URL=... go test -tags integration -p 1 ./apps/backend/internal/platform/httpserver/ -run TestAnalytics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/analytics"
)

const analyticsBrowserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Safari/605.1.15"

type analyticsFixture struct {
	srv *Server
	ts  *httptest.Server
}

func newAnalyticsFixture(t *testing.T) *analyticsFixture {
	t.Helper()
	srv, _ := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return &analyticsFixture{srv: srv, ts: ts}
}

func (f *analyticsFixture) post(t *testing.T, path, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", analyticsBrowserUA)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := f.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// flush writes the buffered events (the same path as the graceful shutdown).
func (f *analyticsFixture) flush(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.srv.closeAnalytics(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

type storedEvent struct {
	Surface, Name, IDKind, IDValue string
	OrgID                          *uuid.UUID
	Props                          map[string]any
	Suspect                        bool
	At                             time.Time
}

func (f *analyticsFixture) stored(t *testing.T, idKind, idValue string) []storedEvent {
	t.Helper()
	rows, err := f.srv.pgxPool.Query(context.Background(),
		`SELECT surface, name, id_kind, id_value, org_id, props, suspect, at
		   FROM analytics.events WHERE id_kind = $1 AND id_value = $2 ORDER BY at, id`, idKind, idValue)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []storedEvent
	for rows.Next() {
		var e storedEvent
		var raw []byte
		if err := rows.Scan(&e.Surface, &e.Name, &e.IDKind, &e.IDValue, &e.OrgID, &raw, &e.Suspect, &e.At); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &e.Props); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func cleanupAnalytics(t *testing.T, f *analyticsFixture, idKind, idValue string) {
	t.Cleanup(func() {
		_, _ = f.srv.pgxPool.Exec(context.Background(),
			`DELETE FROM analytics.events WHERE id_kind = $1 AND id_value = $2`, idKind, idValue)
	})
}

func TestAnalytics_SiteBatchStoresOnlyTheAllowedParts(t *testing.T) {
	f := newAnalyticsFixture(t)
	aid := "a_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	cleanupAnalytics(t, f, "aid", aid)

	body := `{"aid":"` + aid + `","pv":"p_abcdefgh","events":[
		{"name":"page_view","at":"` + time.Now().UTC().Format(time.RFC3339) + `","props":{"path":"/pricing?e=a@b.c","lang":"ru","email":"a@b.c","utm_source":"newsletter"}},
		{"name":"not_in_the_dictionary","props":{"path":"/x"}},
		{"name":"screen_view","props":{"screen":"/events"}},
		{"name":"scroll","props":{"path":"/pricing","depth":50,"text":"hello"}},
		{"name":"email_click","props":{"path":"/forged"}}
	]}`
	if resp := f.post(t, "/v1/t", "", body); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	f.flush(t)

	got := f.stored(t, "aid", aid)
	if len(got) != 2 {
		t.Fatalf("stored %d events, want page_view and scroll only: %+v", len(got), got)
	}
	pv := got[0]
	if pv.Name != "page_view" || pv.Surface != "site" || pv.Suspect || pv.OrgID != nil {
		t.Errorf("page_view row = %+v", pv)
	}
	if pv.Props["path"] != "/pricing" || pv.Props["lang"] != "ru" || pv.Props["utm_source"] != "newsletter" {
		t.Errorf("page_view props = %v", pv.Props)
	}
	if _, has := pv.Props["email"]; has {
		t.Errorf("email property stored: %v", pv.Props)
	}
	sc := got[1]
	if sc.Name != "scroll" || sc.Props["depth"] != float64(50) {
		t.Errorf("scroll row = %+v", sc)
	}
	if _, has := sc.Props["text"]; has {
		t.Errorf("text property stored: %v", sc.Props)
	}
	if time.Since(pv.At) > time.Minute || pv.At.After(time.Now().Add(time.Second)) {
		t.Errorf("stored time %v is not the server's now", pv.At)
	}
}

func TestAnalytics_ContactCodeWritesEmailClick(t *testing.T) {
	f := newAnalyticsFixture(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	code, unknown := "t"+suffix[:9], "u"+suffix[:9]
	aid := "a_" + suffix[:16]
	if _, err := f.srv.pgxPool.Exec(ctx,
		`INSERT INTO analytics.contact_codes (code, twenty_person_id) VALUES ($1, 'twenty-test')`, code); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.srv.pgxPool.Exec(ctx, `DELETE FROM analytics.contact_codes WHERE code = $1`, code)
	})
	cleanupAnalytics(t, f, "aid", aid)
	cleanupAnalytics(t, f, "cid", code)
	cleanupAnalytics(t, f, "cid", unknown)

	batch := func(cid string) string {
		return `{"aid":"` + aid + `","cid":"` + cid + `","pv":"p_abcdefgh","events":[
			{"name":"page_view","props":{"path":"/pricing","utm_campaign":"nov"}}]}`
	}
	f.post(t, "/v1/t", "", batch(code))
	f.post(t, "/v1/t", "", batch(unknown))
	f.flush(t)

	clicks := f.stored(t, "cid", code)
	if len(clicks) != 1 || clicks[0].Name != "email_click" || clicks[0].Surface != "site" {
		t.Fatalf("email_click rows = %+v", clicks)
	}
	if clicks[0].Props["path"] != "/pricing" || clicks[0].Props["utm_campaign"] != "nov" || clicks[0].Props["aid"] != aid {
		t.Errorf("email_click props = %v", clicks[0].Props)
	}
	if n := len(f.stored(t, "cid", unknown)); n != 0 {
		t.Errorf("an unknown code wrote %d events", n)
	}
	if n := len(f.stored(t, "aid", aid)); n != 2 {
		t.Errorf("visitor rows = %d, want the two page_views", n)
	}
}

func TestAnalytics_EventCenterUserFromTokenAndOrgChecked(t *testing.T) {
	f := newAnalyticsFixture(t)
	ctx := context.Background()
	q := gen.New(f.srv.pgxPool)
	run := strings.ToLower(uuid.NewString()[:8])

	user, err := q.InsertUser(ctx, "analytics-a-"+run+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	other, err := q.InsertUser(ctx, "analytics-b-"+run+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	ownOrg, err := q.InsertOrganization(ctx, "Analytics Own "+run, "analytics-own-"+run, "ES", "en", 1200)
	if err != nil {
		t.Fatal(err)
	}
	foreignOrg, err := q.InsertOrganization(ctx, "Analytics Foreign "+run, "analytics-foreign-"+run, "ES", "en", 1200)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.InsertMembership(ctx, user.ID, ownOrg.ID, "organizer"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = f.srv.pgxPool.Exec(bg, `DELETE FROM analytics.events WHERE id_kind = 'user' AND id_value = ANY($1)`, []string{user.ID.String(), other.ID.String()})
		_, _ = f.srv.pgxPool.Exec(bg, `DELETE FROM memberships WHERE org_id = ANY($1)`, []uuid.UUID{ownOrg.ID, foreignOrg.ID})
		_, _ = f.srv.pgxPool.Exec(bg, `DELETE FROM organizations WHERE id = ANY($1)`, []uuid.UUID{ownOrg.ID, foreignOrg.ID})
		_, _ = f.srv.pgxPool.Exec(bg, `DELETE FROM users WHERE id = ANY($1)`, []uuid.UUID{user.ID, other.ID})
	})

	token := mintJWT(t, f.srv.stub, user.ID.String())
	ev := func(step string) string {
		return `{"name":"flow_step","props":{"flow":"event_create","step":"` + step + `"}}`
	}
	// A forged user in the body (top level and inside the event) and a forged
	// kind/value must change nothing.
	forged := `{"user_id":"` + other.ID.String() + `","id_kind":"aid","id_value":"x","org_id":"` + ownOrg.ID.String() + `",
		"events":[{"name":"flow_step","props":{"flow":"event_create","step":"venue","user_id":"` + other.ID.String() + `"}}]}`
	if resp := f.post(t, "/v1/me/t", token, forged); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	foreign := `{"org_id":"` + foreignOrg.ID.String() + `","events":[` + ev("dates") + `]}`
	if resp := f.post(t, "/v1/me/t", token, foreign); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp := f.post(t, "/v1/me/t", "", forged); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token -> %d, want 401", resp.StatusCode)
	}
	f.flush(t)

	if n := len(f.stored(t, "user", other.ID.String())); n != 0 {
		t.Fatalf("a forged user_id attributed %d events to another user", n)
	}
	got := f.stored(t, "user", user.ID.String())
	if len(got) != 2 {
		t.Fatalf("stored %d events, want 2: %+v", len(got), got)
	}
	for _, e := range got {
		if e.Surface != "event_center" || e.Name != "flow_step" {
			t.Errorf("row = %+v", e)
		}
		if _, has := e.Props["user_id"]; has {
			t.Errorf("user_id stored in props: %v", e.Props)
		}
	}
	if got[0].Props["step"] != "venue" || got[0].OrgID == nil || *got[0].OrgID != ownOrg.ID {
		t.Errorf("own-org event = %+v", got[0])
	}
	if got[1].Props["step"] != "dates" || got[1].OrgID != nil {
		t.Errorf("foreign-org event kept an organization: %+v", got[1])
	}
}

func TestAnalytics_PartitionMaintenanceOnTheRealTable(t *testing.T) {
	f := newAnalyticsFixture(t)
	ctx := context.Background()
	db := f.srv.pgxPool
	// An ancient partition that must go, and a name outside the convention
	// that must stay.
	if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS analytics.events_y2019m01 PARTITION OF analytics.events
		FOR VALUES FROM ('2019-01-01 00:00:00+00') TO ('2019-02-01 00:00:00+00')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	created, dropped, err := analytics.MaintainPartitions(ctx, db, now)
	if err != nil {
		t.Fatalf("MaintainPartitions: %v", err)
	}
	if len(dropped) != 1 || dropped[0] != "events_y2019m01" {
		t.Errorf("dropped = %v", dropped)
	}
	// Idempotent, and the horizon is three months ahead.
	if created2, dropped2, err := analytics.MaintainPartitions(ctx, db, now); err != nil || len(created2) != 0 || len(dropped2) != 0 {
		t.Errorf("second run = %v %v %v", created2, dropped2, err)
	}
	// The horizon is three months ahead: lose the last one and it comes back.
	last := analytics.PartitionName(analytics.MonthStart(now).AddDate(0, analytics.PartitionsAhead, 0))
	if _, err := db.Exec(ctx, "DROP TABLE IF EXISTS analytics."+last); err != nil {
		t.Fatal(err)
	}
	created, _, err = analytics.MaintainPartitions(ctx, db, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || created[0] != last {
		t.Errorf("created %v, want [%s]", created, last)
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM pg_inherits i JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace s ON s.oid = p.relnamespace WHERE s.nspname = 'analytics' AND p.relname = 'events'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < 4 {
		t.Errorf("partitions = %d", n)
	}
}

// A client with a wild clock must not be able to fail a COPY: every event of
// the batch is stored, inside [now - 24 h, now].
func TestAnalytics_SkewedClientTimesNeverFailTheWrite(t *testing.T) {
	f := newAnalyticsFixture(t)
	aid := "a_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	cleanupAnalytics(t, f, "aid", aid)

	now := time.Now().UTC()
	times := []string{
		now.AddDate(-30, 0, 0).Format(time.RFC3339), // year 1996
		now.AddDate(30, 0, 0).Format(time.RFC3339),  // year 2056: no partition exists
		now.Add(-47 * time.Hour).Format(time.RFC3339),
		now.Add(23 * time.Hour).Format(time.RFC3339),
		now.Add(-90 * time.Minute).Format(time.RFC3339),
		"not a time",
		"",
	}
	var evs []string
	for _, ts := range times {
		evs = append(evs, `{"name":"page_view","at":"`+ts+`","props":{"path":"/"}}`)
	}
	body := `{"aid":"` + aid + `","events":[` + strings.Join(evs, ",") + `]}`
	if resp := f.post(t, "/v1/t", "", body); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	// Plus an in-process caller with an absurd time, as the bot will be.
	f.srv.analyticsWriter().Track(context.Background(), analytics.Event{
		At: now.AddDate(40, 0, 0), Surface: analytics.SurfaceSite, Name: "page_view",
		IDKind: analytics.IDKindAID, IDValue: aid, Props: map[string]any{"path": "/"},
	})
	f.flush(t)

	got := f.stored(t, "aid", aid)
	if len(got) != len(times)+1 {
		t.Fatalf("stored %d of %d events: the COPY was lost", len(got), len(times)+1)
	}
	for i, e := range got {
		if e.At.Before(now.Add(-24*time.Hour-time.Minute)) || e.At.After(time.Now().Add(time.Minute)) {
			t.Errorf("event %d stored at %v, outside [now-24h, now]", i, e.At)
		}
	}
	var def int
	if err := f.srv.pgxPool.QueryRow(context.Background(), `SELECT count(*) FROM analytics.events_default`).Scan(&def); err != nil || def != 0 {
		t.Errorf("rows in the default partition = %d (%v)", def, err)
	}
}

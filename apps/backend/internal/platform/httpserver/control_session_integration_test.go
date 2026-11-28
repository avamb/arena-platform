//go:build integration

// control_session_integration_test.go — MA-40 (migration 0228) through the REAL
// router, authenticated with an organization API key: one request creates the
// container event and its control_only session, the event is never published,
// takes no sold session, a session never turns into a control one, and
// nothing is announced to the outbox.
//
// Run against a FRESH migrated database (AGENTS.md CI-Integration recipe).
package httpserver

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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
)

type controlSessionFixture struct {
	t       *testing.T
	ts      *httptest.Server
	srv     *Server
	orgID   uuid.UUID
	venueID uuid.UUID
	key     string
}

func newControlSessionFixture(t *testing.T) *controlSessionFixture {
	t.Helper()
	srv, _ := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	ctx := context.Background()
	pool := srv.pgxPool
	q := gen.New(pool)
	suffix := uuid.NewString()[:8]

	user, err := q.InsertUser(ctx, "ctl-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	org, err := q.InsertOrganization(ctx, "Ctl "+suffix, "ctl-"+suffix, "EE", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	f := &controlSessionFixture{t: t, ts: ts, srv: srv, orgID: org.ID, venueID: uuid.New()}
	if _, err := pool.Exec(ctx, `INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`,
		f.venueID, org.ID, "Ctl Venue "+suffix); err != nil {
		t.Fatalf("venue: %v", err)
	}
	_, f.key, err = apikeys.Issue(ctx, apikeys.NewStoreFromQueries(q), apikeys.IssueInput{
		OrgID: org.ID, Name: "ctl-" + suffix,
		Scopes: []string{"session.create", "session.read", "session.update", "session.delete",
			"event.create", "event.read", "event.update", "event.publish", "tier.create"},
		CreatedBy: user.ID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}
	t.Cleanup(func() {
		for _, stmt := range []string{
			`DELETE FROM outbox_events WHERE payload->>'org_id' = $1::text`,
			`DELETE FROM api_keys WHERE org_id = $1::uuid`,
			`DELETE FROM inventory_ledger WHERE session_id IN (SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1::uuid)`,
			`DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1::uuid)`,
			`DELETE FROM events WHERE org_id = $1::uuid`,
			`DELETE FROM venues WHERE org_id = $1::uuid`,
			`DELETE FROM organizations WHERE id = $1::uuid`,
		} {
			if _, err := pool.Exec(ctx, stmt, org.ID.String()); err != nil {
				t.Logf("control cleanup: %s: %v", stmt, err)
			}
		}
		if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID); err != nil {
			t.Logf("control cleanup user: %v", err)
		}
	})
	return f
}

func (f *controlSessionFixture) do(method, path, body string) (int, map[string]any) {
	f.t.Helper()
	resp := integDoRequest(f.t, f.ts.Client(), method, f.ts.URL+path, f.key, body)
	raw := integReadBody(f.t, resp)
	out := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			f.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	return resp.StatusCode, out
}

func (f *controlSessionFixture) base() string { return "/v1/organizations/" + f.orgID.String() }

func TestControlSessionHTTP_CreateGuardsAndSilence(t *testing.T) {
	f := newControlSessionFixture(t)
	ctx := context.Background()
	pool := f.srv.pgxPool
	start := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Minute)

	// ── Validation first: nothing is written ───────────────────────────────
	st, body := f.do(http.MethodPost, f.base()+"/control-sessions", `{"venue_id":"`+f.venueID.String()+`","start_at":"`+start.Format(time.RFC3339)+`","currency":"EUR"}`)
	if st != http.StatusBadRequest || scErrCode(body) != "session.missing_title" {
		t.Fatalf("no title: %d %v", st, body)
	}
	// A venue with no geography needs an explicit currency.
	st, body = f.do(http.MethodPost, f.base()+"/control-sessions", `{"title":"Hall","venue_id":"`+f.venueID.String()+`","start_at":"`+start.Format(time.RFC3339)+`"}`)
	if st != http.StatusUnprocessableEntity || scErrCode(body) != "session.currency_unresolvable" {
		t.Fatalf("no currency: %d %v", st, body)
	}
	var events int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE org_id = $1`, f.orgID).Scan(&events)
	if events != 0 {
		t.Fatalf("a refused create left %d events behind", events)
	}

	// ── One request: container event + control session ─────────────────────
	st, body = f.do(http.MethodPost, f.base()+"/control-sessions",
		`{"title":"  Town hall concert  ","venue_id":"`+f.venueID.String()+`","start_at":"`+start.Format(time.RFC3339)+`","capacity":300,"currency":"eur"}`)
	if st != http.StatusCreated {
		t.Fatalf("create: %d %v", st, body)
	}
	ev, _ := body["event"].(map[string]any)
	sess, _ := body["session"].(map[string]any)
	if ev["name"] != "Town hall concert" || ev["status"] != "draft" || ev["visibility"] != "private" || ev["control_only"] != true {
		t.Fatalf("event = %v", ev)
	}
	if sess["sales_mode"] != "control_only" || sess["status"] != "scheduled" || sess["admission_mode"] != "general_admission" ||
		sess["capacity_total"] != float64(300) || sess["currency"] != "EUR" || sess["sales_state"] != "ended" || sess["external"] != nil {
		t.Fatalf("session = %v", sess)
	}
	eventID, _ := ev["id"].(string)
	sessionID, _ := sess["id"].(string)
	if sess["event_id"] != eventID {
		t.Fatalf("session.event_id = %v, want %s", sess["event_id"], eventID)
	}
	if got, want := sess["end_at"], start.Add(3*time.Hour).Format(time.RFC3339); got != want {
		t.Errorf("end_at = %v, want the default three hours %s", got, want)
	}
	var ledger, tiers int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM inventory_ledger WHERE session_id = $1`, sessionID).Scan(&ledger)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM ticket_tiers WHERE session_id = $1`, sessionID).Scan(&tiers)
	if ledger != 0 || tiers != 0 {
		t.Fatalf("ledger rows %d, tiers %d; a control session owns neither", ledger, tiers)
	}

	// ── The reads carry the flag; sales_state is never on_sale ─────────────
	st, body = f.do(http.MethodGet, "/v1/events/"+eventID, "")
	if st != http.StatusOK {
		t.Fatalf("GET event: %d %v", st, body)
	}
	if e, _ := body["event"].(map[string]any); e["control_only"] != true || e["sales_state"] != "draft" {
		t.Fatalf("GET event = %v, want control_only true and sales_state draft", e)
	}
	st, body = f.do(http.MethodGet, f.base()+"/events", "")
	if st != http.StatusOK {
		t.Fatalf("list events: %d %v", st, body)
	}
	list, _ := body["events"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["control_only"] != true {
		t.Fatalf("list = %v, want one control event", list)
	}

	// ── The event is never published ───────────────────────────────────────
	st, body = f.do(http.MethodPost, f.base()+"/events/"+eventID+"/status", `{"status":"published"}`)
	if st != http.StatusConflict || scErrCode(body) != "event.control_only" {
		t.Fatalf("publish: %d %v, want 409 event.control_only", st, body)
	}
	var status string
	_ = pool.QueryRow(ctx, `SELECT status FROM events WHERE id = $1`, eventID).Scan(&status)
	if status != "draft" {
		t.Fatalf("event status = %q after a refused publish", status)
	}

	// ── No session that is sold joins it ───────────────────────────────────
	sessionsURL := f.base() + "/events/" + eventID + "/sessions"
	plain := `{"venue_id":"` + f.venueID.String() + `","start_at":"` + start.Format(time.RFC3339) + `","end_at":"` + start.Add(time.Hour).Format(time.RFC3339) + `","currency":"EUR"}`
	st, body = f.do(http.MethodPost, sessionsURL, plain)
	if st != http.StatusConflict || scErrCode(body) != "event.control_only" {
		t.Fatalf("session on a control event: %d %v, want 409 event.control_only", st, body)
	}

	// ── A session never turns into a control one, nor back ─────────────────
	st, body = f.do(http.MethodPost, f.base()+"/events", `{"name":"Ordinary"}`)
	if st != http.StatusCreated {
		t.Fatalf("create event: %d %v", st, body)
	}
	ordEvent, _ := body["event"].(map[string]any)
	ordURL := f.base() + "/events/" + ordEvent["id"].(string) + "/sessions"
	st, body = f.do(http.MethodPost, ordURL, strings.Replace(plain, `{`, `{"sales_mode":"control_only",`, 1))
	if st != http.StatusConflict || scErrCode(body) != "session.sales_mode_locked" {
		t.Fatalf("create with sales_mode control_only: %d %v, want 409 session.sales_mode_locked", st, body)
	}
	st, body = f.do(http.MethodPost, ordURL, plain)
	if st != http.StatusCreated {
		t.Fatalf("ordinary session: %d %v", st, body)
	}
	ordSession := body["session"].(map[string]any)
	st, body = f.do(http.MethodPatch, ordURL+"/"+ordSession["id"].(string), `{"sales_mode":"control_only"}`)
	if st != http.StatusConflict || scErrCode(body) != "session.sales_mode_locked" {
		t.Fatalf("PATCH ordinary to control_only: %d %v, want 409 session.sales_mode_locked", st, body)
	}
	for _, patch := range []string{`{"sales_mode":"platform"}`, `{"sales_mode":"external","external":{"url":"https://www.eventim.de/x","seller_name":"E"}}`} {
		st, body = f.do(http.MethodPatch, sessionsURL+"/"+sessionID, patch)
		if st != http.StatusConflict || scErrCode(body) != "session.sales_mode_locked" {
			t.Fatalf("PATCH control session %s: %d %v, want 409 session.sales_mode_locked", patch, st, body)
		}
	}
	st, body = f.do(http.MethodGet, f.base()+"/events", "")
	for _, e := range body["events"].([]any) {
		m := e.(map[string]any)
		if m["id"] == ordEvent["id"] && m["control_only"] != false {
			t.Fatalf("ordinary event reads control_only %v", m["control_only"])
		}
	}

	// ── Tiers: a control session owns none ────────────────────────────────
	st, body = f.do(http.MethodPost, sessionsURL+"/"+sessionID+"/tiers",
		`{"name":"GA","pricing_mode":"fixed","price_amount":1000,"capacity":10}`)
	if st != http.StatusConflict || scErrCode(body) != "session.sold_externally" {
		t.Fatalf("tier on a control session: %d %v, want 409 session.sold_externally", st, body)
	}

	// ── Silence: editing the control session announces nothing ─────────────
	st, body = f.do(http.MethodPatch, sessionsURL+"/"+sessionID, `{"start_at":"`+start.Add(24*time.Hour).Format(time.RFC3339)+`"}`)
	if st != http.StatusOK {
		t.Fatalf("PATCH control session: %d %v", st, body)
	}
	st, body = f.do(http.MethodPatch, sessionsURL+"/"+sessionID, `{"status":"cancelled"}`)
	if st != http.StatusOK {
		t.Fatalf("cancel control session: %d %v", st, body)
	}
	var rows int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events
	  WHERE (aggregate_id::text = $1::text OR payload->>'event_id' = $1::text OR payload->>'session_id' = $2::text)`, eventID, sessionID).Scan(&rows)
	if rows != 0 {
		t.Fatalf("%d outbox rows for a control event; it is never announced", rows)
	}
	// The same edit of an ordinary session IS announced, so the zero above
	// proves the guard and not a server without an outbox.
	st, body = f.do(http.MethodPatch, ordURL+"/"+ordSession["id"].(string), `{"start_at":"`+start.Add(48*time.Hour).Format(time.RFC3339)+`"}`)
	if st != http.StatusOK {
		t.Fatalf("PATCH ordinary session: %d %v", st, body)
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE payload->>'event_id' = $1::text`, ordEvent["id"].(string)).Scan(&rows)
	if rows == 0 {
		t.Fatal("an ordinary session edit wrote no outbox row: the silence check above proves nothing")
	}
}

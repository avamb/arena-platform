//go:build integration

// session_change_http_integration_test.go — the session-change surface through
// the REAL router, authenticated with an organization API key: the dry run
// (GET .../change-impact), the organizer contact (PUT .../events/{id}/contact),
// a PATCH that moves a session with a paying buyer (refused without a contact,
// accepted with one, letter queued, length kept) and a DELETE that cancels it.
//
// Run against a FRESH migrated database (AGENTS.md CI-Integration recipe).
package httpserver

import (
	"context"
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
)

type scHTTPFixture struct {
	t         *testing.T
	ts        *httptest.Server
	pool      *pgxpool.Pool
	q         *gen.Queries
	orgID     uuid.UUID
	eventID   uuid.UUID
	sessionID uuid.UUID
	orderID   uuid.UUID
	channelID uuid.UUID
	key       string
	start     time.Time
	cleanup   func()
}

func newSCHTTPFixture(t *testing.T) *scHTTPFixture {
	t.Helper()
	srv, _ := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	ctx := context.Background()
	pool := srv.pgxPool
	q := gen.New(pool)
	suffix := uuid.NewString()[:8]

	user, err := q.InsertUser(ctx, "sc-http-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	org, err := q.InsertOrganization(ctx, "SC HTTP "+suffix, "sc-http-"+suffix, "EE", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	f := &scHTTPFixture{
		t: t, ts: ts, pool: pool, q: q, orgID: org.ID,
		eventID: uuid.New(), sessionID: uuid.New(), orderID: uuid.New(), channelID: uuid.New(),
		start: time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Minute),
	}
	venueID, resID, csID, ticketID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	email := "buyer-" + suffix + "@example.com"
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, []any{venueID, org.ID, "SC Venue " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'private')`, []any{f.eventID, org.ID, "SC Event " + suffix}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		  VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 100, 'scheduled', 'EUR', 'override')`, []any{f.sessionID, f.eventID, venueID, f.start}},
		{`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`, []any{f.channelID, org.ID, "SC Channel " + suffix}},
		{`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
		  VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, []any{resID, org.ID, f.channelID, f.sessionID}},
		{`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`, []any{csID, org.ID, f.channelID, resID}},
		{`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
		                      source, status, currency, subtotal, discount, charge, total, buyer_email)
		  VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 2500, 0, 0, 2500, $8)`,
			[]any{f.orderID, org.ID, f.channelID, f.eventID, f.sessionID, csID, resID, email}},
		{`INSERT INTO tickets (id, checkout_session_id, session_id, holder_email, order_id) VALUES ($1, $2, $3, $4, $5)`,
			[]any{ticketID, csID, f.sessionID, email, f.orderID}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("fixture step %d: %v", i, err)
		}
	}
	_, f.key, err = apikeys.Issue(ctx, apikeys.NewStoreFromQueries(q), apikeys.IssueInput{
		OrgID: org.ID, Name: "sc-http-" + suffix,
		Scopes:    []string{"session.read", "session.update", "session.delete", "event.read", "event.update"},
		CreatedBy: user.ID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}
	f.cleanup = func() {
		for _, stmt := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM worker_jobs WHERE job_type = 'session.change_email' AND payload->>'order_id' = $1`, []any{f.orderID.String()}},
			{`DELETE FROM session_changes WHERE org_id = $1`, []any{org.ID}},
			{`DELETE FROM tickets WHERE id = $1`, []any{ticketID}},
			{`DELETE FROM orders WHERE id = $1`, []any{f.orderID}},
			{`DELETE FROM checkout_sessions WHERE id = $1`, []any{csID}},
			{`DELETE FROM reservations WHERE id = $1`, []any{resID}},
			{`DELETE FROM api_keys WHERE org_id = $1`, []any{org.ID}},
			{`DELETE FROM audit_events WHERE metadata->>'org_id' = $1`, []any{org.ID.String()}},
			{`DELETE FROM sales_channels WHERE id = $1`, []any{f.channelID}},
			{`DELETE FROM sessions WHERE id = $1`, []any{f.sessionID}},
			{`DELETE FROM events WHERE id = $1`, []any{f.eventID}},
			{`DELETE FROM venues WHERE id = $1`, []any{venueID}},
			{`DELETE FROM organizations WHERE id = $1`, []any{org.ID}},
			{`DELETE FROM users WHERE id = $1`, []any{user.ID}},
		} {
			if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
				t.Logf("sc http cleanup: %s: %v", stmt.sql, err)
			}
		}
	}
	t.Cleanup(f.cleanup)
	return f
}

func (f *scHTTPFixture) do(method, path, body string) (int, map[string]any) {
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

func (f *scHTTPFixture) sessionStart() time.Time {
	f.t.Helper()
	var s time.Time
	if err := f.pool.QueryRow(context.Background(), `SELECT start_at FROM sessions WHERE id = $1`, f.sessionID).Scan(&s); err != nil {
		f.t.Fatal(err)
	}
	return s
}

func scErrCode(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		if c, ok := e["code"].(string); ok {
			return c
		}
	}
	return ""
}

func TestSessionChangeHTTP_ImpactContactMoveAndCancel(t *testing.T) {
	f := newSCHTTPFixture(t)
	base := "/v1/organizations/" + f.orgID.String()
	sessionURL := base + "/events/" + f.eventID.String() + "/sessions/" + f.sessionID.String()
	newStart := f.start.Add(7 * 24 * time.Hour)

	// ── Dry run: one paying buyer, but nobody for them to answer to ─────────
	impactURL := base + "/sessions/" + f.sessionID.String() + "/change-impact?locale=ru&start_at=" + newStart.Format(time.RFC3339)
	st, body := f.do(http.MethodGet, impactURL, "")
	if st != http.StatusOK {
		t.Fatalf("change-impact: %d %v", st, body)
	}
	if body["orders"] != float64(1) || body["tickets"] != float64(1) || body["arena_orders"] != float64(1) ||
		body["blocked"] != "contact_missing" {
		t.Fatalf("impact = %v, want 1 order / 1 ticket / blocked contact_missing", body)
	}
	if kinds, _ := body["kinds"].([]any); len(kinds) != 1 || kinds[0] != "date" {
		t.Fatalf("kinds = %v, want [date]", body["kinds"])
	}
	contact, _ := body["contact"].(map[string]any)
	if contact["complete"] != false || contact["target_kind"] != "organization" || contact["target_id"] != f.orgID.String() {
		t.Fatalf("contact = %v, want an incomplete organization contact", contact)
	}
	if msg, _ := body["default_message"].(string); !strings.Contains(msg, "мероприяти") {
		t.Fatalf("default_message = %q, want the Russian default", msg)
	}
	// No change proposed: nothing to tell anyone.
	st, body = f.do(http.MethodGet, base+"/sessions/"+f.sessionID.String()+"/change-impact", "")
	if st != http.StatusOK || body["orders"] != float64(0) || body["blocked"] != "" {
		t.Fatalf("empty impact = %d %v", st, body)
	}
	// Another organization's session is invisible.
	if st, _ := f.do(http.MethodGet, "/v1/organizations/"+uuid.NewString()+"/sessions/"+f.sessionID.String()+"/change-impact", ""); st == http.StatusOK {
		t.Fatal("change-impact answered for a foreign organization")
	}

	// ── The move is refused, and nothing is written ────────────────────────
	patch := fmt.Sprintf(`{"start_at":%q,"notice":{"message":"Moved by one week."}}`, newStart.Format(time.RFC3339))
	st, body = f.do(http.MethodPatch, sessionURL, patch)
	if st != http.StatusUnprocessableEntity || scErrCode(body) != "organization.contact_missing" {
		t.Fatalf("PATCH without contact: %d %v, want 422 organization.contact_missing", st, body)
	}
	if !f.sessionStart().Equal(f.start) {
		t.Fatal("the session moved although the PATCH was refused")
	}

	// ── The contact, then the move ─────────────────────────────────────────
	if st, body = f.do(http.MethodPut, base+"/events/"+f.eventID.String()+"/contact", `{"email":"not-an-address"}`); st != http.StatusBadRequest {
		t.Fatalf("invalid contact e-mail: %d %v", st, body)
	}
	st, body = f.do(http.MethodPut, base+"/events/"+f.eventID.String()+"/contact",
		`{"email":"organizer@example.com","phone":"+34 600 000 000","phone_hidden":true}`)
	if st != http.StatusOK {
		t.Fatalf("set contact: %d %v", st, body)
	}
	c, _ := body["contact"].(map[string]any)
	if c["complete"] != true || c["email"] != "organizer@example.com" || c["phone_hidden"] != true || c["source"] != "organization" {
		t.Fatalf("contact after PUT = %v", c)
	}
	st, body = f.do(http.MethodGet, impactURL, "")
	if st != http.StatusOK || body["blocked"] != "" {
		t.Fatalf("impact after the contact: %d %v, want not blocked", st, body)
	}

	st, body = f.do(http.MethodPatch, sessionURL, patch)
	if st != http.StatusOK {
		t.Fatalf("PATCH move: %d %v", st, body)
	}
	change, _ := body["change"].(map[string]any)
	if change == nil || change["orders"] != float64(1) || change["tickets"] != float64(1) || change["queued"] != float64(1) {
		t.Fatalf("change = %v, want 1 order, 1 ticket, 1 letter queued", body["change"])
	}
	if got := f.sessionStart(); !got.Equal(newStart) {
		t.Fatalf("start = %v, want %v", got, newStart)
	}
	// Moving only the start keeps the session's length (2 h).
	var length time.Duration
	var secs float64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT extract(epoch FROM end_at - start_at) FROM sessions WHERE id = $1`, f.sessionID).Scan(&secs); err != nil {
		t.Fatal(err)
	}
	length = time.Duration(secs) * time.Second
	if length != 2*time.Hour {
		t.Fatalf("session length after the move = %v, want 2h", length)
	}
	var jobs int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM worker_jobs WHERE job_type = 'session.change_email' AND payload->>'order_id' = $1`,
		f.orderID.String()).Scan(&jobs)
	if jobs != 1 {
		t.Fatalf("queued letters = %d, want 1", jobs)
	}

	// A message that is too long is refused without writing anything.
	st, body = f.do(http.MethodPatch, sessionURL, fmt.Sprintf(`{"start_at":%q,"notice":{"message":%q}}`,
		newStart.Add(24*time.Hour).Format(time.RFC3339), strings.Repeat("x", 1001)))
	if st != http.StatusUnprocessableEntity || scErrCode(body) != "session.change_message_too_long" {
		t.Fatalf("long message: %d %v", st, body)
	}

	// A capacity-only edit is invisible to buyers: no `change` object.
	st, body = f.do(http.MethodPatch, sessionURL, `{"status":"scheduled"}`)
	if st != http.StatusOK || body["change"] != nil {
		t.Fatalf("no-op PATCH: %d change=%v", st, body["change"])
	}

	// ── Cancelling through DELETE is a cancellation for the buyer ──────────
	st, body = f.do(http.MethodDelete, sessionURL, `{"notice":{"message":"Cancelled, sorry."}}`)
	if st != http.StatusOK {
		t.Fatalf("DELETE: %d %v", st, body)
	}
	change, _ = body["change"].(map[string]any)
	if kinds, _ := change["kinds"].([]any); change == nil || len(kinds) != 1 || kinds[0] != "cancelled" || change["queued"] != float64(1) {
		t.Fatalf("delete change = %v, want cancelled with one letter", body["change"])
	}
}

func TestSessionChangeHTTP_SiteSoldOrderBlocksTheMove(t *testing.T) {
	f := newSCHTTPFixture(t)
	base := "/v1/organizations/" + f.orgID.String()
	if _, err := f.pool.Exec(context.Background(), `UPDATE organizations SET contact_email = 'organizer@example.com' WHERE id = $1`, f.orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO webhook_subscribers (site_url, callback_url, signing_secret, kind, channel_id, active)
		 VALUES ('https://site.example', $2, 'secret', 'bil24_wp', $1, true)`,
		f.channelID, "https://site.example/hook/"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	impactURL := base + "/sessions/" + f.sessionID.String() + "/change-impact?status=cancelled"
	st, body := f.do(http.MethodGet, impactURL, "")
	if st != http.StatusOK || body["blocked"] != "site_route_unsupported" || body["site_orders"] != float64(1) {
		t.Fatalf("impact = %d %v, want blocked site_route_unsupported", st, body)
	}
	sessionURL := base + "/events/" + f.eventID.String() + "/sessions/" + f.sessionID.String()
	st, body = f.do(http.MethodPatch, sessionURL, `{"status":"cancelled"}`)
	if st != http.StatusUnprocessableEntity || scErrCode(body) != "session.change_site_unsupported" {
		t.Fatalf("PATCH cancel: %d %v, want 422 session.change_site_unsupported", st, body)
	}
}

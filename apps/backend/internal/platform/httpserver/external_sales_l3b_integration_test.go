//go:build integration

// external_sales_l3b_integration_test.go — spec 63 §14.1, §14.3, §14.4
// (migration 0220) through the real router: an organizer hands a session to a
// partner box office with POST/PATCH .../sessions, the lock rules answer 409
// session.sales_mode_locked, the session owns no tiers (409
// session.sold_externally), and the public feed shows the partner block with
// sales_state "external", no tiers, and keeps an event whose sessions are all
// external in the list.
//
// Prerequisites: DATABASE_URL against a migrated database.
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// l3bDo is sstDo for a platform superadmin: it adds the X-Admin-Reason header.
func l3bDo(t *testing.T, srv *Server, method, path, token, _, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Admin-Reason", "L3 integration test")
	}
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func l3bErrCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func TestL3b_SessionSalesMode_APIAndFeed(t *testing.T) {
	srv, secret := productionIntegrationServer(t)
	pool := integrationPool(t)
	ctx := context.Background()

	f := newW1A6cFixture(t, ctx, pool)
	q := gen.New(pool)
	user, err := q.InsertUser(ctx, "l3b-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	if _, err := q.InsertMembership(ctx, user.ID, f.orgID, "organizer"); err != nil {
		t.Fatalf("InsertMembership: %v", err)
	}
	// session.create is held by an administrator role, not by the plain
	// membership: grant the platform superadmin role to the caller.
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
		SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, user.ID); err != nil {
		t.Fatalf("grant superadmin: %v", err)
	}
	token, _, err := auth.IssueJWT(secret, user.ID, nil, nil, "arena-api", "arena-api", time.Hour)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	defer func() {
		for _, s := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM reservations WHERE session_id IN (SELECT id FROM sessions WHERE event_id = $1 AND id <> $2)`, []any{f.eventID, f.sessionID}},
			{`DELETE FROM outbox_events WHERE aggregate_id = $1::text`, []any{f.eventID}},
			{`DELETE FROM audit_events WHERE resource_id IN (SELECT id::text FROM sessions WHERE event_id = $1)`, []any{f.eventID}},
			{`DELETE FROM sessions WHERE event_id = $1 AND id <> $2`, []any{f.eventID, f.sessionID}},
			{`UPDATE sessions SET deleted_at = NULL WHERE id = $1`, []any{f.sessionID}},
			{`DELETE FROM memberships WHERE org_id = $1`, []any{f.orgID}},
			{`DELETE FROM user_roles WHERE user_id = $1`, []any{user.ID}},
			{`DELETE FROM users WHERE id = $1`, []any{user.ID}},
		} {
			if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
				t.Logf("cleanup (%.50s): %v", s.sql, err)
			}
		}
		f.cleanup()
	}()

	base := "/v1/organizations/" + f.orgID.String() + "/events/" + f.eventID.String() + "/sessions"
	start := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Hour)
	createBody := func(extra string) string {
		return `{"venue_id":"` + f.venueID.String() + `","start_at":"` + start.Format(time.RFC3339) +
			`","end_at":"` + start.Add(2*time.Hour).Format(time.RFC3339) + `","currency":"EUR"` + extra + `}`
	}
	const goodExternal = `,"sales_mode":"external","external":{"url":"https://www.eventim.de/event/77","seller_name":"Eventim","price_from":39.5,"currency":"EUR"}`

	// The fixture session owns a tier: it cannot be handed to a partner.
	st, out := l3bDo(t, srv, http.MethodPatch, base+"/"+f.sessionID.String(), token, "",
		`{"sales_mode":"external","external":{"url":"https://www.eventim.de/event/1","seller_name":"Eventim"}}`)
	if st != http.StatusConflict || l3bErrCode(out) != "session.sales_mode_locked" {
		t.Fatalf("PATCH to external on a session with a tier = %d %v, want 409 session.sales_mode_locked", st, out)
	}

	// Refused blocks: 422 with the field named, nothing created.
	for name, extra := range map[string]string{
		"http address":      `,"sales_mode":"external","external":{"url":"http://www.eventim.de/x","seller_name":"E"}`,
		"ip address":        `,"sales_mode":"external","external":{"url":"https://203.0.113.5/x","seller_name":"E"}`,
		"platform host":     `,"sales_mode":"external","external":{"url":"https://tickets.arenasoldout.com/x","seller_name":"E"}`,
		"no seller name":    `,"sales_mode":"external","external":{"url":"https://www.eventim.de/x","seller_name":" "}`,
		"price only":        `,"sales_mode":"external","external":{"url":"https://www.eventim.de/x","seller_name":"E","price_from":5}`,
		"no block":          `,"sales_mode":"external"`,
		"block on platform": `,"external":{"url":"https://www.eventim.de/x","seller_name":"E"}`,
	} {
		if st, out := l3bDo(t, srv, http.MethodPost, base, token, "", createBody(extra)); st != http.StatusUnprocessableEntity {
			t.Errorf("%s: POST = %d %v, want 422", name, st, out)
		}
	}
	if st, out := l3bDo(t, srv, http.MethodPost, base, token, "", createBody(`,"sales_mode":"partner"`)); st != http.StatusBadRequest ||
		l3bErrCode(out) != "session.invalid_sales_mode" {
		t.Errorf("unknown mode: POST = %d %v, want 400 session.invalid_sales_mode", st, out)
	}

	// Create a partner-sold session.
	st, out = l3bDo(t, srv, http.MethodPost, base, token, "", createBody(goodExternal))
	if st != http.StatusCreated {
		t.Fatalf("POST external session = %d %v", st, out)
	}
	sess, _ := out["session"].(map[string]any)
	extID, _ := sess["id"].(string)
	block, _ := sess["external"].(map[string]any)
	if sess["sales_mode"] != "external" || block["url"] != "https://www.eventim.de/event/77" ||
		block["seller_name"] != "Eventim" || block["price_from"] != 39.5 || block["currency"] != "EUR" {
		t.Fatalf("created session = %v", sess)
	}

	// It owns no tiers.
	st, out = l3bDo(t, srv, http.MethodPost, base+"/"+extID+"/tiers", token, "",
		`{"name":"General","pricing_mode":"fixed","price_amount":1000,"capacity":10}`)
	if st != http.StatusConflict || l3bErrCode(out) != "session.sold_externally" {
		t.Fatalf("POST tier on an external session = %d %v, want 409 session.sold_externally", st, out)
	}

	// The feed: the partner block, state "external", no tiers.
	feedSession := func(id string) map[string]any {
		t.Helper()
		st, out := l3bDo(t, srv, http.MethodGet, "/v1/public/feeds/"+f.feedToken+"/events/"+f.eventID.String(), "", "", "")
		if st != http.StatusOK {
			t.Fatalf("public feed = %d %v", st, out)
		}
		ev, _ := out["event"].(map[string]any)
		sessions, _ := ev["sessions"].([]any)
		for _, s := range sessions {
			if sm, _ := s.(map[string]any); sm["id"] == id {
				return sm
			}
		}
		t.Fatalf("session %s missing from the feed: %v", id, out)
		return nil
	}
	fs := feedSession(extID)
	fblock, _ := fs["external"].(map[string]any)
	if fs["sales_mode"] != "external" || fs["sales_state"] != "external" || fblock["url"] != "https://www.eventim.de/event/77" ||
		fblock["seller_name"] != "Eventim" || fblock["price_from"] != 39.5 || fblock["currency"] != "EUR" {
		t.Fatalf("feed session = %v", fs)
	}
	if tiers, _ := fs["tiers"].([]any); len(tiers) != 0 {
		t.Fatalf("feed tiers of an external session = %v, want []", tiers)
	}
	if plat := feedSession(f.sessionID.String()); plat["sales_mode"] != "platform" || plat["external"] != nil {
		t.Fatalf("the platform session's feed block = %v", plat)
	}

	// An event whose sessions are all external stays in the feed list.
	if _, err := pool.Exec(ctx, `UPDATE sessions SET deleted_at = now() WHERE id = $1`, f.sessionID); err != nil {
		t.Fatalf("withdraw the platform session: %v", err)
	}
	st, out = l3bDo(t, srv, http.MethodGet, "/v1/public/feeds/"+f.feedToken+"/events", "", "", "")
	if st != http.StatusOK {
		t.Fatalf("feed list = %d %v", st, out)
	}
	listed := false
	events, _ := out["events"].([]any)
	for _, e := range events {
		if em, _ := e.(map[string]any); em["id"] == f.eventID.String() {
			listed = true
		}
	}
	if !listed {
		t.Fatalf("an event with only external sessions is missing from the feed list: %v", out)
	}

	// Back to the platform while it owns nothing; then to a partner again.
	st, out = l3bDo(t, srv, http.MethodPatch, base+"/"+extID, token, "", `{"sales_mode":"platform"}`)
	if sess, _ := out["session"].(map[string]any); st != http.StatusOK || sess["sales_mode"] != "platform" || sess["external"] != nil {
		t.Fatalf("PATCH to platform = %d %v", st, out)
	}
	st, out = l3bDo(t, srv, http.MethodPatch, base+"/"+extID, token, "",
		`{"sales_mode":"external","external":{"url":"https://tickets.example.cz/x","seller_name":"Ticketportal"}}`)
	if sess, _ := out["session"].(map[string]any); st != http.StatusOK || sess["sales_mode"] != "external" {
		t.Fatalf("PATCH back to external = %d %v", st, out)
	}

	// An order or a hold blocks the hand-over: put a hold on a platform session.
	st, out = l3bDo(t, srv, http.MethodPatch, base+"/"+extID, token, "", `{"sales_mode":"platform"}`)
	if st != http.StatusOK {
		t.Fatalf("PATCH to platform (2) = %d %v", st, out)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at)
	  VALUES ($1, $2, $3, $4, 1, 'active', now() + interval '10 minutes')`,
		uuid.New(), f.orgID, f.channelID, extID); err != nil {
		t.Fatalf("insert a hold: %v", err)
	}
	st, out = l3bDo(t, srv, http.MethodPatch, base+"/"+extID, token, "",
		`{"sales_mode":"external","external":{"url":"https://tickets.example.cz/x","seller_name":"Ticketportal"}}`)
	if st != http.StatusConflict || l3bErrCode(out) != "session.sales_mode_locked" {
		t.Fatalf("PATCH to external with a hold = %d %v, want 409 session.sales_mode_locked", st, out)
	}
}

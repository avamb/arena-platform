//go:build integration

// external_sales_guards_integration_test.go — spec 63 §14.2 (migration 0218)
// through the real router: a session a partner box office sells takes no
// seating plan (409 session.sold_externally on the bind) and no staff
// invitation (409 session.sold_externally on the issue), and the organizer's
// events list reports an event sold only by partners as "external", never as
// "sold_out".
//
// Prerequisites: DATABASE_URL against a migrated database.
package httpserver

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

func TestL3Guards_ExternalSession_BindInvitationEventsList(t *testing.T) {
	srv, secret := productionIntegrationServer(t)
	pool := integrationPool(t)
	ctx := context.Background()

	f := newW1A6cFixture(t, ctx, pool)
	q := gen.New(pool)
	user, err := q.InsertUser(ctx, "l3g-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	if _, err := q.InsertMembership(ctx, user.ID, f.orgID, "organizer"); err != nil {
		t.Fatalf("InsertMembership: %v", err)
	}
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
	body := `{"venue_id":"` + f.venueID.String() + `","start_at":"` + start.Format(time.RFC3339) +
		`","end_at":"` + start.Add(2*time.Hour).Format(time.RFC3339) + `","currency":"EUR"` +
		`,"sales_mode":"external","external":{"url":"HTTPS://www.eventim.de/event/91","seller_name":"Eventim"}}`
	st, out := l3bDo(t, srv, http.MethodPost, base, token, "", body)
	if st != http.StatusCreated {
		t.Fatalf("POST external session = %d %v", st, out)
	}
	sess, _ := out["session"].(map[string]any)
	extID, _ := sess["id"].(string)
	if block, _ := sess["external"].(map[string]any); block["url"] != "https://www.eventim.de/event/91" {
		t.Fatalf("an upper-case scheme must be stored in lower case, got %v", sess["external"])
	}

	// D1: a seating plan is never bound to it, and nothing is written.
	st, out = l3bDo(t, srv, http.MethodPost, base+"/"+extID+"/seating", token, "",
		`{"seating_plan_version_id":"`+uuid.NewString()+`","admission_mode":"assigned_seats","category_tier_map":{},"auto_create_tiers":true}`)
	if st != http.StatusConflict || l3bErrCode(out) != "session.sold_externally" {
		t.Fatalf("bind on an external session = %d %v, want 409 session.sold_externally", st, out)
	}
	var seats, tiers int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM session_seats WHERE session_id = $1),
		(SELECT count(*) FROM ticket_tiers WHERE session_id = $1)`, extID).Scan(&seats, &tiers); err != nil {
		t.Fatalf("count places: %v", err)
	}
	if seats != 0 || tiers != 0 {
		t.Fatalf("after the refused bind: %d seats, %d tiers, want none", seats, tiers)
	}

	// D1: no staff invitation either.
	inv := `{"request_id":"` + uuid.NewString() + `","ga":[{"tier_id":"` + uuid.NewString() + `","qty":1}],` +
		`"guest":{"name":"Guest","email":"guest@example.test"},"delivery":"guest_email"}`
	st, out = l3bDo(t, srv, http.MethodPost, "/v1/organizations/"+f.orgID.String()+"/sessions/"+extID+"/invitations", token, "", inv)
	if st != http.StatusConflict || l3bErrCode(out) != "session.sold_externally" {
		t.Fatalf("invitation on an external session = %d %v, want 409 session.sold_externally", st, out)
	}
	var orders int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE session_id = $1`, extID).Scan(&orders); err != nil {
		t.Logf("count orders: %v", err)
	} else if orders != 0 {
		t.Fatalf("the refused invitation left %d orders", orders)
	}

	// D2: the events list.
	eventState := func() string {
		t.Helper()
		st, out := l3bDo(t, srv, http.MethodGet, "/v1/organizations/"+f.orgID.String()+"/events", token, "", "")
		if st != http.StatusOK {
			t.Fatalf("events list = %d %v", st, out)
		}
		events, _ := out["events"].([]any)
		for _, e := range events {
			if em, _ := e.(map[string]any); em["id"] == f.eventID.String() {
				s, _ := em["sales_state"].(string)
				return s
			}
		}
		t.Fatalf("event %s missing from the list: %v", f.eventID, out)
		return ""
	}
	// Mixed: the platform session decides.
	if got := eventState(); got == "external" {
		t.Fatalf("a mixed event reports %q, want the platform session's state", got)
	}
	// Only the partner session left: external, not sold out.
	if _, err := pool.Exec(ctx, `UPDATE sessions SET deleted_at = now() WHERE id = $1`, f.sessionID); err != nil {
		t.Fatalf("withdraw the platform session: %v", err)
	}
	if got := eventState(); got != "external" {
		t.Fatalf("an event sold only by a partner reports %q, want external", got)
	}
}

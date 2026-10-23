//go:build integration

// seat_batches_integration_test.go — task S1c through the REAL router:
// the four seat-batch routes, the two PATCH routes that now write batches,
// and organization isolation (TestSeatBatches_StayInsideTheOrganization
// covers the orgIsolationAuditedSafe entries of the batch routes).
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

type seatBatchFixture struct {
	f                            *prom0113Fixture
	org, other                   uuid.UUID
	event, session, tier1, tier2 uuid.UUID
	manager, agent, outsider     string
	users                        []uuid.UUID
}

func newSeatBatchFixture(t *testing.T) *seatBatchFixture {
	t.Helper()
	_, secret := productionIntegrationServer(t)
	f := newProm0113Fixture(t)
	ctx := context.Background()
	x := &seatBatchFixture{f: f, org: f.org(t, "S1c"), other: f.org(t, "S1cOther"),
		event: uuid.New(), session: uuid.New(), tier1: uuid.New(), tier2: uuid.New()}
	member := func(org uuid.UUID, role string) string {
		t.Helper()
		user, err := f.q.InsertUser(ctx, "s1c-"+role+"-"+uuid.NewString()+"@example.test", "x", "en")
		if err != nil {
			t.Fatalf("InsertUser: %v", err)
		}
		if _, err := f.q.InsertMembership(ctx, user.ID, org, role); err != nil {
			t.Fatalf("InsertMembership: %v", err)
		}
		x.users = append(x.users, user.ID)
		tok, _, err := auth.IssueJWT(secret, user.ID, nil, nil, "arena-api", "arena-api", time.Hour)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		return tok
	}
	x.manager = member(x.org, "organizer")
	x.agent = member(x.org, "agent")
	x.outsider = member(x.other, "organizer")

	venue, plan, version := uuid.New(), uuid.New(), uuid.New()
	sfx := uuid.NewString()[:8]
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.q.DB().Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		for _, s := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM session_seat_batches WHERE session_id = $1`, []any{x.session}},
			{`DELETE FROM session_seats WHERE session_id = $1`, []any{x.session}},
			{`DELETE FROM inventory_ledger WHERE session_id = $1`, []any{x.session}},
			{`DELETE FROM ticket_tiers WHERE session_id = $1`, []any{x.session}},
			{`DELETE FROM sessions WHERE id = $1`, []any{x.session}},
			{`DELETE FROM seating_plan_versions WHERE id = $1`, []any{version}},
			{`DELETE FROM seating_plans WHERE id = $1`, []any{plan}},
			{`DELETE FROM events WHERE id = $1`, []any{x.event}},
			{`DELETE FROM venues WHERE id = $1`, []any{venue}},
			{`DELETE FROM memberships WHERE user_id = ANY($1::uuid[])`, []any{x.users}},
			{`DELETE FROM organizations WHERE id = ANY($1::uuid[])`, []any{[]uuid.UUID{x.org, x.other}}},
			{`DELETE FROM users WHERE id = ANY($1::uuid[])`, []any{x.users}},
		} {
			if _, err := f.q.DB().Exec(c, s.sql, s.args...); err != nil {
				t.Logf("s1c cleanup %q: %v", s.sql, err)
			}
		}
	})
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, venue, x.org, "S1c Hall "+sfx)
	exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, x.event, x.org, "S1c Event "+sfx)
	exec(`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status) VALUES ($1, $2, $3, $4, 'assigned_seats', 'active')`, plan, venue, x.org, "S1c Plan "+sfx)
	exec(`INSERT INTO seating_plan_versions (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated, locked_at)
	      VALUES ($1, $2, 1, '{}'::jsonb, $3, 4, now())`, version, plan, "s1c-"+sfx)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, seating_plan_version_id, currency, currency_source)
	      VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 4, 'scheduled', 'assigned_seats', $4, 'EUR', 'override')`,
		x.session, x.event, venue, version)
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, unit_seq, is_open)
	      VALUES ($1, $2, 'Parter', 'fixed', 1000, 'EUR', 4, 1, true)`, x.tier1, x.session)
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, unit_seq, is_open)
	      VALUES ($1, $2, 'Balcony', 'fixed', 2000, 'EUR', 4, 2, true)`, x.tier2, x.session)
	exec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, 4)`, x.session)
	exec(`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
	      SELECT $1, 'S|A|' || gs::text, 'S', 'A', gs::text, $2, CASE WHEN gs = 4 THEN 'sold' ELSE 'available' END, 'seat'
	      FROM generate_series(1, 4) gs`, x.session, x.tier1)
	return x
}

func (x *seatBatchFixture) base(org uuid.UUID) string {
	return "/v1/organizations/" + org.String() + "/sessions/" + x.session.String()
}

func TestSeatBatches_ApplyListHistoryRevert(t *testing.T) {
	x := newSeatBatchFixture(t)
	f := x.f
	base := x.base(x.org)

	body := fmt.Sprintf(`{"kind":"paint","changes":[{"seat_key":"S|A|1","tier_id":%q},{"seat_key":"S|A|4","tier_id":%q}]}`, x.tier2, x.tier2)
	code, resp := f.do(t, http.MethodPost, base+"/seat-batches", x.manager, body)
	if code != http.StatusCreated {
		t.Fatalf("apply = %d %v", code, resp)
	}
	batchID, _ := resp["id"].(string)
	if ch, _ := resp["changes"].([]any); len(ch) != 1 {
		t.Fatalf("changes = %v", resp["changes"])
	}
	sk, _ := resp["skipped"].([]any)
	if len(sk) != 1 || sk[0].(map[string]any)["reason"] != "sold" {
		t.Fatalf("skipped = %v", resp["skipped"])
	}

	if code, resp := f.do(t, http.MethodPost, base+"/seat-batches", x.manager,
		`{"kind":"paint","changes":[{"seat_key":"S|A|1","tier_id":"`+x.tier1.String()+`"}],"new_tiers":[{"name":"VIP"}]}`); code != http.StatusUnprocessableEntity || prom0113Code(resp) != "seat_batch.new_tiers_unsupported" {
		t.Fatalf("new_tiers = %d %v", code, resp)
	}
	if code, resp := f.do(t, http.MethodPost, base+"/seat-batches", x.manager,
		`{"kind":"paint","changes":[{"seat_key":"S|A|1","tier_id":"`+uuid.NewString()+`"}]}`); code != http.StatusUnprocessableEntity || prom0113Code(resp) != "seat_batch.tier_not_in_session" {
		t.Fatalf("foreign tier = %d %v", code, resp)
	}
	if code, _ := f.do(t, http.MethodPost, base+"/seat-batches", x.agent, body); code != http.StatusForbidden {
		t.Fatalf("agent apply = %d", code)
	}

	// The PATCH routes write batches too.
	if code, resp := f.do(t, http.MethodPatch, "/v1/event-sessions/"+x.session.String()+"/seats/category", x.manager,
		fmt.Sprintf(`{"seat_keys":["S|A|2"],"tier_id":%q}`, x.tier2)); code != http.StatusOK {
		t.Fatalf("category patch = %d %v", code, resp)
	}
	if code, resp := f.do(t, http.MethodPatch, "/v1/event-sessions/"+x.session.String()+"/seats/category", x.manager,
		fmt.Sprintf(`{"seat_keys":["S|A|4"],"tier_id":%q}`, x.tier2)); code != http.StatusConflict || prom0113Code(resp) != "seating.category_reassign_conflict" {
		t.Fatalf("category patch on sold seat = %d %v", code, resp)
	}
	if code, resp := f.do(t, http.MethodPatch, "/v1/organizations/"+x.org.String()+"/events/"+x.event.String()+"/sessions/"+x.session.String()+"/seats",
		x.manager, `{"action":"block","seat_keys":["S|A|1","S|A|4"]}`); code != http.StatusOK {
		t.Fatalf("block = %d %v", code, resp)
	}

	code, resp = f.do(t, http.MethodGet, base+"/seat-batches?limit=10", x.manager, "")
	if code != http.StatusOK || resp["total"].(float64) != 3 {
		t.Fatalf("list = %d %v", code, resp)
	}
	items := resp["items"].([]any)
	kinds := []string{}
	for _, it := range items {
		kinds = append(kinds, it.(map[string]any)["kind"].(string))
	}
	if fmt.Sprint(kinds) != "[close paint paint]" {
		t.Fatalf("kinds newest first = %v", kinds)
	}

	code, resp = f.do(t, http.MethodGet, base+"/seats/"+url.PathEscape("S|A|1")+"/history", x.manager, "")
	if code != http.StatusOK || resp["seat_key"] != "S|A|1" || resp["total"].(float64) != 2 {
		t.Fatalf("history = %d %v", code, resp)
	}

	// Seat 1 was closed after the paint, so the paint has nothing to put
	// back: refused, nothing written. Reverting the close first unblocks it.
	code, resp = f.do(t, http.MethodPost, base+"/seat-batches/"+batchID+"/revert", x.manager, "")
	if code != http.StatusConflict || prom0113Code(resp) != "seat_batch.nothing_to_revert" {
		t.Fatalf("revert before the close is undone = %d %v", code, resp)
	}
	closeID := items[0].(map[string]any)["id"].(string)
	if code, resp := f.do(t, http.MethodPost, base+"/seat-batches/"+closeID+"/revert", x.manager, ""); code != http.StatusCreated {
		t.Fatalf("revert of the close = %d %v", code, resp)
	}
	code, resp = f.do(t, http.MethodPost, base+"/seat-batches/"+batchID+"/revert", x.manager, "")
	if code != http.StatusCreated || resp["kind"] != "revert" || resp["reverts_batch_id"] != batchID {
		t.Fatalf("revert = %d %v", code, resp)
	}
	if ch, _ := resp["changes"].([]any); len(ch) != 1 {
		t.Fatalf("revert changes = %v", resp["changes"])
	}
	if code, resp := f.do(t, http.MethodPost, base+"/seat-batches/"+batchID+"/revert", x.manager, ""); code != http.StatusConflict || prom0113Code(resp) != "seat_batch.already_reverted" {
		t.Fatalf("second revert = %d %v", code, resp)
	}
}

func TestSeatBatches_StayInsideTheOrganization(t *testing.T) {
	x := newSeatBatchFixture(t)
	f := x.f
	body := `{"kind":"close","changes":[{"seat_key":"S|A|1","status":"unavailable"}]}`

	code, resp := f.do(t, http.MethodPost, x.base(x.org)+"/seat-batches", x.manager, body)
	if code != http.StatusCreated {
		t.Fatalf("own apply = %d %v", code, resp)
	}
	batchID := resp["id"].(string)

	// The other organization's owner names A's session under its OWN org.
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, x.base(x.other) + "/seat-batches", body},
		{http.MethodGet, x.base(x.other) + "/seat-batches", ""},
		{http.MethodPost, x.base(x.other) + "/seat-batches/" + batchID + "/revert", ""},
		{http.MethodGet, x.base(x.other) + "/seats/" + url.PathEscape("S|A|1") + "/history", ""},
	} {
		if code, resp := f.do(t, c.method, c.path, x.outsider, c.body); code != http.StatusNotFound || prom0113Code(resp) != "session.not_found" {
			t.Fatalf("%s %s = %d %v, want 404 session.not_found", c.method, c.path, code, resp)
		}
	}
	// ... and under A's org it is not a member.
	if code, _ := f.do(t, http.MethodGet, x.base(x.org)+"/seat-batches", x.outsider, ""); code != http.StatusForbidden {
		t.Fatalf("outsider in A = %d", code)
	}
	// A batch id of the session under another session is a 404 too.
	if code, resp := f.do(t, http.MethodPost, x.base(x.org)+"/seat-batches/"+uuid.NewString()+"/revert", x.manager, ""); code != http.StatusNotFound || prom0113Code(resp) != "seat_batch.not_found" {
		t.Fatalf("unknown batch = %d %v", code, resp)
	}
	var n int
	if err := f.q.DB().QueryRow(context.Background(), `SELECT count(*) FROM session_seat_batches WHERE session_id = $1`, x.session).Scan(&n); err != nil || n != 1 {
		t.Fatalf("batches after the attacks = %d (%v), want 1", n, err)
	}
	var status string
	if err := f.q.DB().QueryRow(context.Background(), `SELECT status FROM session_seats WHERE session_id = $1 AND seat_key = 'S|A|1'`, x.session).Scan(&status); err != nil || status != "unavailable" {
		t.Fatalf("seat 1 = %s (%v)", status, err)
	}
}

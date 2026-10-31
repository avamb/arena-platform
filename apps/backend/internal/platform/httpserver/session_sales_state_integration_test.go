//go:build integration

// session_sales_state_integration_test.go — the session's sales state
// (migration 0182) through the real router against a live database: a
// manager (membership role organizer, no roles claim) reads and switches it,
// the switch is journalled with its client and a v1.session.updated outbox
// row, a repeat writes nothing, and while the state is set the public feed
// reports it and the widget's quote is refused; "open" brings the sale back
// with the places it had.
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

func sstDo(t *testing.T, srv *Server, method, path, token, channel, body string) (int, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if channel != "" {
		req.Header.Set("X-Client-Channel", channel)
	}
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestSessionSalesState_ManagerSwitchesEveryChannel(t *testing.T) {
	srv, secret := productionIntegrationServer(t)
	pool := integrationPool(t)
	ctx := context.Background()

	f := newW1A6cFixture(t, ctx, pool)
	q := gen.New(pool)
	member := func(role string) (uuid.UUID, string) {
		t.Helper()
		user, err := q.InsertUser(ctx, "sst-"+role+"-"+uuid.NewString()+"@example.test", "x", "en")
		if err != nil {
			t.Fatalf("InsertUser: %v", err)
		}
		if _, err := q.InsertMembership(ctx, user.ID, f.orgID, role); err != nil {
			t.Fatalf("InsertMembership(%s): %v", role, err)
		}
		tok, _, err := auth.IssueJWT(secret, user.ID, nil, nil, "arena-api", "arena-api", time.Hour)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		return user.ID, tok
	}
	managerID, manager := member("organizer")
	agentID, agent := member("agent")
	defer func() {
		for _, s := range []struct {
			sql string
			arg any
		}{
			{`DELETE FROM outbox_events WHERE aggregate_id = $1::text`, f.eventID},
			{`DELETE FROM audit_events WHERE resource_id = $1::text`, f.sessionID},
			{`DELETE FROM session_sales_state_changes WHERE session_id = $1`, f.sessionID},
			{`DELETE FROM session_sales_states WHERE session_id = $1`, f.sessionID},
			{`DELETE FROM memberships WHERE org_id = $1`, f.orgID},
			{`DELETE FROM users WHERE id = $1`, managerID},
			{`DELETE FROM users WHERE id = $1`, agentID},
		} {
			if _, err := pool.Exec(ctx, s.sql, s.arg); err != nil {
				t.Logf("cleanup (%.50s): %v", s.sql, err)
			}
		}
		f.cleanup()
	}()

	path := "/v1/organizations/" + f.orgID.String() + "/sessions/" + f.sessionID.String() + "/sales-state"
	quotePath := "/v1/public/feeds/" + f.feedToken + "/checkout/quote"
	quote := `{"session_id":"` + f.sessionID.String() + `","ga_items":[{"tier_id":"` + f.tierID.String() + `","quantity":1}]}`
	feedState := func() (string, *int) {
		t.Helper()
		st, out := sstDo(t, srv, http.MethodGet, "/v1/public/feeds/"+f.feedToken+"/events/"+f.eventID.String(), "", "", "")
		if st != http.StatusOK {
			t.Fatalf("public feed = %d %v", st, out)
		}
		ev, _ := out["event"].(map[string]any)
		sessions, _ := ev["sessions"].([]any)
		for _, s := range sessions {
			sm, _ := s.(map[string]any)
			if sm["id"] != f.sessionID.String() {
				continue
			}
			state, _ := sm["sales_state"].(string)
			tiers, _ := sm["tiers"].([]any)
			if len(tiers) == 0 {
				return state, nil
			}
			tm, _ := tiers[0].(map[string]any)
			if a, ok := tm["available"].(float64); ok {
				n := int(a)
				return state, &n
			}
			return state, nil
		}
		t.Fatalf("session missing from the feed: %v", out)
		return "", nil
	}
	journalRows := func() (n int) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_sales_state_changes WHERE session_id = $1`, f.sessionID).Scan(&n); err != nil {
			t.Fatalf("count journal: %v", err)
		}
		return n
	}

	// Baseline: open, 5 places, no change yet.
	st, out := sstDo(t, srv, http.MethodGet, path, manager, "", "")
	if st != http.StatusOK {
		t.Fatalf("GET sales-state = %d %v", st, out)
	}
	if out["effective"] != "open" || out["manual"] != nil || out["free_places"] != float64(5) || out["changed_by"] != nil {
		t.Fatalf("baseline = %v, want open, manual null, 5 free, no change", out)
	}

	// An agent may read events but not switch sales.
	if st, out := sstDo(t, srv, http.MethodPut, path, agent, "", `{"state":"sold_out"}`); st != http.StatusForbidden {
		t.Errorf("agent PUT = %d %v, want 403", st, out)
	}
	if st, out := sstDo(t, srv, http.MethodPut, path, manager, "", `{"state":"soldout"}`); st != http.StatusBadRequest {
		t.Errorf("unknown state = %d %v, want 400", st, out)
	}
	other := "/v1/organizations/" + f.orgID.String() + "/sessions/" + uuid.NewString() + "/sales-state"
	if st, out := sstDo(t, srv, http.MethodPut, other, manager, "", `{"state":"sold_out"}`); st != http.StatusNotFound {
		t.Errorf("unknown session = %d %v, want 404", st, out)
	}

	// The manager closes the sale from the Telegram bot.
	st, out = sstDo(t, srv, http.MethodPut, path, manager, "telegram_bot", `{"state":"sold_out"}`)
	if st != http.StatusOK {
		t.Fatalf("PUT sold_out = %d %v", st, out)
	}
	if out["changed"] != true || out["manual"] != "sold_out" || out["effective"] != "sold_out" {
		t.Errorf("PUT sold_out answer = %v", out)
	}
	if by, _ := out["changed_by"].(map[string]any); by["role"] != "organizer" || by["source"] != "telegram_bot" || by["type"] != "user" {
		t.Errorf("changed_by = %v, want the manager through telegram_bot", out["changed_by"])
	}
	if out["free_places"] != float64(5) {
		t.Errorf("free_places = %v, want 5: the state never rewrites the places", out["free_places"])
	}
	var outboxRows int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM outbox_events
		WHERE aggregate_id = $1::text AND event_type = 'v1.session.updated'
		  AND payload->'session_ids' ? $2`, f.eventID, f.sessionID.String()).Scan(&outboxRows); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if outboxRows != 1 {
		t.Errorf("v1.session.updated rows = %d, want 1 (the sites must be told)", outboxRows)
	}

	// A repeated press changes nothing.
	st, out = sstDo(t, srv, http.MethodPut, path, manager, "telegram_bot", `{"state":"sold_out"}`)
	if st != http.StatusOK || out["changed"] != false {
		t.Errorf("repeat PUT = %d %v, want 200 changed=false", st, out)
	}
	if n := journalRows(); n != 1 {
		t.Errorf("journal rows = %d after a repeat, want 1", n)
	}

	// Every channel follows: the feed shows the state with no place on offer
	// and the widget's quote is refused with the state named.
	if state, avail := feedState(); state != "sold_out" || avail == nil || *avail != 0 {
		t.Errorf("feed = %q available %v, want sold_out with 0", state, avail)
	}
	st, out = sstDo(t, srv, http.MethodPost, quotePath, "", "", quote)
	if st != http.StatusConflict {
		t.Fatalf("quote while sold out = %d %v, want 409", st, out)
	}
	if e, _ := out["error"].(map[string]any); e["code"] != "session.sold_out" {
		t.Errorf("quote error = %v, want session.sold_out", out["error"])
	}

	// "Coming soon" refuses with its own code.
	if st, out := sstDo(t, srv, http.MethodPut, path, manager, "event_center_web", `{"state":"soon"}`); st != http.StatusOK || out["effective"] != "soon" {
		t.Fatalf("PUT soon = %d %v", st, out)
	}
	st, out = sstDo(t, srv, http.MethodPost, quotePath, "", "", quote)
	if e, _ := out["error"].(map[string]any); st != http.StatusConflict || e["code"] != "session.sales_not_started" {
		t.Errorf("quote while soon = %d %v, want 409 session.sales_not_started", st, out)
	}

	// Open again: the sale comes back with the places it had.
	if st, out := sstDo(t, srv, http.MethodPut, path, manager, "", `{"state":"open"}`); st != http.StatusOK || out["manual"] != nil || out["effective"] != "open" {
		t.Fatalf("PUT open = %d %v", st, out)
	}
	if state, avail := feedState(); state != "open" || avail == nil || *avail != 5 {
		t.Errorf("feed after open = %q available %v, want open with 5", state, avail)
	}
	if st, out := sstDo(t, srv, http.MethodPost, quotePath, "", "", quote); st != http.StatusOK {
		t.Errorf("quote after open = %d %v, want 200", st, out)
	}

	// The journal, newest first.
	st, out = sstDo(t, srv, http.MethodGet, path+"/history", manager, "", "")
	if st != http.StatusOK {
		t.Fatalf("history = %d %v", st, out)
	}
	items, _ := out["items"].([]any)
	want := [][2]string{{"soon", "open"}, {"sold_out", "soon"}, {"open", "sold_out"}}
	if len(items) != len(want) {
		t.Fatalf("history items = %d, want %d: %v", len(items), len(want), items)
	}
	for i, w := range want {
		it, _ := items[i].(map[string]any)
		if it["old_state"] != w[0] || it["new_state"] != w[1] {
			t.Errorf("history[%d] = %v -> %v, want %s -> %s", i, it["old_state"], it["new_state"], w[0], w[1])
		}
		if it["site_sync"] != "none" {
			t.Errorf("history[%d] site_sync = %v, want none (no WordPress site sells this event)", i, it["site_sync"])
		}
	}
}

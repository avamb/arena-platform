//go:build integration

// control_session_integration_test.go — MA-40 (migration 0228): a session for
// entry control only (sessions.sales_mode = 'control_only') does not exist for
// the gateway, and an ordinary session next to it is unchanged:
//
//   - GET_ALL_ACTIONS leaves out the control session, its action, its name and
//     its venue, and still lists the harness event;
//   - GET_SEAT_LIST answers "not found";
//   - RESERVATION on it answers 101 and takes no hold.
//
// Every check runs twice: on the natural state (the container event is a
// draft) and on a FORCED state, where the invariant of the container is broken
// with a trigger-free write (published, listed, even with a category) so that
// only the sales_mode filters of the queries and gates stand in the way. A
// positive control flips the very same row to a platform session and expects
// the gateway to show it, which proves the checks can see a leak.
package compat_bil24_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
)

// ctlSetSalesMode and ctlForce write with a connection that fires no trigger:
// the guards of migration 0228 would refuse both. The connection is closed,
// never returned to the pool, so the replica role cannot reach another test.
func ctlReplicaExec(t *testing.T, st *harnessState, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	pc, err := st.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	conn := pc.Hijack()
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `SET session_replication_role = replica`); err != nil {
		t.Fatalf("cannot switch triggers off (needs a superuser): %v", err)
	}
	if _, err := conn.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec (%.80s): %v", sql, err)
	}
}

func TestCompatBil24_ControlSession_NotListedNotSold(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	ctx := context.Background()
	sfx := uuid.NewString()[:8]

	// The control session lives in its own container event and its own venue,
	// whose name no other row carries.
	venueID, eventID, sessionID := uuid.New(), uuid.New(), uuid.New()
	venueName := "CTRLVENUE-" + sfx
	title := "CTRLTITLE-" + sfx
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := st.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec (%.80s): %v", sql, err)
		}
	}
	exec(`INSERT INTO venues (id, org_id, city_id, name, country, timezone)
	      SELECT $1, org_id, city_id, $2, country, timezone FROM venues WHERE id = $3`, venueID, venueName, st.VenueID)
	exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'private')`, eventID, st.OrgID, title)
	start := time.Now().Add(25 * 24 * time.Hour).UTC().Truncate(time.Second)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status,
	          admission_mode, currency, currency_source, sales_mode)
	      VALUES ($1, $2, $3, $4, $5, 50, 'scheduled', 'general_admission', 'CZK', 'derived', 'control_only')`,
		sessionID, eventID, venueID, start, start.Add(3*time.Hour))
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = st.Pool.Exec(bg, `DELETE FROM reservations WHERE session_id = $1`, sessionID)
		_, _ = st.Pool.Exec(bg, `DELETE FROM inventory_ledger WHERE session_id = $1`, sessionID)
		_, _ = st.Pool.Exec(bg, `DELETE FROM ticket_tiers WHERE session_id = $1`, sessionID)
		_, _ = st.Pool.Exec(bg, `DELETE FROM sessions WHERE id = $1`, sessionID)
		_, _ = st.Pool.Exec(bg, `DELETE FROM events WHERE id = $1`, eventID)
		_, _ = st.Pool.Exec(bg, `DELETE FROM venues WHERE id = $1`, venueID)
	})

	ctlActionEventID := mustActionEventID(t, st, sessionID.String())
	ctlActionID, err := compatids.Ensure(ctx, st.Pool, compatids.KindAction, eventID)
	if err != nil {
		t.Fatalf("compatids.Ensure(action): %v", err)
	}
	gaActionEventID := mustActionEventID(t, st, st.GAsessID)
	actionID, err := compatids.Ensure(ctx, st.Pool, compatids.KindAction, uuid.MustParse(st.EventID))
	if err != nil {
		t.Fatalf("compatids.Ensure(harness action): %v", err)
	}
	gaTierWireID := sc6TierWireID(t, st, sc2EarlyBirdTierID(t, st))

	// actionSessions[actionId] is the number of sessions the action lists.
	var actionSessions map[int64]int
	catalog := func() (actions, actionEvents map[int64]bool, raw string) {
		t.Helper()
		resp := postBil24(t, base, map[string]any{
			"command": "GET_ALL_ACTIONS", "fid": st.ChannelFID, "token": st.ChannelToken, "locale": "en-US",
		})
		if code := numberField(t, resp, "resultCode"); code != 0 {
			t.Fatalf("GET_ALL_ACTIONS resultCode = %v (description %v)", code, resp["description"])
		}
		b, _ := json.Marshal(resp)
		actions, actionEvents = map[int64]bool{}, map[int64]bool{}
		actionSessions = map[int64]int{}
		for _, a := range sc1Objects(t, resp, "actionList") {
			evs, _ := a["actionEventList"].([]any)
			if id, ok := a["actionId"].(float64); ok {
				actions[int64(id)] = true
				actionSessions[int64(id)] = len(evs)
			}
			for _, e := range evs {
				if em, _ := e.(map[string]any); em != nil {
					if id, ok := em["actionEventId"].(float64); ok {
						actionEvents[int64(id)] = true
					}
				}
			}
		}
		return actions, actionEvents, string(b)
	}
	holds := func(session string) int {
		t.Helper()
		var n int
		if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM reservations WHERE session_id = $1`, session).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// published is true on the forced state, where the container event itself
	// is (illegally) published: the event is then listed like any event with no
	// session on sale (an action with an EMPTY actionEventList), which is the
	// pre-existing answer for an event without sessions and not a leak of the
	// session. On the natural state (a draft container) not even the action
	// may appear.
	check := func(t *testing.T, published bool) {
		t.Helper()
		actions, events, raw := catalog()
		if !actions[actionID] || !events[gaActionEventID] {
			t.Errorf("the harness event is missing from GET_ALL_ACTIONS: actions=%v events=%v", actions, events)
		}
		if events[ctlActionEventID] {
			t.Errorf("GET_ALL_ACTIONS lists the control session as actionEvent %d: %.1500s", ctlActionEventID, raw)
		}
		if actions[ctlActionID] && (!published || actionSessions[ctlActionID] != 0) {
			t.Errorf("GET_ALL_ACTIONS lists the control event as action %d with %d sessions (published container: %v)",
				ctlActionID, actionSessions[ctlActionID], published)
		}
		if strings.Contains(raw, venueName) {
			t.Errorf("GET_ALL_ACTIONS shows the control session's venue %q: %.1500s", venueName, raw)
		}
		if published && actions[ctlActionID] {
			t.Logf("a published container with no session on sale is listed as an empty action (name %q)", title)
		}

		seatList := postBil24(t, base, map[string]any{
			"command": "GET_SEAT_LIST", "fid": st.ChannelFID, "token": st.ChannelToken, "locale": "en-US",
			"actionEventId": ctlActionEventID,
		})
		if code := numberField(t, seatList, "resultCode"); code == 0 {
			t.Errorf("GET_SEAT_LIST of the control session answered 0, want not found")
		}

		// RESERVATION: first with the harness category (it belongs to another
		// session), then, where a category of the control session itself
		// exists, with that one.
		before := holds(sessionID.String())
		sess, user := createGatewayUser(t, base, st, "harness-ctl-"+uuid.NewString()[:8]+"@example.test")
		for name, tierWire := range map[string]int64{"harness category": gaTierWireID} {
			resp := postBil24(t, base, map[string]any{
				"command": "RESERVATION", "fid": st.ChannelFID, "token": st.ChannelToken, "locale": "en-US",
				"type": "RESERVE", "userId": user, "sessionId": sess, "actionEventId": ctlActionEventID,
				"categoryList": []any{map[string]any{"categoryPriceId": tierWire, "quantity": 1, "tariffPlanId": nil}},
			})
			if code := numberField(t, resp, "resultCode"); code == 0 {
				t.Errorf("RESERVATION (%s) on the control session answered 0 (%v)", name, resp["description"])
			}
		}
		if after := holds(sessionID.String()); after != before {
			t.Errorf("reservations %d -> %d; a refused RESERVATION must create no hold", before, after)
		}
	}

	t.Run("natural state: the container is a draft", func(t *testing.T) { check(t, false) })

	// Forced: the container is published, and the control session carries a
	// category and an inventory row, so RESERVATION reaches the sales-mode gate
	// itself and not an earlier "no such category".
	ctlReplicaExec(t, st, `UPDATE events SET status = 'published', visibility = 'public' WHERE id = $1`, eventID)
	tierID := uuid.New()
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, sort_order)
	      VALUES ($1, $2, 'Ctl Tier', 'fixed', 1000, 'CZK', 0)`, tierID, sessionID)
	exec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, 50)`, sessionID)
	ctlTierWire := sc6TierWireID(t, st, tierID.String())

	t.Run("forced state: the container is published", func(t *testing.T) {
		check(t, true)

		// RESERVATION with the control session's OWN category: 101, no hold.
		before := holds(sessionID.String())
		sess, user := createGatewayUser(t, base, st, "harness-ctl2-"+uuid.NewString()[:8]+"@example.test")
		resp := postBil24(t, base, map[string]any{
			"command": "RESERVATION", "fid": st.ChannelFID, "token": st.ChannelToken, "locale": "en-US",
			"type": "RESERVE", "userId": user, "sessionId": sess, "actionEventId": ctlActionEventID,
			"categoryList": []any{map[string]any{"categoryPriceId": ctlTierWire, "quantity": 1, "tariffPlanId": nil}},
		})
		if code := numberField(t, resp, "resultCode"); code != 101 {
			t.Errorf("RESERVATION with the control session's own category: resultCode = %v, want 101 (description %v)", code, resp["description"])
		}
		if after := holds(sessionID.String()); after != before {
			t.Errorf("reservations %d -> %d after a refused RESERVATION", before, after)
		}
	})

	// Positive control: the same rows as a platform session ARE listed, so the
	// absence above is the filter's doing and not the fixture's.
	t.Run("positive control: the forced session as a platform session shows", func(t *testing.T) {
		ctlReplicaExec(t, st, `UPDATE sessions SET sales_mode = 'platform' WHERE id = $1`, sessionID)
		defer ctlReplicaExec(t, st, `UPDATE sessions SET sales_mode = 'control_only' WHERE id = $1`, sessionID)
		actions, events, raw := catalog()
		if !actions[ctlActionID] || !events[ctlActionEventID] {
			t.Errorf("a platform-mode session of a published event is missing from GET_ALL_ACTIONS: actions=%v events=%v", actions, events)
		}
		if !strings.Contains(raw, venueName) {
			t.Errorf("the platform-mode session's venue is missing from GET_ALL_ACTIONS")
		}
	})
}

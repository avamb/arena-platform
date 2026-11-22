//go:build integration

// external_session_l3_integration_test.go — spec 63 §14.2 (migration 0220):
// a session a partner box office sells (sessions.sales_mode = 'external')
// does not exist for the gateway:
//
//   - RESERVATION on it answers 101 and takes no hold;
//   - GET_ALL_ACTIONS leaves it out, and leaves the whole action out once
//     every session of the event is external;
//   - GET_SEAT_LIST answers "not found".
package compat_bil24_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
)

func l3SetExternal(t *testing.T, st *harnessState, sessionID string, external bool) {
	t.Helper()
	q := `UPDATE sessions SET sales_mode = 'platform', external_ticket_url = NULL, external_seller_name = NULL WHERE id = $1`
	if external {
		q = `UPDATE sessions SET sales_mode = 'external', external_ticket_url = 'https://www.eventim.de/l3', external_seller_name = 'Eventim' WHERE id = $1`
	}
	if _, err := st.Pool.Exec(context.Background(), q, sessionID); err != nil {
		t.Fatalf("set session %s external=%v: %v", sessionID, external, err)
	}
}

// l3Catalog returns the action ids and the action event ids GET_ALL_ACTIONS lists.
func l3Catalog(t *testing.T, base string, st *harnessState) (actions, actionEvents map[int64]bool) {
	t.Helper()
	resp := postBil24(t, base, map[string]any{
		"command": "GET_ALL_ACTIONS",
		"fid":     st.ChannelFID,
		"token":   st.ChannelToken,
		"locale":  "en-US",
	})
	if code := numberField(t, resp, "resultCode"); code != 0 {
		t.Fatalf("GET_ALL_ACTIONS resultCode = %v (description %v)", code, resp["description"])
	}
	actions, actionEvents = map[int64]bool{}, map[int64]bool{}
	list, _ := resp["actionList"].([]any)
	for _, a := range list {
		am, _ := a.(map[string]any)
		if id, ok := am["actionId"].(float64); ok {
			actions[int64(id)] = true
		}
		evs, _ := am["actionEventList"].([]any)
		for _, e := range evs {
			em, _ := e.(map[string]any)
			if id, ok := em["actionEventId"].(float64); ok {
				actionEvents[int64(id)] = true
			}
		}
	}
	return actions, actionEvents
}

func TestCompatBil24_ExternalSession_NotSoldNotListed(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	ctx := context.Background()
	defer l3SetExternal(t, st, st.GAsessID, false)
	defer l3SetExternal(t, st, st.AssignedSessID, false)

	gaActionEventID := mustActionEventID(t, st, st.GAsessID)
	seatedActionEventID := mustActionEventID(t, st, st.AssignedSessID)
	gaTierWireID := sc6TierWireID(t, st, sc2EarlyBirdTierID(t, st))
	actionID, err := compatids.Ensure(ctx, st.Pool, compatids.KindAction, uuid.MustParse(st.EventID))
	if err != nil {
		t.Fatalf("compatids.Ensure(action): %v", err)
	}

	actions, events := l3Catalog(t, base, st)
	if !actions[actionID] || !events[gaActionEventID] || !events[seatedActionEventID] {
		t.Fatalf("baseline catalog misses the harness event: actions=%v events=%v", actions, events)
	}

	// ── one session external: the session goes, the action stays ─────────
	l3SetExternal(t, st, st.GAsessID, true)

	actions, events = l3Catalog(t, base, st)
	if events[gaActionEventID] {
		t.Errorf("GET_ALL_ACTIONS lists the external session %d", gaActionEventID)
	}
	if !events[seatedActionEventID] || !actions[actionID] {
		t.Errorf("GET_ALL_ACTIONS lost the platform session or the action: actions=%v events=%v", actions, events)
	}

	var before, after int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM reservations WHERE session_id = $1`, st.GAsessID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	sess, user := createGatewayUser(t, base, st, "harness-l3-"+uuid.NewString()[:8]+"@example.test")
	resp := postBil24(t, base, map[string]any{
		"command":       "RESERVATION",
		"fid":           st.ChannelFID,
		"token":         st.ChannelToken,
		"locale":        "en-US",
		"type":          "RESERVE",
		"userId":        user,
		"sessionId":     sess,
		"actionEventId": gaActionEventID,
		"categoryList": []any{map[string]any{
			"categoryPriceId": gaTierWireID,
			"quantity":        1,
			"tariffPlanId":    nil,
		}},
	})
	if code := numberField(t, resp, "resultCode"); code != 101 {
		t.Fatalf("RESERVATION on an external session: resultCode = %v, want 101 (description %v)",
			code, resp["description"])
	}
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM reservations WHERE session_id = $1`, st.GAsessID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("reservations %d -> %d; a refused RESERVATION must create no hold", before, after)
	}

	seatList := postBil24(t, base, map[string]any{
		"command":       "GET_SEAT_LIST",
		"fid":           st.ChannelFID,
		"token":         st.ChannelToken,
		"locale":        "en-US",
		"actionEventId": gaActionEventID,
	})
	if code := numberField(t, seatList, "resultCode"); code == 0 {
		t.Errorf("GET_SEAT_LIST of an external session answered 0, want not found")
	}

	// ── every session external: the action itself goes ───────────────────
	l3SetExternal(t, st, st.AssignedSessID, true)
	actions, events = l3Catalog(t, base, st)
	if actions[actionID] || events[gaActionEventID] || events[seatedActionEventID] {
		t.Errorf("an event sold only by a partner is still in GET_ALL_ACTIONS: actions=%v events=%v", actions, events)
	}
}

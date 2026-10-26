//go:build integration

// cancelled_session_uxh23_integration_test.go — UXH-23: a session cancelled
// through the session-change flow keeps its row and its open categories, and
// used to accept a RESERVATION. The hold gate now refuses on the session's
// status; the gateway answers it as a closed sale: resultCode 101 with the
// bil24.sales_closed description, the same answer CREATE_ORDER_EXT gives.
package compat_bil24_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestCompatBil24_Reservation_CancelledSessionIsRefused(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	ctx := context.Background()

	gaActionEventID := mustActionEventID(t, st, st.GAsessID)
	gaTierWireID := sc6TierWireID(t, st, sc2EarlyBirdTierID(t, st))
	seatedActionEventID := mustActionEventID(t, st, st.AssignedSessID)
	labels := sortedSeatLabels(st)
	if len(labels) < 1 {
		t.Fatalf("seed produced %d seats, need at least 1", len(labels))
	}

	gaReserve := func(sess string, user float64) map[string]any {
		return map[string]any{
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
		}
	}
	seatedReserve := func(sess string, user float64) map[string]any {
		return map[string]any{
			"command":       "RESERVATION",
			"fid":           st.ChannelFID,
			"token":         st.ChannelToken,
			"locale":        "en-US",
			"type":          "RESERVE",
			"userId":        user,
			"sessionId":     sess,
			"actionEventId": seatedActionEventID,
			"seatList":      []any{map[string]any{"seatId": st.SeatIDs[labels[0]]}},
		}
	}

	setStatus := func(sessionID, status string) {
		t.Helper()
		if _, err := st.Pool.Exec(ctx, `UPDATE sessions SET status = $2 WHERE id = $1`, sessionID, status); err != nil {
			t.Fatalf("set session %s to %q: %v", sessionID, status, err)
		}
	}
	reservations := func(sessionID string) (n int) {
		t.Helper()
		if err := st.Pool.QueryRow(ctx,
			`SELECT count(*) FROM reservations WHERE session_id = $1`, sessionID).Scan(&n); err != nil {
			t.Fatalf("count reservations: %v", err)
		}
		return n
	}

	for _, tc := range []struct {
		name      string
		sessionID string
		request   func(sess string, user float64) map[string]any
	}{
		{"general admission categoryList", st.GAsessID, gaReserve},
		{"assigned seatList", st.AssignedSessID, seatedReserve},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh gateway session per case: a cart is bound to one
			// currency, and the two sessions do not share one.
			sess, user := createGatewayUser(t, base, st, "harness-cancelled-"+uuid.NewString()[:8]+"@example.test")
			before := reservations(tc.sessionID)
			setStatus(tc.sessionID, "cancelled")
			defer setStatus(tc.sessionID, "scheduled")

			resp := postBil24(t, base, tc.request(sess, user))
			if code := numberField(t, resp, "resultCode"); code != 101 {
				t.Fatalf("RESERVATION on a cancelled session: resultCode = %v, want 101 (description %v)",
					code, resp["description"])
			}
			desc, _ := resp["description"].(string)
			if !strings.Contains(strings.ToLower(desc), "closed") {
				t.Errorf("description = %q, want the bil24.sales_closed text", desc)
			}
			if after := reservations(tc.sessionID); after != before {
				t.Errorf("reservations %d -> %d; a refused RESERVATION must create no hold", before, after)
			}

			// Reinstated, the same request goes through: the gate reads the
			// status on every hold.
			setStatus(tc.sessionID, "scheduled")
			ok := postBil24(t, base, tc.request(sess, user))
			if code := numberField(t, ok, "resultCode"); code != 0 {
				t.Fatalf("RESERVATION after reinstating: resultCode = %v, want 0 (description %v)",
					code, ok["description"])
			}
		})
	}
}

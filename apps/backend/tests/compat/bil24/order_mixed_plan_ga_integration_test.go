//go:build integration

// order_mixed_plan_ga_integration_test.go — a GA place picked on a mixed plan.
//
// Lampyris' IVO DIMCHEV session (2026-09-25) has seats AND a General
// Admission zone on one plan. The site reserves a GA place there through
// seatList, so the hold has a reservation seat of kind ga_unit and no GA
// lines. CREATE_ORDER_EXT priced only the GA lines, found none and answered
// 101 "your hold has expired" to every GA-only cart; a real buyer could not
// pay. This walks RESERVE → CREATE_ORDER_EXT → PAY_ORDER end to end.
package compat_bil24_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/google/uuid"
)

func TestCompatBil24_MixedPlan_GAPlaceOnlyCart_OrdersAndPays(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	ctx := context.Background()

	// A GA category with one place on the ASSIGNED session, priced like the
	// basic fixtures expect (500 + the channel's 5 % = 525).
	gaTier := uuid.New()
	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode,
		     price_amount, currency, sort_order, capacity, unit_seq, is_open)
		 SELECT $1, $2, 'General admission', 'fixed', 50000, currency, 9, 1, 9, true
		 FROM ticket_tiers WHERE id = $3`,
		gaTier, st.AssignedSessID, st.AssignedTierID,
	); err != nil {
		t.Fatalf("seed GA tier on the assigned session: %v", err)
	}
	var gaSeatWireID int64
	if err := st.Pool.QueryRow(ctx,
		`INSERT INTO session_seats
		     (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
		 VALUES ($1, 'ga|t9|000001', '', '', '', $2, 'available', 'ga_unit')
		 RETURNING system_seat_id`,
		st.AssignedSessID, gaTier,
	).Scan(&gaSeatWireID); err != nil {
		t.Fatalf("seed GA place on the assigned session: %v", err)
	}
	if _, err := st.Pool.Exec(ctx,
		`UPDATE inventory_ledger SET capacity_total = capacity_total + 1
		 WHERE session_id = $1 AND tier_id IS NULL`, st.AssignedSessID,
	); err != nil {
		t.Fatalf("grow the session capacity by the GA place: %v", err)
	}

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	gaWireID := sc6TierWireID(t, st, gaTier.String())
	sess, user := createGatewayUser(t, base, st, "harness-mixed-ga@example.test")

	reserved := postBil24(t, base, map[string]any{
		"command": "RESERVATION", "fid": st.ChannelFID, "token": st.ChannelToken,
		"locale": "ru-RU", "type": "RESERVE", "userId": user, "sessionId": sess,
		"actionEventId": actionEventID,
		"seatList":      []any{map[string]any{"seatId": strconv.FormatInt(gaSeatWireID, 10)}},
	})
	if code := numberField(t, reserved, "resultCode"); code != 0 {
		t.Fatalf("RESERVE GA place resultCode = %v, want 0 (description %v)", code, reserved["description"])
	}

	req, _ := loadWPFixture(t, "CREATE_ORDER_EXT", "basic")
	req = resolveGolden(req, st, map[string]string{
		"actionEventId":   strconv.FormatInt(actionEventID, 10),
		"categoryPriceId": strconv.FormatInt(gaWireID, 10),
		"sessionId":       sess,
	})
	req["fid"] = st.ChannelFID
	req["token"] = st.ChannelToken
	req["userId"] = user
	req["orderId"] = "mixed-ga-1"
	req["email"] = "harness-mixed-ga@example.test"
	req["phone"] = "+420700" + strconv.FormatInt(int64(user), 10)
	created := postBil24(t, base, req)
	if code := numberField(t, created, "resultCode"); code != 0 {
		t.Fatalf("CREATE_ORDER_EXT for a GA-only cart on a mixed plan resultCode = %v, want 0 (description %v)",
			code, created["description"])
	}
	orderID := sc6OrderID(t, st, "mixed-ga", created)

	var total int64
	if err := st.Pool.QueryRow(ctx, `SELECT total FROM orders WHERE id=$1`, orderID).Scan(&total); err != nil {
		t.Fatalf("read order total: %v", err)
	}
	if total != 52500 {
		t.Fatalf("orders.total = %d, want 52500 (one GA place at 500 + 5 %%)", total)
	}

	paid := pwPay(t, base, st, sess, user, strconv.FormatInt(sc5SystemID(t, st, orderID), 10))
	if code := numberField(t, paid, "resultCode"); code != 0 {
		t.Fatalf("PAY_ORDER resultCode = %v, want 0 (description %v)", code, paid["description"])
	}
	var tickets int
	if err := st.Pool.QueryRow(ctx,
		`SELECT count(*) FROM tickets WHERE order_id=$1 AND tier_id=$2 AND seat_key='ga|t9|000001'`,
		orderID, gaTier,
	).Scan(&tickets); err != nil {
		t.Fatalf("count tickets: %v", err)
	}
	if tickets != 1 {
		t.Fatalf("tickets for the GA place = %d, want 1", tickets)
	}
}

//go:build integration

// order_complimentary_integration_test.go — CREATE_ORDER_EXT's
// `complimentary` flag (functional run 2026-09-19): an invitation is priced
// by arena itself, not patched over on the selling site.
//
//   - the order is written with source 'complimentary', the ticket keeps its
//     face value, the whole subtotal is discounted, no service charge
//     applies, and the total is 0 — on the wire and in the database;
//   - PAY_ORDER with amount 0 pays it without an amount_mismatch event;
//   - the export MACS and the site webhooks read carries totalPrice 0 with
//     the discount reason "Приглашение".
package compat_bil24_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/orderexport"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

func TestCompatBil24_CreateOrderExt_ComplimentaryIsPricedZeroInArena(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	ctx := context.Background()

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	labels := sortedSeatLabels(st)
	if len(labels) < 1 {
		t.Fatalf("seed produced %d seats, need at least 1", len(labels))
	}
	tierWireID := sc6TierWireID(t, st, st.AssignedTierID)

	const email = "harness-comp-invite@example.test"
	sess, user := createGatewayUser(t, base, st, email)
	reserved := postBil24(t, base, map[string]any{
		"command":       "RESERVATION",
		"fid":           st.ChannelFID,
		"token":         st.ChannelToken,
		"locale":        "ru-RU",
		"type":          "RESERVE",
		"userId":        user,
		"sessionId":     sess,
		"actionEventId": actionEventID,
		"seatList":      []any{map[string]any{"seatId": st.SeatIDs[labels[0]]}},
	})
	if code := numberField(t, reserved, "resultCode"); code != 0 {
		t.Fatalf("RESERVE resultCode = %v, want 0 (description %v)", code, reserved["description"])
	}

	req, _ := loadWPFixture(t, "CREATE_ORDER_EXT", "basic")
	req = resolveGolden(req, st, map[string]string{
		"actionEventId":   strconv.FormatInt(actionEventID, 10),
		"categoryPriceId": strconv.FormatInt(tierWireID, 10),
		"sessionId":       sess,
	})
	req["fid"] = st.ChannelFID
	req["token"] = st.ChannelToken
	req["userId"] = user
	req["orderId"] = "comp-invite-1"
	req["email"] = email
	req["phone"] = "+420700" + strconv.FormatInt(int64(user), 10)
	req["total"] = 0
	// PHP sends the flag as "1" as often as true; the flexible decode is
	// covered by the wire unit test, the quoted form is exercised here.
	req["complimentary"] = "1"
	created := postBil24(t, base, req)
	if code := numberField(t, created, "resultCode"); code != 0 {
		t.Fatalf("CREATE_ORDER_EXT resultCode = %v, want 0 (description %v)", code, created["description"])
	}

	sum := numberField(t, created, "sum")
	if sum <= 0 {
		t.Fatalf("CREATE_ORDER_EXT sum = %v, want the ticket's face value (> 0)", sum)
	}
	if got := numberField(t, created, "discount"); got != sum {
		t.Errorf("CREATE_ORDER_EXT discount = %v, want the full sum %v", got, sum)
	}
	if got := numberField(t, created, "charge"); got != 0 {
		t.Errorf("CREATE_ORDER_EXT charge = %v, want 0 for an invitation", got)
	}
	if got := numberField(t, created, "totalSum"); got != 0 {
		t.Errorf("CREATE_ORDER_EXT totalSum = %v, want 0 for an invitation", got)
	}

	orderID := sc6OrderID(t, st, "comp/"+email, created)
	var (
		source                          string
		subtotal, discount, charge, tot int64
	)
	if err := st.Pool.QueryRow(ctx,
		`SELECT source, subtotal, discount, charge, total FROM orders WHERE id=$1`, orderID,
	).Scan(&source, &subtotal, &discount, &charge, &tot); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if source != ordering.SourceComplimentary {
		t.Errorf("orders.source = %q, want %q", source, ordering.SourceComplimentary)
	}
	if subtotal <= 0 || discount != subtotal || charge != 0 || tot != 0 {
		t.Errorf("orders subtotal/discount/charge/total = %d/%d/%d/%d, want face/face/0/0",
			subtotal, discount, charge, tot)
	}
	var itemsTotal int64
	if err := st.Pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(total), 0) FROM order_items WHERE order_id=$1`, orderID,
	).Scan(&itemsTotal); err != nil {
		t.Fatalf("sum order_items: %v", err)
	}
	if itemsTotal != 0 {
		t.Errorf("order_items total = %d, want 0 — the items must reconcile with the order", itemsTotal)
	}

	// PAY_ORDER: the site reports the 0 it charged.
	systemID := sc5SystemID(t, st, orderID)
	payReq, _ := loadWPFixture(t, "PAY_ORDER", "basic")
	payReq = resolveGolden(payReq, st, map[string]string{
		"sessionId": sess, "orderId": strconv.FormatInt(systemID, 10),
	})
	payReq["fid"] = st.ChannelFID
	payReq["token"] = st.ChannelToken
	payReq["userId"] = user
	payReq["amount"] = 0
	payReq["method"] = "free_ticket"
	paid := postBil24(t, base, payReq)
	if code := numberField(t, paid, "resultCode"); code != 0 {
		t.Fatalf("PAY_ORDER resultCode = %v, want 0 (description %v)", code, paid["description"])
	}
	sc5AssertOrderPaid(t, st, orderID, "free_ticket")
	if n := pwCountOrderEvents(t, st, orderID, ordering.EventAmountMismatch); n != 0 {
		t.Errorf("amount_mismatch events = %d, want 0 — an invitation paid with 0 matches its total", n)
	}

	// The projection MACS and the site webhooks are built from.
	exported, err := orderexport.QueryOrder(ctx, st.Pool, orderID)
	if err != nil {
		t.Fatalf("orderexport.QueryOrder: %v", err)
	}
	if exported == nil || len(exported.Tickets) != 1 {
		t.Fatalf("exported order = %+v, want one ticket", exported)
	}
	if exported.Total != 0 {
		t.Errorf("exported order total = %d, want 0", exported.Total)
	}
	tk := exported.Tickets[0]
	if tk.TotalPrice != 0 || tk.Discount != tk.Price || tk.Price <= 0 {
		t.Errorf("exported ticket price/discount/totalPrice = %d/%d/%d, want face/face/0",
			tk.Price, tk.Discount, tk.TotalPrice)
	}
	if tk.DiscountReason != "Приглашение" {
		t.Errorf("exported ticket discountReason = %q, want %q", tk.DiscountReason, "Приглашение")
	}
	if exported.DiscountReason != "Приглашение" {
		t.Errorf("exported order discountReason = %q, want %q", exported.DiscountReason, "Приглашение")
	}
}

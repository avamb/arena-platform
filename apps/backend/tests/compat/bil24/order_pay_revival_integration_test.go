//go:build integration

// order_pay_revival_integration_test.go — money-safety fix, two related
// defects found under concurrent-buyer load against the live gateway
// (2026-09-13):
//
//  1. CREATE_ORDER_EXT's one-open-order rule (spec §7.7 step 5,
//     cmd_order_create.go orderWriteAggregate) used to expire a customer's
//     existing pending_payment order UNCONDITIONALLY whenever a second
//     CREATE_ORDER_EXT for the same customer+session pointed at a different
//     reservation — on the unchecked assumption that a second open order can
//     only mean "the first one expired". Two gateway buyers who happen to
//     share a checkout identity (the WordPress plugin sends whatever the
//     buyer typed at checkout, not a stable account id) could therefore have
//     buyer A's still-live hold silently stolen by buyer B's checkout a few
//     milliseconds later, and A's subsequent PAY_ORDER would then fail even
//     though WooCommerce had already charged them.
//  2. PAY_ORDER (cmd_order_pay.go) fell through to ordering.MarkPaid for an
//     'expired' order, which only accepts pending_payment -> paid and
//     answered ErrInvalidTransition — surfaced as bil24 resultCode -1
//     (transient). Since nothing about order.status ever changed on its
//     own, the WordPress plugin's retry loop got -1 FOREVER for a buyer who
//     had already been charged and received no ticket. A parked
//     manual_review order had the same problem one level up: a replay
//     re-ran the whole park-and-alert sequence, producing a duplicate
//     hold_expired audit event and a second operator alert on every retry.
//
// This file proves the fix end to end against the real server and a live
// database (setupHarness/startHarnessServer — nothing below HTTP is
// stubbed), reusing the sc5*/sc6* fixture helpers from
// scenario05_pay_order_test.go and scenario06_create_order_test.go, which
// live in this same package.
package compat_bil24_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

// revBackdateOrderExpiry pushes orders.expires_at into the past so it becomes
// a candidate for ordering.RunExpireSweep, reproducing order.expire_sweep
// (spec §14.1) closing the aggregate a minute or more before the shop's late
// PAY_ORDER arrives.
func revBackdateOrderExpiry(t *testing.T, st *harnessState, orderID uuid.UUID) {
	t.Helper()
	if _, err := st.Pool.Exec(context.Background(),
		`UPDATE orders SET expires_at = now() - interval '1 hour' WHERE id=$1`, orderID,
	); err != nil {
		t.Fatalf("backdate orders.expires_at for %s: %v", orderID, err)
	}
}

// revRunExpireSweep drives the real order.expire_sweep pass (rather than
// hand-writing orders.status='expired', which would skip the candidate query
// and the hold_expired audit row the production job also writes).
func revRunExpireSweep(t *testing.T, st *harnessState) int {
	t.Helper()
	expired, err := ordering.RunExpireSweep(context.Background(), gen.New(st.Pool), time.Now().UTC(), 500, nil)
	if err != nil {
		t.Fatalf("RunExpireSweep: %v", err)
	}
	return expired
}

// revCountOrderEvents counts order_events rows of one type — the idempotency
// proof needs exact counts, not sc5AssertOrderEvent's "at least one" check.
func revCountOrderEvents(t *testing.T, st *harnessState, orderID uuid.UUID, typ string) int {
	t.Helper()
	var got int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM order_events WHERE order_id=$1 AND type=$2`, orderID, typ,
	).Scan(&got); err != nil {
		t.Fatalf("count order_events %q for %s: %v", typ, orderID, err)
	}
	return got
}

// revCountManualReviewAlerts counts the operator-alert audit rows PAY_ORDER's
// manual-review park writes (see sc5AssertManualReviewAlert for the single-row
// shape this mirrors).
func revCountManualReviewAlerts(t *testing.T, st *harnessState, orderID uuid.UUID) int {
	t.Helper()
	var got int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events
		 WHERE  action = 'bil24.pay_order.manual_review'
		   AND  resource_type = 'order'
		   AND  resource_id = $1`, orderID.String(),
	).Scan(&got); err != nil {
		t.Fatalf("count manual-review alerts for %s: %v", orderID, err)
	}
	return got
}

// TestCompatBil24_PayOrderRevival_ExpiredOrderWithReacquirableHold is case
// (b): order.expire_sweep closes the order while its reservation's TTL has
// ALSO passed but the seats are still exclusively held by it — the ordinary
// "sweep beat the late payment by a minute" case. PAY_ORDER must re-take the
// hold (hold_reacquired, exactly like the pre-existing pending_payment path),
// revive the order (revived_for_payment) and complete the sale.
func TestCompatBil24_PayOrderRevival_ExpiredOrderWithReacquirableHold(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	labels := sortedSeatLabels(st)
	if len(labels) < 1 {
		t.Fatalf("seed produced %d seats for the assigned-seats session, need at least 1", len(labels))
	}
	tierWireID := sc6TierWireID(t, st, st.AssignedTierID)

	sess, user := createGatewayUser(t, base, st, "harness-revival-reacquire@example.test")
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
		t.Fatalf("RESERVE %s resultCode = %v, want 0 (description %v)", labels[0], code, reserved["description"])
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
	req["orderId"] = "revival-reacquire-1"
	created := postBil24(t, base, req)
	if code := numberField(t, created, "resultCode"); code != 0 {
		t.Fatalf("CREATE_ORDER_EXT resultCode = %v, want 0 (description %v)", code, created["description"])
	}
	orderID := sc6OrderID(t, st, "revival-reacquire", created)

	// Reproduce the exact window the defect lived in: the hold's own TTL has
	// passed (so PAY_ORDER's step 2 must reacquire it) AND the order.status
	// has already been flipped to 'expired' by the real sweep job.
	reservationID, _ := sc5OrderRefs(t, st, orderID)
	sc5ExpireReservation(t, st, reservationID)
	revBackdateOrderExpiry(t, st, orderID)
	if n := revRunExpireSweep(t, st); n < 1 {
		t.Fatalf("RunExpireSweep expired %d orders, want at least 1 (this order)", n)
	}
	sc5AssertOrderStatus(t, st, orderID, "expired")

	systemID := sc5SystemID(t, st, orderID)
	pay, _ := loadWPFixture(t, "PAY_ORDER", "basic")
	pay = resolveGolden(pay, st, map[string]string{"sessionId": sess, "orderId": strconv.FormatInt(systemID, 10)})
	pay["fid"] = st.ChannelFID
	pay["token"] = st.ChannelToken
	pay["userId"] = user
	resp := postBil24(t, base, pay)
	if code := numberField(t, resp, "resultCode"); code != 0 {
		t.Fatalf("PAY_ORDER on a revivable expired order resultCode = %v, want 0 (description %v) — "+
			"the shop's money already moved, refusing it strands the buyer", code, resp["description"])
	}

	sc5AssertOrderPaid(t, st, orderID, "woo_bank_card")
	sc5AssertStates(t, st, orderID, "completed", "converted")
	sc5AssertTickets(t, st, orderID, 1, "buyer@example.com")
	// Both halves of the revival must be on the record.
	sc5AssertOrderEvent(t, st, orderID, "hold_reacquired")
	sc5AssertOrderEvent(t, st, orderID, "revived_for_payment")
}

// TestCompatBil24_PayOrderRevival_ManualReviewReplayIsIdempotent is case (c):
// the sweep closes the order AND the seats are genuinely gone (stolen by
// another reservation, exactly like sc5's expired_manual_review case), so
// PAY_ORDER cannot revive it and parks it in manual_review instead. The
// WordPress plugin retries any non-zero resultCode, so a SECOND PAY_ORDER on
// the same parked order must answer the identical 101 without writing a
// second hold_expired event or raising a second operator alert.
func TestCompatBil24_PayOrderRevival_ManualReviewReplayIsIdempotent(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	labels := sortedSeatLabels(st)
	if len(labels) < 2 {
		t.Fatalf("seed produced %d seats for the assigned-seats session, need at least 2", len(labels))
	}
	tierWireID := sc6TierWireID(t, st, st.AssignedTierID)

	newOrder := func(email, seatLabel, externalRef string) (sess string, user float64, orderID uuid.UUID) {
		t.Helper()
		sess, user = createGatewayUser(t, base, st, email)
		reserved := postBil24(t, base, map[string]any{
			"command":       "RESERVATION",
			"fid":           st.ChannelFID,
			"token":         st.ChannelToken,
			"locale":        "ru-RU",
			"type":          "RESERVE",
			"userId":        user,
			"sessionId":     sess,
			"actionEventId": actionEventID,
			"seatList":      []any{map[string]any{"seatId": st.SeatIDs[seatLabel]}},
		})
		if code := numberField(t, reserved, "resultCode"); code != 0 {
			t.Fatalf("RESERVE %s resultCode = %v, want 0 (description %v)", seatLabel, code, reserved["description"])
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
		req["orderId"] = externalRef
		// The fixture's default checkout identity (buyer@example.com) is
		// shared by every CREATE_ORDER_EXT case in this package on purpose
		// (sc5's pattern). This test needs TWO SIMULTANEOUS open orders for
		// the SAME session — `lost` and `other` — which the §7.7
		// open_order_exists fix (this same wave) now correctly refuses for
		// one shared identity, so each buyer here gets its own checkout
		// email/phone instead.
		req["email"] = email
		req["phone"] = "+420700" + strconv.FormatInt(int64(user), 10)
		created := postBil24(t, base, req)
		if code := numberField(t, created, "resultCode"); code != 0 {
			t.Fatalf("CREATE_ORDER_EXT for %s resultCode = %v, want 0 (description %v)", email, code, created["description"])
		}
		return sess, user, sc6OrderID(t, st, "manual-review/"+email, created)
	}

	// `other`'s reservation is the safe FK-valid steal target (sc5's own
	// pattern): its identity does not matter, only that the row exists — it
	// just must be a DIFFERENT customer from `lost`, or the open_order_exists
	// guard (this same wave) would refuse `lost`'s own CREATE_ORDER_EXT.
	_, _, otherOrderID := newOrder("harness-revival-other@example.test", labels[1], "revival-manual-other")
	lostSess, lostUser, lostOrderID := newOrder("harness-revival-lost@example.test", labels[0], "revival-manual-lost")

	lostReservation, lostCheckout := sc5OrderRefs(t, st, lostOrderID)
	otherReservation, _ := sc5OrderRefs(t, st, otherOrderID)
	sc5ExpireReservation(t, st, lostReservation)
	sc5StealSeats(t, st, lostReservation, otherReservation)

	revBackdateOrderExpiry(t, st, lostOrderID)
	if n := revRunExpireSweep(t, st); n < 1 {
		t.Fatalf("RunExpireSweep expired %d orders, want at least 1 (this order)", n)
	}
	sc5AssertOrderStatus(t, st, lostOrderID, "expired")
	// The sweep itself writes its own order_events.hold_expired (actor
	// 'system') when it closes the order — a SEPARATE row from the one
	// PAY_ORDER's manual-review park writes (actor 'gateway:...') under the
	// same event type. The idempotency proof below is about PAY_ORDER's OWN
	// write, so it counts from this baseline rather than assuming zero.
	baselineHoldExpired := revCountOrderEvents(t, st, lostOrderID, "hold_expired")

	systemID := sc5SystemID(t, st, lostOrderID)
	pay := func() map[string]interface{} {
		t.Helper()
		req, _ := loadWPFixture(t, "PAY_ORDER", "basic")
		req = resolveGolden(req, st, map[string]string{"sessionId": lostSess, "orderId": strconv.FormatInt(systemID, 10)})
		req["fid"] = st.ChannelFID
		req["token"] = st.ChannelToken
		req["userId"] = lostUser
		return postBil24(t, base, req)
	}

	first := pay()
	if code := numberField(t, first, "resultCode"); code != 101 {
		t.Fatalf("first PAY_ORDER on an unrecoverable expired order resultCode = %v, want 101 bil24.hold_expired "+
			"(description %v)", code, first["description"])
	}
	if desc, _ := first["description"].(string); desc == "" {
		t.Error("bil24.hold_expired carries an empty description; §7.9 has the site render it verbatim")
	}
	sc5AssertOrderStatus(t, st, lostOrderID, "manual_review")
	sc5AssertCheckoutState(t, st, lostCheckout, "manual_review")
	sc5AssertPaymentIntentCount(t, st, lostOrderID, 0)
	sc5AssertTicketCount(t, st, lostOrderID, 0)
	if got := revCountOrderEvents(t, st, lostOrderID, "hold_expired"); got != baselineHoldExpired+1 {
		t.Fatalf("hold_expired events after the first PAY_ORDER = %d, want %d (baseline %d + PAY_ORDER's own park)",
			got, baselineHoldExpired+1, baselineHoldExpired)
	}
	if got := revCountManualReviewAlerts(t, st, lostOrderID); got != 1 {
		t.Fatalf("manual-review alerts after the first PAY_ORDER = %d, want 1", got)
	}

	// The replay — same shape as the plugin's -1/101 retry loop.
	second := pay()
	if code := numberField(t, second, "resultCode"); code != 101 {
		t.Fatalf("replayed PAY_ORDER on a manual_review order resultCode = %v, want 101 (description %v)",
			code, second["description"])
	}
	if desc, _ := second["description"].(string); desc == "" {
		t.Error("replayed bil24.hold_expired carries an empty description")
	}
	sc5AssertOrderStatus(t, st, lostOrderID, "manual_review")
	sc5AssertCheckoutState(t, st, lostCheckout, "manual_review")
	if got := revCountOrderEvents(t, st, lostOrderID, "hold_expired"); got != baselineHoldExpired+1 {
		t.Errorf("hold_expired events after the REPLAYED PAY_ORDER = %d, want still %d — "+
			"a replay must not double-audit the park", got, baselineHoldExpired+1)
	}
	if got := revCountManualReviewAlerts(t, st, lostOrderID); got != 1 {
		t.Errorf("manual-review alerts after the REPLAYED PAY_ORDER = %d, want still 1 — "+
			"alert-storm defect: an operator would be paged again on every WordPress retry", got)
	}
}

// TestCompatBil24_CreateOrderExt_OpenOrderExistsProtectsLiveHold is case (a)
// plus the (d) regression check: two gateway buyers who happen to share the
// checkout identity the fixture carries (buyer@example.com /
// +420123456789 — the WordPress plugin sends whatever the buyer typed, not a
// stable account id) each reserve their OWN seat for the same session.
// Buyer B's CREATE_ORDER_EXT must be refused with 101 bil24.open_order_exists
// rather than silently expiring buyer A's still-live order, and buyer A's
// order must remain fully payable afterwards. The SAME buyer's own repeat
// CREATE_ORDER_EXT (same reservation) must still update in place, unaffected
// by the new guard.
func TestCompatBil24_CreateOrderExt_OpenOrderExistsProtectsLiveHold(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	labels := sortedSeatLabels(st)
	if len(labels) < 2 {
		t.Fatalf("seed produced %d seats for the assigned-seats session, need at least 2", len(labels))
	}
	tierWireID := sc6TierWireID(t, st, st.AssignedTierID)
	runtime := map[string]string{
		"actionEventId":   strconv.FormatInt(actionEventID, 10),
		"categoryPriceId": strconv.FormatInt(tierWireID, 10),
	}

	reserve := func(user float64, sess, label string) {
		t.Helper()
		resp := postBil24(t, base, map[string]any{
			"command":       "RESERVATION",
			"fid":           st.ChannelFID,
			"token":         st.ChannelToken,
			"locale":        "ru-RU",
			"type":          "RESERVE",
			"userId":        user,
			"sessionId":     sess,
			"actionEventId": actionEventID,
			"seatList":      []any{map[string]any{"seatId": st.SeatIDs[label]}},
		})
		if code := numberField(t, resp, "resultCode"); code != 0 {
			t.Fatalf("RESERVE %s resultCode = %v, want 0 (description %v)", label, code, resp["description"])
		}
	}
	createOrder := func(sess string, user float64, orderIDRaw string) map[string]interface{} {
		t.Helper()
		rt := map[string]string{"sessionId": sess}
		for k, v := range runtime {
			rt[k] = v
		}
		req, _ := loadWPFixture(t, "CREATE_ORDER_EXT", "basic")
		req = resolveGolden(req, st, rt)
		req["fid"] = st.ChannelFID
		req["token"] = st.ChannelToken
		req["userId"] = user
		req["orderId"] = orderIDRaw
		return postBil24(t, base, req)
	}

	sessA, userA := createGatewayUser(t, base, st, "harness-open-order-a@example.test")
	reserve(userA, sessA, labels[0])
	respA := createOrder(sessA, userA, "open-order-a-1")
	if code := numberField(t, respA, "resultCode"); code != 0 {
		t.Fatalf("CREATE_ORDER_EXT (buyer A) resultCode = %v, want 0 (description %v)", code, respA["description"])
	}
	orderA := sc6OrderID(t, st, "open-order-a", respA)
	sc6AssertOpenOrderCount(t, st, 1)

	// ── regression (d): the same cart re-sending CREATE_ORDER_EXT still
	// updates in place — the new guard only fires for a DIFFERENT reservation.
	respA2 := createOrder(sessA, userA, "open-order-a-2")
	if code := numberField(t, respA2, "resultCode"); code != 0 {
		t.Fatalf("repeat CREATE_ORDER_EXT (buyer A, same cart) resultCode = %v, want 0 (description %v)",
			code, respA2["description"])
	}
	if got := sc6OrderID(t, st, "open-order-a-repeat", respA2); got != orderA {
		t.Fatalf("repeat CREATE_ORDER_EXT (same cart) minted a SECOND order %s, want the same %s", got, orderA)
	}
	sc6AssertOpenOrderCount(t, st, 1)

	// ── the defect: buyer B reserves a DIFFERENT seat for the SAME session
	// and shares buyer A's checkout identity. Before the fix, CREATE_ORDER_EXT
	// would silently expire buyer A's still-live order to make room for B's.
	sessB, userB := createGatewayUser(t, base, st, "harness-open-order-b@example.test")
	reserve(userB, sessB, labels[1])
	respB := createOrder(sessB, userB, "open-order-b-1")
	if code := numberField(t, respB, "resultCode"); code != 101 {
		t.Fatalf("CREATE_ORDER_EXT (buyer B, colliding open order) resultCode = %v, want 101 "+
			"bil24.open_order_exists (description %v)", code, respB["description"])
	}
	if desc, _ := respB["description"].(string); desc == "" {
		t.Error("bil24.open_order_exists carries an empty description; the site renders it verbatim")
	}
	// Nothing B touched was written: still exactly one open order, and it is
	// still A's, untouched.
	sc6AssertOpenOrderCount(t, st, 1)
	sc5AssertOrderStatus(t, st, orderA, "pending_payment")

	// ── buyer A's order is provably unharmed: it can still be paid ─────────
	systemIDA := sc5SystemID(t, st, orderA)
	payReq, _ := loadWPFixture(t, "PAY_ORDER", "basic")
	payReq = resolveGolden(payReq, st, map[string]string{"sessionId": sessA, "orderId": strconv.FormatInt(systemIDA, 10)})
	payReq["fid"] = st.ChannelFID
	payReq["token"] = st.ChannelToken
	payReq["userId"] = userA
	payResp := postBil24(t, base, payReq)
	if code := numberField(t, payResp, "resultCode"); code != 0 {
		t.Fatalf("PAY_ORDER for buyer A's untouched order resultCode = %v, want 0 (description %v)",
			code, payResp["description"])
	}
	sc5AssertOrderPaid(t, st, orderA, "woo_bank_card")
}

//go:build integration

// order_pay_window_integration_test.go — the payment-window contract (owner
// decision 2026-09-13): a buyer gets a FIXED window to pay from the moment
// CREATE_ORDER_EXT creates (or restarts) the order, after which payment is
// no longer accepted and the order's seats/GA units go back on sale
// automatically. There is NO MANUAL REVIEW anywhere in the gateway path any
// more — every PAY_ORDER outcome resolves on its own.
//
// This file replaces order_pay_revival_integration_test.go (removed): the
// revival machinery it tested (ordering.ReviveForPayment,
// ErrOpenOrderConflict, EventRevivedForPayment, payParkManualReview) is gone
// by owner decision, because it contradicted the fixed-window rule — a late
// payment for an order whose window has elapsed is refused, not resurrected.
//
// TestCompatBil24_CreateOrderExt_OpenOrderExistsProtectsLiveHold is kept
// verbatim: the one-open-order guard (spec §7.7 step 5,
// bil24.open_order_exists) is unrelated to revival and still valid.
//
// Proves end to end against the real server and a live database
// (setupHarness/startHarnessServer — nothing below HTTP is stubbed):
//
//   - CREATE_ORDER_EXT sets orders.expires_at = reservations.expires_at =
//     now + window + grace, returns paymentDeadline/paymentTimeout, and
//     honours a channel's payment_window_seconds override.
//   - A cart RESERVE issued after the order exists does not shorten the
//     hold below the order's payment deadline.
//   - PAY_ORDER inside the window pays and issues tickets.
//   - PAY_ORDER after expires_at expires the order and releases the hold —
//     no manual_review row anywhere — and a replay is a no-op.
//   - An already-expired order never revives.
//   - A hold lost to someone else within the window cancels the order
//     automatically (no manual review).
//   - 10 concurrent PAY_ORDER calls at the deadline boundary neither
//     double-pay nor corrupt inventory.
package compat_bil24_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// shared fixture / DB helpers
// ─────────────────────────────────────────────────────────────────────────────

// pwBackdateOrderAndReservation pushes both orders.expires_at and
// reservations.expires_at into the past, reproducing "the payment window has
// elapsed and neither sweep has run yet" without waiting for a real clock.
func pwBackdateOrderAndReservation(t *testing.T, st *harnessState, orderID, reservationID uuid.UUID) {
	t.Helper()
	if _, err := st.Pool.Exec(context.Background(),
		`UPDATE orders SET expires_at = now() - interval '1 hour' WHERE id=$1`, orderID,
	); err != nil {
		t.Fatalf("backdate orders.expires_at for %s: %v", orderID, err)
	}
	if _, err := st.Pool.Exec(context.Background(),
		`UPDATE reservations SET expires_at = now() - interval '1 hour' WHERE id=$1`, reservationID,
	); err != nil {
		t.Fatalf("backdate reservations.expires_at for %s: %v", reservationID, err)
	}
}

// pwSetChannelPaymentWindow overrides settings.gateway.payment_window_seconds
// for the harness channel. seedHarness writes the LEGACY top-level
// {"gateway_token_hash": "..."} shape (no "gateway" object at all): a plain
// jsonb_set on the "{gateway,payment_window_seconds}" path would be a silent
// no-op (jsonb_set never creates missing INTERMEDIATE objects, only the
// final leaf), and — more importantly — parseGatewaySettings' precedence
// rule means introducing ANY "gateway" object at all makes it stop looking
// at the legacy top-level hash entirely (auth.go's documented precedence:
// nested wins). So this migrates the legacy hash into the nested shape
// (enabled=true, token_hash carried over) in the SAME update that sets the
// window, or the channel's token would stop authenticating.
func pwSetChannelPaymentWindow(t *testing.T, st *harnessState, seconds int) {
	t.Helper()
	if _, err := st.Pool.Exec(context.Background(),
		`UPDATE sales_channels
		 SET settings = jsonb_set(
		       coalesce(settings, '{}'::jsonb),
		       '{gateway}',
		       jsonb_build_object(
		         'enabled', true,
		         'token_hash', coalesce(settings->'gateway'->>'token_hash', settings->>'gateway_token_hash'),
		         'payment_window_seconds', $1::int
		       ),
		       true
		     )
		 WHERE display_number = $2`,
		seconds, st.ChannelFID,
	); err != nil {
		t.Fatalf("set channel payment_window_seconds=%d: %v", seconds, err)
	}
}

func pwOrderExpiresAt(t *testing.T, st *harnessState, orderID uuid.UUID) time.Time {
	t.Helper()
	var v time.Time
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT expires_at FROM orders WHERE id=$1`, orderID).Scan(&v); err != nil {
		t.Fatalf("read orders.expires_at for %s: %v", orderID, err)
	}
	return v
}

func pwReservationExpiresAt(t *testing.T, st *harnessState, reservationID uuid.UUID) time.Time {
	t.Helper()
	var v time.Time
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT expires_at FROM reservations WHERE id=$1`, reservationID).Scan(&v); err != nil {
		t.Fatalf("read reservations.expires_at for %s: %v", reservationID, err)
	}
	return v
}

func pwReservationState(t *testing.T, st *harnessState, reservationID uuid.UUID) string {
	t.Helper()
	var v string
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT state FROM reservations WHERE id=$1`, reservationID).Scan(&v); err != nil {
		t.Fatalf("read reservations.state for %s: %v", reservationID, err)
	}
	return v
}

// pwSeatStatus reads a seat's status by its WIRE id (st.SeatIDs[label] — the
// session_seats.system_seat_id bigint minted for the `seatId` JSON field),
// NOT session_seats.id (a UUID) and NOT seat_key (built from sec.Key/row.Key,
// a different string than the sec.Name/row.Name label st.SeatIDs is keyed by).
func pwSeatStatus(t *testing.T, st *harnessState, wireSeatID string) string {
	t.Helper()
	id, err := strconv.ParseInt(wireSeatID, 10, 64)
	if err != nil {
		t.Fatalf("wire seat id %q is not an int64: %v", wireSeatID, err)
	}
	var v string
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT status FROM session_seats WHERE system_seat_id=$1`, id).Scan(&v); err != nil {
		t.Fatalf("read session_seats.status for system_seat_id %d: %v", id, err)
	}
	return v
}

func pwCapacityHeld(t *testing.T, st *harnessState, sessionID string) int32 {
	t.Helper()
	var v int32
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT capacity_held FROM inventory_ledger WHERE session_id=$1 AND tier_id IS NULL`,
		sessionID).Scan(&v); err != nil {
		t.Fatalf("read inventory_ledger.capacity_held for %s: %v", sessionID, err)
	}
	return v
}

func pwCountOrderEvents(t *testing.T, st *harnessState, orderID uuid.UUID, typ string) int {
	t.Helper()
	var got int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM order_events WHERE order_id=$1 AND type=$2`, orderID, typ,
	).Scan(&got); err != nil {
		t.Fatalf("count order_events %q for %s: %v", typ, orderID, err)
	}
	return got
}

func pwAssertNoManualReview(t *testing.T, st *harnessState, orderID uuid.UUID) {
	t.Helper()
	var status string
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT status FROM orders WHERE id=$1`, orderID).Scan(&status); err != nil {
		t.Fatalf("read order status for %s: %v", orderID, err)
	}
	if status == "manual_review" {
		t.Fatalf("orders.status = manual_review for %s — owner decision 2026-09-13 forbids this outcome", orderID)
	}
	var alerts int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events
		 WHERE action = 'bil24.pay_order.manual_review' AND resource_type = 'order' AND resource_id = $1`,
		orderID.String()).Scan(&alerts); err != nil {
		t.Fatalf("count manual-review alerts for %s: %v", orderID, err)
	}
	if alerts != 0 {
		t.Fatalf("manual-review alerts for %s = %d, want 0 — no manual review any more", orderID, alerts)
	}
}

// pwNewOrder walks a fresh buyer to a pending_payment order on the given seat
// label, returning the session/user pair the caller pays with, the response
// body (so CREATE_ORDER_EXT fields can be inspected) and the resolved order
// row references.
func pwNewOrder(t *testing.T, base string, st *harnessState, actionEventID, tierWireID int64, email, seatLabel, externalRef string) (sess string, user float64, resp map[string]interface{}, orderID, reservationID, checkoutID uuid.UUID) {
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
	// The "basic" fixture's checkout identity (buyer@example.com /
	// +420123456789) is shared by every CREATE_ORDER_EXT case in this
	// package on purpose. Two DIFFERENT buyers in the SAME test must not
	// collide on the one-open-order-per-customer-session guard, so each
	// gets its own checkout email/phone — distinct from the createGatewayUser
	// email, which only identifies the GATEWAY SESSION, not the checkout
	// buyer customers.Resolve() sees.
	req["email"] = email
	req["phone"] = "+420700" + strconv.FormatInt(int64(user), 10)
	created := postBil24(t, base, req)
	if code := numberField(t, created, "resultCode"); code != 0 {
		t.Fatalf("CREATE_ORDER_EXT for %s resultCode = %v, want 0 (description %v)", email, code, created["description"])
	}
	orderID = sc6OrderID(t, st, "pw/"+email, created)
	reservationID, checkoutID = sc5OrderRefs(t, st, orderID)
	return sess, user, created, orderID, reservationID, checkoutID
}

func pwPay(t *testing.T, base string, st *harnessState, sess string, user float64, orderRef string) map[string]interface{} {
	t.Helper()
	req, _ := loadWPFixture(t, "PAY_ORDER", "basic")
	req = resolveGolden(req, st, map[string]string{"sessionId": sess, "orderId": orderRef})
	req["fid"] = st.ChannelFID
	req["token"] = st.ChannelToken
	req["userId"] = user
	return postBil24(t, base, req)
}

// ─────────────────────────────────────────────────────────────────────────────
// CREATE_ORDER_EXT: the window fields
// ─────────────────────────────────────────────────────────────────────────────

func TestCompatBil24_PaymentWindow_CreateOrderExtSetsDeadlineAndFields(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	labels := sortedSeatLabels(st)
	if len(labels) < 1 {
		t.Fatalf("seed produced %d seats, need at least 1", len(labels))
	}
	tierWireID := sc6TierWireID(t, st, st.AssignedTierID)

	before := time.Now().UTC()
	sess, user, resp, orderID, reservationID, _ := pwNewOrder(
		t, base, st, actionEventID, tierWireID, "harness-pw-fields@example.test", labels[0], "pw-fields-1")
	after := time.Now().UTC()
	_ = sess
	_ = user

	// Default window (1200s) + default grace (120s) = 1320s from creation.
	wantDeadline := before.Add(1200 * time.Second)
	wantExpiry := before.Add(1320 * time.Second)

	gotDeadline := numberField(t, resp, "paymentDeadline")
	deadlineTime := time.Unix(int64(gotDeadline), 0).UTC()
	if deadlineTime.Before(wantDeadline.Add(-5*time.Second)) || deadlineTime.After(after.Add(1200*time.Second+5*time.Second)) {
		t.Errorf("paymentDeadline = %v, want ~%v (now+1200s, tolerance 5s)", deadlineTime, wantDeadline)
	}
	if got := numberField(t, resp, "paymentTimeout"); got != 1200 {
		t.Errorf("paymentTimeout = %v, want 1200 (the default payment window)", got)
	}

	orderExpiresAt := pwOrderExpiresAt(t, st, orderID)
	reservationExpiresAt := pwReservationExpiresAt(t, st, reservationID)
	if !orderExpiresAt.Equal(reservationExpiresAt) {
		t.Errorf("orders.expires_at %v != reservations.expires_at %v — the payment window and the hold "+
			"must expire at exactly the same instant", orderExpiresAt, reservationExpiresAt)
	}
	if orderExpiresAt.Before(wantExpiry.Add(-5*time.Second)) || orderExpiresAt.After(after.Add(1320*time.Second+5*time.Second)) {
		t.Errorf("orders.expires_at = %v, want ~%v (now+1320s, tolerance 5s)", orderExpiresAt, wantExpiry)
	}
}

// A channel override of the window must be honoured, and the response must
// reflect the OVERRIDE, not the platform default.
func TestCompatBil24_PaymentWindow_ChannelOverrideIsHonoured(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	pwSetChannelPaymentWindow(t, st, 300)

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	labels := sortedSeatLabels(st)
	if len(labels) < 1 {
		t.Fatalf("seed produced %d seats, need at least 1", len(labels))
	}
	tierWireID := sc6TierWireID(t, st, st.AssignedTierID)

	before := time.Now().UTC()
	_, _, resp, orderID, reservationID, _ := pwNewOrder(
		t, base, st, actionEventID, tierWireID, "harness-pw-override@example.test", labels[0], "pw-override-1")

	if got := numberField(t, resp, "paymentTimeout"); got != 300 {
		t.Fatalf("paymentTimeout = %v, want 300 (channel override)", got)
	}
	orderExpiresAt := pwOrderExpiresAt(t, st, orderID)
	// 300s window + 120s default grace = 420s.
	wantExpiry := before.Add(420 * time.Second)
	if d := orderExpiresAt.Sub(wantExpiry); d < -10*time.Second || d > 10*time.Second {
		t.Errorf("orders.expires_at = %v, want ~%v (window override 300s + default grace 120s, tolerance 10s)",
			orderExpiresAt, wantExpiry)
	}
	reservationExpiresAt := pwReservationExpiresAt(t, st, reservationID)
	if !orderExpiresAt.Equal(reservationExpiresAt) {
		t.Errorf("orders.expires_at %v != reservations.expires_at %v", orderExpiresAt, reservationExpiresAt)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Cart RESERVE after the order must never shorten the hold
// ─────────────────────────────────────────────────────────────────────────────

func TestCompatBil24_PaymentWindow_CartReserveDoesNotShortenHold(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	// A short channel window (300s+120s=420s) is still LONGER than nothing,
	// but SHORTER than the platform's default cart TTL (1200s) is NOT what
	// we want here — we want the ORDER's deadline to be the longer one, so
	// the default window (1320s total) against the default cart TTL (1200s)
	// is exactly the regression shape: a plain RESERVE refresh must not claw
	// the reservation back down to 1200s.

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	labels := sortedSeatLabels(st)
	if len(labels) < 2 {
		t.Fatalf("seed produced %d seats, need at least 2", len(labels))
	}
	tierWireID := sc6TierWireID(t, st, st.AssignedTierID)

	sess, user, _, orderID, reservationID, _ := pwNewOrder(
		t, base, st, actionEventID, tierWireID, "harness-pw-noshort@example.test", labels[0], "pw-noshort-1")
	_ = orderID

	beforeReserve := pwReservationExpiresAt(t, st, reservationID)

	// A second RESERVE on the SAME cart (adding another seat) is exactly
	// what the site's cart UI does; it must not shorten expires_at.
	resp := postBil24(t, base, map[string]any{
		"command":       "RESERVATION",
		"fid":           st.ChannelFID,
		"token":         st.ChannelToken,
		"locale":        "ru-RU",
		"type":          "RESERVE",
		"userId":        user,
		"sessionId":     sess,
		"actionEventId": actionEventID,
		"seatList":      []any{map[string]any{"seatId": st.SeatIDs[labels[1]]}},
	})
	if code := numberField(t, resp, "resultCode"); code != 0 {
		t.Fatalf("RESERVE (second seat) resultCode = %v, want 0 (description %v)", code, resp["description"])
	}

	afterReserve := pwReservationExpiresAt(t, st, reservationID)
	if afterReserve.Before(beforeReserve) {
		t.Fatalf("reservations.expires_at moved BACKWARD after a cart RESERVE: %v -> %v — "+
			"the payment window must never be shortened by a cart mutation", beforeReserve, afterReserve)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PAY_ORDER inside the window
// ─────────────────────────────────────────────────────────────────────────────

func TestCompatBil24_PaymentWindow_PayInsideWindowPaysAndIssuesTickets(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	allLabels := sortedSeatLabels(st)
	if len(allLabels) < 1 {
		t.Fatalf("seed produced %d seats, need at least 1", len(allLabels))
	}
	label := allLabels[0]
	tierWireID := sc6TierWireID(t, st, st.AssignedTierID)

	sess, user, _, orderID, _, _ := pwNewOrder(
		t, base, st, actionEventID, tierWireID, "harness-pw-basic@example.test", label, "pw-basic-1")
	systemID := sc5SystemID(t, st, orderID)

	resp := pwPay(t, base, st, sess, user, strconv.FormatInt(systemID, 10))
	if code := numberField(t, resp, "resultCode"); code != 0 {
		t.Fatalf("PAY_ORDER resultCode = %v, want 0 (description %v)", code, resp["description"])
	}
	sc5AssertOrderPaid(t, st, orderID, "woo_bank_card")
	sc5AssertTickets(t, st, orderID, 1, "harness-pw-basic@example.test")
}

// ─────────────────────────────────────────────────────────────────────────────
// PAY_ORDER after the window elapsed
// ─────────────────────────────────────────────────────────────────────────────

func TestCompatBil24_PaymentWindow_PayAfterExpiryExpiresAndReleases(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	allLabels := sortedSeatLabels(st)
	if len(allLabels) < 1 {
		t.Fatalf("seed produced %d seats, need at least 1", len(allLabels))
	}
	label := allLabels[0]
	tierWireID := sc6TierWireID(t, st, st.AssignedTierID)

	sess, user, _, orderID, reservationID, checkoutID := pwNewOrder(
		t, base, st, actionEventID, tierWireID, "harness-pw-late@example.test", label, "pw-late-1")
	_ = checkoutID

	systemID := sc5SystemID(t, st, orderID)

	heldBefore := pwCapacityHeld(t, st, st.AssignedSessID)

	// Simulate: the payment window elapsed and NEITHER sweep has run yet —
	// this is the exact case (d) window PAY_ORDER must close itself.
	pwBackdateOrderAndReservation(t, st, orderID, reservationID)

	first := pwPay(t, base, st, sess, user, strconv.FormatInt(systemID, 10))
	if code := numberField(t, first, "resultCode"); code != 101 {
		t.Fatalf("PAY_ORDER after expiry resultCode = %v, want 101 bil24.order_expired (description %v)",
			code, first["description"])
	}
	if desc, _ := first["description"].(string); desc == "" {
		t.Error("bil24.order_expired carries an empty description; the site renders it verbatim")
	}

	sc5AssertOrderStatus(t, st, orderID, "expired")
	if got := pwReservationState(t, st, reservationID); got != "expired" {
		t.Errorf("reservations.state = %q, want expired", got)
	}
	if got := pwSeatStatus(t, st, st.SeatIDs[label]); got != "available" {
		t.Errorf("session_seats.status = %q, want available — the seat must go back on sale", got)
	}
	heldAfter := pwCapacityHeld(t, st, st.AssignedSessID)
	if heldAfter >= heldBefore {
		t.Errorf("inventory_ledger.capacity_held = %d, want less than %d (the seat's capacity was released)",
			heldAfter, heldBefore)
	}
	sc5AssertPaymentIntentCount(t, st, orderID, 0)
	sc5AssertTicketCount(t, st, orderID, 0)
	pwAssertNoManualReview(t, st, orderID)

	baselineExpired := pwCountOrderEvents(t, st, orderID, "hold_expired")

	// The replay: same shape as the WordPress plugin's non-zero retry.
	second := pwPay(t, base, st, sess, user, strconv.FormatInt(systemID, 10))
	if code := numberField(t, second, "resultCode"); code != 101 {
		t.Fatalf("replayed PAY_ORDER resultCode = %v, want 101 (description %v)", code, second["description"])
	}
	sc5AssertOrderStatus(t, st, orderID, "expired")
	if got := pwCountOrderEvents(t, st, orderID, "hold_expired"); got != baselineExpired {
		t.Errorf("hold_expired events after the REPLAYED PAY_ORDER = %d, want still %d — "+
			"a replay must write nothing new", got, baselineExpired)
	}
	sc5AssertPaymentIntentCount(t, st, orderID, 0)
	sc5AssertTicketCount(t, st, orderID, 0)
}

// ─────────────────────────────────────────────────────────────────────────────
// An already-expired order never revives
// ─────────────────────────────────────────────────────────────────────────────

func TestCompatBil24_PaymentWindow_ExpiredOrderNeverRevives(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	allLabels := sortedSeatLabels(st)
	if len(allLabels) < 1 {
		t.Fatalf("seed produced %d seats, need at least 1", len(allLabels))
	}
	label := allLabels[0]
	tierWireID := sc6TierWireID(t, st, st.AssignedTierID)

	sess, user, _, orderID, reservationID, _ := pwNewOrder(
		t, base, st, actionEventID, tierWireID, "harness-pw-norevive@example.test", label, "pw-norevive-1")
	systemID := sc5SystemID(t, st, orderID)

	// Expire it first (via a normal late PAY_ORDER, exactly like the previous
	// test), THEN try to pay it again — this must NOT resurrect it.
	pwBackdateOrderAndReservation(t, st, orderID, reservationID)
	first := pwPay(t, base, st, sess, user, strconv.FormatInt(systemID, 10))
	if code := numberField(t, first, "resultCode"); code != 101 {
		t.Fatalf("PAY_ORDER after expiry resultCode = %v, want 101", code)
	}
	sc5AssertOrderStatus(t, st, orderID, "expired")

	second := pwPay(t, base, st, sess, user, strconv.FormatInt(systemID, 10))
	if code := numberField(t, second, "resultCode"); code != 101 {
		t.Fatalf("PAY_ORDER on an already-expired order resultCode = %v, want 101 bil24.order_expired "+
			"(description %v) — no revival, by owner decision", code, second["description"])
	}
	sc5AssertOrderStatus(t, st, orderID, "expired")
	sc5AssertPaymentIntentCount(t, st, orderID, 0)
	sc5AssertTicketCount(t, st, orderID, 0)
	pwAssertNoManualReview(t, st, orderID)
}

// ─────────────────────────────────────────────────────────────────────────────
// A hold lost to someone else within the window cancels the order
// ─────────────────────────────────────────────────────────────────────────────

func TestCompatBil24_PaymentWindow_HoldStolenWithinWindowCancelsOrder(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	allLabels := sortedSeatLabels(st)
	if len(allLabels) < 2 {
		t.Fatalf("seed produced %d seats, need at least 2", len(allLabels))
	}
	tierWireID := sc6TierWireID(t, st, st.AssignedTierID)

	// `other`'s order is the safe FK-valid steal target: its identity does
	// not matter, only that its reservation row exists.
	_, _, _, otherOrderID, otherReservationID, _ := pwNewOrder(
		t, base, st, actionEventID, tierWireID, "harness-pw-steal-other@example.test", allLabels[1], "pw-steal-other")
	_ = otherOrderID

	sess, user, _, orderID, reservationID, checkoutID := pwNewOrder(
		t, base, st, actionEventID, tierWireID, "harness-pw-steal-lost@example.test", allLabels[0], "pw-steal-lost")
	systemID := sc5SystemID(t, st, orderID)

	// The hold's own TTL passes but the payment window has NOT (so this is
	// case c, not case d): the reservation itself expires, and the seat gets
	// re-pointed at another reservation while staying 'held' — precisely
	// what a competing buyer's RESERVE would leave behind.
	sc5ExpireReservation(t, st, reservationID)
	sc5StealSeats(t, st, reservationID, otherReservationID)

	resp := pwPay(t, base, st, sess, user, strconv.FormatInt(systemID, 10))
	if code := numberField(t, resp, "resultCode"); code != 101 {
		t.Fatalf("PAY_ORDER on a stolen hold resultCode = %v, want 101 bil24.hold_expired "+
			"(description %v)", code, resp["description"])
	}
	if desc, _ := resp["description"].(string); desc == "" {
		t.Error("bil24.hold_expired carries an empty description")
	}

	sc5AssertOrderStatus(t, st, orderID, "cancelled")
	sc5AssertPaymentIntentCount(t, st, orderID, 0)
	sc5AssertTicketCount(t, st, orderID, 0)
	pwAssertNoManualReview(t, st, orderID)

	var reason string
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT payload->>'reason' FROM order_events WHERE order_id=$1 AND type='cancelled'
		 ORDER BY created_at DESC LIMIT 1`, orderID,
	).Scan(&reason); err != nil {
		t.Fatalf("read cancelled event reason for %s: %v", orderID, err)
	}
	if reason != "hold_expired" {
		t.Errorf("order_events.cancelled payload.reason = %q, want hold_expired", reason)
	}
	_ = checkoutID
}

// ─────────────────────────────────────────────────────────────────────────────
// Concurrency: PAY_ORDER at the deadline boundary
// ─────────────────────────────────────────────────────────────────────────────

// TestCompatBil24_PaymentWindow_ConcurrentPayOrderAtDeadline fires 10
// concurrent PAY_ORDER calls for the same order, backdated to be right at
// (past) the payment deadline. Every call must answer either 0 (at most
// once) or 101, and afterwards the order/inventory must be in EXACTLY one
// consistent terminal state — never double-paid, never leaking or
// double-releasing capacity.
func TestCompatBil24_PaymentWindow_ConcurrentPayOrderAtDeadline(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	allLabels := sortedSeatLabels(st)
	if len(allLabels) < 1 {
		t.Fatalf("seed produced %d seats, need at least 1", len(allLabels))
	}
	label := allLabels[0]
	tierWireID := sc6TierWireID(t, st, st.AssignedTierID)

	sess, user, _, orderID, reservationID, _ := pwNewOrder(
		t, base, st, actionEventID, tierWireID, "harness-pw-race@example.test", label, "pw-race-1")
	systemID := sc5SystemID(t, st, orderID)
	orderRef := strconv.FormatInt(systemID, 10)

	// Backdate to the boundary: the order IS past its deadline, so every
	// racer takes the expire branch unless (rarely) it wins the lock before
	// another already flipped it — either way, exactly one terminal outcome.
	pwBackdateOrderAndReservation(t, st, orderID, reservationID)

	const n = 10
	results := make([]float64, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			resp := pwPay(t, base, st, sess, user, orderRef)
			results[i] = numberField(t, resp, "resultCode")
		}(i)
	}
	wg.Wait()

	var zeros, oneOhOnes, other int
	for _, code := range results {
		switch code {
		case 0:
			zeros++
		case 101:
			oneOhOnes++
		default:
			other++
		}
	}
	if other != 0 {
		t.Fatalf("resultCodes = %v, want only 0 or 101", results)
	}
	if zeros > 1 {
		t.Fatalf("resultCode 0 answered %d times, want at most 1 — the order was paid more than once", zeros)
	}
	if zeros+oneOhOnes != n {
		t.Fatalf("got %d responses, want %d", zeros+oneOhOnes, n)
	}

	// Exactly one payment_intent (or zero, if every racer lost to the
	// expiry) and exactly the matching ticket count — never more.
	var intentCount int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM payment_intents pi JOIN orders o ON o.checkout_session_id = pi.checkout_session_id
		 WHERE o.id = $1`, orderID).Scan(&intentCount); err != nil {
		t.Fatalf("count payment intents: %v", err)
	}
	if intentCount > 1 {
		t.Fatalf("payment_intents for the raced order = %d, want at most 1", intentCount)
	}
	var ticketCount int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM tickets WHERE order_id=$1`, orderID).Scan(&ticketCount); err != nil {
		t.Fatalf("count tickets: %v", err)
	}
	if ticketCount > 1 {
		t.Fatalf("tickets for the raced order = %d, want at most 1", ticketCount)
	}
	if zeros == 1 && (intentCount != 1 || ticketCount != 1) {
		t.Fatalf("resultCode 0 was answered but intentCount=%d ticketCount=%d, want both 1", intentCount, ticketCount)
	}
	if zeros == 0 && intentCount != 0 {
		t.Fatalf("no resultCode 0 was answered but intentCount=%d, want 0 — capacity/money must not move "+
			"without a corresponding successful answer", intentCount)
	}

	var status string
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT status FROM orders WHERE id=$1`, orderID).Scan(&status); err != nil {
		t.Fatalf("read final order status: %v", err)
	}
	if status != "paid" && status != "expired" && status != "cancelled" {
		t.Fatalf("final orders.status = %q, want paid/expired/cancelled", status)
	}
	if zeros == 1 && status != "paid" {
		t.Fatalf("resultCode 0 was answered but final status = %q, want paid", status)
	}
	pwAssertNoManualReview(t, st, orderID)
}

// ─────────────────────────────────────────────────────────────────────────────
// CREATE_ORDER_EXT's one-open-order rule (unrelated to revival, still valid)
// ─────────────────────────────────────────────────────────────────────────────

// TestCompatBil24_CreateOrderExt_OpenOrderExistsProtectsLiveHold is case (a):
// two gateway buyers who happen to share the checkout identity the fixture
// carries (buyer@example.com / +420123456789 — the WordPress plugin sends
// whatever the buyer typed, not a stable account id) each reserve their OWN
// seat for the same session. Buyer B's CREATE_ORDER_EXT must be refused with
// 101 bil24.open_order_exists rather than silently expiring buyer A's
// still-live order, and buyer A's order must remain fully payable afterwards.
// The SAME buyer's own repeat CREATE_ORDER_EXT (same reservation) must still
// update in place, unaffected by the new guard.
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

// ─────────────────────────────────────────────────────────────────────────────
// Decision 1 — closing a category never strands an order that already exists
// ─────────────────────────────────────────────────────────────────────────────

// TestCompatBil24_PayOrder_CategoryClosedAfterOrderStillPays is decision 1 of
// plan 08_architecture/23: the close/sale-window gate introduced in step 4
// refuses only NEW holds. An operator who closes a category inside the
// 20-minute payment window must not strand a buyer whose card the shop has
// already charged — the places behind that order are already theirs.
func TestCompatBil24_PayOrder_CategoryClosedAfterOrderStillPays(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	allLabels := sortedSeatLabels(st)
	if len(allLabels) < 1 {
		t.Fatalf("seed produced %d seats, need at least 1", len(allLabels))
	}
	label := allLabels[0]
	tierWireID := sc6TierWireID(t, st, st.AssignedTierID)

	sess, user, _, orderID, _, _ := pwNewOrder(
		t, base, st, actionEventID, tierWireID, "harness-pw-closed@example.test", label, "pw-closed-1")
	systemID := sc5SystemID(t, st, orderID)

	// The operator closes the category AND puts its sale window in the past
	// — both halves of the gate at once — after the order exists.
	if _, err := st.Pool.Exec(context.Background(),
		`UPDATE ticket_tiers
		    SET is_open = false, sale_window_end = now() - interval '1 hour'
		  WHERE id = $1`, st.AssignedTierID,
	); err != nil {
		t.Fatalf("close the category: %v", err)
	}

	resp := pwPay(t, base, st, sess, user, strconv.FormatInt(systemID, 10))
	if code := numberField(t, resp, "resultCode"); code != 0 {
		t.Fatalf("PAY_ORDER after the category was closed resultCode = %v, want 0 (description %v)",
			code, resp["description"])
	}
	sc5AssertOrderPaid(t, st, orderID, "woo_bank_card")
	sc5AssertTickets(t, st, orderID, 1, "harness-pw-closed@example.test")

	// The gate is still armed for NEW business on that category: a fresh
	// RESERVATION of another seat of the same category is refused.
	sess2, user2 := createGatewayUser(t, base, st, "harness-pw-closed-2@example.test")
	blocked := postBil24(t, base, map[string]any{
		"command":       "RESERVATION",
		"fid":           st.ChannelFID,
		"token":         st.ChannelToken,
		"locale":        "ru-RU",
		"type":          "RESERVE",
		"userId":        user2,
		"sessionId":     sess2,
		"actionEventId": actionEventID,
		"seatList":      []any{st.SeatIDs[allLabels[1]]},
	})
	if code := numberField(t, blocked, "resultCode"); code == 0 {
		t.Errorf("RESERVATION on a closed category resultCode = 0, want a refusal (description %v)",
			blocked["description"])
	}
}

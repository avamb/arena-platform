//go:build integration

// scenario05_pay_order_test.go — spec §15.3 scenario 5, PAY_ORDER (feature
// #494, W1-B2a, spec §7.9).
//
// PAY_ORDER is the only command where the money has ALREADY moved before we
// are called: WooCommerce charged the card and is reporting the fact. That
// asymmetry is what this scenario exists to pin down, over the REAL server
// against a live database — real chi router, real hbil24 handler, real
// ordering aggregate, real htickets issuance. Nothing below HTTP is stubbed.
//
// The four checked-in cases, in the order the shop performs them:
//
//	basic                 a live hold → one transaction (payment_intents
//	                      provider=manual, checkout_sessions completed,
//	                      reservation converted, orders paid) and SYNCHRONOUS
//	                      issuance, so the site's first GET_TICKETS_BY_ORDER
//	                      poll already finds the ticket
//	repeat                the shop's post-timeout replay is a no-op 0 — not a
//	                      second payment intent and not a second ticket
//	expired_reacquired    a hold past its TTL whose seats are still ours is
//	                      re-taken, recorded as order_events.hold_reacquired,
//	                      and the payment proceeds to 0
//	expired_manual_review a hold past its TTL whose seat now belongs to another
//	                      reservation cannot be restored: the order and its
//	                      checkout session are parked in manual_review,
//	                      order_events.hold_expired is written, an operator
//	                      audit alert is raised, and the answer is 101 — the
//	                      ONLY 101 after payment (§7.9 step 2)
//
// Everything of substance is asserted against the database rather than the
// envelope, because assertGoldenKeySet compares KEY SETS only: all four
// goldens carry the same three keys, so a golden alone can distinguish
// nothing here.
package compat_bil24_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/google/uuid"
)

// sc5Order is one buyer's fully-created (but unpaid) order plus the gateway
// credentials needed to pay for it.
type sc5Order struct {
	sess        string
	user        float64
	orderID     uuid.UUID
	externalRef string
}

// runScenario05PayOrder is the body of the 05_expired_hold_on_pay_order
// sub-test.
func runScenario05PayOrder(t *testing.T, st *harnessState) {
	t.Helper()

	// The server is booted FIRST on purpose: startHarnessServer registers the
	// wire-row sweep with t.Cleanup, and t.Cleanup is LIFO. Anything this
	// scenario creates afterwards is therefore torn down before that sweep
	// reaches the reservations it depends on.
	base := startHarnessServer(t, st)

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	allLabels := sortedSeatLabels(st)
	if len(allLabels) < 8 {
		t.Fatalf("seed produced %d seats for the assigned-seats session, need at least 8", len(allLabels))
	}
	// This scenario is the only one that SELLS seats (a paid order converts the
	// reservation and the seats become status='sold' for the rest of the run),
	// so it takes them from the tail of the list. The scenarios that follow —
	// 06 uses labels[0]/labels[1] — must still find free inventory at the head.
	// labels[len-1] is reserved for the scenario 4 refund flow.
	labels := []string{
		allLabels[len(allLabels)-2],
		allLabels[len(allLabels)-3],
		allLabels[len(allLabels)-4],
	}
	// Spec §4: categoryPriceId travels as the int64 compatibility id, never as
	// the platform UUID. sc6TierWireID mints it.
	tierWireID := sc6TierWireID(t, st, st.AssignedTierID)

	// payment_intents.provider_payment_id — 'wc:<external_ref>:<method>' — is
	// covered by a GLOBAL unique index, unscoped by org. A literal shop order
	// number would therefore collide with a leftover row from an interrupted
	// run against the shared dev-stand, so every ref carries a per-run tag.
	runTag := uuid.NewString()[:8]
	ref := func(n string) string { return n + "-" + runTag }

	// newOrder walks a fresh buyer all the way to a pending_payment order:
	// CREATE_USER → RESERVATION → CREATE_ORDER_EXT. Each buyer gets their own
	// seat, so the four cases never contend for inventory.
	newOrder := func(email, seatLabel, externalRef string) sc5Order {
		t.Helper()
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
			"seatList":      []any{map[string]any{"seatId": st.SeatIDs[seatLabel]}},
		})
		if code := numberField(t, reserved, "resultCode"); code != 0 {
			t.Fatalf("RESERVE %s resultCode = %v, want 0 (description %v)",
				seatLabel, code, reserved["description"])
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
		created := postBil24(t, base, req)
		if code := numberField(t, created, "resultCode"); code != 0 {
			t.Fatalf("CREATE_ORDER_EXT for %s resultCode = %v, want 0 (description %v)",
				email, code, created["description"])
		}
		return sc5Order{
			sess:        sess,
			user:        user,
			orderID:     sc6OrderID(t, "sc5/"+email, created),
			externalRef: externalRef,
		}
	}

	// pay sends a checked-in PAY_ORDER fixture for one buyer and asserts the
	// golden's key set. `orderRef` is what the site echoes back to us as
	// orderId — the spec names orders.system_id, and the transitional gateway
	// also answers with the platform UUID, so both shapes are exercised.
	pay := func(caseName string, o sc5Order, orderRef string) map[string]interface{} {
		t.Helper()
		req, gld := loadWPFixture(t, "PAY_ORDER", caseName)
		rt := map[string]string{"sessionId": o.sess, "orderId": orderRef}
		req = resolveGolden(req, st, rt)
		req["fid"] = st.ChannelFID
		req["token"] = st.ChannelToken
		req["userId"] = o.user
		resp := postBil24(t, base, req)
		assertGoldenKeySet(t, resp, resolveGolden(gld, st, rt))
		return resp
	}

	// ── case basic: a live hold pays, and the ticket exists immediately ─────
	basicOrder := newOrder("harness-494-basic@example.test", labels[0], ref("1001"))
	// The spec's own lookup key: orders.system_id on the wire, as a bigint.
	basicSystemID := sc5SystemID(t, st, basicOrder.orderID)

	basic := pay("basic", basicOrder, strconv.FormatInt(basicSystemID, 10))
	if code := numberField(t, basic, "resultCode"); code != 0 {
		t.Fatalf("PAY_ORDER basic resultCode = %v, want 0 (description %v)",
			code, basic["description"])
	}

	// §7.9 step 4 — the order side of the transaction.
	sc5AssertOrderPaid(t, st, basicOrder.orderID, "woo_bank_card")
	// §7.9 step 4 — the money side. amount is the ORDER's total, never the
	// figure the shop reported: arena's ledger must not inherit the site's
	// arithmetic.
	sc5AssertPaymentIntent(t, st, basicOrder.orderID,
		"wc:"+basicOrder.externalRef+":woo_bank_card", 525, "CZK")
	// §7.9 step 4 — the inventory side.
	sc5AssertStates(t, st, basicOrder.orderID, "completed", "converted")
	// §7.9 step 5 — SYNCHRONOUS issuance. If this were left to the worker the
	// site's first GET_TICKETS_BY_ORDER poll (§7.10) would be a coin flip.
	sc5AssertTickets(t, st, basicOrder.orderID, 1, "buyer@example.com")
	// §7.9 step 4 tail — a paying buyer becomes a customer of the org.
	sc5AssertCustomerLinked(t, st, basicOrder.orderID)
	// §7.9 step 6 — the WordPress shop mails its own PDF; two e-mails per
	// buyer is a support incident, so arena's delivery must stay silent while
	// settings.gateway.platform_email is false.
	sc5AssertNoDeliveryJobs(t, st, basicOrder.orderID)
	// Step 3's amount check is non-blocking, but 525 matches exactly here, so
	// nothing may have been recorded.
	sc5AssertNoOrderEvent(t, st, basicOrder.orderID, "amount_mismatch")

	// ── case repeat: the shop's post-timeout replay is an idempotent 0 ──────
	//
	// This time the platform UUID is used as orderId, which is what
	// CREATE_ORDER_EXT actually answered with — a site echoing our own id back
	// must not be punished for our transitional format.
	repeat := pay("repeat", basicOrder, basicOrder.orderID.String())
	if code := numberField(t, repeat, "resultCode"); code != 0 {
		t.Fatalf("PAY_ORDER repeat resultCode = %v, want 0 (description %v)",
			code, repeat["description"])
	}
	// The replay must have written nothing: a second payment intent would
	// double-count revenue and a second ticket would double-count inventory.
	sc5AssertPaymentIntentCount(t, st, basicOrder.orderID, 1)
	sc5AssertTickets(t, st, basicOrder.orderID, 1, "buyer@example.com")

	// ── case expired_reacquired: a stale hold on seats still ours ───────────
	//
	// Backdating expires_at is exactly what the TTL sweeper's clock does; the
	// seats are still held BY THIS RESERVATION, so ReacquireHoldTx re-takes
	// them and the payment proceeds normally.
	reacq := newOrder("harness-494-reacquire@example.test", labels[1], ref("1003"))
	reacqReservation, _ := sc5OrderRefs(t, st, reacq.orderID)
	sc5ExpireReservation(t, st, reacqReservation)

	reacqResp := pay("expired_reacquired", reacq, strconv.FormatInt(sc5SystemID(t, st, reacq.orderID), 10))
	if code := numberField(t, reacqResp, "resultCode"); code != 0 {
		t.Fatalf("PAY_ORDER expired_reacquired resultCode = %v, want 0 (description %v)",
			code, reacqResp["description"])
	}
	sc5AssertOrderPaid(t, st, reacq.orderID, "woo_bank_card")
	sc5AssertStates(t, st, reacq.orderID, "completed", "converted")
	sc5AssertTickets(t, st, reacq.orderID, 1, "buyer@example.com")
	// The re-take must be on the record: a silently extended hold is a hold
	// nobody can reconcile against the sweeper's log.
	sc5AssertOrderEvent(t, st, reacq.orderID, "hold_reacquired")

	// ── case expired_manual_review: a stale hold on a seat now lost ─────────
	//
	// Backdating alone is not enough to lose the inventory, so the seat is
	// re-pointed at ANOTHER reservation while staying 'held' — precisely the
	// state a competing buyer's RESERVE would have produced in the window.
	// ReacquireHoldTx then reports a seat conflict and the order is parked.
	lost := newOrder("harness-494-manual@example.test", labels[2], ref("1004"))
	lostReservation, lostCheckout := sc5OrderRefs(t, st, lost.orderID)
	sc5ExpireReservation(t, st, lostReservation)
	// The basic case's reservation is 'converted' and owns no session_seats
	// row any more, which makes it a safe FK-valid stand-in for "somebody
	// else's hold".
	basicReservation, _ := sc5OrderRefs(t, st, basicOrder.orderID)
	sc5StealSeats(t, st, lostReservation, basicReservation)

	manual := pay("expired_manual_review", lost, strconv.FormatInt(sc5SystemID(t, st, lost.orderID), 10))
	if code := numberField(t, manual, "resultCode"); code != 101 {
		t.Fatalf("PAY_ORDER expired_manual_review resultCode = %v, want 101 bil24.hold_expired "+
			"(description %v)", code, manual["description"])
	}
	if desc, _ := manual["description"].(string); desc == "" {
		t.Error("bil24.hold_expired carries an empty description; §7.9 has the site render it verbatim")
	}

	// The park itself. Both halves matter: an order in manual_review whose
	// checkout session still looks payable would be re-paid by the next poll.
	sc5AssertOrderStatus(t, st, lost.orderID, "manual_review")
	sc5AssertCheckoutState(t, st, lostCheckout, "manual_review")
	sc5AssertOrderEvent(t, st, lost.orderID, "hold_expired")
	// The payment transaction was rolled back wholesale — no intent, no
	// tickets, nothing half-written.
	sc5AssertPaymentIntentCount(t, st, lost.orderID, 0)
	sc5AssertTicketCount(t, st, lost.orderID, 0)
	// §7.9 step 2: the buyer has been charged and nobody can fix that from a
	// log line alone, so an operator alert is mandatory.
	sc5AssertManualReviewAlert(t, st, lost.orderID)
}

// ─────────────────────────────────────────────────────────────────────────────
// fixture manipulation
// ─────────────────────────────────────────────────────────────────────────────

// sc5SystemID reads the bigint wire id of an order — the key spec §7.9 step 1
// names for the lookup.
func sc5SystemID(t *testing.T, st *harnessState, orderID uuid.UUID) int64 {
	t.Helper()
	var systemID int64
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT system_id FROM orders WHERE id=$1`, orderID).Scan(&systemID); err != nil {
		t.Fatalf("read orders.system_id for %s: %v", orderID, err)
	}
	return systemID
}

// sc5OrderRefs returns the order's reservation and checkout session.
func sc5OrderRefs(t *testing.T, st *harnessState, orderID uuid.UUID) (uuid.UUID, uuid.UUID) {
	t.Helper()
	var reservationID, checkoutID uuid.UUID
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT reservation_id, checkout_session_id FROM orders WHERE id=$1`, orderID,
	).Scan(&reservationID, &checkoutID); err != nil {
		t.Fatalf("read order refs for %s: %v", orderID, err)
	}
	return reservationID, checkoutID
}

// sc5ExpireReservation backdates a hold past its TTL, reproducing what the
// clock does while the buyer is on the WooCommerce payment page. The state is
// deliberately left mutable: an 'active' reservation with a past expires_at is
// exactly the row PAY_ORDER has to re-take.
func sc5ExpireReservation(t *testing.T, st *harnessState, reservationID uuid.UUID) {
	t.Helper()
	if _, err := st.Pool.Exec(context.Background(),
		`UPDATE reservations SET expires_at = now() - interval '1 hour' WHERE id=$1`,
		reservationID,
	); err != nil {
		t.Fatalf("expire reservation %s: %v", reservationID, err)
	}
}

// sc5StealSeats re-points every session_seats row held by `from` at `to`,
// leaving the status 'held'. That is the shape a competing buyer's RESERVE
// leaves behind once the original hold lapses, and it is the only condition
// under which ReacquireHoldTx legitimately fails.
func sc5StealSeats(t *testing.T, st *harnessState, from, to uuid.UUID) {
	t.Helper()
	tag, err := st.Pool.Exec(context.Background(),
		`UPDATE session_seats SET reservation_id=$2 WHERE reservation_id=$1`, from, to)
	if err != nil {
		t.Fatalf("re-point session_seats from %s to %s: %v", from, to, err)
	}
	if tag.RowsAffected() == 0 {
		t.Fatalf("reservation %s held no session_seats; the manual-review case would "+
			"silently test nothing", from)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// assertions
// ─────────────────────────────────────────────────────────────────────────────

// sc5AssertOrderPaid pins the §7.9 step 4 order projection.
func sc5AssertOrderPaid(t *testing.T, st *harnessState, orderID uuid.UUID, wantMethod string) {
	t.Helper()
	var (
		status        string
		paidAt        *string
		paymentMethod *string
	)
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT status, paid_at::text, payment_method FROM orders WHERE id=$1`, orderID,
	).Scan(&status, &paidAt, &paymentMethod); err != nil {
		t.Fatalf("read order %s: %v", orderID, err)
	}
	if status != "paid" {
		t.Errorf("orders.status = %q, want paid", status)
	}
	if paidAt == nil {
		t.Error("orders.paid_at is NULL after PAY_ORDER")
	}
	if paymentMethod == nil || *paymentMethod != wantMethod {
		t.Errorf("orders.payment_method = %v, want %q — the site's `method` is what an "+
			"operator reconciles the WooCommerce transaction against", paymentMethod, wantMethod)
	}
}

func sc5AssertOrderStatus(t *testing.T, st *harnessState, orderID uuid.UUID, want string) {
	t.Helper()
	var status string
	var paidAt *string
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT status, paid_at::text FROM orders WHERE id=$1`, orderID,
	).Scan(&status, &paidAt); err != nil {
		t.Fatalf("read order %s: %v", orderID, err)
	}
	if status != want {
		t.Errorf("orders.status = %q, want %q", status, want)
	}
	if want == "manual_review" && paidAt != nil {
		t.Errorf("orders.paid_at = %v on a parked order; the payment transaction was "+
			"rolled back, so nothing may claim it succeeded", *paidAt)
	}
}

// sc5AssertPaymentIntent pins the §7.9 step 4 provider reference. The
// wc:<external_ref>:<method> shape is what an operator greps for when the shop
// and the platform disagree about a transaction.
func sc5AssertPaymentIntent(
	t *testing.T,
	st *harnessState,
	orderID uuid.UUID,
	wantProviderPaymentID string,
	wantAmount int64,
	wantCurrency string,
) {
	t.Helper()
	var (
		provider, state, currency string
		providerPaymentID         *string
		amount                    int64
	)
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT pi.provider, pi.provider_payment_id, pi.amount, pi.currency, pi.state
		 FROM   payment_intents pi
		 JOIN   orders o ON o.checkout_session_id = pi.checkout_session_id
		 WHERE  o.id = $1`, orderID,
	).Scan(&provider, &providerPaymentID, &amount, &currency, &state); err != nil {
		t.Fatalf("read payment intent for order %s: %v", orderID, err)
	}
	if provider != "manual" {
		t.Errorf("payment_intents.provider = %q, want manual — WooCommerce took the money, "+
			"there is no PSP for arena to name", provider)
	}
	if state != "succeeded" {
		t.Errorf("payment_intents.state = %q, want succeeded", state)
	}
	if providerPaymentID == nil || *providerPaymentID != wantProviderPaymentID {
		t.Errorf("payment_intents.provider_payment_id = %v, want %q",
			providerPaymentID, wantProviderPaymentID)
	}
	if amount != wantAmount {
		t.Errorf("payment_intents.amount = %d, want %d (orders.total, NOT the reported amount)",
			amount, wantAmount)
	}
	if currency != wantCurrency {
		t.Errorf("payment_intents.currency = %q, want %q", currency, wantCurrency)
	}
}

func sc5AssertPaymentIntentCount(t *testing.T, st *harnessState, orderID uuid.UUID, want int) {
	t.Helper()
	var got int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*)
		 FROM   payment_intents pi
		 JOIN   orders o ON o.checkout_session_id = pi.checkout_session_id
		 WHERE  o.id = $1`, orderID).Scan(&got); err != nil {
		t.Fatalf("count payment intents for %s: %v", orderID, err)
	}
	if got != want {
		t.Errorf("payment_intents for order %s = %d, want %d", orderID, got, want)
	}
}

// sc5AssertStates pins the checkout session and reservation transitions that
// share the payment transaction.
func sc5AssertStates(t *testing.T, st *harnessState, orderID uuid.UUID, wantCheckout, wantReservation string) {
	t.Helper()
	var checkoutState, reservationState string
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT cs.state, r.state
		 FROM   orders o
		 JOIN   checkout_sessions cs ON cs.id = o.checkout_session_id
		 JOIN   reservations      r  ON r.id  = o.reservation_id
		 WHERE  o.id = $1`, orderID,
	).Scan(&checkoutState, &reservationState); err != nil {
		t.Fatalf("read states for order %s: %v", orderID, err)
	}
	if checkoutState != wantCheckout {
		t.Errorf("checkout_sessions.state = %q, want %q", checkoutState, wantCheckout)
	}
	if reservationState != wantReservation {
		t.Errorf("reservations.state = %q, want %q — an unconverted reservation is a seat "+
			"the TTL sweeper will hand to somebody else", reservationState, wantReservation)
	}
}

func sc5AssertCheckoutState(t *testing.T, st *harnessState, checkoutID uuid.UUID, want string) {
	t.Helper()
	var state string
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT state FROM checkout_sessions WHERE id=$1`, checkoutID).Scan(&state); err != nil {
		t.Fatalf("read checkout session %s: %v", checkoutID, err)
	}
	if state != want {
		t.Errorf("checkout_sessions.state = %q, want %q", state, want)
	}
}

// sc5AssertTickets proves §7.9 step 5 end to end: the right number of tickets
// exist, each carries the buyer's address (so delivery never has to re-derive
// it), each points back at the order, and every order item points forward at
// its ticket.
func sc5AssertTickets(t *testing.T, st *harnessState, orderID uuid.UUID, want int, wantHolder string) {
	t.Helper()
	sc5AssertTicketCount(t, st, orderID, want)

	var missingHolder int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM tickets WHERE order_id=$1
		   AND (holder_email IS NULL OR holder_email <> $2)`, orderID, wantHolder,
	).Scan(&missingHolder); err != nil {
		t.Fatalf("check ticket holder_email for %s: %v", orderID, err)
	}
	if missingHolder != 0 {
		t.Errorf("%d ticket(s) of order %s do not carry holder_email %q — orders.buyer_email "+
			"is the source of truth at issuance", missingHolder, orderID, wantHolder)
	}

	var unlinkedItems int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM order_items WHERE order_id=$1 AND ticket_id IS NULL`, orderID,
	).Scan(&unlinkedItems); err != nil {
		t.Fatalf("check order_items.ticket_id for %s: %v", orderID, err)
	}
	if unlinkedItems != 0 {
		t.Errorf("%d order_items of %s have a NULL ticket_id after issuance", unlinkedItems, orderID)
	}
}

func sc5AssertTicketCount(t *testing.T, st *harnessState, orderID uuid.UUID, want int) {
	t.Helper()
	var got int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM tickets WHERE order_id=$1`, orderID).Scan(&got); err != nil {
		t.Fatalf("count tickets for %s: %v", orderID, err)
	}
	if got != want {
		t.Errorf("tickets for order %s = %d, want %d", orderID, got, want)
	}
}

// sc5AssertCustomerLinked proves the §7.9 step 4 tail: paying makes the buyer a
// customer of the organization, which is what the organizer's CRM reads.
func sc5AssertCustomerLinked(t *testing.T, st *harnessState, orderID uuid.UUID) {
	t.Helper()
	var links int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*)
		 FROM   customer_org_links col
		 JOIN   orders o ON o.customer_id = col.customer_id AND o.org_id = col.org_id
		 WHERE  o.id = $1`, orderID).Scan(&links); err != nil {
		t.Fatalf("count customer_org_links for %s: %v", orderID, err)
	}
	if links == 0 {
		t.Errorf("order %s has no customer_org_links row after payment", orderID)
	}

	// The address the buyer actually paid with is proven real.
	var unverified int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*)
		 FROM   customer_identities ci
		 JOIN   orders o ON o.customer_id = ci.customer_id
		 WHERE  o.id = $1 AND ci.kind = 'email' AND ci.verified_at IS NULL`,
		orderID).Scan(&unverified); err != nil {
		t.Fatalf("count unverified identities for %s: %v", orderID, err)
	}
	if unverified != 0 {
		t.Errorf("%d e-mail identities of order %s are still unverified after payment", unverified, orderID)
	}
}

// sc5AssertNoDeliveryJobs enforces §7.9 step 6. The channel's
// settings.gateway.platform_email defaults to false, and the WordPress shop
// sends its own PDF, so arena must queue nothing.
func sc5AssertNoDeliveryJobs(t *testing.T, st *harnessState, orderID uuid.UUID) {
	t.Helper()
	var got int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*)
		 FROM   delivery_jobs dj
		 JOIN   tickets t ON t.id = dj.ticket_id
		 WHERE  t.order_id = $1`, orderID).Scan(&got); err != nil {
		t.Fatalf("count delivery jobs for %s: %v", orderID, err)
	}
	if got != 0 {
		t.Errorf("delivery_jobs for gateway order %s = %d, want 0 — the shop mails its own "+
			"PDF and two e-mails per buyer is a support incident", orderID, got)
	}
}

// sc5AssertOrderEvent requires at least one order_events row of `typ`, written
// by the gateway principal.
func sc5AssertOrderEvent(t *testing.T, st *harnessState, orderID uuid.UUID, typ string) {
	t.Helper()
	var actor string
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT actor FROM order_events WHERE order_id=$1 AND type=$2
		 ORDER BY created_at DESC LIMIT 1`, orderID, typ,
	).Scan(&actor); err != nil {
		t.Fatalf("order %s has no order_events row of type %q: %v", orderID, typ, err)
	}
	want := "gateway:" + strconv.FormatInt(st.ChannelFID, 10)
	if actor != want {
		t.Errorf("order_events.%s actor = %q, want %q", typ, actor, want)
	}
}

func sc5AssertNoOrderEvent(t *testing.T, st *harnessState, orderID uuid.UUID, typ string) {
	t.Helper()
	var got int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM order_events WHERE order_id=$1 AND type=$2`, orderID, typ,
	).Scan(&got); err != nil {
		t.Fatalf("count order_events %q for %s: %v", typ, orderID, err)
	}
	if got != 0 {
		t.Errorf("order %s has %d %q event(s); the reported amount matched the order total exactly",
			orderID, got, typ)
	}
}

// sc5AssertManualReviewAlert proves the operator really is told. A parked paid
// order that only produces a log line is a buyer with a charge and no ticket
// and nobody watching.
func sc5AssertManualReviewAlert(t *testing.T, st *harnessState, orderID uuid.UUID) {
	t.Helper()
	var raw []byte
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT metadata FROM audit_events
		 WHERE  action = 'bil24.pay_order.manual_review'
		   AND  resource_type = 'order'
		   AND  resource_id = $1
		 ORDER BY occurred_at DESC LIMIT 1`, orderID.String(),
	).Scan(&raw); err != nil {
		t.Fatalf("no manual-review audit alert for order %s: %v", orderID, err)
	}
	var md map[string]interface{}
	if err := json.Unmarshal(raw, &md); err != nil {
		t.Fatalf("parse audit metadata %s: %v", raw, err)
	}
	// audit_events.actor_id is a uuid column, so the gateway's principal label
	// has to travel as metadata — passing it as ActorID aborts the enclosing
	// transaction with SQLSTATE 22P02.
	wantActor := "gateway:" + strconv.FormatInt(st.ChannelFID, 10)
	if got, _ := md["actor"].(string); got != wantActor {
		t.Errorf("audit metadata.actor = %#v, want %q", md["actor"], wantActor)
	}
	if got, _ := md["reason"].(string); got != "hold_expired" {
		t.Errorf("audit metadata.reason = %#v, want \"hold_expired\"", md["reason"])
	}
	if _, ok := md["external_ref"]; !ok {
		t.Errorf("audit metadata has no external_ref; an operator cannot find the "+
			"WooCommerce order without it: %s", raw)
	}
}

// TestCompatBil24_494_PayOrderGoldensExist keeps the checked-in §7.9 fixtures
// honest without needing a database. PAY_ORDER's envelope is the bare
// three-key error/OK shape, so the interesting invariant is the RESULT CODES:
// three of the four cases must be 0, and exactly one — the hold that could not
// be re-taken — may be 101. A golden drifting to any other code would be
// asserting that arena refuses a payment the shop has already taken.
func TestCompatBil24_494_PayOrderGoldensExist(t *testing.T) {
	want := map[string]float64{
		"basic":                 0,
		"repeat":                0,
		"expired_reacquired":    0,
		"expired_manual_review": 101,
	}
	for name, wantCode := range want {
		gld := mustReadJSON(t, filepath.Join("testdata", "wp", "golden", "PAY_ORDER", name+".json"))
		for _, k := range []string{"resultCode", "description", "command"} {
			if _, ok := gld[k]; !ok {
				t.Errorf("golden PAY_ORDER/%s.json has no %s", name, k)
			}
		}
		if got, _ := gld["resultCode"].(float64); got != wantCode {
			t.Errorf("golden PAY_ORDER/%s.json resultCode = %v, want %v", name, got, wantCode)
		}
		if got, _ := gld["command"].(string); got != "PAY_ORDER" {
			t.Errorf("golden PAY_ORDER/%s.json command = %q, want PAY_ORDER", name, got)
		}
		if desc, _ := gld["description"].(string); desc == "" {
			t.Errorf("golden PAY_ORDER/%s.json has an empty description; the site renders "+
				"it verbatim to the buyer", name)
		}

		req := mustReadJSON(t, filepath.Join("testdata", "wp", "requests", "PAY_ORDER", name+".json"))
		for _, k := range []string{"orderId", "sessionId", "token", "amount", "currency", "method"} {
			if _, ok := req[k]; !ok {
				t.Errorf("request PAY_ORDER/%s.json has no %s", name, k)
			}
		}
		// The order id is minted per run, so it can only travel as a
		// placeholder — a hard-coded id would address another run's order.
		if got, _ := req["orderId"].(string); got != "{{orderId}}" {
			t.Errorf("request PAY_ORDER/%s.json orderId = %#v, want the {{orderId}} placeholder",
				name, req["orderId"])
		}
		// 525 = the seeded CZK 500 seat plus the channel's 5 %% charge. A
		// different figure would silently exercise §7.9 step 3's
		// amount_mismatch branch instead of the happy path.
		if got, _ := req["amount"].(float64); got != 525 {
			t.Errorf("request PAY_ORDER/%s.json amount = %v, want 525 (the seeded order total)",
				name, got)
		}
	}
}

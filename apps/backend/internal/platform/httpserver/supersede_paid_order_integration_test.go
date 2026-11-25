//go:build integration

// supersede_paid_order_integration_test.go — a buyer's second checkout for
// the same session must never cancel an order that can still be paid, and a
// payment for an order that WAS cancelled must never answer 500 (the provider
// would retry it forever). Live database and the real router, AllPay stubbed.
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ -run 'TestSupersede'
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/latepayment"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

// supersedeStart starts a checkout of 2 places and returns its checkout id.
// An empty email or phone keeps the fixture's.
func supersedeStart(t *testing.T, srv *Server, f *allpayFixture, email, phone string) uuid.UUID {
	t.Helper()
	extra := map[string]any{}
	if email != "" || phone != "" {
		if email == "" {
			email = f.buyerMail
		}
		if phone == "" {
			phone = f.buyerPhone
		}
		extra["buyer"] = map[string]any{"email": email, "name": "Late Buyer", "phone": phone}
	}
	st := lateStartCheckout(t, srv, f, extra)
	return uuid.MustParse(st.CheckoutSession.ID)
}

// payViaAllPay marks the checkout's AllPay page paid and posts the signed
// webhook; it fails the test unless the answer is 200.
func payViaAllPay(t *testing.T, srv *Server, stub *stubAllPayServer, f *allpayFixture, q *gen.Queries, csID uuid.UUID) gen.PaymentIntentRow {
	t.Helper()
	pi, err := q.GetPaymentIntentByProviderID(t.Context(), csID.String())
	if err != nil {
		t.Fatalf("payment intent of %s: %v", csID, err)
	}
	stub.markPaid(t, csID.String())
	if code, body := postAllPayWebhook(t, srv, f.configWebhookPath(), f.webhook(t, csID.String(), 1, pi.Amount, f.apiKey)); code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", code, body)
	}
	return pi
}

func issueJobs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, csID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = 'checkout.issue_tickets' AND payload->>'checkout_session_id' = $1`,
		csID.String()).Scan(&n); err != nil {
		t.Fatalf("count issue jobs: %v", err)
	}
	return n
}

func failIntentsOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, csID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE payment_intents SET state = 'failed', failed_at = now() WHERE checkout_session_id = $1`, csID); err != nil {
		t.Fatalf("fail intents: %v", err)
	}
}

// Same e-mail, the first checkout's page is still open (its intent is
// 'created'): the first order stays pending, the second is minted without a
// customer link, and the first one's payment pays it and issues tickets.
func TestSupersede_SameEmailLiveAttemptKeepsFirstOrderPayable(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, done := lateFixture(t, ctx, pool, "sup1")
	defer done()
	stub := newStubAllPayServer(t, f)
	srv := allpayServer(t, pool, stub)
	q := gen.New(pool)

	firstCS := supersedeStart(t, srv, f, "", "")
	secondCS := supersedeStart(t, srv, f, "", "")

	first, _ := q.GetOrderByCheckoutSession(ctx, firstCS)
	second, _ := q.GetOrderByCheckoutSession(ctx, secondCS)
	if first.Status != ordering.StatusPendingPayment || first.CustomerID == nil {
		t.Fatalf("first order = %s customer %v; want pending with its customer", first.Status, first.CustomerID)
	}
	if second.Status != ordering.StatusPendingPayment || second.CustomerID != nil {
		t.Fatalf("second order = %s customer %v; want pending without a customer link", second.Status, second.CustomerID)
	}
	if held := heldOf(t, ctx, pool, f.sessionID); held != 4 {
		t.Fatalf("held = %d; both holds must stay", held)
	}

	payViaAllPay(t, srv, stub, f, q, firstCS)
	first, _ = q.GetOrderByCheckoutSession(ctx, firstCS)
	if first.Status != ordering.StatusPaid {
		t.Fatalf("first order after payment = %s; want paid", first.Status)
	}
	if n := issueJobs(t, ctx, pool, firstCS); n != 1 {
		t.Fatalf("issue jobs = %d; want the tickets to go out", n)
	}
	if second, _ = q.GetOrderByCheckoutSession(ctx, secondCS); second.Status != ordering.StatusPendingPayment {
		t.Fatalf("second order = %s; want it still pending", second.Status)
	}
}

// Same phone, another e-mail: customers.Resolve joins both into one customer,
// but the first order is another person's purchase — never cancelled, even
// with every payment attempt failed.
func TestSupersede_OtherEmailSamePhoneLeavesFirstOrder(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, done := lateFixture(t, ctx, pool, "sup2")
	defer done()
	stub := newStubAllPayServer(t, f)
	srv := allpayServer(t, pool, stub)
	q := gen.New(pool)

	// A valid E.164 mobile number (the fixture's +3632 + 7 digits does not
	// normalize, and Resolve would then never join the two e-mails),
	// unique per run because customer_identities_strong_uq is global.
	phone := "+3630" + f.buyerPhone[5:]
	firstCS := supersedeStart(t, srv, f, "", phone)
	// With failed attempts a SAME e-mail would close the first order; only
	// the e-mail difference keeps it here.
	failIntentsOf(t, ctx, pool, firstCS)
	secondCS := supersedeStart(t, srv, f, "other-"+f.buyerMail, phone)

	first, _ := q.GetOrderByCheckoutSession(ctx, firstCS)
	second, _ := q.GetOrderByCheckoutSession(ctx, secondCS)
	// The premise: one customer behind both e-mails.
	var custOfSecondMail uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT customer_id FROM customer_identities WHERE value_normalized = $1`,
		"other-"+f.buyerMail).Scan(&custOfSecondMail); err != nil || first.CustomerID == nil || custOfSecondMail != *first.CustomerID {
		t.Fatalf("second e-mail customer = %v (%v), first order customer %v; want one joined customer", custOfSecondMail, err, first.CustomerID)
	}
	if first.Status != ordering.StatusPendingPayment {
		t.Fatalf("first order = %s; another e-mail must not cancel it", first.Status)
	}
	if second.Status != ordering.StatusPendingPayment || second.CustomerID != nil {
		t.Fatalf("second order = %s customer %v; want pending without a customer link", second.Status, second.CustomerID)
	}
	var csState string
	_ = pool.QueryRow(ctx, `SELECT state FROM checkout_sessions WHERE id = $1`, firstCS).Scan(&csState)
	if csState != "pricing_confirmed" {
		t.Fatalf("first checkout = %s; want it open", csState)
	}

	// The buyer retries on the first page and pays.
	if _, err := pool.Exec(ctx, `UPDATE payment_intents SET state = 'created', failed_at = NULL WHERE checkout_session_id = $1`, firstCS); err != nil {
		t.Fatal(err)
	}
	payViaAllPay(t, srv, stub, f, q, firstCS)
	if first, _ = q.GetOrderByCheckoutSession(ctx, firstCS); first.Status != ordering.StatusPaid {
		t.Fatalf("first order after payment = %s; want paid", first.Status)
	}
	if n := issueJobs(t, ctx, pool, firstCS); n != 1 {
		t.Fatalf("issue jobs = %d; want 1", n)
	}
}

// Same e-mail and every attempt of the first checkout failed: the first
// purchase is closed WHOLE — checkout abandoned, hold cancelled, places back,
// order cancelled as superseded — and the second order takes the customer.
func TestSupersede_SameEmailFailedAttemptsClosesFirstWhole(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, done := lateFixture(t, ctx, pool, "sup3")
	defer done()
	stub := newStubAllPayServer(t, f)
	srv := allpayServer(t, pool, stub)
	q := gen.New(pool)

	firstCS := supersedeStart(t, srv, f, "", "")
	failIntentsOf(t, ctx, pool, firstCS)
	secondCS := supersedeStart(t, srv, f, "", "")

	var csState, ordStatus, resState string
	if err := pool.QueryRow(ctx, `SELECT cs.state, o.status, r.state FROM checkout_sessions cs
		JOIN orders o ON o.checkout_session_id = cs.id JOIN reservations r ON r.id = cs.reservation_id
		WHERE cs.id = $1`, firstCS).Scan(&csState, &ordStatus, &resState); err != nil {
		t.Fatal(err)
	}
	if csState != "abandoned" || ordStatus != ordering.StatusCancelled || resState != "cancelled" {
		t.Fatalf("first checkout=%s order=%s reservation=%s; want abandoned/cancelled/cancelled", csState, ordStatus, resState)
	}
	if held := heldOf(t, ctx, pool, f.sessionID); held != 2 {
		t.Fatalf("held = %d; want only the second hold's 2 places", held)
	}
	first, _ := q.GetOrderByCheckoutSession(ctx, firstCS)
	events, _ := q.ListOrderEventsByOrder(ctx, first.ID)
	if got := ordering.LastCancelReason(events); got != ordering.ReasonSupersededByNewCheckout {
		t.Fatalf("cancel reason = %q; want %s", got, ordering.ReasonSupersededByNewCheckout)
	}
	second, _ := q.GetOrderByCheckoutSession(ctx, secondCS)
	if second.Status != ordering.StatusPendingPayment || second.CustomerID == nil || first.CustomerID == nil || *second.CustomerID != *first.CustomerID {
		t.Fatalf("second order = %s customer %v (first %v); want pending with the same customer", second.Status, second.CustomerID, first.CustomerID)
	}
}

// Data from before the fix: only the ORDER was cancelled as superseded, the
// checkout and hold stayed open. Its payment answers 200 and revives the
// order: paid, tickets queued, revived_from recorded.
func TestSupersede_OldStyleSupersededOrderIsPaidByItsWebhook(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, done := lateFixture(t, ctx, pool, "sup4")
	defer done()
	stub := newStubAllPayServer(t, f)
	srv := allpayServer(t, pool, stub)
	q := gen.New(pool)

	csID := supersedeStart(t, srv, f, "", "")
	ord, _ := q.GetOrderByCheckoutSession(ctx, csID)
	if _, err := ordering.Cancel(ctx, q, ordering.CancelInput{
		OrderID: ord.ID, OrgID: ord.OrgID, Actor: ordering.ActorSystem,
		Reason: ordering.ReasonSupersededByNewCheckout,
	}); err != nil {
		t.Fatalf("old-style supersede: %v", err)
	}

	payViaAllPay(t, srv, stub, f, q, csID)
	ord, _ = q.GetOrderByCheckoutSession(ctx, csID)
	cs, _ := q.GetCheckoutSessionByID(ctx, csID)
	if ord.Status != ordering.StatusPaid || cs.State != "completed" {
		t.Fatalf("order=%s checkout=%s; want paid/completed", ord.Status, cs.State)
	}
	if n := issueJobs(t, ctx, pool, csID); n != 1 {
		t.Fatalf("issue jobs = %d; want 1", n)
	}
	var revived int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM order_events WHERE order_id = $1 AND type = 'paid' AND payload->>'revived_from' = $2`,
		ord.ID, ordering.ReasonSupersededByNewCheckout).Scan(&revived)
	if revived != 1 {
		t.Fatalf("paid events with revived_from = %d; want 1", revived)
	}
	var lates int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM late_payments WHERE org_id = $1`, f.orgID).Scan(&lates)
	if lates != 0 {
		t.Fatalf("late payments = %d; a revived order is not a late payment", lates)
	}
}

// An order cancelled for ANOTHER reason while its checkout stayed open: the
// payment answers 200 and is refunded in full as a late payment, no tickets,
// and a redelivery refunds nothing twice.
func TestSupersede_OrderCancelledForAnotherReasonIsRefunded(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, done := lateFixture(t, ctx, pool, "sup5")
	defer done()
	stub := newStubAllPayServer(t, f)
	srv := allpayServer(t, pool, stub)
	q := gen.New(pool)

	csID := supersedeStart(t, srv, f, "", "")
	ord, _ := q.GetOrderByCheckoutSession(ctx, csID)
	if _, err := ordering.Cancel(ctx, q, ordering.CancelInput{
		OrderID: ord.ID, OrgID: ord.OrgID, Actor: ordering.ActorSystem, Reason: "operator_cancelled",
	}); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	pi := payViaAllPay(t, srv, stub, f, q, csID)
	var lateCase, outcome string
	var refundID *uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT late_case, outcome, refund_id FROM late_payments WHERE payment_intent_id = $1`, pi.ID).
		Scan(&lateCase, &outcome, &refundID); err != nil {
		t.Fatalf("late_payments row: %v", err)
	}
	if lateCase != latepayment.CaseBuyerCancelled || outcome != latepayment.OutcomeRefundRequested || refundID == nil {
		t.Fatalf("late payment = %s/%s refund %v; want buyer_cancelled/refund_requested", lateCase, outcome, refundID)
	}
	var amount int64
	_ = pool.QueryRow(ctx, `SELECT amount FROM refunds WHERE id = $1`, *refundID).Scan(&amount)
	if amount != pi.Amount {
		t.Fatalf("refund = %d; want the full %d", amount, pi.Amount)
	}
	if n := issueJobs(t, ctx, pool, csID); n != 0 {
		t.Fatalf("issue jobs = %d; want no tickets", n)
	}
	if ord, _ = q.GetOrderByCheckoutSession(ctx, csID); ord.Status != ordering.StatusCancelled {
		t.Fatalf("order = %s; want it left cancelled", ord.Status)
	}
	var piState string
	_ = pool.QueryRow(ctx, `SELECT state FROM payment_intents WHERE id = $1`, pi.ID).Scan(&piState)
	if piState != "succeeded" {
		t.Fatalf("intent = %s; the captured payment must stay recorded", piState)
	}

	// AllPay redelivers: 200, no second refund.
	code, body := postAllPayWebhook(t, srv, f.configWebhookPath(), f.webhook(t, csID.String(), 1, pi.Amount, f.apiKey))
	if code != http.StatusOK {
		t.Fatalf("replay = %d: %s", code, body)
	}
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	var refunds int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE payment_intent_id = $1`, pi.ID).Scan(&refunds)
	if refunds != 1 {
		t.Fatalf("refunds after a replay = %d (%v); want 1", refunds, m)
	}
}

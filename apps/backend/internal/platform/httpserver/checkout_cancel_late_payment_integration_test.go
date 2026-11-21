//go:build integration

// checkout_cancel_late_payment_integration_test.go — spec 62 П-2, П-3, П-15,
// П-20 against a live database and the real router, only AllPay stubbed:
//
//   - POST /v1/public/checkout/{token}/cancel gives the places back, closes
//     the order, is idempotent, and does not cancel an order AllPay reports
//     paid (it completes it instead);
//   - a payment confirmed after the buyer's cancel is refunded in full;
//   - a payment notified after the hold expired takes the same places again
//     when they are free, and is refunded when they are not;
//   - the reconciliation pass settles a paid order whose notification was lost;
//   - the channel's payment window reaches AllPay's expire;
//   - the tracking consent travels with checkout/start and is updated by
//     PATCH .../consent, an older choice never replacing a newer one.
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run 'TestBuyerCancel|TestLatePayment|TestPaymentReconcile|TestChannelPaymentWindow|TestTrackingConsent'
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/latepayment"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/trackingconsent"
)

// lateFixture is an AllPay fixture whose cleanup also removes what the
// late-payment path writes (late payments, refunds, letters, audit rows).
func lateFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string) (*allpayFixture, func()) {
	t.Helper()
	f := newAllPayFixture(t, ctx, pool, label)
	return f, func() {
		bg := context.Background()
		for _, s := range []string{
			`DELETE FROM worker_jobs WHERE job_type = 'checkout.late_payment_email' AND payload->>'payment_intent_id' IN
			   (SELECT id::text FROM payment_intents WHERE org_id = $1)`,
			`DELETE FROM late_payments WHERE org_id = $1`,
			`DELETE FROM refunds WHERE org_id = $1`,
			`DELETE FROM email_letters WHERE org_id = $1`,
		} {
			if _, err := pool.Exec(bg, s, f.orgID); err != nil {
				t.Errorf("late fixture cleanup: %v", err)
			}
		}
		_, _ = pool.Exec(bg, `DELETE FROM audit_events WHERE action = 'v1.checkout.buyer_cancel' AND metadata->>'org_id' = $1`, f.orgID.String())
		f.cleanup()
	}
}

type lateStart struct {
	CheckoutSession struct {
		ID string `json:"id"`
	} `json:"checkout_session"`
	CheckoutToken    string `json:"checkout_token"`
	ExpiresAt        string `json:"expires_at"`
	PaymentExpiresAt string `json:"payment_expires_at"`
}

func lateStartCheckout(t *testing.T, srv *Server, f *allpayFixture, extra map[string]any) lateStart {
	t.Helper()
	body := map[string]any{
		"session_id": f.sessionID.String(),
		"tier_id":    f.tierID.String(),
		"qty":        2,
		"return_url": hostedTicketsBaseURL + "/shows/late",
		"buyer":      map[string]any{"email": f.buyerMail, "name": "Late Buyer", "phone": f.buyerPhone},
	}
	for k, v := range extra {
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("checkout/start = %d: %s", rec.Code, rec.Body.String())
	}
	var out lateStart
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	return out
}

func postCancel(t *testing.T, srv *Server, token string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/public/checkout/"+token+"/cancel", nil)
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

type lateStatus struct {
	Status      string  `json:"status"`
	LatePayment *string `json:"late_payment"`
	PaymentURL  *string `json:"payment_url"`
	PayExpires  *string `json:"payment_expires_at"`
}

func getLateStatus(t *testing.T, srv *Server, token string) lateStatus {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/checkout/"+token, nil)
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var s lateStatus
	_ = json.Unmarshal(rec.Body.Bytes(), &s)
	return s
}

func heldOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID uuid.UUID) int64 {
	t.Helper()
	var held int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(capacity_held), 0) FROM inventory_ledger WHERE session_id = $1`, sessionID).Scan(&held); err != nil {
		t.Fatalf("capacity_held: %v", err)
	}
	return held
}

// expireHold runs the two sweeps a hold and its order go through when the
// payment window passes.
func expireHold(t *testing.T, ctx context.Context, pool *pgxpool.Pool, csID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE reservations SET expires_at = now() - interval '1 minute'
		WHERE id = (SELECT reservation_id FROM checkout_sessions WHERE id = $1)`, csID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE orders SET expires_at = now() - interval '1 minute' WHERE checkout_session_id = $1`, csID); err != nil {
		t.Fatal(err)
	}
	q := gen.New(pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := hcheckout.NewReservationProcessor(pool, q, logger).WithCheckoutQueries(q).ProcessExpiredReservations(ctx, 100); err != nil {
		t.Fatalf("reservation sweep: %v", err)
	}
	if _, err := ordering.RunExpireSweep(ctx, q, time.Now().UTC(), 100, logger); err != nil {
		t.Fatalf("order sweep: %v", err)
	}
	var cs, ord string
	_ = pool.QueryRow(ctx, `SELECT cs.state, o.status FROM checkout_sessions cs JOIN orders o ON o.checkout_session_id = cs.id WHERE cs.id = $1`, csID).Scan(&cs, &ord)
	if cs != "expired" || ord != "expired" {
		t.Fatalf("after the sweeps checkout=%s order=%s; want both expired", cs, ord)
	}
}

func TestBuyerCancel_ReleasesTheHoldAndIsIdempotent(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, done := lateFixture(t, ctx, pool, "p2c")
	defer done()
	stub := newStubAllPayServer(t, f)
	srv := allpayServer(t, pool, stub)

	st := lateStartCheckout(t, srv, f, nil)
	if heldOf(t, ctx, pool, f.sessionID) != 2 {
		t.Fatalf("held before the cancel = %d; want 2", heldOf(t, ctx, pool, f.sessionID))
	}
	code, body := postCancel(t, srv, st.CheckoutToken)
	if code != http.StatusOK || body["status"] != "cancelled" || body["cancelled"] != true {
		t.Fatalf("cancel = %d %v; want 200 cancelled", code, body)
	}
	if held := heldOf(t, ctx, pool, f.sessionID); held != 0 {
		t.Fatalf("held after the cancel = %d; the places must go back at once", held)
	}
	csID := uuid.MustParse(st.CheckoutSession.ID)
	var csState, ordStatus, resState string
	_ = pool.QueryRow(ctx, `SELECT cs.state, o.status, r.state FROM checkout_sessions cs
		JOIN orders o ON o.checkout_session_id = cs.id JOIN reservations r ON r.id = cs.reservation_id
		WHERE cs.id = $1`, csID).Scan(&csState, &ordStatus, &resState)
	if csState != "abandoned" || ordStatus != "cancelled" || resState != "cancelled" {
		t.Fatalf("checkout=%s order=%s reservation=%s; want abandoned/cancelled/cancelled", csState, ordStatus, resState)
	}
	if s := getLateStatus(t, srv, st.CheckoutToken); s.Status != "cancelled" || s.PaymentURL != nil {
		t.Fatalf("public status = %+v; want cancelled without a payment link", s)
	}
	var reason string
	_ = pool.QueryRow(ctx, `SELECT payload->>'reason' FROM order_events e JOIN orders o ON o.id = e.order_id
		WHERE o.checkout_session_id = $1 AND e.type = 'cancelled'`, csID).Scan(&reason)
	if reason != hcheckout.BuyerCancelReason {
		t.Fatalf("order_events cancelled reason = %q; want %s", reason, hcheckout.BuyerCancelReason)
	}
	code, body = postCancel(t, srv, st.CheckoutToken)
	if code != http.StatusOK || body["cancelled"] != true {
		t.Fatalf("repeat cancel = %d %v; want the same answer", code, body)
	}
	var events int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM order_events e JOIN orders o ON o.id = e.order_id
		WHERE o.checkout_session_id = $1 AND e.type = 'cancelled'`, csID).Scan(&events)
	if events != 1 {
		t.Fatalf("cancelled events = %d after a repeat; want 1", events)
	}
	if code, _ := postCancel(t, srv, "no-such-token"); code != http.StatusNotFound {
		t.Fatalf("unknown token = %d; want 404", code)
	}
}

func TestBuyerCancel_PaidAtTheProviderIsNotCancelled(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, done := lateFixture(t, ctx, pool, "p2p")
	defer done()
	stub := newStubAllPayServer(t, f)
	srv := allpayServer(t, pool, stub)

	st := lateStartCheckout(t, srv, f, nil)
	stub.markPaid(t, st.CheckoutSession.ID)
	code, body := postCancel(t, srv, st.CheckoutToken)
	if code != http.StatusOK || body["status"] != "paid" || body["cancelled"] != false {
		t.Fatalf("cancel of a paid order = %d %v; want 200 paid, not cancelled", code, body)
	}
	if s := getLateStatus(t, srv, st.CheckoutToken); s.Status != "paid" {
		t.Fatalf("public status = %s; want paid", s.Status)
	}
}

func TestLatePayment_AfterTheBuyerCancelledIsRefunded(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, done := lateFixture(t, ctx, pool, "p15c")
	defer done()
	stub := newStubAllPayServer(t, f)
	srv := allpayServer(t, pool, stub)
	q := gen.New(pool)

	st := lateStartCheckout(t, srv, f, nil)
	if code, body := postCancel(t, srv, st.CheckoutToken); code != http.StatusOK || body["cancelled"] != true {
		t.Fatalf("cancel = %d %v", code, body)
	}
	orderID := st.CheckoutSession.ID
	pi, err := q.GetPaymentIntentByProviderID(ctx, orderID)
	if err != nil || pi.State != "created" {
		t.Fatalf("the intent must stay open after a cancel: %+v %v", pi, err)
	}
	stub.markPaid(t, orderID)
	hook := f.webhook(t, orderID, 1, pi.Amount, f.apiKey)
	if code, body := postAllPayWebhook(t, srv, f.configWebhookPath(), hook); code != http.StatusOK {
		t.Fatalf("late webhook = %d: %s", code, body)
	}
	var lateCase, outcome string
	var refundID *uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT late_case, outcome, refund_id FROM late_payments WHERE payment_intent_id = $1`, pi.ID).
		Scan(&lateCase, &outcome, &refundID); err != nil {
		t.Fatalf("late_payments row: %v", err)
	}
	if lateCase != latepayment.CaseBuyerCancelled || outcome != latepayment.OutcomeRefundRequested || refundID == nil {
		t.Fatalf("late payment = %s/%s refund %v; want buyer_cancelled/refund_requested with a refund", lateCase, outcome, refundID)
	}
	var amount int64
	var state, settlement string
	_ = pool.QueryRow(ctx, `SELECT amount, state, settlement FROM refunds WHERE id = $1`, *refundID).Scan(&amount, &state, &settlement)
	if amount != pi.Amount || state != "provider_pending" || settlement != "provider" {
		t.Fatalf("refund = %d %s %s; want the full %d in provider_pending", amount, state, settlement, pi.Amount)
	}
	var tickets, letters int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = 'checkout.issue_tickets' AND payload->>'checkout_session_id' = $1`, orderID).Scan(&tickets)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'payment_intent_id' = $2`, latepayment.JobTypeEmail, pi.ID.String()).Scan(&letters)
	if tickets != 0 || letters != 1 {
		t.Fatalf("issue jobs = %d, letter jobs = %d; want no tickets and one letter", tickets, letters)
	}
	if s := getLateStatus(t, srv, st.CheckoutToken); s.Status != "cancelled" || s.LatePayment == nil || *s.LatePayment != latepayment.OutcomeRefundRequested {
		t.Fatalf("public status = %+v; want cancelled with late_payment refund_requested", s)
	}
	// AllPay retries: nothing is refunded twice.
	if code, _ := postAllPayWebhook(t, srv, f.configWebhookPath(), hook); code != http.StatusOK {
		t.Fatalf("replay = %d", code)
	}
	var refunds int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE payment_intent_id = $1`, pi.ID).Scan(&refunds)
	if refunds != 1 {
		t.Fatalf("refunds after a replay = %d; want 1", refunds)
	}
}

func TestLatePayment_DelayedNoticeTakesThePlacesAgain(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, done := lateFixture(t, ctx, pool, "p15r")
	defer done()
	stub := newStubAllPayServer(t, f)
	srv := allpayServer(t, pool, stub)
	q := gen.New(pool)

	st := lateStartCheckout(t, srv, f, nil)
	csID := uuid.MustParse(st.CheckoutSession.ID)
	before, _ := q.GetOrderByCheckoutSession(ctx, csID)
	expireHold(t, ctx, pool, csID)
	if heldOf(t, ctx, pool, f.sessionID) != 0 {
		t.Fatal("the expired hold still holds places")
	}

	pi, _ := q.GetPaymentIntentByProviderID(ctx, csID.String())
	stub.markPaid(t, csID.String())
	if code, body := postAllPayWebhook(t, srv, f.configWebhookPath(), f.webhook(t, csID.String(), 1, pi.Amount, f.apiKey)); code != http.StatusOK {
		t.Fatalf("late webhook = %d: %s", code, body)
	}
	cs, _ := q.GetCheckoutSessionByID(ctx, csID)
	ord, _ := q.GetOrderByCheckoutSession(ctx, csID)
	if cs.State != "completed" || ord.Status != "paid" {
		t.Fatalf("checkout=%s order=%s; want completed/paid", cs.State, ord.Status)
	}
	if ord.Total != before.Total || ord.Total != pi.Amount || ord.ID != before.ID {
		t.Fatalf("order %s total %d; want the same order at the paid %d", ord.ID, ord.Total, pi.Amount)
	}
	if cs.ReservationID == before.ReservationID {
		t.Fatal("the places must sit on a new reservation")
	}
	var outcome string
	_ = pool.QueryRow(ctx, `SELECT outcome FROM late_payments WHERE payment_intent_id = $1`, pi.ID).Scan(&outcome)
	if outcome != latepayment.OutcomeReacquired {
		t.Fatalf("outcome = %q; want reacquired", outcome)
	}
	var issue int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = 'checkout.issue_tickets' AND payload->>'checkout_session_id' = $1`, csID.String()).Scan(&issue)
	if issue != 1 {
		t.Fatalf("issue jobs = %d; want the tickets to go out", issue)
	}
	if s := getLateStatus(t, srv, st.CheckoutToken); s.Status != "paid" {
		t.Fatalf("public status = %s; want paid", s.Status)
	}
}

func TestLatePayment_DelayedNoticeWithThePlacesGoneIsRefunded(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, done := lateFixture(t, ctx, pool, "p15s")
	defer done()
	stub := newStubAllPayServer(t, f)
	srv := allpayServer(t, pool, stub)
	q := gen.New(pool)

	st := lateStartCheckout(t, srv, f, nil)
	csID := uuid.MustParse(st.CheckoutSession.ID)
	expireHold(t, ctx, pool, csID)
	// Somebody else bought the hall meanwhile.
	if _, err := pool.Exec(ctx, `UPDATE session_seats SET status = 'sold' WHERE session_id = $1 AND kind = 'ga_unit'`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	pi, _ := q.GetPaymentIntentByProviderID(ctx, csID.String())
	stub.markPaid(t, csID.String())
	if code, body := postAllPayWebhook(t, srv, f.configWebhookPath(), f.webhook(t, csID.String(), 1, pi.Amount, f.apiKey)); code != http.StatusOK {
		t.Fatalf("late webhook = %d: %s", code, body)
	}
	var lateCase, outcome, reason string
	_ = pool.QueryRow(ctx, `SELECT late_case, outcome, reason FROM late_payments WHERE payment_intent_id = $1`, pi.ID).Scan(&lateCase, &outcome, &reason)
	if lateCase != latepayment.CaseDelayedNotice || outcome != latepayment.OutcomeRefundRequested || reason == "" {
		t.Fatalf("late payment = %s/%s (%s); want delayed_notice/refund_requested with a reason", lateCase, outcome, reason)
	}
	ord, _ := q.GetOrderByCheckoutSession(ctx, csID)
	if ord.Status != "expired" {
		t.Fatalf("order = %s; a refused re-acquisition must leave nothing half-done", ord.Status)
	}
	if held := heldOf(t, ctx, pool, f.sessionID); held != 0 {
		t.Fatalf("held = %d after a refused re-acquisition; want 0", held)
	}
}

func TestPaymentReconcile_SettlesAPaymentWithoutItsNotification(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, done := lateFixture(t, ctx, pool, "p15w")
	defer done()
	stub := newStubAllPayServer(t, f)
	srv := allpayServer(t, pool, stub)
	q := gen.New(pool)

	st := lateStartCheckout(t, srv, f, nil)
	csID := uuid.MustParse(st.CheckoutSession.ID)
	pi, _ := q.GetPaymentIntentByProviderID(ctx, csID.String())
	stub.markPaid(t, csID.String())

	rec := testPaymentRecoverer(pool, srv, stub)
	if rs, err := rec.ReconcileUnsettledPayments(ctx, 10); err != nil || rs.Asked != 0 {
		t.Fatalf("a fresh page was asked about: %+v %v", rs, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE payment_intents SET created_at = now() - interval '10 minutes' WHERE id = $1`, pi.ID); err != nil {
		t.Fatal(err)
	}
	rs, err := rec.ReconcileUnsettledPayments(ctx, 10)
	if err != nil || rs.Asked != 1 || rs.Settled != 1 {
		t.Fatalf("reconcile = %+v %v; want one payment settled", rs, err)
	}
	if ord, _ := q.GetOrderByCheckoutSession(ctx, csID); ord.Status != "paid" {
		t.Fatalf("order = %s; want paid", ord.Status)
	}
	if rs, _ := rec.ReconcileUnsettledPayments(ctx, 10); rs.Asked != 0 {
		t.Fatalf("a settled payment was asked about again: %+v", rs)
	}
}

func TestChannelPaymentWindow_ReachesAllPay(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, done := lateFixture(t, ctx, pool, "p3w")
	defer done()
	stub := newStubAllPayServer(t, f)
	srv := allpayServer(t, pool, stub)

	if _, err := pool.Exec(ctx, `UPDATE sales_channels SET payment_window_seconds = 1200 WHERE id = $1`, f.channelID); err != nil {
		t.Fatal(err)
	}
	st := lateStartCheckout(t, srv, f, nil)
	exp, err := stub.last(t)["expire"].(json.Number).Int64()
	if left := exp - time.Now().Unix(); err != nil || left < 1150 || left > 1210 {
		t.Fatalf("expire = %v; want now + the channel's 1200 s", stub.last(t)["expire"])
	}
	pay, err := time.Parse(time.RFC3339, st.PaymentExpiresAt)
	if err != nil || pay.Unix() != exp {
		t.Fatalf("payment_expires_at = %q; want the page's expire %d", st.PaymentExpiresAt, exp)
	}
	hold, _ := time.Parse(time.RFC3339, st.ExpiresAt)
	if !hold.After(pay) {
		t.Fatalf("the hold %s must outlive the page %s", hold, pay)
	}
	if s := getLateStatus(t, srv, st.CheckoutToken); s.PayExpires == nil || *s.PayExpires != st.PaymentExpiresAt {
		t.Fatalf("status payment_expires_at = %v; want %s", s.PayExpires, st.PaymentExpiresAt)
	}
}

func TestTrackingConsent_StartThenPatch(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, done := lateFixture(t, ctx, pool, "p20")
	defer done()
	stub := newStubAllPayServer(t, f)
	srv := allpayServer(t, pool, stub)

	st := lateStartCheckout(t, srv, f, map[string]any{
		"tracking_consent": map[string]any{"granted": true, "version": "banner-v1", "at": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)},
	})
	csID := uuid.MustParse(st.CheckoutSession.ID)
	got, err := trackingconsent.ForCheckout(ctx, pool, csID)
	if err != nil || !got.Allows() || got.Version != "banner-v1" {
		t.Fatalf("consent after start = %+v %v; want granted banner-v1", got, err)
	}

	patch := func(body map[string]any) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPatch, "/v1/public/checkout/"+st.CheckoutToken+"/consent", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.router.ServeHTTP(rec, req)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}
	code, m := patch(map[string]any{"granted": false, "version": "banner-v1"})
	if code != http.StatusOK || m["applied"] != true {
		t.Fatalf("PATCH refusal = %d %v", code, m)
	}
	ord, _ := gen.New(pool).GetOrderByCheckoutSession(ctx, csID)
	if got, _ := trackingconsent.ForOrder(ctx, pool, ord.ID); got.Allows() {
		t.Fatal("the conversion sender must read the refusal from the order")
	}
	code, m = patch(map[string]any{"granted": true, "version": "banner-v1", "at": "2020-01-01T00:00:00Z"})
	if code != http.StatusOK || m["applied"] != false {
		t.Fatalf("an older choice = %d %v; want applied false", code, m)
	}
	if got, _ := trackingconsent.ForCheckout(ctx, pool, csID); got.Allows() {
		t.Fatal("an older choice replaced the newer refusal")
	}
	if code, _ := patch(map[string]any{"version": "banner-v1"}); code != http.StatusBadRequest {
		t.Fatalf("a consent without granted = %d; want 400", code)
	}
}

//go:build integration

// checkout_payment_recover_integration_test.go — П-19 against a live
// database and the real router, only AllPay stubbed:
//
//   - the same Idempotency-Key on checkout/start answers the same checkout
//     and payment link once its hold is consumed, another body is 409;
//   - an AllPay attempt is recorded BEFORE the provider call, so a lost
//     answer leaves a payment_intents row without a page;
//   - checkout.payment_recover asks AllPay by the same order_id: unpaid →
//     left stalled, never created twice; no order → the page is asked for
//     again and offered as payment_url; paid → the order is paid through
//     the webhook's own state machine.
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestCheckoutPaymentRecover
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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hfeed"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
)

func startCheckoutWithKey(t *testing.T, srv *Server, f *allpayFixture, key, returnURL string) (int, []byte) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"session_id": f.sessionID.String(),
		"tier_id":    f.tierID.String(),
		"qty":        2,
		"return_url": returnURL,
		"buyer":      map[string]any{"email": f.buyerMail, "name": "Recover Buyer", "phone": f.buyerPhone},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set(idempotency.HeaderName, key)
	}
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func testPaymentRecoverer(pool *pgxpool.Pool, srv *Server, stub *stubAllPayServer) *hfeed.PaymentRecoverer {
	q := gen.New(pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	webhook := hcheckout.New(q, q, q, q, nil, nil, nil, nil, nil, nil, pool, logger,
		hcheckout.PricingRules{}, "", "", nil, nil, nil)
	return &hfeed.PaymentRecoverer{
		Store: q,
		Provider: hfeed.NewStripePaymentStarter(q, q, "").
			WithFlitt("", srv.cfg.APIPublicBaseURL()).
			WithAllPay(stub.baseURL()),
		Apply: func(ctx context.Context, src hcheckout.ConfirmedPaymentSource, ev payments.NormalizedEvent) error {
			_, _, err := webhook.ApplyProviderConfirmedPayment(ctx, src, ev)
			return err
		},
		ReturnURLs: CheckoutReturnURLPolicy(srv.cfg),
		Grace:      WidgetPaymentGrace(srv.cfg),
		Providers:  []string{"allpay"},
		Logger:     logger,
	}
}

func TestCheckoutPaymentRecover_IdempotencyKeyReplaysTheCheckout(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newAllPayFixture(t, ctx, pool, "p19i")
	defer f.cleanup()
	stub := newStubAllPayServer(t, f)
	srv := allpayServer(t, pool, stub)
	key := "p19-" + uuid.NewString()
	returnURL := hostedTicketsBaseURL + "/shows/p19"

	code, first := startCheckoutWithKey(t, srv, f, key, returnURL)
	if code != http.StatusCreated {
		t.Fatalf("first checkout/start = %d: %s", code, first)
	}
	code, again := startCheckoutWithKey(t, srv, f, key, returnURL)
	// The stored body comes back from jsonb: the same answer, not
	// necessarily the same bytes.
	var a, b hostedStartResponse
	_ = json.Unmarshal(first, &a)
	_ = json.Unmarshal(again, &b)
	if code != http.StatusCreated || a.CheckoutSession.ID == "" || a != b {
		t.Fatalf("replay = %d %s; want the first answer %s", code, again, first)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM checkout_sessions WHERE channel_id = $1`, f.channelID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("checkout sessions = %d (%v); one key must make one checkout", n, err)
	}
	code, body := startCheckoutWithKey(t, srv, f, key, returnURL+"/other")
	if code != http.StatusConflict {
		t.Fatalf("same key, other body = %d %s; want 409", code, body)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE key = $1`, key)
}

func TestCheckoutPaymentRecover_LostAnswerIsRecovered(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newAllPayFixture(t, ctx, pool, "p19r")
	defer f.cleanup()
	stub := newStubAllPayServer(t, f)
	srv := allpayServer(t, pool, stub)
	q := gen.New(pool)

	// ── 1. AllPay creates the page, arena never hears back ───────────────
	stub.loseCreateAnswers = 1
	code, body := startCheckoutWithKey(t, srv, f, "", hostedTicketsBaseURL+"/shows/p19")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("checkout/start with a lost answer = %d %s; want 503", code, body)
	}
	orderID, _ := stub.last(t)["order_id"].(string)
	csID := uuid.MustParse(orderID)

	// ── 2. The attempt was recorded before the call ──────────────────────
	pi, err := q.GetPaymentIntentByProviderID(ctx, orderID)
	if err != nil {
		t.Fatalf("no payment intent recorded for the lost attempt: %v", err)
	}
	if pi.Provider != "allpay" || pi.State != "created" || pi.HostedCheckoutURL != nil {
		t.Fatalf("intent = %+v; want an allpay attempt in created without a page", pi)
	}
	var token string
	if err := pool.QueryRow(ctx, `SELECT checkout_token FROM checkout_sessions WHERE id = $1`, csID).Scan(&token); err != nil {
		t.Fatalf("checkout token: %v", err)
	}

	rec := testPaymentRecoverer(pool, srv, stub)

	// A fresh attempt is left to the request that may still be running.
	if st, err := rec.RecoverStalledPayments(ctx, 10); err != nil || st.Examined != 0 {
		t.Fatalf("young attempt: stats %+v err %v; want it untouched", st, err)
	}
	age := func() {
		if _, err := pool.Exec(ctx, `UPDATE payment_intents SET created_at = now() - interval '10 minutes' WHERE id = $1`, pi.ID); err != nil {
			t.Fatalf("age the attempt: %v", err)
		}
	}
	age()

	// ── 3a. Unpaid at AllPay: the order exists, so it is NEVER created again
	// (a second payable document could be paid twice); the attempt stays
	// stalled, since paymentstatus names no page.
	createsBefore := len(stub.orders)
	st, err := rec.RecoverStalledPayments(ctx, 10)
	if err != nil || st.Pending != 1 || st.Relinked != 0 {
		t.Fatalf("recover unpaid: stats %+v err %v; want the attempt left stalled", st, err)
	}
	if len(stub.orders) != createsBefore {
		t.Fatal("an existing unpaid AllPay order was created a second time")
	}
	if pi, _ = q.GetPaymentIntentByProviderID(ctx, orderID); pi.HostedCheckoutURL != nil {
		t.Fatalf("a page was stored for an unpaid order AllPay named none for: %v", *pi.HostedCheckoutURL)
	}

	// ── 3b. No order at AllPay: the page is asked for again under the same order_id
	stub.mu.Lock()
	delete(stub.ledger, orderID)
	stub.mu.Unlock()
	st, err = rec.RecoverStalledPayments(ctx, 10)
	if err != nil || st.Relinked != 1 {
		t.Fatalf("recover unknown: stats %+v err %v; want one page asked for again", st, err)
	}
	if again := stub.last(t)["order_id"]; again != orderID {
		t.Fatalf("page asked again for order %v; want the same order_id %s", again, orderID)
	}
	pi, _ = q.GetPaymentIntentByProviderID(ctx, orderID)
	if pi.HostedCheckoutURL == nil || *pi.HostedCheckoutURL == "" {
		t.Fatalf("intent page not stored: %+v", pi)
	}
	if status := f.getStatus(t, srv, token); status.PaymentURL == nil || *status.PaymentURL != *pi.HostedCheckoutURL {
		t.Fatalf("payment_url = %v; want the recovered page %s", status.PaymentURL, *pi.HostedCheckoutURL)
	}

	// ── 4. Paid before arena heard: settled through the webhook path ─────
	if _, err := pool.Exec(ctx, `UPDATE payment_intents SET hosted_checkout_url = NULL WHERE id = $1`, pi.ID); err != nil {
		t.Fatalf("clear page: %v", err)
	}
	stub.markPaid(t, orderID)
	st, err = rec.RecoverStalledPayments(ctx, 10)
	if err != nil || st.Applied != 1 {
		t.Fatalf("recover paid: stats %+v err %v; want one payment applied", st, err)
	}
	pi, _ = q.GetPaymentIntentByProviderID(ctx, orderID)
	if pi.State != "succeeded" {
		t.Fatalf("intent state = %s; want succeeded", pi.State)
	}
	cs, err := q.GetCheckoutSessionByID(ctx, csID)
	if err != nil || cs.State != "completed" {
		t.Fatalf("checkout = %s (%v); want completed", cs.State, err)
	}
	// A webhook arriving afterwards changes nothing.
	code, _ = postAllPayWebhook(t, srv, f.configWebhookPath(), f.webhook(t, orderID, 1, pi.Amount, f.apiKey))
	if code != http.StatusOK {
		t.Fatalf("late webhook = %d; want 200", code)
	}
	if st, _ := rec.RecoverStalledPayments(ctx, 10); st.Examined != 0 {
		t.Fatalf("a settled attempt was examined again: %+v", st)
	}
}

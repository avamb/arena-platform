//go:build integration

// allpay_refund_integration_test.go — PAY-14b end to end through the REAL
// router and the real refund engine, only AllPay stubbed
// (stubAllPayServer, a ledger of orders):
//
//   - the answer of a refund is lost; the engine retries and the money goes
//     back ONCE (AllPay's refund method has no idempotency key);
//   - the second ticket of the same order is refunded on top of the first;
//   - a refund AllPay's books cannot explain (made in the AllPay dashboard)
//     stops arena's own refund: nothing is sent, a human checks.
//
// Run with:
//
//	DATABASE_URL=... go test -tags integration -run TestAllPayRefund ./apps/backend/internal/platform/httpserver/
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/allpay"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

func TestAllPayRefund_LostAnswerSecondTicketAndDashboardRefund(t *testing.T) {
	stub := newStubAllPayServer(t)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.AllPayAPIBaseURL = stub.baseURL() })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pf := &prom0113Fixture{ts: ts, client: ts.Client(), q: gen.New(srv.pgxPool)}
	user, err := pf.q.InsertUser(context.Background(), "pay14b-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	org := pf.org(t, "PAY14B")
	key := pf.key(t, org, "refund.read", "refund.create", "refund.approve")
	ctx := context.Background()
	pool := srv.pgxPool

	login, apiKey := "pp14b"+uuid.NewString()[:8], "allpay_key_"+uuid.NewString()[:8]
	stub.account(login, apiKey)
	secrets, _ := json.Marshal(map[string]string{"merchant_id": login, "secret_key": apiKey})

	// newPay03Order seeds a paid order of two 2500 EUR tickets and a 5000
	// payment; it is re-pointed at AllPay here.
	order := func() (*pay03Fixture, string) {
		f := newPay03Order(t, pool, org, "allpay")
		// The fixture files a Stripe config for the org; the org keeps ONE
		// AllPay config instead.
		if _, err := pool.Exec(ctx, `DELETE FROM payment_provider_configs WHERE org_id = $1 AND provider = 'stripe'
		    AND EXISTS (SELECT 1 FROM payment_provider_configs WHERE org_id = $1 AND provider = 'allpay')`, org); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE payment_provider_configs SET provider = 'allpay', secrets = $2::jsonb
		    WHERE org_id = $1 AND provider = 'stripe'`, org, string(secrets)); err != nil {
			t.Fatal(err)
		}
		var ref string
		if err := pool.QueryRow(ctx, `SELECT provider_payment_id FROM payment_intents WHERE id = $1`, f.pay).Scan(&ref); err != nil {
			t.Fatal(err)
		}
		stub.paidOrder(login, ref, 5000, "EUR")
		return f, ref
	}
	refundTicket := func(f *pay03Fixture, i int) map[string]any {
		t.Helper()
		body := fmt.Sprintf(`{"items":[{"ticket_id":%q}],"reason":"AllPay refund test"}`, f.tickets[i])
		st, out := refundOrgPost(t, pf, "/v1/organizations/"+org.String()+"/orders/"+f.order.String()+"/refunds",
			key, "pay14b-"+uuid.NewString(), body)
		if st != http.StatusAccepted {
			t.Fatalf("refund ticket %d: %d %v", i, st, out)
		}
		return out["refunds"].([]any)[0].(map[string]any)
	}
	refundRow := func(id string) (state, providerRefundID, failureCode string) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT state, COALESCE(provider_refund_id, ''), COALESCE(failure_code, '')
		     FROM refunds WHERE id = $1`, id).Scan(&state, &providerRefundID, &failureCode); err != nil {
			t.Fatal(err)
		}
		return
	}
	calls := func() int {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		return stub.refundCalls
	}

	// ── 1. The answer of the first refund is lost on the way back.
	f, ref := order()
	stub.mu.Lock()
	stub.loseRefundAnswers = 1
	stub.mu.Unlock()
	first := refundTicket(f, 0)
	firstID := first["id"].(string)
	if state, _, _ := refundRow(firstID); state != "provider_pending" {
		t.Fatalf("refund with a lost answer = %s; want provider_pending (the outcome is unknown)", state)
	}
	if got := stub.refundsOf(ref); len(got) != 1 || got[0] != 2500 {
		t.Fatalf("AllPay refunded %v; want [2500]", got)
	}
	if s, _ := f.ticket(t, 0); s != "active" {
		t.Fatalf("ticket 0 = %s before the outcome is known; want active", s)
	}

	// ── 2. The engine retries: it reads the order, finds its own refund
	// there, and sends nothing again.
	if _, err := pool.Exec(ctx, `UPDATE refunds SET provider_attempted_at = now() - interval '10 minutes' WHERE id = $1`, firstID); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.refundEngine().Drive(ctx, uuid.MustParse(firstID)); err != nil {
		t.Fatalf("Drive: %v", err)
	}
	state, providerRefundID, _ := refundRow(firstID)
	if state != "succeeded" || providerRefundID != allpay.RefundID(ref, firstID) {
		t.Fatalf("retried refund = %s %q; want succeeded %q", state, providerRefundID, allpay.RefundID(ref, firstID))
	}
	if got := stub.refundsOf(ref); len(got) != 1 || calls() != 1 {
		t.Fatalf("AllPay refunded %v in %d calls; the money must go back once", got, calls())
	}
	if s, link := f.ticket(t, 0); s != "cancelled" || link == nil || link.String() != firstID {
		t.Fatalf("ticket 0 = %s link %v; want cancelled by %s", s, link, firstID)
	}

	// ── 3. The second ticket of the same order.
	second := refundTicket(f, 1)
	if state, _, _ := refundRow(second["id"].(string)); state != "succeeded" {
		t.Fatalf("second refund = %s; want succeeded", state)
	}
	if got := stub.refundsOf(ref); len(got) != 2 || got[1] != 2500 {
		t.Fatalf("AllPay refunded %v; want [2500 2500]", got)
	}
	var orderStatus string
	_ = pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, f.order).Scan(&orderStatus)
	if orderStatus != "refunded" {
		t.Fatalf("order = %s; want refunded", orderStatus)
	}

	// ── 4. Someone refunded 10.00 in the AllPay dashboard. Arena cannot
	// tell what that money was for: it sends nothing and asks a human.
	f2, ref2 := order()
	stub.mu.Lock()
	stub.ledger[ref2].refunded = 1000
	stub.mu.Unlock()
	before := calls()
	third := refundTicket(f2, 0)
	state, _, failureCode := refundRow(third["id"].(string))
	if state != "manual_review" || failureCode != allpay.RefundCodeUnexplained {
		t.Fatalf("refund after a dashboard refund = %s %s; want manual_review %s", state, failureCode, allpay.RefundCodeUnexplained)
	}
	if calls() != before {
		t.Fatalf("AllPay received %d refund call(s); want none", calls()-before)
	}
	if s, _ := f2.ticket(t, 0); s != "active" {
		t.Fatalf("ticket = %s; a refund that was not sent must leave it valid", s)
	}
}

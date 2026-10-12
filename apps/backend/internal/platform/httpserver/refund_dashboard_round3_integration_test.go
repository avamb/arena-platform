//go:build integration

package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

const refundsMaxDashboardConfirmAttempts = refunds.MaxDashboardConfirmAttempts

// dashEnv is one organization with a paid two-ticket order on Stripe, its
// test configuration (the webhook route a refund event arrives on) and a stub
// Stripe that answers refund reads.
type dashEnv struct {
	srv      *Server
	stub     *stubStripe
	f        *pay03Fixture
	org      uuid.UUID
	piRef    string
	configID uuid.UUID
}

func newDashEnv(t *testing.T, label string) *dashEnv {
	t.Helper()
	stub := newStubStripe(t)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.StripeAPIBaseURL = stub.baseURL() })
	ctx := context.Background()
	pf := &prom0113Fixture{q: gen.New(srv.pgxPool)}
	user, err := pf.q.InsertUser(ctx, "dash3-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	e := &dashEnv{srv: srv, stub: stub, org: pf.org(t, label)}
	e.f = newPay03Order(t, srv.pgxPool, e.org, "stripe")
	e.piRef = "pi_d3_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	e.exec(t, `UPDATE payment_intents SET provider_charge_ref = $2 WHERE id = $1`, e.f.pay, e.piRef)
	if err := srv.pgxPool.QueryRow(ctx, `SELECT id FROM payment_provider_configs WHERE org_id = $1 AND provider = 'stripe'`, e.org).Scan(&e.configID); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *dashEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.srv.pgxPool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// event is a signed refund event for refundID on payment, sent to the test
// configuration's route.
func (e *dashEnv) post(t *testing.T, eventType, refundID, payment string, amount int) (int, map[string]any) {
	t.Helper()
	body := fmt.Sprintf(`{"id":"evt_%s","type":%q,"data":{"object":{"id":%q,"object":"refund","status":"succeeded","amount":%d,"currency":"eur","payment_intent":%q}}}`,
		uuid.NewString()[:8], eventType, refundID, amount, payment)
	req := httptest.NewRequest(http.MethodPost, "/v1/payment-intents/webhook/"+e.configID.String(), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", signStripe("whsec_pay03", []byte(body)))
	rec := httptest.NewRecorder()
	e.srv.router.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func (e *dashEnv) count(t *testing.T, where string, args ...any) int {
	t.Helper()
	var n int
	if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT count(*) FROM refunds WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// N3: a refund event is read back with the configuration that verified it,
// even when the organization prefers another one (a live key over a test one).
func TestRefundWebhook_ReadBackUsesTheVerifyingConfiguration(t *testing.T) {
	e := newDashEnv(t, "DASHCFG")
	e.exec(t, `UPDATE organizations SET kyb_status = 'verified' WHERE id = $1`, e.org)
	e.exec(t, `INSERT INTO payment_provider_configs (org_id, provider, mode, secrets, status, is_active)
	           VALUES ($1, 'stripe', 'live', '{"api_key":"sk_live_other","webhook_secret":"whsec_live_other"}'::jsonb, 'configured', true)`, e.org)

	e.stub.setRefund("re_d3_cfg", "succeeded", 2500, "eur", e.piRef)
	if st, out := e.post(t, "refund.created", "re_d3_cfg", e.piRef, 2500); st != http.StatusOK {
		t.Fatalf("webhook = %d %v", st, out)
	}
	if key := e.stub.refundReadKey("re_d3_cfg"); key != "sk_test_pay03" {
		t.Fatalf("refund read back with key %q; want the verifying test configuration's sk_test_pay03", key)
	}
	if n := e.count(t, `payment_intent_id = $1 AND provider_refund_id = 're_d3_cfg'`, e.f.pay); n != 1 {
		t.Fatalf("dashboard refund rows = %d; want 1", n)
	}
}

// N1 + F7: a refund of a payment the organization does not own costs no
// provider call; one of its own payment that cannot be read back is retried by
// the provider, and after MaxDashboardConfirmAttempts deliveries it is
// acknowledged (an operator is alerted) instead of failing for days.
func TestRefundWebhook_DashboardRefundForeignAndUnconfirmed(t *testing.T) {
	e := newDashEnv(t, "DASHN1")
	t.Cleanup(func() {
		_, _ = e.srv.pgxPool.Exec(context.Background(),
			`DELETE FROM audit_events WHERE action = 'v1.refund.dashboard_unconfirmed' AND metadata->>'org_id' = $1::text`, e.org)
	})

	// Another site's payment on the same Stripe account: the key cannot even
	// read refunds, yet the event is acknowledged without asking.
	e.stub.setRefund("re_d3_foreign", "succeeded", 2500, "eur", "pi_other_site")
	if st, out := e.post(t, "refund.created", "re_d3_foreign", "pi_other_site", 2500); st != http.StatusOK {
		t.Fatalf("foreign refund = %d %v; want 200", st, out)
	}
	if key := e.stub.refundReadKey("re_d3_foreign"); key != "" {
		t.Fatalf("a foreign refund was read from the provider (key %q)", key)
	}

	// A refund of OUR payment the provider will not show (a read that fails).
	e.stub.refundMu.Lock()
	if e.stub.refundObjects == nil {
		e.stub.refundObjects = map[string]string{}
	}
	e.stub.refundObjects["re_d3_unread"] = ""
	e.stub.refundMu.Unlock()
	for i := 1; i < refundsMaxDashboardConfirmAttempts; i++ {
		if st, _ := e.post(t, "refund.created", "re_d3_unread", e.piRef, 2500); st != http.StatusInternalServerError {
			t.Fatalf("delivery %d = %d; want 500 so the provider retries", i, st)
		}
	}
	st, out := e.post(t, "refund.created", "re_d3_unread", e.piRef, 2500)
	if st != http.StatusOK || out["processed"] != false {
		t.Fatalf("delivery %d = %d %v; want 200 acknowledged, not processed", refundsMaxDashboardConfirmAttempts, st, out)
	}
	if n := e.count(t, `payment_intent_id = $1 AND provider_refund_id = 're_d3_unread'`, e.f.pay); n != 0 {
		t.Fatalf("an unconfirmed refund wrote %d row(s)", n)
	}
}

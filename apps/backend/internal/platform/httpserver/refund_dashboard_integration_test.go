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
)

// A refund the organizer made in Stripe's own dashboard reaches arena as a
// refund.updated event no arena refund carries (PAY-05, spec 36 section 7):
// it is recorded with origin provider_dashboard, a refund smaller than the
// payment flags the tickets for review, one that covers the payment cancels
// them, and a replay or a foreign payment writes nothing.
func TestRefundWebhook_DashboardRefundIsRecordedAndAppliesTheInboundPolicy(t *testing.T) {
	stub := newStubStripe(t)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.StripeAPIBaseURL = stub.baseURL() })
	ctx := context.Background()
	pf := &prom0113Fixture{q: gen.New(srv.pgxPool)}
	user, err := pf.q.InsertUser(ctx, "dash-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	org := pf.org(t, "DASHBOARD")
	f := newPay03Order(t, srv.pgxPool, org, "stripe")
	piRef := "pi_dash_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	if _, err := srv.pgxPool.Exec(ctx, `UPDATE payment_intents SET provider_charge_ref = $2 WHERE id = $1`, f.pay, piRef); err != nil {
		t.Fatal(err)
	}
	var configID uuid.UUID
	if err := srv.pgxPool.QueryRow(ctx, `SELECT id FROM payment_provider_configs WHERE org_id = $1 AND provider = 'stripe'`, org).Scan(&configID); err != nil {
		t.Fatal(err)
	}
	path := "/v1/payment-intents/webhook/" + configID.String()
	const secret = "whsec_pay03"

	event := func(refundID, payment string, amount int) []byte {
		return []byte(fmt.Sprintf(`{"id":"evt_%s","type":"refund.updated","data":{"object":{"id":%q,"object":"refund","status":"succeeded","amount":%d,"currency":"eur","payment_intent":%q}}}`,
			refundID, refundID, amount, payment))
	}
	count := func(where string, args ...any) int {
		var n int
		if err := srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE `+where, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	post := func(body []byte) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Stripe-Signature", signStripe(secret, body))
		rec := httptest.NewRecorder()
		srv.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("webhook = %d; body %s", rec.Code, rec.Body.String())
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return m
	}

	// a payment arena does not own: acknowledged, nothing written
	post(event("re_dash_foreign", "pi_somebody_elses", 2500))
	if n := count(`payment_intent_id = $1 AND origin = 'provider_dashboard'`, f.pay); n != 0 {
		t.Fatalf("foreign payment wrote %d dashboard refund(s)", n)
	}

	// a partial dashboard refund: recorded, tickets flagged, none cancelled
	out := post(event("re_dash_1", piRef, 2500))
	if out["origin"] != "provider_dashboard" {
		t.Fatalf("answer = %v; want origin provider_dashboard", out)
	}
	var state, origin string
	var settled bool
	if err := srv.pgxPool.QueryRow(ctx, `SELECT state, origin, settled_at IS NOT NULL FROM refunds
		WHERE payment_intent_id = $1 AND provider_refund_id = 're_dash_1'`, f.pay).Scan(&state, &origin, &settled); err != nil {
		t.Fatalf("dashboard refund row: %v", err)
	}
	if state != "succeeded" || origin != "provider_dashboard" || !settled {
		t.Fatalf("row = %s/%s settled=%v; want succeeded/provider_dashboard/settled", state, origin, settled)
	}
	for i := range f.tickets {
		if st, _ := f.ticket(t, i); st != "active" {
			t.Fatalf("ticket %d is %q after a partial dashboard refund; want active", i, st)
		}
	}
	var held int
	if err := srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE order_id = $1 AND review_hold`, f.order).Scan(&held); err != nil || held != 2 {
		t.Fatalf("tickets on review hold = %d (%v); want 2", held, err)
	}

	// a replay writes nothing more
	post(event("re_dash_1", piRef, 2500))
	if n := count(`payment_intent_id = $1 AND origin = 'provider_dashboard'`, f.pay); n != 1 {
		t.Fatalf("after a replay there are %d dashboard refunds; want 1", n)
	}

	// the rest of the payment: the order's tickets are cancelled
	post(event("re_dash_2", piRef, 2500))
	for i := range f.tickets {
		if st, _ := f.ticket(t, i); st != "cancelled" {
			t.Fatalf("ticket %d is %q after the payment was refunded in full; want cancelled", i, st)
		}
	}
}

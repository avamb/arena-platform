//go:build integration

package httpserver

import (
	"context"
	"net/http"
	"testing"
)

// Resolve over HTTP: platform superadmin only, X-Admin-Reason required, and a
// refund whose money really went back beyond the payment can be recorded as
// such instead of only as "not refunded".
func TestRefundResolve_HTTPSuperadminAndBeyondPayment(t *testing.T) {
	e := newSec1Env(t)
	org := e.org(t, "RESOLVE")
	_, super := e.superadmin(t)
	f := newPay03Order(t, e.srv.pgxPool, org, "stripe")
	ctx := context.Background()
	for i, tk := range f.tickets {
		if _, err := e.srv.pgxPool.Exec(ctx, `INSERT INTO refunds (payment_intent_id, org_id, order_id, ticket_id, amount, currency, reason,
			state, settlement, provider, provider_refund_id, provider_status, succeeded_at, settled_at)
			VALUES ($1, $2, $3, $4, 2500, 'EUR', 'first', 'succeeded', 'provider', 'stripe', $5, 'succeeded', now(), now())`,
			f.pay, org, f.order, tk, "re_resolve_ok_"+f.pay.String()[:8]+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	var parked string
	if err := e.srv.pgxPool.QueryRow(ctx, `INSERT INTO refunds (payment_intent_id, org_id, order_id, ticket_id, amount, currency, reason,
		state, settlement, provider, provider_refund_id, provider_attempts, failure_code, cancel_ticket)
		VALUES ($1, $2, $3, $4, 2500, 'EUR', 'late', 'manual_review', 'provider', 'stripe', $5, 1, 'late_acceptance_over_budget', true)
		RETURNING id::text`, f.pay, org, f.order, f.tickets[0], "re_resolve_late_"+f.pay.String()[:8]).Scan(&parked); err != nil {
		t.Fatal(err)
	}
	path := "/v1/organizations/" + org.String() + "/refunds/" + parked + "/resolve"

	if out := e.expect(t, http.StatusConflict, http.MethodPost, path, super, "checked the dashboard", `{"outcome":"refunded","note":"seen at stripe"}`); sec1Code(out) != "refund.over_budget" {
		t.Errorf("refunded beyond the payment: code %q; want refund.over_budget", sec1Code(out))
	}
	if out := e.expect(t, http.StatusBadRequest, http.MethodPost, path, super, "", `{"outcome":"refunded_beyond_payment","note":"seen at stripe"}`); sec1Code(out) != "superadmin.missing_reason" {
		t.Errorf("no reason: code %q; want superadmin.missing_reason", sec1Code(out))
	}
	e.expect(t, http.StatusOK, http.MethodPost, path, super, "checked the dashboard", `{"outcome":"refunded_beyond_payment","note":"seen at stripe twice"}`)
	var state string
	var cancel bool
	if err := e.srv.pgxPool.QueryRow(ctx, `SELECT state, cancel_ticket FROM refunds WHERE id = $1`, parked).Scan(&state, &cancel); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || cancel {
		t.Fatalf("resolved row = %s cancel_ticket=%v; want succeeded without a cancellation", state, cancel)
	}
}

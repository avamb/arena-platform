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

// A refund recorded as refunded beyond the payment changes no ticket: the
// ticket stays valid, so no ticket.refunded event may go out for it, and the
// sweep's repair must not pick it up later either.
func TestRefundResolve_BeyondPaymentAnnouncesNothing(t *testing.T) {
	e := newSec1Env(t)
	org := e.org(t, "RESOLVE")
	_, super := e.superadmin(t)
	f := newPay03Order(t, e.srv.pgxPool, org, "stripe")
	ctx := context.Background()
	// The whole payment went back through a refund that names no ticket.
	if _, err := e.srv.pgxPool.Exec(ctx, `INSERT INTO refunds (payment_intent_id, org_id, order_id, amount, currency, reason,
		state, settlement, provider, provider_refund_id, provider_status, succeeded_at, settled_at)
		VALUES ($1, $2, $3, 5000, 'EUR', 'order level', 'succeeded', 'provider', 'stripe', $4, 'succeeded', now(), now())`,
		f.pay, org, f.order, "re_beyond_order_"+f.pay.String()[:8]); err != nil {
		t.Fatal(err)
	}
	var parked string
	if err := e.srv.pgxPool.QueryRow(ctx, `INSERT INTO refunds (payment_intent_id, org_id, order_id, ticket_id, amount, currency, reason,
		state, settlement, provider, provider_refund_id, provider_attempts, failure_code, cancel_ticket)
		VALUES ($1, $2, $3, $4, 2500, 'EUR', 'late', 'manual_review', 'provider', 'stripe', $5, 1, 'late_acceptance_over_budget', false)
		RETURNING id::text`, f.pay, org, f.order, f.tickets[0], "re_beyond_late_"+f.pay.String()[:8]).Scan(&parked); err != nil {
		t.Fatal(err)
	}
	path := "/v1/organizations/" + org.String() + "/refunds/" + parked + "/resolve"
	e.expect(t, http.StatusOK, http.MethodPost, path, super, "checked the dashboard", `{"outcome":"refunded_beyond_payment","note":"seen at stripe twice"}`)

	var events int
	if err := e.srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM outbox_events
		WHERE aggregate_id = $1::text AND event_type = 'v1.ticket.refunded'`, f.tickets[0]).Scan(&events); err != nil {
		t.Fatal(err)
	}
	var settled, ticketStatus string
	if err := e.srv.pgxPool.QueryRow(ctx, `SELECT (r.settled_at IS NOT NULL)::text, t.status FROM refunds r JOIN tickets t ON t.id = r.ticket_id
		WHERE r.id = $1`, parked).Scan(&settled, &ticketStatus); err != nil {
		t.Fatal(err)
	}
	if events != 0 || settled != "true" || ticketStatus != "active" {
		t.Fatalf("after refunded_beyond_payment: %d ticket.refunded event(s), settled=%s, ticket %s; want 0, true, active",
			events, settled, ticketStatus)
	}
}

// A parked row without a provider (migration 0138 moved such rows to
// manual_review, and the flat approve route can still park one) is closed by
// a platform superadmin, either way, with a note.
func TestRefundResolve_ParkedRowWithoutProvider(t *testing.T) {
	e := newSec1Env(t)
	org := e.org(t, "RESOLVE")
	_, super := e.superadmin(t)
	f := newPay03Order(t, e.srv.pgxPool, org, "stripe")
	ctx := context.Background()
	legacy := func(reason string) string {
		t.Helper()
		var id string
		if err := e.srv.pgxPool.QueryRow(ctx, `INSERT INTO refunds (payment_intent_id, org_id, amount, currency, reason,
			state, settlement, provider_attempts)
			VALUES ($1, $2, 1000, 'EUR', $3, 'manual_review', 'provider', 0) RETURNING id::text`, f.pay, org, reason).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	base := "/v1/organizations/" + org.String() + "/refunds/"
	byHand, never := legacy("returned by hand"), legacy("never sent")

	e.expect(t, http.StatusOK, http.MethodPost, base+byHand+"/resolve", super, "checked the dashboard", `{"outcome":"refunded","note":"returned by hand on 1 Oct"}`)
	e.expect(t, http.StatusOK, http.MethodPost, base+never+"/resolve", super, "checked the dashboard", `{"outcome":"not_refunded","note":"nothing at stripe"}`)
	for id, want := range map[string]string{byHand: "succeeded", never: "failed"} {
		var state string
		if err := e.srv.pgxPool.QueryRow(ctx, `SELECT state FROM refunds WHERE id = $1`, id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != want {
			t.Errorf("row without a provider %s = %s; want %s", id, state, want)
		}
	}
	var audits int
	if err := e.srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'v1.refund.resolve'
		AND resource_id IN ($1, $2) AND metadata->>'note' <> ''`, byHand, never).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Errorf("audit rows with a note = %d; want 2", audits)
	}
}

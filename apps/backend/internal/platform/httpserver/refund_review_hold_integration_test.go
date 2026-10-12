//go:build integration

package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Lifting a review hold: platform superadmin only, with X-Admin-Reason and a
// note, only for a ticket of the path organization, and audited.
func TestReviewHoldClear_SuperadminOnlyInsideTheOrganization(t *testing.T) {
	e := newSec1Env(t)
	org, other := e.org(t, "HOLD"), e.org(t, "HOLDOTHER")
	_, super := e.superadmin(t)
	owner := e.user(t, "hold-owner")
	e.member(t, owner, org, "org_admin")
	f := newPay03Order(t, e.srv.pgxPool, org, "stripe")
	ctx := context.Background()
	if _, err := e.srv.pgxPool.Exec(ctx, `UPDATE tickets SET review_hold = true, review_hold_reason = 'partial refund' WHERE id = $1`, f.tickets[0]); err != nil {
		t.Fatal(err)
	}
	path := "/v1/organizations/" + org.String() + "/tickets/" + f.tickets[0].String() + "/review-hold/clear"
	body := `{"note":"attributed the partial refund to ticket 2"}`

	if out := e.expect(t, http.StatusForbidden, http.MethodPost, path, e.token(t, owner), "", body); sec1Code(out) != "ticket.review_hold_superadmin_only" {
		t.Errorf("owner: code %q", sec1Code(out))
	}
	if out := e.expect(t, http.StatusBadRequest, http.MethodPost, path, super, "", body); sec1Code(out) != "superadmin.missing_reason" {
		t.Errorf("no reason: code %q", sec1Code(out))
	}
	e.expect(t, http.StatusBadRequest, http.MethodPost, path, super, "support ticket 42", `{"note":""}`)
	foreign := "/v1/organizations/" + other.String() + "/tickets/" + f.tickets[0].String() + "/review-hold/clear"
	e.expect(t, http.StatusNotFound, http.MethodPost, foreign, super, "support ticket 42", body)

	e.expect(t, http.StatusOK, http.MethodPost, path, super, "support ticket 42", body)
	var held bool
	if err := e.srv.pgxPool.QueryRow(ctx, `SELECT review_hold FROM tickets WHERE id = $1`, f.tickets[0]).Scan(&held); err != nil || held {
		t.Fatalf("review_hold = %v (%v); want lifted", held, err)
	}
	var audited bool
	_ = e.srv.pgxPool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM audit_events WHERE action = 'v1.ticket.review_hold_clear' AND resource_id = $1)`,
		f.tickets[0].String()).Scan(&audited)
	if !audited {
		t.Error("lifting the hold wrote no audit event")
	}
	if out := e.expect(t, http.StatusConflict, http.MethodPost, path, super, "support ticket 42", body); sec1Code(out) != "ticket.not_on_review_hold" {
		t.Errorf("again: code %q", sec1Code(out))
	}
}

// A partial refund that named no ticket stays unattributed after the hold is
// lifted: the ticket can then be refunded in full on top of it. The operator
// is shown that amount and must confirm it, and the audit keeps it.
func TestReviewHoldClear_ShowsAndConfirmsTheUnattributedAmount(t *testing.T) {
	e := newSec1Env(t)
	org := e.org(t, "HOLD")
	_, super := e.superadmin(t)
	f := newPay03Order(t, e.srv.pgxPool, org, "stripe")
	ctx := context.Background()
	if _, err := e.srv.pgxPool.Exec(ctx, `INSERT INTO refunds (payment_intent_id, org_id, order_id, amount, currency, reason,
		state, settlement, provider, provider_refund_id, provider_status, succeeded_at, settled_at)
		VALUES ($1, $2, $3, 1000, 'EUR', 'partial in the dashboard', 'succeeded', 'provider', 'stripe', $4, 'succeeded', now(), now())`,
		f.pay, org, f.order, "re_hold_partial_"+f.pay.String()[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.pgxPool.Exec(ctx, `UPDATE tickets SET review_hold = true, review_hold_reason = 'partial refund' WHERE id = $1`, f.tickets[0]); err != nil {
		t.Fatal(err)
	}
	path := "/v1/organizations/" + org.String() + "/tickets/" + f.tickets[0].String() + "/review-hold/clear"

	out := e.expect(t, http.StatusConflict, http.MethodPost, path, super, "support ticket 43", `{"note":"looked at it"}`)
	var refused struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &refused); err != nil {
		t.Fatal(err)
	}
	if refused.Error.Code != "ticket.review_hold_unattributed" || refused.Error.Details["unattributed_amount"] != float64(1000) ||
		refused.Error.Details["currency"] != "EUR" {
		t.Fatalf("lifting without confirming: %s", out)
	}
	e.expect(t, http.StatusConflict, http.MethodPost, path, super, "support ticket 43", `{"note":"looked at it","confirm_unattributed":900}`)
	out = e.expect(t, http.StatusOK, http.MethodPost, path, super, "support ticket 43", `{"note":"the 10 EUR belongs to the service fee","confirm_unattributed":1000}`)
	if !strings.Contains(out, `"unattributed_amount":1000`) {
		t.Errorf("answer does not state the unattributed amount: %s", out)
	}
	var audited string
	if err := e.srv.pgxPool.QueryRow(ctx, `SELECT COALESCE(metadata->>'unattributed_amount', '') FROM audit_events
		WHERE action = 'v1.ticket.review_hold_clear' AND resource_id = $1`, f.tickets[0].String()).Scan(&audited); err != nil {
		t.Fatal(err)
	}
	if audited != "1000" {
		t.Errorf("audited unattributed amount = %q; want 1000", audited)
	}
}

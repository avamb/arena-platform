//go:build integration

package httpserver

import (
	"context"
	"net/http"
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

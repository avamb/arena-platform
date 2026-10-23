//go:build integration

package httpserver

// billing_organizer_flag_integration_test.go - BILLING_ORGANIZER_ENABLED.
// While the flag is off, every organizer route of credit packs and platform
// invoices answers 404 billing.organizer_disabled to a member (401 without a
// token), writes nothing, and the operator console still works. Turning the
// flag on opens the same routes without a restart (the config is read per
// request).

import (
	"context"
	"net/http"
	"testing"
)

func TestBillingOrganizerFlag_OffClosesEveryOrganizerRoute(t *testing.T) {
	e := newAcctEnv(t)
	e.srv.cfg.BillingOrganizerEnabled = false
	org := "/v1/organizations/" + e.orgA.String() + "/billing"
	invoice := "00000000-0000-7000-8000-000000000001"
	routes := []struct{ method, path, body string }{
		{http.MethodGet, org + "/balance", ""},
		{http.MethodGet, org + "/packs", ""},
		{http.MethodGet, org + "/ledger", ""},
		{http.MethodGet, org + "/service-fee-quote", ""},
		{http.MethodGet, org + "/account-invoices", ""},
		{http.MethodGet, org + "/account-invoices/" + invoice, ""},
		{http.MethodPost, org + "/packs/" + invoice + "/order", `{"payment_method":"bank_transfer"}`},
		{http.MethodPost, org + "/account-invoices/" + invoice + "/pay-by-card", ""},
		{http.MethodPut, org + "/payer", `{"name":"Org A"}`},
	}
	invoicesOf := func() int {
		var n int
		if err := e.srv.pgxPool.QueryRow(context.Background(),
			`SELECT count(*) FROM invoices WHERE org_id = $1`, e.orgA).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := invoicesOf()
	hdr := map[string]string{"Idempotency-Key": "flag-off-test"}
	for _, r := range routes {
		if code, body := e.req(t, r.method, r.path, "", hdr, r.body); code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: %d %v, want 401", r.method, r.path, code, body["_raw"])
		}
		code, body := e.req(t, r.method, r.path, e.ownerA, hdr, r.body)
		if code != http.StatusNotFound || errCode(body) != "billing.organizer_disabled" {
			t.Errorf("%s %s as the owner: %d %v, want 404 billing.organizer_disabled", r.method, r.path, code, body["_raw"])
		}
	}
	if after := invoicesOf(); after != before {
		t.Errorf("invoices of the organization went %d -> %d with the flag off", before, after)
	}

	// The operator console is not behind the flag.
	if code, body := e.req(t, http.MethodGet, "/v1/admin/billing/payment-fees", e.super, nil, ""); code != http.StatusOK {
		t.Errorf("operator payment fees with the flag off: %d %v, want 200", code, body["_raw"])
	}

	// Turned on, the same route opens without a restart.
	e.srv.cfg.BillingOrganizerEnabled = true
	if code, body := e.req(t, http.MethodGet, org+"/packs", e.ownerA, nil, ""); code != http.StatusOK {
		t.Errorf("packs with the flag on: %d %v, want 200", code, body["_raw"])
	}
}

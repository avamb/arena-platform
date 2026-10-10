//go:build integration

// refund_org_isolation_integration_test.go — PAY-00 (spec 36 §8): the flat
// routes /v1/refunds/*, /v1/payment-intents/{id} and POST /v1/payment-intents
// learn the organization from the row (or the body) and must keep an
// organization API key — which may hold refund.* and payment_intent.* — inside
// its own organization. Until 2026-10-09 they checked only the scope, so a
// key of organization A could read, create and approve refunds of B's payment
// by UUID. A foreign row answers the route's own 404, never 403.
//
// Every request goes through the REAL router with organization API keys
// (prom0113Fixture, promoters_integration_test.go). Run against a fresh
// migrated database (AGENTS.md CI-Integration recipe).
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestRefundOrgIsolation_FlatRoutesStayInsideTheOrganization(t *testing.T) {
	f := newProm0113Fixture(t)
	ctx := context.Background()
	orgA, orgB := f.org(t, "A"), f.org(t, "B")
	keyA := f.key(t, orgA,
		"refund.read", "refund.create", "refund.approve",
		"payment_intent.read", "payment_intent.create", "payment_intent.update",
	)

	// A succeeded payment and a requested refund in each organization, plus a
	// fresh (created) payment of A for the transition route.
	paid := func(org uuid.UUID, state string) uuid.UUID {
		t.Helper()
		ref := "pay00-" + uuid.NewString()
		// A real refundable provider: since PAY-03 an unknown one (the old
		// "mock") is refused before the guard matters. The organization has no
		// Stripe config, so approving drives the refund straight to failed.
		pi, err := f.q.InsertPaymentIntent(ctx, nil, org, "stripe", &ref, 1000, "EUR", state, nil, nil)
		if err != nil {
			t.Fatalf("InsertPaymentIntent: %v", err)
		}
		return pi.ID
	}
	piA, piB, piA2 := paid(orgA, "succeeded"), paid(orgB, "succeeded"), paid(orgA, "created")
	refund := func(org, pi uuid.UUID) uuid.UUID {
		t.Helper()
		rf, err := f.q.InsertRefund(ctx, pi, org, 200, "EUR", nil, nil)
		if err != nil {
			t.Fatalf("InsertRefund: %v", err)
		}
		return rf.ID
	}
	refA, refA2, refB := refund(orgA, piA), refund(orgA, piA), refund(orgB, piB)

	expect := func(method, path, body string, want int, wantCode string) map[string]any {
		t.Helper()
		st, out := f.do(t, method, path, keyA, body)
		if st != want {
			t.Fatalf("%s %s: status %d body %v, want %d", method, path, st, out, want)
		}
		if wantCode != "" && prom0113Code(out) != wantCode {
			t.Fatalf("%s %s: code %q, want %q", method, path, prom0113Code(out), wantCode)
		}
		return out
	}

	// Reads.
	expect(http.MethodGet, "/v1/refunds/"+refA.String(), "", http.StatusOK, "")
	expect(http.MethodGet, "/v1/refunds/"+refB.String(), "", http.StatusNotFound, "refund.not_found")
	expect(http.MethodGet, "/v1/payment-intents/"+piA.String(), "", http.StatusOK, "")
	expect(http.MethodGet, "/v1/payment-intents/"+piB.String(), "", http.StatusNotFound, "payment_intent.not_found")

	// Creating a refund names the payment in the body: a foreign payment is
	// "not found", the own one is refunded.
	createBody := func(pi uuid.UUID) string {
		return fmt.Sprintf(`{"payment_intent_id":%q,"amount":100,"currency":"EUR","reason":"pay00"}`, pi)
	}
	expect(http.MethodPost, "/v1/refunds", createBody(piB), http.StatusNotFound, "refund.payment_intent_not_found")
	created := expect(http.MethodPost, "/v1/refunds", createBody(piA), http.StatusCreated, "")
	if got := created["refund"].(map[string]any)["org_id"]; got != orgA.String() {
		t.Fatalf("created refund org_id = %v, want %s", got, orgA)
	}
	var count int
	if err := f.q.DB().QueryRow(ctx, `SELECT count(*) FROM refunds WHERE payment_intent_id = $1`, piB).Scan(&count); err != nil {
		t.Fatalf("count refunds of B: %v", err)
	}
	if count != 1 {
		t.Fatalf("organization B has %d refunds, want the 1 seeded one — the refused create must write nothing", count)
	}

	// Approve and reject.
	expect(http.MethodPost, "/v1/refunds/"+refB.String()+"/approve", "{}", http.StatusNotFound, "refund.not_found")
	expect(http.MethodPost, "/v1/refunds/"+refB.String()+"/reject", "{}", http.StatusNotFound, "refund.not_found")
	expect(http.MethodPost, "/v1/refunds/"+refA.String()+"/approve", "{}", http.StatusOK, "")
	expect(http.MethodPost, "/v1/refunds/"+refA2.String()+"/reject", "{}", http.StatusOK, "")
	var stateB string
	if err := f.q.DB().QueryRow(ctx, `SELECT state FROM refunds WHERE id = $1`, refB).Scan(&stateB); err != nil {
		t.Fatalf("read refund B: %v", err)
	}
	if stateB != "requested" {
		t.Fatalf("refund B state = %q, want requested (untouched)", stateB)
	}

	// Payment intent transitions and creation.
	expect(http.MethodPost, "/v1/payment-intents/"+piB.String()+"/transition", `{"state":"processing"}`, http.StatusNotFound, "payment_intent.not_found")
	expect(http.MethodPost, "/v1/payment-intents/"+piA2.String()+"/transition", `{"state":"processing"}`, http.StatusOK, "")
	piBody := func(org uuid.UUID) string {
		return fmt.Sprintf(`{"org_id":%q,"provider":"stripe","amount":100,"currency":"EUR"}`, org)
	}
	expect(http.MethodPost, "/v1/payment-intents", piBody(orgB), http.StatusNotFound, "payment_intent.org_not_found")
	// The own organization passes the guard and fails later on the missing
	// provider configuration — proof that the organization check came first.
	expect(http.MethodPost, "/v1/payment-intents", piBody(orgA), http.StatusUnprocessableEntity, "payment.provider_not_configured")
}

//go:build integration

package httpserver

import (
	"net/http"
	"testing"
)

// R3-4: only refunds that reached the provider take the payment's budget. A
// refund still requested, approved or not yet sent moved no money, so a
// dashboard refund of the whole payment is recorded in full and cancels the
// tickets instead of being capped or parked as money beyond the payment.
func TestRefundWebhook_DashboardBudgetCountsOnlyRefundsThatReachedTheProvider(t *testing.T) {
	e := newDashEnv(t, "DASHR4B")
	e.exec(t, `INSERT INTO refunds (payment_intent_id, org_id, amount, currency, reason, state, settlement, provider)
	           VALUES ($1, $2, 2500, 'EUR', 'asked, not approved', 'requested', 'provider', 'stripe'),
	                  ($1, $2, 2500, 'EUR', 'approved, not sent', 'approved', 'provider', 'stripe'),
	                  ($1, $2, 2500, 'EUR', 'claimed, never called', 'provider_pending', 'provider', 'stripe')`, e.f.pay, e.org)
	e.stub.setRefund("re_d4_budget", "succeeded", 5000, "eur", e.piRef)
	if st, out := e.post(t, "refund.created", "re_d4_budget", e.piRef, 5000); st != http.StatusOK || out["processed"] != true {
		t.Fatalf("dashboard refund = %d %v", st, out)
	}
	if state, code, amount, alert := e.row(t, "re_d4_budget"); state != "succeeded" || code != "" || amount != 5000 || alert {
		t.Fatalf("row = %s %q %d alert=%v; want succeeded, no code, 5000, no alert", state, code, amount, alert)
	}
	if n := e.countTickets(t, `status = 'active'`); n != 0 {
		t.Fatalf("%d ticket(s) still active after a full dashboard refund", n)
	}

	// A refund the provider was called for does take the budget.
	e2 := newDashEnv(t, "DASHR4C")
	e2.exec(t, `INSERT INTO refunds (payment_intent_id, org_id, amount, currency, reason, state, settlement, provider,
	                                 provider_attempts, provider_attempted_at, provider_status)
	            VALUES ($1, $2, 2500, 'EUR', 'called, outcome unknown', 'provider_pending', 'provider', 'stripe',
	                    1, now() - interval '1 hour', 'unknown_outcome')`, e2.f.pay, e2.org)
	e2.stub.setRefund("re_d4_called", "succeeded", 5000, "eur", e2.piRef)
	if st, out := e2.post(t, "refund.created", "re_d4_called", e2.piRef, 5000); st != http.StatusOK {
		t.Fatalf("dashboard refund = %d %v", st, out)
	}
	if _, code, amount, alert := e2.row(t, "re_d4_called"); code != "dashboard_capped" || amount != 2500 || !alert {
		t.Fatalf("row = %q %d alert=%v; want dashboard_capped 2500 with an alert", code, amount, alert)
	}
}

func (e *dashEnv) countTickets(t *testing.T, where string) int {
	t.Helper()
	n := 0
	for _, id := range e.f.tickets {
		var ok bool
		if err := e.srv.pgxPool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM tickets WHERE id = $1 AND `+where+`)`, id).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if ok {
			n++
		}
	}
	return n
}

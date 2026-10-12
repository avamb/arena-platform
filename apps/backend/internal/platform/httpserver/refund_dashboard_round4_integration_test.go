//go:build integration

package httpserver

import (
	"net/http"
	"sync"
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

// R3-8 at the route: an event for an arena refund whose read-back keeps
// failing is answered 500 (the provider retries) until the last attempt,
// which is acknowledged with the refund untouched.
func TestRefundWebhook_KnownRefundUnreadableIsAcknowledgedAfterTheLastAttempt(t *testing.T) {
	e := newDashEnv(t, "DASHR4K")
	t.Cleanup(func() {
		_, _ = e.srv.pgxPool.Exec(t.Context(), `DELETE FROM audit_events WHERE resource_type = 'provider_refund' AND metadata->>'org_id' = $1::text`, e.org)
	})
	e.exec(t, `INSERT INTO refunds (payment_intent_id, org_id, amount, currency, reason, state, settlement, provider,
	                                provider_refund_id, provider_status, provider_attempts, provider_attempted_at)
	           VALUES ($1, $2, 2500, 'EUR', 'sent', 'provider_pending', 'provider', 'stripe', 're_d4_known', 'pending', 1, now())`, e.f.pay, e.org)
	e.stub.refundMu.Lock()
	if e.stub.refundObjects == nil {
		e.stub.refundObjects = map[string]string{}
	}
	e.stub.refundObjects["re_d4_known"] = ""
	e.stub.refundMu.Unlock()
	for i := 1; i < refundsMaxDashboardConfirmAttempts; i++ {
		if st, _ := e.post(t, "refund.updated", "re_d4_known", e.piRef, 2500); st != http.StatusInternalServerError {
			t.Fatalf("delivery %d = %d; want 500", i, st)
		}
	}
	if st, out := e.post(t, "refund.updated", "re_d4_known", e.piRef, 2500); st != http.StatusOK || out["processed"] != false {
		t.Fatalf("last delivery = %d %v; want 200 acknowledged, not processed", st, out)
	}
	var state string
	if err := e.srv.pgxPool.QueryRow(t.Context(), `SELECT state FROM refunds WHERE provider_refund_id = 're_d4_known'`).Scan(&state); err != nil || state != "provider_pending" {
		t.Fatalf("state = %q %v; want provider_pending", state, err)
	}
}

// The provider redelivers the same refund event in parallel: one refund row,
// one cancellation per ticket and one v1.ticket.refunded per ticket, however
// the deliveries interleave.
func TestRefundWebhook_ParallelRedeliveriesRecordTheRefundOnce(t *testing.T) {
	e := newDashEnv(t, "DASHR4P")
	e.stub.setRefund("re_d4_parallel", "succeeded", 5000, "eur", e.piRef)
	const n = 6
	codes := make(chan int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			st, _ := e.post(t, "refund.created", "re_d4_parallel", e.piRef, 5000)
			codes <- st
		}()
	}
	close(start)
	wg.Wait()
	close(codes)
	for st := range codes {
		if st != http.StatusOK {
			t.Fatalf("a parallel delivery answered %d; want 200", st)
		}
	}
	if c := e.count(t, `payment_intent_id = $1`, e.f.pay); c != 1 {
		t.Fatalf("%d refund rows; want 1", c)
	}
	if c := e.countTickets(t, `status = 'active'`); c != 0 {
		t.Fatalf("%d ticket(s) still active", c)
	}
	var events int
	if err := e.srv.pgxPool.QueryRow(t.Context(), `SELECT count(*) FROM outbox_events
		WHERE event_type = 'v1.ticket.refunded' AND aggregate_id = ANY($1::text[])`,
		[]string{e.f.tickets[0].String(), e.f.tickets[1].String()}).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("%d v1.ticket.refunded events; want one per ticket", events)
	}
}

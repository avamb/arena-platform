//go:build integration

// engine_review7_integration_test.go — the seventh PAY-03 review: the
// operator-facing TEXTS (stored failure reasons, ops alerts) must never
// assert something that can be false, and never give an unconditional
// instruction to cancel a ticket. Every test here fails on cf36dff.
package refunds_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// lateAcceptanceWithReplacement plays the M-b story on ticket 1: refund A
// (amount aAmount, cancel flag aCancel) is marked failed while its provider
// call is in flight, an operator creates the replacement B for the whole
// ticket with cancellation (not approved yet), and A's provider answers
// with answer. It returns A as recorded and B's id.
func lateAcceptanceWithReplacement(t *testing.T, f *fixture, e *refunds.Engine, m *fakeModule, aAmount int64, aCancel bool) (refunds.Refund, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var (
		once sync.Once
		bid  uuid.UUID
	)
	m.beforeRefund = func(req payments.RefundRequest) bool {
		once.Do(func() {
			f.exec(t, `UPDATE refunds SET state = 'failed', failed_at = now(), failure_code = 'operator' WHERE id = $1`, req.IdempotencyKey)
			in := f.batch("k-b-"+uuid.NewString()[:8], true, refunds.Item{TicketID: f.tickets[0]})
			in.Approved = false
			b, err := e.CreateBatch(ctx, in)
			if err != nil || len(b.Refunds) != 1 {
				t.Errorf("replacement: %v %+v", err, b.Refunds)
				return
			}
			bid = b.Refunds[0].ID
		})
		return true
	}
	a, err := e.CreateBatch(ctx, f.batch("k-a-"+uuid.NewString()[:8], aCancel, refunds.Item{TicketID: f.tickets[0], Amount: amountPtr(aAmount)}))
	if err != nil {
		t.Fatal(err)
	}
	if bid == uuid.Nil {
		t.Fatal("no replacement")
	}
	return a.Refunds[0], bid
}

// TestEngine_HeldBackReasonsNeverClaimAPartialRefundCoveredTheTicket
// (seventh review, item 1): A is a money-only refund of 500 on a 2500
// ticket, accepted late; B, the whole 2500 with cancellation, is held back.
// Only 500 went back — the reasons must not say the ticket's money went
// back through A, or a person cancels the ticket and the buyer loses 2000.
func TestEngine_HeldBackReasonsNeverClaimAPartialRefundCoveredTheTicket(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){succeeded}}
	e := f.engine(m, true)
	a, bid := lateAcceptanceWithReplacement(t, f, e, m, 500, false)
	if a.State != refunds.StateManualReview || deref(a.FailureCode) != "late_acceptance_over_budget" {
		t.Fatalf("A = %s / %s; want parked over budget", a.State, deref(a.FailureCode))
	}
	f.approveNow(t, bid)
	b, err := e.Drive(ctx, bid)
	if err != nil || b.State != refunds.StateManualReview || m.callCount() != 1 {
		t.Fatalf("B = %s calls %d err %v; want held back unsent", b.State, m.callCount(), err)
	}
	ra := f.refundState(t, a.ID)
	for name, reason := range map[string]string{"A": deref(ra.FailureReason), "B": deref(b.FailureReason)} {
		for _, wrong := range []string{"went back through", "went back once", "already returned", ") cover its price", "Nothing was sent twice"} {
			if strings.Contains(reason, wrong) {
				t.Fatalf("%s reason %q claims %q; only 500 of 2500 went back", name, reason, wrong)
			}
		}
	}
	if !strings.Contains(deref(b.FailureReason), "do NOT cover") {
		t.Fatalf("B reason %q; want it to say the accepted refunds do not cover the ticket", deref(b.FailureReason))
	}
	if st, _ := f.ticketStatus(t, f.tickets[0]); st != "active" {
		t.Fatalf("ticket = %s", st)
	}
}

// TestEngine_PendingLateAcceptanceIsNotCalledConfirmed (seventh review,
// item 3): A's late acceptance is PENDING. A parked row is never read back
// by the lookup, so its reason and alert must say the provider has not
// confirmed it yet, and B's reason must not say the money went back.
func TestEngine_PendingLateAcceptanceIsNotCalledConfirmed(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){pending}}
	e := f.engine(m, true)
	a, bid := lateAcceptanceWithReplacement(t, f, e, m, 2500, true)
	if a.State != refunds.StateManualReview || deref(a.ProviderStatus) != "pending" {
		t.Fatalf("A = %s provider_status %s; want parked with a pending acceptance", a.State, deref(a.ProviderStatus))
	}
	f.approveNow(t, bid)
	b, err := e.Drive(ctx, bid)
	if err != nil {
		t.Fatal(err)
	}
	ra := f.refundState(t, a.ID)
	if !strings.Contains(deref(ra.FailureReason), "not confirmed") {
		t.Fatalf("A reason %q; want it to say the provider has not confirmed it", deref(ra.FailureReason))
	}
	for name, reason := range map[string]string{"A": deref(ra.FailureReason), "B": deref(b.FailureReason)} {
		if strings.Contains(reason, "went back through") || strings.Contains(reason, "went back once") || strings.Contains(reason, "Nothing was sent twice") {
			t.Fatalf("%s reason %q calls an unconfirmed refund done", name, reason)
		}
	}
	n := &recordingNotifier{}
	if _, err := e.Sweep(ctx, time.Now(), n); err != nil {
		t.Fatal(err)
	}
	var aAlert string
	for _, text := range n.texts {
		if strings.Contains(text, a.ID.String()) {
			aAlert = text
		}
	}
	if !strings.Contains(aAlert, "not confirmed") {
		t.Fatalf("A alert %q; want it to say the provider has not confirmed it", aAlert)
	}
}

// TestEngine_RevivedMoneyOnlyRefundAlertDoesNotSayTheTicketIsCancelled
// (seventh review, item 4): a money-only refund marked failed and accepted
// late within budget is revived as succeeded; it cancels nothing, so its
// "accepted late" alert must not say its ticket is cancelled.
func TestEngine_RevivedMoneyOnlyRefundAlertDoesNotSayTheTicketIsCancelled(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){succeeded}}
	e := f.engine(m, true)
	var once sync.Once
	m.beforeRefund = func(req payments.RefundRequest) bool {
		once.Do(func() {
			f.exec(t, `UPDATE refunds SET state = 'failed', failed_at = now(), failure_code = 'operator' WHERE id = $1`, req.IdempotencyKey)
		})
		return true
	}
	res, err := e.CreateBatch(ctx, f.batch("k-money-only", false, refunds.Item{TicketID: f.tickets[0], Amount: amountPtr(500)}))
	if err != nil || res.Refunds[0].State != refunds.StateSucceeded {
		t.Fatalf("revived: %v %+v", err, res.Refunds)
	}
	n := &recordingNotifier{}
	if _, err := e.Sweep(ctx, time.Now(), n); err != nil {
		t.Fatal(err)
	}
	if len(n.texts) != 1 || strings.Contains(n.texts[0], "ticket is cancelled") || !strings.Contains(n.texts[0], "still active") {
		t.Fatalf("alerts = %v; want one that says the ticket is still active", n.texts)
	}
	if st, _ := f.ticketStatus(t, f.tickets[0]); st != "active" {
		t.Fatalf("ticket = %s", st)
	}
}

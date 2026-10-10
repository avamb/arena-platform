//go:build integration

// engine_review5_integration_test.go — the findings of the FIFTH PAY-03
// review, each pinned against a live database (same fixture and fake module
// as engine_integration_test.go). Every test here fails on the code before
// its fix.
package refunds_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

func amountPtr(v int64) *int64 { return &v }

// TestEngine_AttemptedRefundParkedForBudgetKeepsCounting (M-1): refund A of
// 1500 on ticket 1 is marked failed by an operator while its provider call
// is in flight; the operator's replacement B of the whole ticket (2500) is
// sent, and its call ends without an answer (a timeout may have created the
// refund at the provider). Then A's provider accepts after all. B's retry is
// parked for the budget — but B WAS attempted, so its money may be out: it
// must keep counting against the payment, or a new refund of ticket 2 lets
// the money going back exceed what was paid (1500 + 2500 + 2500 > 5000).
func TestEngine_AttemptedRefundParkedForBudgetKeepsCounting(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	// The first answer goes to B (sent from inside A's call), the second
	// to A.
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){timeout, succeeded}}
	e := f.engine(m, true)
	var (
		mu    sync.Mutex
		fired bool
		bid   uuid.UUID
	)
	// Not sync.Once: B is driven from INSIDE this hook and calls it again.
	m.beforeRefund = func(req payments.RefundRequest) bool {
		mu.Lock()
		first := !fired
		fired = true
		mu.Unlock()
		if first {
			f.exec(t, `UPDATE refunds SET state = 'failed', failed_at = now(), failure_code = 'operator' WHERE id = $1`, req.IdempotencyKey)
			b, err := e.CreateBatch(ctx, f.batch("k-replacement", true, refunds.Item{TicketID: f.tickets[0]}))
			if err != nil || len(b.Refunds) != 1 {
				t.Errorf("replacement: %v %+v", err, b.Refunds)
				return true
			}
			bid = b.Refunds[0].ID
		}
		return true
	}
	a, err := e.CreateBatch(ctx, f.batch("k-original", true, refunds.Item{TicketID: f.tickets[0], Amount: amountPtr(1500)}))
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Refunds[0]; got.State != refunds.StateManualReview || deref(got.FailureCode) != "late_acceptance_over_budget" {
		t.Fatalf("A = %s / %s; want parked over budget", got.State, deref(got.FailureCode))
	}
	if bid == uuid.Nil {
		t.Fatal("no replacement")
	}
	if b := f.refundState(t, bid); b.State != refunds.StateProviderPending || b.ProviderAttempts != 1 {
		t.Fatalf("B = %s attempts %d; want waiting after an unanswered call", b.State, b.ProviderAttempts)
	}
	f.staleMarker(t, bid)
	b, err := e.Drive(ctx, bid)
	if err != nil {
		t.Fatal(err)
	}
	if m.callCount() != 2 {
		t.Fatalf("provider calls = %d; B's retry must not be sent", m.callCount())
	}
	// B's money may be out: the payment has 5000 - 1500 - 2500 = 1000 left.
	_, err = e.CreateBatch(ctx, f.batch("k-ticket2", true, refunds.Item{TicketID: f.tickets[1]}))
	if refusalCode(err) != refunds.CodeAmountExceeds {
		t.Fatalf("refund of ticket 2: %v; want %s — B still counts", err, refunds.CodeAmountExceeds)
	}
	if b.State != refunds.StateManualReview || deref(b.FailureCode) == "budget_taken_by_another_refund" ||
		strings.Contains(deref(b.FailureReason), "never sent") {
		t.Fatalf("B = %s / %s %q; want parked with a code and reason that say it may have been sent",
			b.State, deref(b.FailureCode), deref(b.FailureReason))
	}
	if m.callCount() != 2 {
		t.Fatalf("provider calls = %d after the refused batch", m.callCount())
	}
}

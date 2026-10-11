//go:build integration

package refunds_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// TestEngine_AcceptedAgainStartsWithAFreshRepairBudget (PAY-03 follow-up 2):
// repair_attempts counts failed attempts to finish an accepted refund. A
// refund that is accepted AGAIN (a late acceptance reviving a parked or
// failed row) must not inherit the failures of its earlier life, or the very
// first hiccup parks it with ticket_cancellation_failed.
func TestEngine_AcceptedAgainStartsWithAFreshRepairBudget(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	f := newFixture(t, pool, "pay03fake")
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){succeeded}}
	e := f.engine(m, true)
	var first atomic.Bool
	m.beforeRefund = func(req payments.RefundRequest) bool {
		if first.CompareAndSwap(false, true) {
			// The row is failed meanwhile, carrying ten old repair failures.
			f.exec(t, `UPDATE refunds SET state = 'failed', failed_at = now(), failure_code = 'operator', repair_attempts = 10 WHERE id = $1`, req.IdempotencyKey)
		}
		return true
	}
	res, err := e.CreateBatch(ctx, f.batch("k-fresh-repair", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil {
		t.Fatal(err)
	}
	var attempts int
	var state string
	if err := pool.QueryRow(ctx, `SELECT repair_attempts, state FROM refunds WHERE id = $1`, res.Refunds[0].ID).Scan(&attempts, &state); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || attempts != 0 {
		t.Fatalf("state %q repair_attempts %d; want succeeded with a fresh budget", state, attempts)
	}
}

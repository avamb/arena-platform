//go:build integration

package refunds_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

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

// TestEngine_TicketCancelRefundPublishesEvenWhenTheLinkFailed (PAY-03
// follow-up 3): POST /v1/tickets/{id}/cancel links tickets.refund_id on a
// best-effort basis. When that write failed, the refund's own marker
// (cancelled_ticket_id) still names the ticket it speaks for, so the
// accepted refund must publish v1.ticket.refunded for that ticket and not
// settle with an empty scope.
func TestEngine_TicketCancelRefundPublishesEvenWhenTheLinkFailed(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	adult := f.tickets[0]
	f.exec(t, `UPDATE order_items SET total = CASE WHEN ticket_id = $1 THEN 5000 ELSE 0 END WHERE order_id = $2`, adult, f.order)
	id := f.ticketCancelRefund(t, adult, 2500)
	f.exec(t, `UPDATE tickets SET refund_id = NULL WHERE id = $1`, adult) // the link failed
	f.approveNow(t, id)

	pub := &publishCounter{}
	e := f.engineWith(&fakeModule{partial: true}, true, func(o *refunds.Options) { o.PublishRefunded = pub.fn })
	r, err := e.Drive(ctx, id)
	if err != nil || r.State != refunds.StateSucceeded {
		t.Fatalf("Drive: %v %s", err, r.State)
	}
	if pub.get(adult) != 1 || pub.total() != 1 {
		t.Fatalf("published = %v; want v1.ticket.refunded for the cancelled ticket once", pub.per)
	}
	var settled *time.Time
	if err := f.pool.QueryRow(ctx, `SELECT settled_at FROM refunds WHERE id = $1`, id).Scan(&settled); err != nil || settled == nil {
		t.Fatalf("settled_at %v err %v; the refund must settle", settled, err)
	}
}

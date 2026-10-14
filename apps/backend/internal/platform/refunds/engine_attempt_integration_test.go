//go:build integration

// engine_attempt_integration_test.go — PAY-14b review L-A: a provider without
// an idempotency key (AllPay) decides whether it may send a refund from what
// the engine tells it about earlier calls. If the engine stopped filling
// Attempt, FirstAttemptAt or the two sums, that protection would switch off
// without any other test noticing.
package refunds_test

import (
	"context"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

func TestEngine_TellsTheProviderAboutEarlierCalls(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ok := func(req payments.RefundRequest) (payments.RefundResult, error) {
		return payments.RefundResult{ProviderRefundID: "re_" + req.IdempotencyKey, Status: payments.RefundSucceeded}, nil
	}
	// Ticket 0: the first call has no answer. Ticket 1: accepted. Ticket 0
	// again: accepted on the retry.
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){timeout, ok, ok}}
	e := f.engine(m, true)
	ctx := context.Background()

	first, err := e.CreateBatch(ctx, f.batch("k-attempt-0", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil {
		t.Fatal(err)
	}
	r0 := first.Refunds[0]
	f.staleMarker(t, r0.ID) // the call is over; another refund of the payment may be claimed

	if _, err := e.CreateBatch(ctx, f.batch("k-attempt-1", true, refunds.Item{TicketID: f.tickets[1]})); err != nil {
		t.Fatal(err)
	}
	f.staleMarker(t, r0.ID)
	if _, err := e.Sweep(ctx, time.Now(), nil); err != nil {
		t.Fatal(err)
	}

	if m.callCount() != 3 {
		t.Fatalf("calls = %d; want 3 (ticket 0, ticket 1, ticket 0 again)", m.callCount())
	}
	c0, c1, c2 := m.calls[0], m.calls[1], m.calls[2]
	if c0.attempt != 1 || c0.firstAttemptAt.IsZero() || c0.known != 0 || c0.unsure != 0 {
		t.Fatalf("first call = %+v; want attempt 1, a first-attempt time, nothing known or unsure", c0)
	}
	// Ticket 1 is refunded while ticket 0's first call is unanswered: its
	// 2500 may already be back.
	if c1.key == c0.key || c1.attempt != 1 || c1.known != 0 || c1.unsure != 2500 {
		t.Fatalf("second refund's call = %+v; want attempt 1, known 0, unsure 2500", c1)
	}
	// The retry of ticket 0: attempt 2, the SAME first-attempt time, and
	// ticket 1's accepted 2500 as known.
	if c2.key != c0.key || c2.attempt != 2 || !c2.firstAttemptAt.Equal(c0.firstAttemptAt) || c2.known != 2500 || c2.unsure != 0 {
		t.Fatalf("retry = %+v; want key %s, attempt 2, first attempt %s, known 2500, unsure 0", c2, c0.key, c0.firstAttemptAt)
	}
	var stored time.Time
	if err := f.pool.QueryRow(ctx, `SELECT first_attempted_at FROM refunds WHERE id = $1`, r0.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !stored.Equal(c0.firstAttemptAt) {
		t.Fatalf("FirstAttemptAt = %s; want the stored first_attempted_at %s", c0.firstAttemptAt, stored)
	}
}

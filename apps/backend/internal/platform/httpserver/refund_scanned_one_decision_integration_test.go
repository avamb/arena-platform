//go:build integration

// refund_scanned_one_decision_integration_test.go - one scan exception never
// covers two money decisions made at the same time.
package httpserver

import (
	"context"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// L1: two money decisions read the same live exception at once; only the one
// whose UPDATE wins may go on, the other is refused as scanned.
func TestRefundScanned_OneExceptionCoversOneDecision(t *testing.T) {
	e := newScanEnv(t)
	e.dropExceptionsOnCleanup(t)
	ctx := context.Background()
	e.exec(t, `UPDATE tickets SET used_at = now() - interval '1 hour' WHERE id = $1`, e.f.tickets[0])
	e.grantException(t, 0)
	first, err := e.srv.pgxPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Rollback(ctx) }()
	if blocked, err := refunds.GuardTicketMoneyTx(ctx, first, e.f.tickets[0], nil, true); err != nil || blocked {
		t.Fatalf("first decision: blocked=%v err=%v", blocked, err)
	}
	second, err := e.srv.pgxPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Rollback(ctx) }()
	var pid int
	if err := second.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	type result struct {
		blocked bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		b, err := refunds.GuardTicketMoneyTx(ctx, second, e.f.tickets[0], nil, true)
		done <- result{b, err}
	}()
	// Wait until the second decision has read the exception as live and is
	// waiting for the first one's row lock.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err := e.srv.pgxPool.QueryRow(ctx, `SELECT wait_event_type = 'Lock' FROM pg_stat_activity WHERE pid = $1`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the second decision never waited for the first one's lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || !got.blocked {
		t.Fatalf("second decision on the same exception: blocked=%v err=%v; want blocked", got.blocked, got.err)
	}
}

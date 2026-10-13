//go:build integration

package refunds_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// The second check in CreateBatch's insert: when a concurrent writer (a
// manual cancellation) spends a scanned ticket's exception after the plan saw
// it live and before the new refund could take it, the refund is refused
// with ticket_scanned and nothing is written; one exception never covers two
// money decisions.
func TestEngine_ExceptionSpentByAConcurrentWriterRefusesTheRefund(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	f.exec(t, `UPDATE tickets SET used_at = now() - interval '1 hour' WHERE id = $1`, f.tickets[0])
	f.exec(t, `INSERT INTO refund_scan_exceptions (org_id, ticket_id, granted_by, reason, created_at, expires_at)
		VALUES ($1, $2, 'operator', 'show stopped', now() - interval '1 minute', now() + interval '1 hour')`, f.org, f.tickets[0])
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM refund_scan_exceptions WHERE ticket_id = $1`, f.tickets[0])
	})

	// The concurrent writer holds the exception's row while the refund is
	// written, then spends it for a decision with no refund row.
	holder, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, `SELECT 1 FROM refund_scan_exceptions WHERE ticket_id = $1 FOR UPDATE`, f.tickets[0]); err != nil {
		t.Fatal(err)
	}
	e := f.engine(&fakeModule{partial: true}, true)
	done := make(chan error, 1)
	go func() {
		_, err := e.CreateBatch(ctx, f.batch("k-second-layer", true, refunds.Item{TicketID: f.tickets[0]}))
		done <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query ILIKE '%refund_scan_exceptions%' AND pid <> pg_backend_pid()`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the refund never waited for the exception's row")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := holder.Exec(ctx, `UPDATE refund_scan_exceptions SET used_at = now() WHERE ticket_id = $1`, f.tickets[0]); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	err = <-done
	var re *refunds.Error
	if !errors.As(err, &re) || re.Code != refunds.CodeTicketScanned {
		t.Fatalf("refund after the exception was spent elsewhere: %v; want %s", err, refunds.CodeTicketScanned)
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE ticket_id = $1`, f.tickets[0]).Scan(&n); err != nil || n != 0 {
		t.Fatalf("refund rows for the ticket = %d (%v); want none", n, err)
	}
}

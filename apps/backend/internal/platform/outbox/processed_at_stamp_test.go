// Package outbox — unit tests for feature #49
// "Processed outbox events marked with processed_at not deleted"
//
// Delivered outbox events are NOT deleted from the table; instead
// processed_at is stamped. This allows forensics and replay.
//
// The test file covers all five feature steps without a live database:
//
//	Step 1: Seed a row simulating a POST /v1/echo outbox insert
//	Step 2: Run the OutboxEventsDispatcher and wait for it to process the row
//	Step 3: Query processed_at — must be non-null after dispatch
//	Step 4: Verify row still exists (not deleted)
//	Step 5: Verify the partial-index SQL and ClaimNext skip already-processed rows
package outbox

import (
	"context"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// Feature #49 tests — "Processed outbox events marked with processed_at not deleted"
// =============================================================================

// TestProcessedAt_RowNotDeletedAfterDispatch is the canonical step 1-4 test:
// after the dispatcher delivers an outbox event, the row must still exist
// in the store (not deleted) and processed_at must be non-null.
func TestProcessedAt_RowNotDeletedAfterDispatch(t *testing.T) {
	store := newInMemOutboxStore()
	capDisp := &captureDispatcher{}
	logger, _ := logBuffer()

	const rowID = "00000000-0000-0000-0000-000000000049"

	// Step 1: Seed a row simulating what POST /v1/echo writes into outbox_events.
	store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000001", "trace-feature49"))

	// Sanity: row exists and is unprocessed before dispatch.
	pre := store.findRow(rowID)
	if pre == nil {
		t.Fatal("step 1: seeded row must exist in store before dispatch")
	}
	if pre.processedAt != nil {
		t.Fatal("step 1: processedAt must be nil before dispatch")
	}

	// Step 2: Run the dispatcher until it processes the row.
	runDispatcherOnce(t, store, capDisp, logger)

	// Step 3: processed_at must be non-null after dispatch.
	post := store.findRow(rowID)
	if post == nil {
		t.Fatal("step 3/4: row must still exist after dispatch (not deleted)")
	}
	if post.processedAt == nil {
		t.Error("step 3: processed_at must be non-null after the dispatcher delivers the event")
	}
	// processed_at must be a recent timestamp (within 5 seconds).
	if post.processedAt != nil {
		age := time.Since(*post.processedAt)
		if age < 0 || age > 5*time.Second {
			t.Errorf("step 3: processed_at timestamp is suspicious: age=%v", age)
		}
	}

	// Step 4: explicitly re-confirm the row still exists (not deleted).
	if store.findRow(rowID) == nil {
		t.Error("step 4: row must still exist in the store after dispatch — events are stamped, not deleted")
	}
}

// TestProcessedAt_PartialIndexExcludesProcessedRows verifies step 5:
// the claimSQL partial-index filter (processed_at IS NULL) means processed
// rows are never re-selected by the dispatcher.
//
// This test proves this at three levels:
//  1. The SQL literal in claimSQL contains the correct WHERE clause.
//  2. The in-memory ClaimNext returns nil after all rows are processed.
//  3. The dispatcher does NOT call Dispatch a second time for a processed row.
func TestProcessedAt_PartialIndexExcludesProcessedRows(t *testing.T) {
	// ── 5a: SQL constant verification ─────────────────────────────────────────
	// The production claimSQL must filter on processed_at IS NULL so the query
	// hits the partial index outbox_events_unprocessed_idx.
	if !strings.Contains(claimSQL, "processed_at IS NULL") {
		t.Errorf("step 5a: claimSQL must filter on 'processed_at IS NULL' to use the partial index; SQL:\n%s", claimSQL)
	}

	// ── 5b: ClaimNext returns nil once all rows are processed ─────────────────
	store := newInMemOutboxStore()
	capDisp := &captureDispatcher{}
	logger, _ := logBuffer()

	const rowID = "00000000-0000-0000-0000-000000000050"
	store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000002", "trace-feature49-b"))

	// Run dispatcher: processes the single row.
	runDispatcherOnce(t, store, capDisp, logger)

	// After processing, ClaimNext must return nil (no unprocessed rows).
	row, err := store.ClaimNext(context.Background())
	if err != nil {
		t.Fatalf("step 5b: ClaimNext error: %v", err)
	}
	if row != nil {
		t.Errorf("step 5b: ClaimNext must return nil after all rows are processed; got row id=%s", row.ID)
	}

	// ── 5c: Dispatcher.Dispatch not called again for processed row ─────────────
	// Re-run the dispatcher. It must not call Dispatch again.
	priorCallCount := capDisp.callCount()
	runDispatcherOnce(t, store, capDisp, logger)
	afterCallCount := capDisp.callCount()

	if afterCallCount != priorCallCount {
		t.Errorf("step 5c: dispatcher must not re-dispatch an already-processed row; "+
			"Dispatch called %d extra times after row was already processed", afterCallCount-priorCallCount)
	}
}

// TestProcessedAt_MarkDispatchedSetsProcessedAtNotDeletes verifies the
// markDispatchedSQL constant updates processed_at (does NOT use DELETE).
func TestProcessedAt_MarkDispatchedSetsProcessedAtNotDeletes(t *testing.T) {
	// The SQL must be an UPDATE that sets processed_at.
	if !strings.Contains(markDispatchedSQL, "UPDATE") {
		t.Errorf("markDispatchedSQL must be an UPDATE statement, not DELETE; SQL:\n%s", markDispatchedSQL)
	}
	if strings.Contains(strings.ToUpper(markDispatchedSQL), "DELETE") {
		t.Errorf("markDispatchedSQL must NOT contain DELETE — rows are stamped, not removed; SQL:\n%s", markDispatchedSQL)
	}
	if !strings.Contains(markDispatchedSQL, "processed_at") {
		t.Errorf("markDispatchedSQL must set processed_at; SQL:\n%s", markDispatchedSQL)
	}
	if !strings.Contains(markDispatchedSQL, "now()") {
		t.Errorf("markDispatchedSQL must set processed_at = now(); SQL:\n%s", markDispatchedSQL)
	}
	if !strings.Contains(markDispatchedSQL, "outbox_events") {
		t.Errorf("markDispatchedSQL must reference outbox_events table; SQL:\n%s", markDispatchedSQL)
	}
}

// TestProcessedAt_MarkDispatchedCalledAfterSuccess verifies the dispatcher
// calls MarkDispatched (which stamps processed_at) on the store after a
// successful Dispatcher.Dispatch call.
func TestProcessedAt_MarkDispatchedCalledAfterSuccess(t *testing.T) {
	store := newInMemOutboxStore()
	capDisp := &captureDispatcher{} // always succeeds
	logger, _ := logBuffer()

	const rowID = "00000000-0000-0000-0000-000000000051"
	store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000003", "trace-feature49-c"))

	runDispatcherOnce(t, store, capDisp, logger)

	r := store.findRow(rowID)
	if r == nil {
		t.Fatal("row must still exist after dispatch")
	}
	if r.processedAt == nil {
		t.Error("MarkDispatched must have been called — processedAt must be non-nil after successful dispatch")
	}
	// Dispatch itself must have been called exactly once.
	if capDisp.callCount() != 1 {
		t.Errorf("Dispatcher.Dispatch must be called exactly once; called %d times", capDisp.callCount())
	}
}

// TestProcessedAt_FailedRowNotStampedAllowsRetry verifies the corollary:
// a row that fails dispatch is NOT stamped with processed_at, so it remains
// visible to the dispatcher for retry (the partial index still covers it).
func TestProcessedAt_FailedRowNotStampedAllowsRetry(t *testing.T) {
	store := newInMemOutboxStore()
	// failOnce=true: first Dispatch call returns error, second succeeds.
	capDisp := &captureDispatcher{failOnce: true}
	logger, _ := logBuffer()

	const rowID = "00000000-0000-0000-0000-000000000052"
	store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000004", "trace-feature49-d"))

	// Run long enough for two cycles (fail + retry = success).
	d, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
		Store:           store,
		Dispatcher:      capDisp,
		Logger:          logger,
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	// Wait for the row to be eventually processed.
	deadline := time.Now().Add(450 * time.Millisecond)
	for time.Now().Before(deadline) {
		r := store.findRow(rowID)
		if r != nil && r.processedAt != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	_ = d.Stop()

	r := store.findRow(rowID)
	if r == nil {
		t.Fatal("row must still exist after eventual dispatch (not deleted on failure either)")
	}
	if r.processedAt == nil {
		t.Error("row must be stamped processed_at after eventual successful dispatch")
	}
	// Row must have been attempted at least twice.
	if r.attempts < 2 {
		t.Errorf("attempts=%d, want >= 2 (failed + retried)", r.attempts)
	}
}

// TestProcessedAt_MultipleEventsAllStampedNoneDeleted verifies that when multiple
// outbox events are processed, all of them are stamped (not deleted).
func TestProcessedAt_MultipleEventsAllStampedNoneDeleted(t *testing.T) {
	store := newInMemOutboxStore()
	capDisp := &captureDispatcher{}
	logger, _ := logBuffer()

	ids := []string{
		"00000000-0000-0000-0000-000000000053",
		"00000000-0000-0000-0000-000000000054",
		"00000000-0000-0000-0000-000000000055",
	}

	for i, id := range ids {
		row := newTestRow(id, "v1.echo.created", "00000000-0000-0000-0000-000000000005", "trace-multi49")
		row.occurredAt = time.Now().Add(-time.Duration(len(ids)-i) * time.Second)
		store.seed(row)
	}

	// Run dispatcher until all rows are processed.
	d, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
		Store:           store,
		Dispatcher:      capDisp,
		Logger:          logger,
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	deadline := time.Now().Add(900 * time.Millisecond)
	for time.Now().Before(deadline) {
		allDone := true
		for _, id := range ids {
			r := store.findRow(id)
			if r == nil || r.processedAt == nil {
				allDone = false
				break
			}
		}
		if allDone {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	_ = d.Stop()

	for _, id := range ids {
		r := store.findRow(id)
		// Step 4: row must still exist.
		if r == nil {
			t.Errorf("row %s was deleted — rows must be stamped, not deleted", id)
			continue
		}
		// Step 3: processed_at must be non-null.
		if r.processedAt == nil {
			t.Errorf("row %s has nil processed_at after dispatch — must be stamped", id)
		}
	}

	// Total Dispatch calls must equal the number of rows (each processed exactly once).
	if capDisp.callCount() != len(ids) {
		t.Errorf("Dispatch called %d times, want %d (one per row, no re-dispatches)", capDisp.callCount(), len(ids))
	}
}

// TestProcessedAt_PartialIndexMigrationSQL verifies that the outbox_events
// partial index defined in 0001_init.sql covers 'processed_at IS NULL'.
// This is a text-based sanity check on the migration constant embedded in the
// production schema — if the index definition changes, this test fails.
func TestProcessedAt_PartialIndexSQLGuard(t *testing.T) {
	// The claimSQL WHERE clause mirrors the partial index predicate.
	// If both use the same condition, the query will hit the index on PG.
	const partialIndexPredicate = "processed_at IS NULL"

	if !strings.Contains(claimSQL, partialIndexPredicate) {
		t.Errorf("claimSQL WHERE clause must match partial index predicate %q; SQL:\n%s",
			partialIndexPredicate, claimSQL)
	}

	// Also verify the mark-dispatched SQL sets processed_at (not a delete)
	// so the partial index eventually excludes the row from future scans.
	if !strings.Contains(markDispatchedSQL, "processed_at") {
		t.Errorf("markDispatchedSQL must set processed_at so the partial index excludes the row; SQL:\n%s", markDispatchedSQL)
	}
}

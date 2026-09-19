//go:build integration

// sales_feed_integration_test.go is the integration test for the sales-feed
// check family: it must never replay pre-existing orders on its first run
// (cursor seeds to now()), and it must emit exactly one message per newly
// paid order, not a duplicate on the next run.
package opswatchdog_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opswatchdog"
)

func TestSalesFeed_LiveDB_NoReplayOncePerOrder(t *testing.T) {
	pool := connectWatchdogTestDB(t)
	ctx := context.Background()

	f := newWDFixture(t, ctx, pool)
	defer f.cleanup()

	// Simulate "first run on a database with history": a pre-existing paid
	// order already sits in the table, then the cursor is reset to
	// simulate this watchdog never having run before.
	preExistingPaidAt := time.Now().UTC().Add(-1 * time.Hour)
	preExistingOrderID := f.seedOrder(t, ctx, "paid", &preExistingPaidAt)
	var preExistingSystemID int64
	if err := pool.QueryRow(ctx, `SELECT system_id FROM orders WHERE id=$1`, preExistingOrderID).Scan(&preExistingSystemID); err != nil {
		t.Fatalf("read system_id: %v", err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM ops_watchdog_state WHERE key = 'sales_feed'`); err != nil {
		t.Fatalf("reset sales_feed cursor: %v", err)
	}

	notifier := &recordingNotifier{}
	handler := opswatchdog.NewHandler(opswatchdog.Options{
		Pool:     pool,
		Notifier: notifier,
		Logger:   quietTestLogger(),
	})

	// ---- Run 1: first-ever run must NOT replay the pre-existing order ------
	if err := handler(ctx, nil); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	preExistingMarker := fmt.Sprintf("#%d", preExistingSystemID)
	if got := countContaining(notifier.messages(), preExistingMarker); got != 0 {
		t.Fatalf("run 1: pre-existing order %s was replayed (%d messages), want 0", preExistingMarker, got)
	}

	// ---- Seed a NEW paid order (after the cursor was seeded to now()) ------
	newPaidAt := time.Now().UTC()
	newOrderID := f.seedOrder(t, ctx, "paid", &newPaidAt)
	var newSystemID int64
	if err := pool.QueryRow(ctx, `SELECT system_id FROM orders WHERE id=$1`, newOrderID).Scan(&newSystemID); err != nil {
		t.Fatalf("read system_id: %v", err)
	}
	newMarker := fmt.Sprintf("#%d", newSystemID)

	// ---- Run 2: the new order must be reported exactly once ----------------
	if err := handler(ctx, nil); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	msgs := notifier.messages()
	if got := countContaining(msgs, newMarker); got != 1 {
		t.Fatalf("run 2: messages mentioning %s = %d, want 1\nall messages:\n%s",
			newMarker, got, strings.Join(msgs, "\n---\n"))
	}
	if !strings.Contains(oneContaining(msgs, newMarker), "sale") {
		t.Errorf("run 2: expected a sale message, got: %s", oneContaining(msgs, newMarker))
	}

	// ---- Run 3: no duplicate for the already-reported order -----------------
	if err := handler(ctx, nil); err != nil {
		t.Fatalf("run 3: %v", err)
	}
	msgs = notifier.messages()
	if got := countContaining(msgs, newMarker); got != 1 {
		t.Fatalf("run 3: messages mentioning %s = %d, want still 1 (no duplicate)\nall messages:\n%s",
			newMarker, got, strings.Join(msgs, "\n---\n"))
	}
}

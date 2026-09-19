//go:build integration

// lag_integration_test.go is the integration test for the lag check
// family (oldest pending worker_jobs row, undelivered outbox_events
// backlog). Both signals are GLOBAL, unscoped by any fixture this test
// owns — the shared dev-stand database may already carry an arbitrary
// worker_jobs/outbox_events backlog from other packages' fixtures or a
// live arena-worker. Rather than asserting a fixed alert outcome (which
// would be flaky against that shared state, the same trap AGENTS.md
// documents for the outbox lag probe and backlog monitor), this test reads
// the ACTUAL current state with the same queries the check uses and
// asserts the watchdog's alert store agrees with what that state implies —
// so the assertion is correct regardless of what else is in the database.
package opswatchdog_test

import (
	"context"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opswatchdog"
)

func TestLag_LiveDB_AlertStateMatchesObservedBacklog(t *testing.T) {
	pool := connectWatchdogTestDB(t)
	ctx := context.Background()

	const pendingJobLagThreshold = 5 * time.Minute
	const outboxBacklogThreshold = int64(100)

	var oldestPending *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT min(scheduled_at) FROM worker_jobs WHERE status = 'pending'`,
	).Scan(&oldestPending); err != nil {
		t.Fatalf("query oldest pending worker_jobs: %v", err)
	}
	wantWorkerJobsAlert := oldestPending != nil && time.Since(*oldestPending) > pendingJobLagThreshold

	var outboxBacklog int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox_events WHERE processed_at IS NULL AND dead_lettered_at IS NULL`,
	).Scan(&outboxBacklog); err != nil {
		t.Fatalf("query outbox backlog: %v", err)
	}
	wantOutboxAlert := outboxBacklog >= outboxBacklogThreshold

	notifier := &recordingNotifier{}
	handler := opswatchdog.NewHandler(opswatchdog.Options{
		Pool:     pool,
		Notifier: notifier,
		Logger:   quietTestLogger(),
	})
	if err := handler(ctx, nil); err != nil {
		t.Fatalf("run: %v", err)
	}

	store := opswatchdog.NewPGAlertStore(pool)

	workerJobsRow, err := store.Get(ctx, "lag:worker_jobs")
	if err != nil {
		t.Fatalf("get lag:worker_jobs: %v", err)
	}
	gotWorkerJobsOpen := workerJobsRow != nil && workerJobsRow.ResolvedAt == nil
	if gotWorkerJobsOpen != wantWorkerJobsAlert {
		t.Errorf("lag:worker_jobs open = %v, want %v (oldest_pending=%v, threshold=%v)",
			gotWorkerJobsOpen, wantWorkerJobsAlert, oldestPending, pendingJobLagThreshold)
	}

	outboxRow, err := store.Get(ctx, "lag:outbox_backlog")
	if err != nil {
		t.Fatalf("get lag:outbox_backlog: %v", err)
	}
	gotOutboxOpen := outboxRow != nil && outboxRow.ResolvedAt == nil
	if gotOutboxOpen != wantOutboxAlert {
		t.Errorf("lag:outbox_backlog open = %v, want %v (backlog=%d, threshold=%d)",
			gotOutboxOpen, wantOutboxAlert, outboxBacklog, outboxBacklogThreshold)
	}
}

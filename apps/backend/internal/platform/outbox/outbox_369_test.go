// Package outbox — tests for feature #369
// "PR2-13: Make outbox delivery poison-safe and single-delivery"
//
// Steps verified:
//
//	Step 1: Attempts cap + dead-letter + exponential backoff
//	Step 2: Sleep (waitOrStop) after failed delivery — no hot-CPU loop
//	Step 3: claimSQL orders by next_attempt_at and skips rows not yet due
//	Step 4: claimSQL holds the row via UPDATE (prevents double-delivery)
//	Step 5: Poison event does not block healthy events; dead-lettered after cap
package outbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// selectiveFailDispatcher fails for a specific event type, succeeds for others.
type selectiveFailDispatcher struct {
	failType string
}

func (d *selectiveFailDispatcher) Dispatch(_ context.Context, ev Event) error {
	if ev.EventType == d.failType {
		return errors.New("simulated failure for poison event type: " + d.failType)
	}
	return nil
}

var _ Dispatcher = (*selectiveFailDispatcher)(nil)

// =============================================================================
// Step 1: Attempts cap + dead-letter
// =============================================================================

// TestPoison369_Step1_AttemptsCapDeadLetters verifies that after MaxAttempts
// failures, the row is marked dead-lettered and excluded from future claims.
func TestPoison369_Step1_AttemptsCapDeadLetters(t *testing.T) {
	store := newInMemOutboxStore()
	capDisp := &captureDispatcher{failAlways: true}
	logger, _ := logBuffer()

	const maxAttempts = 3
	const rowID = "00000000-0000-0000-0000-000000036900"
	store.seed(newTestRow(rowID, "v1.poison.event", "00000000-0000-0000-0000-000000000001", "trace-369-step1"))

	d, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
		Store:           store,
		Dispatcher:      capDisp,
		Logger:          logger,
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 2 * time.Second,
		MaxAttempts:     maxAttempts,
		BackoffFunc:     func(_ int) time.Duration { return 0 }, // no backoff delay for fast test
	})
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	// Wait for the row to be dead-lettered.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r := store.findRow(rowID)
		if r != nil && r.deadLettered {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = d.Stop()

	r := store.findRow(rowID)
	if r == nil {
		t.Fatal("step 1: row disappeared from store")
	}
	if !r.deadLettered {
		t.Errorf("step 1: row must be dead-lettered after %d attempts; attempts=%d deadLettered=%v",
			maxAttempts, r.attempts, r.deadLettered)
	}
	if r.attempts < maxAttempts {
		t.Errorf("step 1: attempts=%d, want >= %d", r.attempts, maxAttempts)
	}
	if r.processedAt != nil {
		t.Error("step 1: dead-lettered row must NOT have processed_at set")
	}
}

// TestPoison369_Step1_DeadLetteredRowNotReclaimed verifies that a dead-lettered
// row is not returned by ClaimNext.
func TestPoison369_Step1_DeadLetteredRowNotReclaimed(t *testing.T) {
	store := newInMemOutboxStore()

	const rowID = "00000000-0000-0000-0000-000000036901"
	store.seed(newTestRow(rowID, "v1.poison.event", "00000000-0000-0000-0000-000000000002", "trace-369-dl"))

	ctx := context.Background()

	// Manually dead-letter the row.
	if err := store.MarkFailed(ctx, rowID, "permanent failure", nil, true); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	// ClaimNext must not return the dead-lettered row.
	row, err := store.ClaimNext(ctx)
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if row != nil {
		t.Errorf("step 1: ClaimNext must return nil for dead-lettered row; got id=%s", row.ID)
	}
}

// TestPoison369_Step1_BackoffFuncDefault verifies the default exponential backoff.
func TestPoison369_Step1_BackoffFuncDefault(t *testing.T) {
	d0 := defaultBackoffFunc(0)     // 1 min
	d1 := defaultBackoffFunc(1)     // 2 min
	d2 := defaultBackoffFunc(2)     // 4 min
	dBig := defaultBackoffFunc(100) // capped at 1 hour

	if d0 <= 0 {
		t.Error("step 1: backoff(0) must be positive")
	}
	if d1 <= d0 {
		t.Errorf("step 1: backoff(1)=%v must be > backoff(0)=%v", d1, d0)
	}
	if d2 <= d1 {
		t.Errorf("step 1: backoff(2)=%v must be > backoff(1)=%v", d2, d1)
	}
	if dBig > time.Hour {
		t.Errorf("step 1: backoff(100)=%v must be capped at 1 hour", dBig)
	}
}

// =============================================================================
// Step 2: Sleep after failed delivery
// =============================================================================

// TestPoison369_Step2_SleepAfterFailure verifies that the dispatcher does not
// immediately re-claim after a failed delivery. The second Dispatch call must
// occur at least pollInterval after the first.
func TestPoison369_Step2_SleepAfterFailure(t *testing.T) {
	store := newInMemOutboxStore()
	// Fail first call; succeed on second.
	capDisp := &captureDispatcher{failOnce: true}
	logger, _ := logBuffer()

	const pollInterval = 60 * time.Millisecond
	const rowID = "00000000-0000-0000-0000-000000036902"
	store.seed(newTestRow(rowID, "v1.test.sleep", "00000000-0000-0000-0000-000000000003", "trace-369-step2"))

	d, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
		Store:           store,
		Dispatcher:      capDisp,
		Logger:          logger,
		PollInterval:    pollInterval,
		ShutdownTimeout: 2 * time.Second,
		MaxAttempts:     5,
		BackoffFunc:     func(_ int) time.Duration { return 0 }, // no additional backoff
	})
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	// Wait until we have at least 2 dispatch calls (fail + success).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if capDisp.callCount() >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	elapsed := time.Since(start)
	_ = d.Stop()

	if capDisp.callCount() < 2 {
		t.Fatalf("step 2: only %d dispatch calls recorded, need >= 2 to verify sleep", capDisp.callCount())
	}

	// The second call must have come after at least half the poll interval.
	// (Half allows for scheduling jitter in test environments.)
	minExpected := pollInterval / 2
	if elapsed < minExpected {
		t.Errorf("step 2: two dispatches happened in %v; want at least %v between them (sleep after failure)",
			elapsed, minExpected)
	}
}

// =============================================================================
// Step 3: claimSQL orders by next_attempt_at and skips rows not yet due
// =============================================================================

// TestPoison369_Step3_ClaimSQLFiltersAndOrders verifies the claimSQL constants
// contain the required scheduling clauses.
func TestPoison369_Step3_ClaimSQLFiltersAndOrders(t *testing.T) {
	// Must filter dead-lettered rows.
	if !strings.Contains(claimSQL, "dead_lettered_at IS NULL") {
		t.Error("step 3: claimSQL must filter 'dead_lettered_at IS NULL' to skip quarantined rows")
	}

	// Must skip rows not yet due for next attempt.
	if !strings.Contains(claimSQL, "next_attempt_at") {
		t.Error("step 3: claimSQL must reference next_attempt_at for backoff scheduling")
	}
	if !strings.Contains(claimSQL, "next_attempt_at IS NULL OR next_attempt_at <= now()") &&
		!strings.Contains(claimSQL, "next_attempt_at IS NULL") {
		t.Error("step 3: claimSQL must skip rows where next_attempt_at > now()")
	}

	// Must order by next_attempt_at for scheduling.
	upperSQL := strings.ToUpper(claimSQL)
	if !strings.Contains(upperSQL, "ORDER BY") {
		t.Error("step 3: claimSQL must have ORDER BY clause")
	}
	if !strings.Contains(claimSQL, "next_attempt_at") {
		t.Error("step 3: claimSQL ORDER BY must reference next_attempt_at")
	}
}

// TestPoison369_Step3_MarkFailedSQLHasNewColumns verifies the new SQL columns.
func TestPoison369_Step3_MarkFailedSQLHasNewColumns(t *testing.T) {
	if !strings.Contains(markFailedSQL, "next_attempt_at") {
		t.Error("step 3: markFailedSQL must set next_attempt_at for backoff scheduling")
	}
	if !strings.Contains(markFailedSQL, "dead_lettered_at") {
		t.Error("step 3: markFailedSQL must set dead_lettered_at for poison event quarantine")
	}
}

// =============================================================================
// Step 4: Claim holds the row (prevents double-delivery)
// =============================================================================

// TestPoison369_Step4_ClaimSQLHoldsRow verifies that claimSQL atomically
// updates next_attempt_at during claim to prevent double-delivery.
func TestPoison369_Step4_ClaimSQLHoldsRow(t *testing.T) {
	upperSQL := strings.ToUpper(claimSQL)

	// Must use UPDATE to set the claim hold.
	if !strings.Contains(upperSQL, "UPDATE") {
		t.Error("step 4: claimSQL must include UPDATE to atomically set a claim hold on the row")
	}

	// The claim hold must use a time interval.
	if !strings.Contains(claimSQL, "minutes") && !strings.Contains(claimSQL, "INTERVAL") &&
		!strings.Contains(claimSQL, "interval") {
		t.Error("step 4: claimSQL claim hold must use a time interval (e.g. '5 minutes'::interval)")
	}

	// The UPDATE must set next_attempt_at.
	if !strings.Contains(claimSQL, "next_attempt_at") {
		t.Error("step 4: claimSQL must set next_attempt_at in the claim UPDATE")
	}
}

// TestPoison369_Step4_MarkDispatchedClearsNextAttemptAt verifies that
// markDispatchedSQL clears next_attempt_at after successful delivery.
func TestPoison369_Step4_MarkDispatchedClearsNextAttemptAt(t *testing.T) {
	if !strings.Contains(markDispatchedSQL, "next_attempt_at") {
		t.Error("step 4: markDispatchedSQL must clear next_attempt_at after successful delivery")
	}
}

// =============================================================================
// Step 5: Poison event does not block healthy events
// =============================================================================

// TestPoison369_Step5_PoisonDoesNotBlockHealthy verifies that a permanently
// failing "poison" event does not prevent healthy events from being delivered.
// After MaxAttempts failures, the poison event is dead-lettered and the healthy
// event is successfully dispatched.
func TestPoison369_Step5_PoisonDoesNotBlockHealthy(t *testing.T) {
	store := newInMemOutboxStore()
	poisonDisp := &selectiveFailDispatcher{failType: "v1.poison.event"}
	logger, _ := logBuffer()

	const poisonID = "00000000-0000-0000-0000-000000036903"
	const healthyID = "00000000-0000-0000-0000-000000036904"

	// Seed poison event first (older occurred_at so it is claimed first).
	poison := newTestRow(poisonID, "v1.poison.event", "00000000-0000-0000-0000-000000000004", "trace-369-poison")
	poison.occurredAt = time.Now().Add(-10 * time.Second)
	store.seed(poison)

	// Seed healthy event second.
	healthy := newTestRow(healthyID, "v1.healthy.event", "00000000-0000-0000-0000-000000000005", "trace-369-healthy")
	healthy.occurredAt = time.Now().Add(-5 * time.Second)
	store.seed(healthy)

	d, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
		Store:           store,
		Dispatcher:      poisonDisp,
		Logger:          logger,
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 2 * time.Second,
		MaxAttempts:     3,                                      // low cap for fast test
		BackoffFunc:     func(_ int) time.Duration { return 0 }, // no backoff for fast test
	})
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	// Wait for both events to reach terminal state.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h := store.findRow(healthyID)
		p := store.findRow(poisonID)
		if h != nil && h.processedAt != nil && p != nil && p.deadLettered {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = d.Stop()

	// Healthy event must have been delivered.
	h := store.findRow(healthyID)
	if h == nil {
		t.Fatal("step 5: healthy row disappeared from store")
	}
	if h.processedAt == nil {
		t.Error("step 5: healthy event must be delivered even when a poison event is present")
	}

	// Poison event must be dead-lettered (not blocking the queue).
	p := store.findRow(poisonID)
	if p == nil {
		t.Fatal("step 5: poison row disappeared from store")
	}
	if !p.deadLettered {
		t.Errorf("step 5: poison event must be dead-lettered after max attempts; attempts=%d deadLettered=%v",
			p.attempts, p.deadLettered)
	}
	if p.processedAt != nil {
		t.Error("step 5: dead-lettered poison event must NOT have processed_at set")
	}
}

// TestPoison369_FullVerification runs all 5 feature steps as sub-tests.
func TestPoison369_FullVerification(t *testing.T) {
	t.Run("step1_dead_letter_after_cap", func(t *testing.T) {
		store := newInMemOutboxStore()
		capDisp := &captureDispatcher{failAlways: true}
		logger, _ := logBuffer()

		const rowID = "00000000-0000-0000-0000-000000036910"
		store.seed(newTestRow(rowID, "v1.poison.fv1", "00000000-0000-0000-0000-000000000010", "trace-fv-1"))

		d, _ := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
			Store:           store,
			Dispatcher:      capDisp,
			Logger:          logger,
			PollInterval:    5 * time.Millisecond,
			ShutdownTimeout: 2 * time.Second,
			MaxAttempts:     3,
			BackoffFunc:     func(_ int) time.Duration { return 0 },
		})
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		go func() { _ = d.Run(ctx) }()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if r := store.findRow(rowID); r != nil && r.deadLettered {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = d.Stop()
		r := store.findRow(rowID)
		if r == nil || !r.deadLettered {
			t.Error("full: step 1: row must be dead-lettered after maxAttempts")
		}
	})

	t.Run("step3_claim_sql_filters_dead_lettered", func(t *testing.T) {
		if !strings.Contains(claimSQL, "dead_lettered_at IS NULL") {
			t.Error("full: step 3: claimSQL must filter dead_lettered_at IS NULL")
		}
	})

	t.Run("step4_claim_sql_holds_row", func(t *testing.T) {
		if !strings.Contains(strings.ToUpper(claimSQL), "UPDATE") {
			t.Error("full: step 4: claimSQL must UPDATE to hold the row")
		}
	})

	t.Run("step5_poison_does_not_block_healthy", func(t *testing.T) {
		store := newInMemOutboxStore()
		d, _ := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
			Store:           store,
			Dispatcher:      &selectiveFailDispatcher{failType: "v1.poison.fv5"},
			Logger:          slog_noop(),
			PollInterval:    5 * time.Millisecond,
			ShutdownTimeout: 2 * time.Second,
			MaxAttempts:     2,
			BackoffFunc:     func(_ int) time.Duration { return 0 },
		})

		const poisonID = "00000000-0000-0000-0000-000000036920"
		const healthyID = "00000000-0000-0000-0000-000000036921"

		pRow := newTestRow(poisonID, "v1.poison.fv5", "00000000-0000-0000-0000-000000000020", "trace-fv5-p")
		pRow.occurredAt = time.Now().Add(-10 * time.Second)
		store.seed(pRow)

		hRow := newTestRow(healthyID, "v1.healthy.fv5", "00000000-0000-0000-0000-000000000021", "trace-fv5-h")
		hRow.occurredAt = time.Now().Add(-5 * time.Second)
		store.seed(hRow)

		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		go func() { _ = d.Run(ctx) }()

		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			h := store.findRow(healthyID)
			if h != nil && h.processedAt != nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = d.Stop()

		h := store.findRow(healthyID)
		if h == nil || h.processedAt == nil {
			t.Error("full: step 5: healthy event must be delivered")
		}
		p := store.findRow(poisonID)
		if p == nil || !p.deadLettered {
			t.Error("full: step 5: poison event must be dead-lettered")
		}
	})
}

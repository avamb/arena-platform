// Package idempotency — cleanup_test.go
//
// Tests for feature #48: "Expired idempotency keys cleaned by maintenance job".
//
// Step-by-step coverage (all steps, no live database required):
//
//   Step 1: Insert 3 idempotency_keys rows: A expired 1h ago, B expired 1d ago,
//           C still valid (expires 23h from now).
//   Step 2: Enqueue/invoke the cleanup job handler directly (no worker queue needed
//           for unit tests — the handler function is called synchronously).
//   Step 3: Wait for completion (handler call returns).
//   Step 4: Verify rows A and B were deleted; row C is still present.
//   Step 5: Verify idempotency_cleanup_deleted_total counter incremented by 2.
//   Step 6: Verify cleanup job self-schedules the next run (cron-like), confirming
//           the startup scheduling mechanism works.
package idempotency

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// ----------------------------------------------------------------------------
// inMemoryCleaner — in-memory Cleaner implementation for tests
// ----------------------------------------------------------------------------

// cleanerRow represents one row in the in-memory idempotency_keys table.
type cleanerRow struct {
	key       string
	scope     string
	expiresAt time.Time
}

// inMemoryCleaner implements Cleaner without requiring a live database.
type inMemoryCleaner struct {
	mu   sync.Mutex
	rows []cleanerRow
}

// seed inserts a row into the in-memory table.
func (c *inMemoryCleaner) seed(key, scope string, expiresAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows = append(c.rows, cleanerRow{key: key, scope: scope, expiresAt: expiresAt})
}

// DeleteExpired implements Cleaner by removing rows with expiresAt < cutoff.
func (c *inMemoryCleaner) DeleteExpired(_ context.Context, cutoff time.Time) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var remaining []cleanerRow
	var deleted int64
	for _, r := range c.rows {
		if r.expiresAt.Before(cutoff) {
			deleted++
		} else {
			remaining = append(remaining, r)
		}
	}
	c.rows = remaining
	return deleted, nil
}

// rowCount returns the current number of rows in the table.
func (c *inMemoryCleaner) rowCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.rows)
}

// contains returns true if the given key+scope is still in the table.
func (c *inMemoryCleaner) contains(key, scope string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.rows {
		if r.key == key && r.scope == scope {
			return true
		}
	}
	return false
}

// compile-time check
var _ Cleaner = (*inMemoryCleaner)(nil)

// ----------------------------------------------------------------------------
// fakeCleanupScheduler — records ScheduleNext calls for step 6
// ----------------------------------------------------------------------------

type fakeCleanupScheduler struct {
	mu    sync.Mutex
	calls []time.Time
}

func (f *fakeCleanupScheduler) ScheduleNext(_ context.Context, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, at)
	return nil
}

func (f *fakeCleanupScheduler) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeCleanupScheduler) firstCallAt() (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return time.Time{}, false
	}
	return f.calls[0], true
}

// compile-time check
var _ CleanupScheduler = (*fakeCleanupScheduler)(nil)

// ----------------------------------------------------------------------------
// Prometheus counter helper
// ----------------------------------------------------------------------------

// counterValue reads the current float64 value from a prometheus.Counter.
func counterValue(c prometheus.Counter) float64 {
	var m dto.Metric
	_ = c.Write(&m)
	if m.Counter == nil {
		return 0
	}
	return m.Counter.GetValue()
}

// ----------------------------------------------------------------------------
// Shared test setup
// ----------------------------------------------------------------------------

const (
	cleanupScopeA = "POST /v1/order"
	cleanupScopeB = "POST /v1/payment"
	cleanupScopeC = "POST /v1/cart"
)

// seedThreeRows seeds the three rows described in feature #48 step 1:
//   - Row A: expires 1 hour ago (expired)
//   - Row B: expires 1 day ago (expired)
//   - Row C: expires 23 hours from now (valid)
func seedThreeRows(c *inMemoryCleaner) {
	now := time.Now()
	c.seed("KEY_A", cleanupScopeA, now.Add(-1*time.Hour))   // expired 1h ago
	c.seed("KEY_B", cleanupScopeB, now.Add(-24*time.Hour))  // expired 1d ago
	c.seed("KEY_C", cleanupScopeC, now.Add(23*time.Hour))   // still valid
}

// ----------------------------------------------------------------------------
// Step 1-4: Cleanup deletes only expired rows
// ----------------------------------------------------------------------------

// TestCleanup_DeletesExpiredRowsOnly verifies steps 1-4:
// After running the cleanup handler, rows A (expired 1h) and B (expired 1d)
// are deleted while row C (still valid) remains untouched.
func TestCleanup_DeletesExpiredRowsOnly(t *testing.T) {
	cleaner := &inMemoryCleaner{}
	seedThreeRows(cleaner)

	// Confirm initial state: 3 rows
	if n := cleaner.rowCount(); n != 3 {
		t.Fatalf("setup: want 3 rows, got %d", n)
	}

	handler := NewCleanupHandler(CleanupOptions{Cleaner: cleaner})

	// Step 2-3: invoke handler (equivalent to the worker picking up the job)
	if err := handler(context.Background(), nil); err != nil {
		t.Fatalf("cleanup handler error: %v", err)
	}

	// Step 4a: row A must be gone
	if cleaner.contains("KEY_A", cleanupScopeA) {
		t.Error("step 4: row A (expired 1h ago) was NOT deleted")
	}

	// Step 4b: row B must be gone
	if cleaner.contains("KEY_B", cleanupScopeB) {
		t.Error("step 4: row B (expired 1d ago) was NOT deleted")
	}

	// Step 4c: row C must still be present
	if !cleaner.contains("KEY_C", cleanupScopeC) {
		t.Error("step 4: row C (still valid) was deleted but must be kept")
	}
}

// TestCleanup_DeletedCountIsTwo verifies that exactly 2 rows are deleted
// (rows A and B), not more, not fewer.
func TestCleanup_DeletedCountIsTwo(t *testing.T) {
	cleaner := &inMemoryCleaner{}
	seedThreeRows(cleaner)

	handler := NewCleanupHandler(CleanupOptions{Cleaner: cleaner})
	if err := handler(context.Background(), nil); err != nil {
		t.Fatalf("handler error: %v", err)
	}

	// 1 row remaining (C)
	if n := cleaner.rowCount(); n != 1 {
		t.Errorf("want 1 remaining row (C), got %d", n)
	}
}

// TestCleanup_ValidRowNotDeleted is a focused assertion that the still-valid
// row C survives cleanup.
func TestCleanup_ValidRowNotDeleted(t *testing.T) {
	cleaner := &inMemoryCleaner{}
	seedThreeRows(cleaner)

	handler := NewCleanupHandler(CleanupOptions{Cleaner: cleaner})
	if err := handler(context.Background(), nil); err != nil {
		t.Fatalf("handler error: %v", err)
	}

	if !cleaner.contains("KEY_C", cleanupScopeC) {
		t.Error("row C (valid) must survive cleanup but was deleted")
	}
}

// ----------------------------------------------------------------------------
// Step 5: Prometheus counter incremented by 2
// ----------------------------------------------------------------------------

// TestCleanup_CounterIncrementedBy2 verifies step 5:
// After the cleanup run that deletes rows A and B, the
// idempotency_cleanup_deleted_total counter value equals 2.
func TestCleanup_CounterIncrementedBy2(t *testing.T) {
	cleaner := &inMemoryCleaner{}
	seedThreeRows(cleaner)

	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "idempotency_cleanup_deleted_total_test",
		Help: "test counter for feature #48 step 5",
	})

	handler := NewCleanupHandler(CleanupOptions{
		Cleaner:        cleaner,
		DeletedCounter: counter,
	})

	before := counterValue(counter)

	if err := handler(context.Background(), nil); err != nil {
		t.Fatalf("handler error: %v", err)
	}

	after := counterValue(counter)
	delta := after - before
	if delta != 2 {
		t.Errorf("step 5: want counter delta 2 (rows A+B deleted), got %.0f", delta)
	}
}

// TestCleanup_CounterStartsAtZero verifies the counter starts at 0 before
// any cleanup job runs.
func TestCleanup_CounterStartsAtZero(t *testing.T) {
	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "idempotency_cleanup_zero_test",
		Help: "test counter: should start at zero",
	})
	if v := counterValue(counter); v != 0 {
		t.Errorf("counter must start at 0, got %v", v)
	}
}

// TestCleanup_CounterNotIncrementedWhenNothingExpired verifies that the counter
// is not incremented when there are no expired rows.
func TestCleanup_CounterNotIncrementedWhenNothingExpired(t *testing.T) {
	cleaner := &inMemoryCleaner{}
	// Only row C (still valid)
	cleaner.seed("KEY_C", cleanupScopeC, time.Now().Add(23*time.Hour))

	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "idempotency_cleanup_noexp_test",
		Help: "test counter: should not increment",
	})

	handler := NewCleanupHandler(CleanupOptions{
		Cleaner:        cleaner,
		DeletedCounter: counter,
	})
	if err := handler(context.Background(), nil); err != nil {
		t.Fatalf("handler error: %v", err)
	}

	if v := counterValue(counter); v != 0 {
		t.Errorf("counter must stay at 0 when no rows expired, got %.0f", v)
	}
}

// TestCleanup_CounterNilSafe verifies that passing nil DeletedCounter does not
// panic — it is an opt-in field.
func TestCleanup_CounterNilSafe(t *testing.T) {
	cleaner := &inMemoryCleaner{}
	seedThreeRows(cleaner)

	handler := NewCleanupHandler(CleanupOptions{Cleaner: cleaner}) // no counter

	if err := handler(context.Background(), nil); err != nil {
		t.Fatalf("nil-counter handler must not error: %v", err)
	}
}

// TestCleanup_MultipleRunsAccumulateCounter verifies counter accumulation
// across multiple cleanup runs (e.g. 2 runs × 2 deletions each = 4 total).
func TestCleanup_MultipleRunsAccumulateCounter(t *testing.T) {
	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "idempotency_cleanup_multi_test",
		Help: "test accumulation",
	})

	for run := 0; run < 3; run++ {
		cleaner := &inMemoryCleaner{}
		seedThreeRows(cleaner)
		handler := NewCleanupHandler(CleanupOptions{
			Cleaner:        cleaner,
			DeletedCounter: counter,
		})
		if err := handler(context.Background(), nil); err != nil {
			t.Fatalf("run %d handler error: %v", run, err)
		}
	}

	if v := counterValue(counter); v != 6 {
		t.Errorf("3 runs × 2 deletions = 6; got %.0f", v)
	}
}

// ----------------------------------------------------------------------------
// Step 6: Cron-like self-scheduling
// ----------------------------------------------------------------------------

// TestCleanup_HandlerSelfSchedulesNextRun verifies step 6 (part 1):
// After a successful cleanup run, Scheduler.ScheduleNext is called exactly
// once, scheduling the next cleanup job in the future.
func TestCleanup_HandlerSelfSchedulesNextRun(t *testing.T) {
	cleaner := &inMemoryCleaner{}
	seedThreeRows(cleaner)

	sched := &fakeCleanupScheduler{}

	handler := NewCleanupHandler(CleanupOptions{
		Cleaner:   cleaner,
		Scheduler: sched,
	})

	before := time.Now()
	if err := handler(context.Background(), nil); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	after := time.Now()

	if n := sched.callCount(); n != 1 {
		t.Errorf("step 6: want exactly 1 ScheduleNext call, got %d", n)
	}

	at, ok := sched.firstCallAt()
	if !ok {
		t.Fatal("step 6: no ScheduleNext call recorded")
	}

	// Scheduled time must be in the future (at least now+CleanupInterval).
	// We allow 1 second tolerance for test execution time.
	minExpected := before.Add(DefaultCleanupInterval - time.Second)
	maxExpected := after.Add(DefaultCleanupInterval + time.Second)

	if at.Before(minExpected) {
		t.Errorf("step 6: next run scheduled at %v is too soon (expected >= %v)", at, minExpected)
	}
	if at.After(maxExpected) {
		t.Errorf("step 6: next run scheduled at %v is too far (expected <= %v)", at, maxExpected)
	}
}

// TestCleanup_NextRunScheduledAfterDefaultInterval verifies that the default
// cleanup interval of 1 hour is used when CleanupInterval is not set.
func TestCleanup_NextRunScheduledAfterDefaultInterval(t *testing.T) {
	cleaner := &inMemoryCleaner{}
	sched := &fakeCleanupScheduler{}

	handler := NewCleanupHandler(CleanupOptions{
		Cleaner:   cleaner,
		Scheduler: sched,
		// CleanupInterval: zero → must default to DefaultCleanupInterval
	})

	before := time.Now()
	if err := handler(context.Background(), nil); err != nil {
		t.Fatalf("handler error: %v", err)
	}

	at, ok := sched.firstCallAt()
	if !ok {
		t.Fatal("no ScheduleNext call recorded")
	}

	expectedMin := before.Add(DefaultCleanupInterval - time.Second)
	if at.Before(expectedMin) {
		t.Errorf("default interval: next run at %v is before expected minimum %v", at, expectedMin)
	}
}

// TestCleanup_SchedulerNilSafe verifies that nil Scheduler does not cause a panic
// or error — cron behaviour is optional (useful for one-shot test jobs).
func TestCleanup_SchedulerNilSafe(t *testing.T) {
	cleaner := &inMemoryCleaner{}
	seedThreeRows(cleaner)

	handler := NewCleanupHandler(CleanupOptions{Cleaner: cleaner}) // nil Scheduler

	if err := handler(context.Background(), nil); err != nil {
		t.Fatalf("nil scheduler must not cause error: %v", err)
	}
}

// TestCleanup_DefaultCleanupIntervalIsOneHour verifies the constant value.
func TestCleanup_DefaultCleanupIntervalIsOneHour(t *testing.T) {
	if DefaultCleanupInterval != time.Hour {
		t.Errorf("DefaultCleanupInterval must be 1h, got %v", DefaultCleanupInterval)
	}
}

// TestCleanup_JobTypeConstant verifies the job_type string value.
func TestCleanup_JobTypeConstant(t *testing.T) {
	if CleanupJobType != "idempotency.cleanup" {
		t.Errorf("CleanupJobType must be %q, got %q", "idempotency.cleanup", CleanupJobType)
	}
}

// TestCleanup_CustomRetentionBuffer verifies that a non-zero RetentionBuffer
// provides an extra grace period: rows expired only within the buffer window
// are NOT yet deleted.
func TestCleanup_CustomRetentionBuffer(t *testing.T) {
	const buffer = 2 * time.Hour

	cleaner := &inMemoryCleaner{}
	now := time.Now()
	// Row expired 1h ago — within the 2h buffer, so should NOT be deleted.
	cleaner.seed("KEY_RECENT", "scope1", now.Add(-1*time.Hour))
	// Row expired 3h ago — outside the 2h buffer, so SHOULD be deleted.
	cleaner.seed("KEY_OLD", "scope1", now.Add(-3*time.Hour))
	// Row still valid — must not be deleted.
	cleaner.seed("KEY_VALID", "scope1", now.Add(23*time.Hour))

	handler := NewCleanupHandler(CleanupOptions{
		Cleaner:         cleaner,
		RetentionBuffer: buffer,
	})

	if err := handler(context.Background(), nil); err != nil {
		t.Fatalf("handler error: %v", err)
	}

	// KEY_RECENT expired 1h ago but buffer=2h → must still be present
	if !cleaner.contains("KEY_RECENT", "scope1") {
		t.Error("KEY_RECENT (expired 1h ago, buffer 2h) must NOT be deleted yet")
	}
	// KEY_OLD expired 3h ago, > buffer → must be deleted
	if cleaner.contains("KEY_OLD", "scope1") {
		t.Error("KEY_OLD (expired 3h ago, buffer 2h) must be deleted")
	}
	// KEY_VALID still live → must be present
	if !cleaner.contains("KEY_VALID", "scope1") {
		t.Error("KEY_VALID (still valid) must NOT be deleted")
	}
}

// TestCleanup_CustomCleanupInterval verifies that a non-default CleanupInterval
// is used when scheduling the next run.
func TestCleanup_CustomCleanupInterval(t *testing.T) {
	const custom = 30 * time.Minute

	cleaner := &inMemoryCleaner{}
	sched := &fakeCleanupScheduler{}

	handler := NewCleanupHandler(CleanupOptions{
		Cleaner:         cleaner,
		Scheduler:       sched,
		CleanupInterval: custom,
	})

	before := time.Now()
	if err := handler(context.Background(), nil); err != nil {
		t.Fatalf("handler error: %v", err)
	}

	at, ok := sched.firstCallAt()
	if !ok {
		t.Fatal("no ScheduleNext call")
	}

	expectedMin := before.Add(custom - time.Second)
	expectedMax := before.Add(custom + time.Second)

	if at.Before(expectedMin) || at.After(expectedMax) {
		t.Errorf("next run at %v outside expected [%v, %v]", at, expectedMin, expectedMax)
	}
}

// ----------------------------------------------------------------------------
// Full verification sweep (all 6 steps in one test)
// ----------------------------------------------------------------------------

// TestCleanup_FullVerification runs all 6 feature steps in sequence.
func TestCleanup_FullVerification(t *testing.T) {
	// Step 1: Insert 3 rows — A expired 1h ago, B expired 1d ago, C still valid.
	cleaner := &inMemoryCleaner{}
	seedThreeRows(cleaner)

	if n := cleaner.rowCount(); n != 3 {
		t.Fatalf("step 1 setup: want 3 rows, got %d", n)
	}

	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "idempotency_cleanup_full_test",
		Help: "feature #48 full verification counter",
	})
	sched := &fakeCleanupScheduler{}

	before := time.Now()

	// Step 2: Enqueue/run the cleanup job.
	handler := NewCleanupHandler(CleanupOptions{
		Cleaner:        cleaner,
		DeletedCounter: counter,
		Scheduler:      sched,
	})

	// Step 3: Wait for completion (synchronous call).
	if err := handler(context.Background(), nil); err != nil {
		t.Fatalf("step 3: handler returned error: %v", err)
	}

	// Step 4: Verify A and B deleted, C still present.
	if cleaner.contains("KEY_A", cleanupScopeA) {
		t.Error("step 4: row A (expired 1h ago) must be deleted")
	}
	if cleaner.contains("KEY_B", cleanupScopeB) {
		t.Error("step 4: row B (expired 1d ago) must be deleted")
	}
	if !cleaner.contains("KEY_C", cleanupScopeC) {
		t.Error("step 4: row C (still valid) must NOT be deleted")
	}
	if n := cleaner.rowCount(); n != 1 {
		t.Errorf("step 4: want 1 remaining row (C), got %d", n)
	}

	// Step 5: Counter incremented by 2.
	if v := counterValue(counter); v != 2 {
		t.Errorf("step 5: counter must be 2, got %.0f", v)
	}

	// Step 6: Cleanup job self-scheduled (cron-like).
	if n := sched.callCount(); n != 1 {
		t.Errorf("step 6: want 1 ScheduleNext call, got %d", n)
	}
	at, ok := sched.firstCallAt()
	if !ok {
		t.Fatal("step 6: no ScheduleNext call recorded")
	}
	if !at.After(before.Add(DefaultCleanupInterval - time.Second)) {
		t.Errorf("step 6: next run at %v must be approx now()+1h", at)
	}

	t.Logf("Full verification PASS: 2 rows deleted, counter=2, next run at %v", at)
}

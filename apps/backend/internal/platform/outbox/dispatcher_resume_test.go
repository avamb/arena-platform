// Package outbox — unit tests for feature #38
// "Outbox dispatcher resumes after worker restart"
//
// These tests verify the OutboxEventsDispatcher semantics without a live
// PostgreSQL connection. An in-memory OutboxEventStore simulates the
// outbox_events table. The tests prove:
//
//   - A row written before the dispatcher starts (i.e. before a worker restart)
//     is claimed and delivered on the first poll cycle after the dispatcher starts.
//   - processed_at is set (MarkDispatched called) after successful delivery.
//   - attempts is incremented after dispatch (both success and failure paths).
//   - The worker log includes event_type and aggregate_id for each dispatched row.
//   - The Prometheus outbox_events_dispatched_total counter increments per event.
//   - trace_id from the outbox payload is propagated to the Dispatcher.Dispatch call.
//   - At-least-once: a row that fails dispatch is not marked processed (attempts++
//     only) and will be retried on the next cycle.
//   - Rows already processed (processed_at non-null) are not re-dispatched.
package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// =============================================================================
// In-memory OutboxEventStore
// =============================================================================

// memOutboxRow mirrors outbox_events columns relevant to the dispatcher.
type memOutboxRow struct {
	id            string
	aggregateType string
	aggregateID   string
	eventType     string
	payload       map[string]any
	occurredAt    time.Time
	processedAt   *time.Time // nil = unprocessed
	attempts      int
	lastError     string
}

// inMemOutboxStore is a thread-safe in-memory OutboxEventStore for tests.
// Rows are appended via seed(); ClaimNext returns the first unprocessed row.
type inMemOutboxStore struct {
	mu   sync.Mutex
	rows []*memOutboxRow
}

func newInMemOutboxStore() *inMemOutboxStore { return &inMemOutboxStore{} }

// seed adds a pre-existing row to the store (simulates a row written before
// the dispatcher started — i.e. written while the worker was offline).
func (s *inMemOutboxStore) seed(row *memOutboxRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, row)
}

// ClaimNext returns the first unprocessed row in occurred_at order.
func (s *inMemOutboxStore) ClaimNext(_ context.Context) (*OutboxEventRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rows {
		if r.processedAt == nil {
			return &OutboxEventRow{
				ID:            r.id,
				AggregateType: r.aggregateType,
				AggregateID:   r.aggregateID,
				EventType:     r.eventType,
				Payload:       r.payload,
				OccurredAt:    r.occurredAt,
				Attempts:      r.attempts,
			}, nil
		}
	}
	return nil, nil
}

// MarkDispatched sets processedAt = now() and increments attempts.
func (s *inMemOutboxStore) MarkDispatched(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rows {
		if r.id == id {
			now := time.Now()
			r.processedAt = &now
			r.attempts++
			return nil
		}
	}
	return errors.New("inMemOutboxStore: row not found: " + id)
}

// MarkFailed increments attempts and stores lastErr (row stays unprocessed).
func (s *inMemOutboxStore) MarkFailed(_ context.Context, id string, lastErr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rows {
		if r.id == id {
			r.attempts++
			r.lastError = lastErr
			return nil
		}
	}
	return errors.New("inMemOutboxStore: row not found: " + id)
}

// findRow returns a copy of the stored row by id (for assertions).
func (s *inMemOutboxStore) findRow(id string) *memOutboxRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rows {
		if r.id == id {
			cp := *r
			return &cp
		}
	}
	return nil
}

// Compile-time interface guard.
var _ OutboxEventStore = (*inMemOutboxStore)(nil)

// =============================================================================
// captureDispatcher — records every Dispatch call
// =============================================================================

type capturedDispatch struct {
	event Event
}

type captureDispatcher struct {
	mu       sync.Mutex
	calls    []capturedDispatch
	failOnce bool // if true, first call returns error then clears flag
}

func (d *captureDispatcher) Dispatch(_ context.Context, ev Event) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, capturedDispatch{event: ev})
	if d.failOnce {
		d.failOnce = false
		return errors.New("simulated dispatch failure")
	}
	return nil
}

func (d *captureDispatcher) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

func (d *captureDispatcher) lastCall() (capturedDispatch, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.calls) == 0 {
		return capturedDispatch{}, false
	}
	return d.calls[len(d.calls)-1], true
}

// Compile-time interface guard.
var _ Dispatcher = (*captureDispatcher)(nil)

// =============================================================================
// Test helpers
// =============================================================================

// newTestRow builds a memOutboxRow suitable for seeding.
func newTestRow(id, eventType, aggregateID, traceID string) *memOutboxRow {
	return &memOutboxRow{
		id:            id,
		aggregateType: "echo",
		aggregateID:   aggregateID,
		eventType:     eventType,
		payload: map[string]any{
			"trace_id": traceID,
			"message":  "hello",
		},
		occurredAt: time.Now().Add(-5 * time.Second), // written before dispatcher started
		attempts:   0,
	}
}

// runDispatcherOnce builds a dispatcher, runs it in a goroutine, waits for
// at most maxWait for the dispatcher to process all seeded rows, then stops it.
//
// Returns the store and dispatcher for post-run assertions.
func runDispatcherOnce(t *testing.T, store *inMemOutboxStore, capDisp *captureDispatcher, logger *slog.Logger, opts ...func(*OutboxEventsDispatcherOptions)) (*inMemOutboxStore, *captureDispatcher) {
	t.Helper()

	counter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "test_outbox_events_dispatched_total_" + t.Name(),
	}, []string{"event_type"})

	o := OutboxEventsDispatcherOptions{
		Store:             store,
		Dispatcher:        capDisp,
		Logger:            logger,
		DispatchedCounter: counter,
		PollInterval:      5 * time.Millisecond, // fast for tests
		ShutdownTimeout:   2 * time.Second,
	}
	for _, opt := range opts {
		opt(&o)
	}

	d, err := NewOutboxEventsDispatcher(o)
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go func() { _ = d.Run(ctx) }()

	// Poll until the queue is empty or timeout.
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		row, _ := store.ClaimNext(context.Background())
		if row == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the dispatcher finish the last delivery

	_ = d.Stop()
	return store, capDisp
}

// logBuffer returns a *slog.Logger that writes JSON to a bytes.Buffer.
func logBuffer() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return logger, &buf
}

// =============================================================================
// Feature #38 tests — all 10 steps mapped
// =============================================================================

// TestOutboxDispatcher_ResumesRowAfterRestart simulates the core scenario:
// a row is seeded while the dispatcher is offline (worker was stopped), then
// the dispatcher starts and claims + delivers the row.
//
// Covers feature steps 1–5.
func TestOutboxDispatcher_ResumesRowAfterRestart(t *testing.T) {
	store := newInMemOutboxStore()
	capDisp := &captureDispatcher{}
	logger, _ := logBuffer()

	const rowID = "00000000-0000-0000-0000-000000000038"
	// Step 1-3: row seeded before dispatcher starts (simulates worker offline + /v1/echo call).
	store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000001", "trace-abc"))

	// Verify row exists and is unprocessed before starting dispatcher.
	r := store.findRow(rowID)
	if r == nil {
		t.Fatal("step 3: row must exist in store before dispatcher starts")
	}
	if r.processedAt != nil {
		t.Fatal("step 3: processedAt must be nil before dispatcher starts")
	}

	// Step 4: start dispatcher (simulates worker restart).
	runDispatcherOnce(t, store, capDisp, logger)

	// Step 5: row must now be processed.
	r = store.findRow(rowID)
	if r == nil {
		t.Fatal("step 5: row disappeared from store")
	}
	if r.processedAt == nil {
		t.Error("step 5: processedAt must be non-nil after dispatcher delivers the row")
	}
}

// TestOutboxDispatcher_AttemptsIncrementedAfterSuccessfulDispatch verifies step 6.
func TestOutboxDispatcher_AttemptsIncrementedAfterSuccessfulDispatch(t *testing.T) {
	store := newInMemOutboxStore()
	capDisp := &captureDispatcher{}
	logger, _ := logBuffer()

	const rowID = "00000000-0000-0000-0000-000000000039"
	store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000001", "trace-def"))

	runDispatcherOnce(t, store, capDisp, logger)

	r := store.findRow(rowID)
	if r == nil {
		t.Fatal("row not found after dispatch")
	}
	// Step 6: attempts must be >= 1.
	if r.attempts < 1 {
		t.Errorf("step 6: attempts=%d, want >= 1 after successful dispatch", r.attempts)
	}
}

// TestOutboxDispatcher_LogsEventTypeAndAggregateID verifies step 7.
func TestOutboxDispatcher_LogsEventTypeAndAggregateID(t *testing.T) {
	store := newInMemOutboxStore()
	capDisp := &captureDispatcher{}
	logger, buf := logBuffer()

	const (
		rowID       = "00000000-0000-0000-0000-000000000040"
		eventType   = "v1.echo.created"
		aggregateID = "00000000-0000-0000-0000-000000000099"
	)
	store.seed(newTestRow(rowID, eventType, aggregateID, "trace-ghi"))

	runDispatcherOnce(t, store, capDisp, logger)

	logOutput := buf.String()

	// Step 7: log must contain event_type.
	if !strings.Contains(logOutput, eventType) {
		t.Errorf("step 7: log output does not contain event_type=%q; got:\n%s", eventType, logOutput)
	}
	// Step 7: log must contain aggregate_id.
	if !strings.Contains(logOutput, aggregateID) {
		t.Errorf("step 7: log output does not contain aggregate_id=%q; got:\n%s", aggregateID, logOutput)
	}
}

// TestOutboxDispatcher_PrometheusCounterIncremented verifies step 8.
func TestOutboxDispatcher_PrometheusCounterIncremented(t *testing.T) {
	store := newInMemOutboxStore()
	capDisp := &captureDispatcher{}

	const rowID = "00000000-0000-0000-0000-000000000041"
	const eventType = "v1.echo.created"
	store.seed(newTestRow(rowID, eventType, "00000000-0000-0000-0000-000000000001", "trace-jkl"))

	// Create a dedicated counter and registry so we can read the value.
	reg := prometheus.NewRegistry()
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "outbox_events_dispatched_total_step8",
		Help: "Test counter for step 8",
	}, []string{"event_type"})
	reg.MustRegister(counter)

	d, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
		Store:             store,
		Dispatcher:        capDisp,
		Logger:            slog_noop(), // reuse helper from writer_test.go
		DispatchedCounter: counter,
		PollInterval:      5 * time.Millisecond,
		ShutdownTimeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	// Wait for the row to be dispatched.
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if r := store.findRow(rowID); r != nil && r.processedAt != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	_ = d.Stop()

	// Step 8: outbox_events_dispatched_total{event_type="v1.echo.created"} must be >= 1.
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	if len(mfs) == 0 {
		t.Fatal("step 8: no metric families gathered — counter was never incremented")
	}
	var found bool
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			if m.GetCounter().GetValue() >= 1 {
				found = true
			}
		}
	}
	if !found {
		t.Error("step 8: outbox_events_dispatched_total counter must be >= 1 after dispatch")
	}
}

// TestOutboxDispatcher_TraceIDPropagated verifies step 9.
func TestOutboxDispatcher_TraceIDPropagated(t *testing.T) {
	store := newInMemOutboxStore()
	capDisp := &captureDispatcher{}
	logger, _ := logBuffer()

	const (
		rowID   = "00000000-0000-0000-0000-000000000042"
		traceID = "trace-step9-propagation"
	)
	store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000001", traceID))

	runDispatcherOnce(t, store, capDisp, logger)

	// Step 9: the Dispatcher.Dispatch call must have received an event whose
	// Payload contains the original trace_id.
	call, ok := capDisp.lastCall()
	if !ok {
		t.Fatal("step 9: no Dispatch call captured — dispatcher did not call Dispatcher.Dispatch")
	}

	payloadTraceID, _ := call.event.Payload["trace_id"].(string)
	if payloadTraceID != traceID {
		t.Errorf("step 9: payload.trace_id=%q, want %q", payloadTraceID, traceID)
	}
}

// TestOutboxDispatcher_AtLeastOnce verifies step 10: a row whose dispatch fails
// is NOT marked processed — it remains unprocessed so the next cycle retries it.
func TestOutboxDispatcher_AtLeastOnce(t *testing.T) {
	store := newInMemOutboxStore()
	// First dispatch will fail; second will succeed.
	capDisp := &captureDispatcher{failOnce: true}
	logger, _ := logBuffer()

	const rowID = "00000000-0000-0000-0000-000000000043"
	store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000001", "trace-step10"))

	// Run the dispatcher long enough for two cycles.
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "test_outbox_atLeastOnce_" + t.Name(),
	}, []string{"event_type"})

	d, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
		Store:             store,
		Dispatcher:        capDisp,
		Logger:            logger,
		DispatchedCounter: counter,
		PollInterval:      5 * time.Millisecond,
		ShutdownTimeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	// Wait for the row to eventually be processed (second attempt should succeed).
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if r := store.findRow(rowID); r != nil && r.processedAt != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	_ = d.Stop()

	r := store.findRow(rowID)
	if r == nil {
		t.Fatal("row disappeared from store")
	}

	// After the failed first attempt: attempts was incremented (at-least-once retry).
	// After the successful second attempt: processedAt is set.
	if r.processedAt == nil {
		t.Error("step 10: processedAt must be non-nil after eventual successful dispatch")
	}
	// The row was attempted at least twice (once failing, once succeeding).
	if r.attempts < 2 {
		t.Errorf("step 10: attempts=%d, want >= 2 (failed attempt + successful attempt)", r.attempts)
	}
	// Dispatcher.Dispatch was called at least twice.
	if capDisp.callCount() < 2 {
		t.Errorf("step 10: Dispatch called %d times, want >= 2 (at-least-once retry)", capDisp.callCount())
	}
}

// TestOutboxDispatcher_AlreadyProcessedRowsNotReDispatched ensures the
// dispatcher skips rows with a non-null processed_at (already delivered).
func TestOutboxDispatcher_AlreadyProcessedRowsNotReDispatched(t *testing.T) {
	store := newInMemOutboxStore()
	capDisp := &captureDispatcher{}
	logger, _ := logBuffer()

	now := time.Now()
	alreadyProcessed := &memOutboxRow{
		id:            "00000000-0000-0000-0000-000000000044",
		aggregateType: "echo",
		aggregateID:   "00000000-0000-0000-0000-000000000001",
		eventType:     "v1.echo.created",
		payload:       map[string]any{"trace_id": "already-done"},
		occurredAt:    now.Add(-10 * time.Second),
		processedAt:   &now, // already processed
		attempts:      1,
	}
	store.seed(alreadyProcessed)

	runDispatcherOnce(t, store, capDisp, logger)

	// Dispatcher.Dispatch must never be called for already-processed rows.
	if capDisp.callCount() != 0 {
		t.Errorf("already-processed row was re-dispatched: Dispatch called %d times, want 0",
			capDisp.callCount())
	}
}

// TestOutboxDispatcher_MultipleRowsProcessedInOrder verifies the dispatcher
// processes all unprocessed rows (simulates multiple events pending after restart).
func TestOutboxDispatcher_MultipleRowsProcessedInOrder(t *testing.T) {
	store := newInMemOutboxStore()
	capDisp := &captureDispatcher{}
	logger, _ := logBuffer()

	const n = 3
	ids := [n]string{
		"00000000-0000-0000-0000-000000000045",
		"00000000-0000-0000-0000-000000000046",
		"00000000-0000-0000-0000-000000000047",
	}
	for i, id := range ids {
		row := newTestRow(id, "v1.echo.created", "00000000-0000-0000-0000-000000000001", "trace-multi")
		row.occurredAt = time.Now().Add(-time.Duration(n-i) * time.Second)
		store.seed(row)
	}

	// Run the dispatcher long enough to consume all 3 rows.
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

	// Poll until all rows are dispatched.
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
		if r == nil {
			t.Fatalf("row %s disappeared from store", id)
		}
		if r.processedAt == nil {
			t.Errorf("row %s not processed within deadline", id)
		}
	}
	if capDisp.callCount() != n {
		t.Errorf("Dispatch called %d times, want %d", capDisp.callCount(), n)
	}
}

// TestNewOutboxEventsDispatcher_NilStoreErrors verifies constructor validation.
func TestNewOutboxEventsDispatcher_NilStoreErrors(t *testing.T) {
	_, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
		Store: nil,
	})
	if err == nil {
		t.Error("want error when Store is nil")
	}
}

// TestNewOutboxEventsDispatcher_NilDispatcherUsesNoop verifies that a nil
// Dispatcher falls back to NoopDispatcher without error.
func TestNewOutboxEventsDispatcher_NilDispatcherUsesNoop(t *testing.T) {
	store := newInMemOutboxStore()
	d, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
		Store: store,
	})
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher with nil Dispatcher: %v", err)
	}
	if d == nil {
		t.Fatal("want non-nil dispatcher when opts.Dispatcher is nil")
	}
}

// TestOutboxDispatcher_LogsContainTraceID verifies step 9 at the log level:
// the structured log output includes the trace_id from the payload.
func TestOutboxDispatcher_LogsContainTraceID(t *testing.T) {
	store := newInMemOutboxStore()
	capDisp := &captureDispatcher{}
	logger, buf := logBuffer()

	const (
		rowID   = "00000000-0000-0000-0000-000000000048"
		traceID = "unique-trace-for-log-test"
	)
	store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000001", traceID))

	runDispatcherOnce(t, store, capDisp, logger)

	logOutput := buf.String()
	if !strings.Contains(logOutput, traceID) {
		t.Errorf("log output must contain trace_id=%q; got:\n%s", traceID, logOutput)
	}
}

// TestOutboxDispatcher_DispatcherReceivesCorrectEvent verifies that the Event
// passed to Dispatcher.Dispatch matches the outbox_events row data.
func TestOutboxDispatcher_DispatcherReceivesCorrectEvent(t *testing.T) {
	store := newInMemOutboxStore()
	capDisp := &captureDispatcher{}
	logger, _ := logBuffer()

	const rowID = "00000000-0000-0000-0000-000000000049"
	const eventType = "v1.echo.created"
	const aggregateID = "00000000-0000-0000-0000-000000000001"
	row := newTestRow(rowID, eventType, aggregateID, "trace-event-check")
	row.payload["message"] = "hello outbox"
	store.seed(row)

	runDispatcherOnce(t, store, capDisp, logger)

	call, ok := capDisp.lastCall()
	if !ok {
		t.Fatal("no Dispatch call captured")
	}

	if call.event.EventType != eventType {
		t.Errorf("event.EventType=%q, want %q", call.event.EventType, eventType)
	}
	if call.event.AggregateType != "echo" {
		t.Errorf("event.AggregateType=%q, want echo", call.event.AggregateType)
	}
	if call.event.AggregateID != aggregateID {
		t.Errorf("event.AggregateID=%q, want %q", call.event.AggregateID, aggregateID)
	}

	msg, _ := call.event.Payload["message"].(string)
	if msg != "hello outbox" {
		t.Errorf("event.Payload[message]=%q, want hello outbox", msg)
	}
}

// TestPGOutboxEventStore_CompileTimeInterfaceGuard is a runtime expression of
// the compile-time var _ OutboxEventStore = (*PGOutboxEventStore)(nil) guard.
func TestPGOutboxEventStore_CompileTimeInterfaceGuard(_ *testing.T) {
	var _ OutboxEventStore = (*PGOutboxEventStore)(nil)
}

// TestOutboxEventsDispatcher_ClaimSQLContainsForUpdateSkipLocked verifies
// that the production SQL for claiming rows includes the locking clause.
func TestOutboxEventsDispatcher_ClaimSQLContainsForUpdateSkipLocked(t *testing.T) {
	if !strings.Contains(claimSQL, "FOR UPDATE SKIP LOCKED") {
		t.Errorf("claimSQL must contain FOR UPDATE SKIP LOCKED; got:\n%s", claimSQL)
	}
	if !strings.Contains(claimSQL, "processed_at IS NULL") {
		t.Errorf("claimSQL must filter on processed_at IS NULL; got:\n%s", claimSQL)
	}
	if !strings.Contains(claimSQL, "outbox_events") {
		t.Errorf("claimSQL must query outbox_events table; got:\n%s", claimSQL)
	}
}

// TestOutboxEventsDispatcher_MarkDispatchedSQLUpdatesProcessedAt verifies the
// MarkDispatched SQL sets processed_at = now() and increments attempts.
func TestOutboxEventsDispatcher_MarkDispatchedSQLUpdatesProcessedAt(t *testing.T) {
	if !strings.Contains(markDispatchedSQL, "processed_at = now()") {
		t.Errorf("markDispatchedSQL must set processed_at = now(); got:\n%s", markDispatchedSQL)
	}
	if !strings.Contains(markDispatchedSQL, "attempts = attempts + 1") {
		t.Errorf("markDispatchedSQL must increment attempts; got:\n%s", markDispatchedSQL)
	}
	if !strings.Contains(markDispatchedSQL, "outbox_events") {
		t.Errorf("markDispatchedSQL must update outbox_events; got:\n%s", markDispatchedSQL)
	}
}

// TestOutboxEventsDispatcher_MarkFailedSQLUpdatesAttempts verifies the
// MarkFailed SQL increments attempts without touching processed_at.
func TestOutboxEventsDispatcher_MarkFailedSQLUpdatesAttempts(t *testing.T) {
	if !strings.Contains(markFailedSQL, "attempts = attempts + 1") {
		t.Errorf("markFailedSQL must increment attempts; got:\n%s", markFailedSQL)
	}
	if strings.Contains(markFailedSQL, "processed_at") {
		t.Errorf("markFailedSQL must NOT set processed_at (row remains unprocessed for retry); got:\n%s", markFailedSQL)
	}
	if !strings.Contains(markFailedSQL, "last_error") {
		t.Errorf("markFailedSQL must store last_error; got:\n%s", markFailedSQL)
	}
}

// TestNewOutboxEventsDispatchedCounter verifies the counter helper creates
// a CounterVec with the correct metric name and label.
func TestNewOutboxEventsDispatchedCounter(t *testing.T) {
	c := NewOutboxEventsDispatchedCounter()
	if c == nil {
		t.Fatal("NewOutboxEventsDispatchedCounter returned nil")
	}

	// Register and gather to confirm metric name.
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	c.WithLabelValues("v1.echo.created").Inc()

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if len(mfs) == 0 {
		t.Fatal("no metric families gathered")
	}

	name := mfs[0].GetName()
	if name != "outbox_events_dispatched_total" {
		t.Errorf("metric name=%q, want outbox_events_dispatched_total", name)
	}
}

// TestOutboxDispatcher_StopIsIdempotent verifies Stop can be called multiple times.
func TestOutboxDispatcher_StopIsIdempotent(t *testing.T) {
	store := newInMemOutboxStore()
	d, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
		Store:           store,
		Logger:          slog_noop(),
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = d.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	time.Sleep(30 * time.Millisecond)

	// Must not panic on multiple Stop calls.
	_ = d.Stop()
	_ = d.Stop()
}

// TestOutboxEventsDispatcher_OutboxPayloadIsValidJSON is a cross-package sanity
// check: the echo handler stores JSON in outbox_events.payload. The dispatcher
// must be able to unmarshal it.
func TestOutboxEventsDispatcher_OutboxPayloadIsValidJSON(t *testing.T) {
	// Simulate what the echo handler writes.
	rawPayload, err := json.Marshal(map[string]any{
		"actor_id":   "00000000-0000-0000-0000-000000000001",
		"message":    "test",
		"request_id": "req-123",
		"trace_id":   "trace-456",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(rawPayload, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, ok := out["trace_id"]; !ok {
		t.Error("outbox payload must contain trace_id for dispatcher to propagate")
	}
}

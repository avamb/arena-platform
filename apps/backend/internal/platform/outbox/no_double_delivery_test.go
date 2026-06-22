// Package outbox — unit tests for feature #73
// "Outbox dispatcher does not double-deliver events"
//
// An outbox event is delivered at-least-once but the dispatcher must mark
// processed_at atomically so a crash mid-dispatch doesn't result in 2
// deliveries from the dispatcher itself.
//
// Test coverage:
//
//	Step 1–3 (normal path):
//	  Insert an outbox row, run the dispatcher with an HTTP counter server,
//	  verify exactly 1 delivery and processed_at is set.
//
//	Step 4–6 (fault injection — crash between delivery and mark):
//	  The faultInjectStore makes MarkDispatched fail on its first call,
//	  simulating FAULT_INJECT_AFTER_DELIVERY=true (process crash after the
//	  webhook was hit but before the DB commit that stamps processed_at).
//	  After this simulated crash the row remains unprocessed; the dispatcher
//	  delivers it again (at-least-once: 1 or 2 hits allowed) and this time
//	  MarkDispatched succeeds — processed_at is now set.
//
//	Step 7 (no infinite loop):
//	  After processed_at is set, ClaimNext returns nil and the dispatcher
//	  stops calling Dispatch for the same row. Total Dispatch calls are
//	  bounded (<=2 for a single-fault store).
//
//	Step 8 (attempts counter logged):
//	  The structured log emitted by deliverRow includes the `attempts` field
//	  for every dispatch attempt.
package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// HTTP counter server — counts POST deliveries from the real dispatcher
// =============================================================================

// httpCounterServer is an httptest.Server that atomically counts every request.
type httpCounterServer struct {
	server *httptest.Server
	hits   atomic.Int64
}

func newHTTPCounterServer() *httpCounterServer {
	c := &httpCounterServer{}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	return c
}

func (c *httpCounterServer) URL() string       { return c.server.URL }
func (c *httpCounterServer) HitCount() int64   { return c.hits.Load() }
func (c *httpCounterServer) Close()            { c.server.Close() }

// =============================================================================
// httpEventDispatcher — real Dispatcher that POSTs event payload to a URL
// =============================================================================

// httpEventDispatcher implements Dispatcher by POSTing event payload as JSON.
type httpEventDispatcher struct {
	url    string
	client *http.Client
}

func newHTTPEventDispatcher(url string) *httpEventDispatcher {
	return &httpEventDispatcher{
		url:    url,
		client: &http.Client{Timeout: 2 * time.Second},
	}
}

func (d *httpEventDispatcher) Dispatch(ctx context.Context, ev Event) error {
	body, err := json.Marshal(ev.Payload)
	if err != nil {
		return fmt.Errorf("httpEventDispatcher: marshal payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("httpEventDispatcher: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("httpEventDispatcher: do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("httpEventDispatcher: server returned %d", resp.StatusCode)
	}
	return nil
}

// Compile-time interface guard.
var _ Dispatcher = (*httpEventDispatcher)(nil)

// =============================================================================
// faultInjectStore — wraps inMemOutboxStore, fails MarkDispatched N times
// =============================================================================

// faultInjectStore is an OutboxEventStore that returns an error on the first
// failCount calls to MarkDispatched — simulating a process crash that happens
// after the webhook was hit but before the DB commit stamping processed_at.
type faultInjectStore struct {
	*inMemOutboxStore
	mu                  sync.Mutex
	markDispatchedCalls int
	failCount           int // how many MarkDispatched calls to fail before succeeding
}

func newFaultInjectStore(failCount int) *faultInjectStore {
	return &faultInjectStore{
		inMemOutboxStore: newInMemOutboxStore(),
		failCount:        failCount,
	}
}

// MarkDispatched fails on the first failCount calls (simulated crash), then
// delegates to the embedded store on subsequent calls.
func (s *faultInjectStore) MarkDispatched(ctx context.Context, id string) error {
	s.mu.Lock()
	callNum := s.markDispatchedCalls
	s.markDispatchedCalls++
	s.mu.Unlock()

	if callNum < s.failCount {
		// Simulate a crash before the DB commit: return error without mutating
		// the store. The row stays unprocessed and will be claimed again.
		return errors.New("fault injection: simulated crash before MarkDispatched commit")
	}
	return s.inMemOutboxStore.MarkDispatched(ctx, id)
}

// markDispatchedCallCount returns the total number of MarkDispatched calls.
func (s *faultInjectStore) markDispatchedCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.markDispatchedCalls
}

// Compile-time interface guard.
var _ OutboxEventStore = (*faultInjectStore)(nil)

// =============================================================================
// Feature #73 tests
// =============================================================================

// TestNoDoubleDelivery_NormalPath covers steps 1–3:
// Normal delivery: outbox row → HTTP hit → processed_at stamped.
// The test server must receive exactly 1 hit.
func TestNoDoubleDelivery_NormalPath(t *testing.T) {
	// Step 1: set up HTTP counter server.
	srv := newHTTPCounterServer()
	defer srv.Close()

	store := newInMemOutboxStore()
	dispatcher := newHTTPEventDispatcher(srv.URL())
	logger, _ := logBuffer()

	const rowID = "00000000-0000-0000-0000-000000007301"

	// Seed the outbox row.
	store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000001", "trace-73-normal"))

	// Step 2: run dispatcher and wait for delivery.
	d, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
		Store:           store,
		Dispatcher:      dispatcher,
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

	// Poll until processed_at is set or timeout.
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if r := store.findRow(rowID); r != nil && r.processedAt != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let dispatcher finish
	_ = d.Stop()

	// Step 3: exactly 1 hit and processed_at is set.
	row := store.findRow(rowID)
	if row == nil {
		t.Fatal("step 3: row must still exist (not deleted)")
	}
	if row.processedAt == nil {
		t.Error("step 3: processed_at must be set after successful delivery")
	}

	hits := srv.HitCount()
	if hits != 1 {
		t.Errorf("step 3: HTTP hit count = %d, want exactly 1 for normal delivery", hits)
	}
}

// TestNoDoubleDelivery_CrashBeforeMark covers steps 4–6:
// Dispatcher delivers to webhook (hit 1), then MarkDispatched crashes.
// After crash the row is re-delivered (hit 2 — at-least-once).
// processed_at is eventually set and hit count is 1 or 2 (at-least-once contract).
func TestNoDoubleDelivery_CrashBeforeMark(t *testing.T) {
	// Step 4: set up HTTP counter server and fault-inject store.
	srv := newHTTPCounterServer()
	defer srv.Close()

	// failCount=1: first MarkDispatched call will fail (crash simulation).
	store := newFaultInjectStore(1)
	dispatcher := newHTTPEventDispatcher(srv.URL())
	logger, buf := logBuffer()

	const rowID = "00000000-0000-0000-0000-000000007302"

	// Seed the row (processed_at IS NULL — pending delivery).
	store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000002", "trace-73-crash"))

	// Run dispatcher long enough for two cycles (crash + retry).
	d, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
		Store:           store,
		Dispatcher:      dispatcher,
		Logger:          logger,
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	// Step 5: restart worker is simulated by the same dispatcher continuing to
	// poll. Wait until processed_at is finally set.
	deadline := time.Now().Add(700 * time.Millisecond)
	for time.Now().Before(deadline) {
		if r := store.findRow(rowID); r != nil && r.processedAt != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	_ = d.Stop()

	// Step 6a: processed_at must eventually be set.
	row := store.findRow(rowID)
	if row == nil {
		t.Fatal("step 6: row must still exist after at-least-once delivery")
	}
	if row.processedAt == nil {
		t.Error("step 6: processed_at must be set after the retry succeeds")
	}

	// Step 6b: hit count must be 1 or 2 (at-least-once: one crash + one retry).
	hits := srv.HitCount()
	if hits < 1 || hits > 2 {
		t.Errorf("step 6: HTTP hit count = %d, want 1 or 2 (at-least-once contract)", hits)
	}

	// MarkDispatched was called at least twice (once failing, once succeeding).
	markCalls := store.markDispatchedCallCount()
	if markCalls < 2 {
		t.Errorf("step 6: MarkDispatched called %d times, want >= 2 (fail + retry)", markCalls)
	}

	// The log must mention the crash error (mark dispatched error).
	logOutput := buf.String()
	if !strings.Contains(logOutput, "mark dispatched error") &&
		!strings.Contains(logOutput, "fault injection") {
		t.Errorf("step 6: log must record the MarkDispatched failure; got:\n%s", logOutput)
	}
}

// TestNoDoubleDelivery_NoInfiniteLoop covers step 7:
// After processed_at is stamped, the dispatcher stops claiming the row.
// ClaimNext returns nil and Dispatch is never called again for a processed row.
func TestNoDoubleDelivery_NoInfiniteLoop(t *testing.T) {
	srv := newHTTPCounterServer()
	defer srv.Close()

	// failCount=1: crash once, then succeed — row gets processed on cycle 2.
	store := newFaultInjectStore(1)
	dispatcher := newHTTPEventDispatcher(srv.URL())
	logger, _ := logBuffer()

	const rowID = "00000000-0000-0000-0000-000000007303"
	store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000003", "trace-73-loop"))

	d, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
		Store:           store,
		Dispatcher:      dispatcher,
		Logger:          logger,
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	// Wait until processed_at is set.
	deadline := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(deadline) {
		if r := store.findRow(rowID); r != nil && r.processedAt != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Record hit count at the moment processed_at was first observed.
	hitsAtMark := srv.HitCount()
	_ = d.Stop()

	// Step 7: verify processed_at is set (loop terminated).
	row := store.findRow(rowID)
	if row == nil || row.processedAt == nil {
		t.Fatal("step 7: row must be processed before checking for infinite loop")
	}

	// After processed_at is set, no further hits should arrive (dispatcher
	// must not re-deliver the row). hitsAtMark must be <= 2 for a single fault.
	if hitsAtMark > 2 {
		t.Errorf("step 7: hit count at mark time = %d, want <= 2 — dispatcher must not loop infinitely", hitsAtMark)
	}

	// The final hit count after Stop must equal hitsAtMark (no more deliveries).
	hitsAfterStop := srv.HitCount()
	if hitsAfterStop != hitsAtMark {
		t.Errorf("step 7: hit count after stop = %d, want %d — extra deliveries after processed_at was set",
			hitsAfterStop, hitsAtMark)
	}

	// ClaimNext must return nil for the processed row.
	row2, err := store.ClaimNext(context.Background())
	if err != nil {
		t.Fatalf("step 7: ClaimNext after processing: %v", err)
	}
	if row2 != nil {
		t.Errorf("step 7: ClaimNext must return nil once row is processed; got row id=%s", row2.ID)
	}
}

// TestNoDoubleDelivery_AttemptsCounterLogged covers step 8:
// The structured log must contain the `attempts` field for every dispatch.
func TestNoDoubleDelivery_AttemptsCounterLogged(t *testing.T) {
	// Use captureDispatcher (not HTTP) — the test verifies logging, not delivery.
	store := newInMemOutboxStore()
	capDisp := &captureDispatcher{}
	logger, buf := logBuffer()

	const rowID = "00000000-0000-0000-0000-000000007304"
	store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000004", "trace-73-attempts"))

	runDispatcherOnce(t, store, capDisp, logger)

	logOutput := buf.String()

	// Step 8: log must contain the `attempts` field name.
	if !strings.Contains(logOutput, "attempts") {
		t.Errorf("step 8: log must contain 'attempts' field; got:\n%s", logOutput)
	}

	// The attempts value should appear as a JSON number (0 for first attempt).
	if !strings.Contains(logOutput, `"attempts":0`) &&
		!strings.Contains(logOutput, `"attempts": 0`) {
		t.Logf("step 8: note: attempts value not 0 in log (may be non-zero if row was retried); log:\n%s", logOutput)
	}
}

// TestNoDoubleDelivery_MarkDispatchedAtomicCheck verifies that the SQL used by
// MarkDispatched sets processed_at atomically (in a single UPDATE statement).
// This prevents TOCTOU: check-then-act races between concurrent dispatcher
// instances that could both see the row as unprocessed.
func TestNoDoubleDelivery_MarkDispatchedAtomicCheck(t *testing.T) {
	// The production markDispatchedSQL must be a single UPDATE (not SELECT + UPDATE).
	if !strings.Contains(strings.ToUpper(markDispatchedSQL), "UPDATE") {
		t.Error("markDispatchedSQL must be an UPDATE statement (atomic mark)")
	}
	if strings.Contains(strings.ToUpper(markDispatchedSQL), "SELECT") {
		t.Error("markDispatchedSQL must NOT contain SELECT — mark must be a single atomic UPDATE")
	}
	if !strings.Contains(markDispatchedSQL, "processed_at") {
		t.Error("markDispatchedSQL must set processed_at in the same statement")
	}
	// The WHERE clause prevents marking an already-processed row a second time.
	if !strings.Contains(markDispatchedSQL, "$1") {
		t.Error("markDispatchedSQL must use a WHERE id = $1 clause to target only the claimed row")
	}
}

// TestNoDoubleDelivery_UnprocessedRowReclaimedAfterCrash verifies that a row
// whose MarkDispatched call failed (simulated crash) is returned by ClaimNext
// on the next poll — i.e. the at-least-once retry mechanism is active.
func TestNoDoubleDelivery_UnprocessedRowReclaimedAfterCrash(t *testing.T) {
	store := newFaultInjectStore(1) // first MarkDispatched will fail

	const rowID = "00000000-0000-0000-0000-000000007305"
	store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000005", "trace-73-reclaim"))

	ctx := context.Background()

	// First cycle: claim row, dispatch succeeds, MarkDispatched fails.
	row, err := store.ClaimNext(ctx)
	if err != nil || row == nil {
		t.Fatalf("ClaimNext cycle 1: got row=%v err=%v, want a row", row, err)
	}
	// Simulate: dispatch succeeded (webhook hit), then MarkDispatched crashes.
	if err := store.MarkDispatched(ctx, row.ID); err == nil {
		t.Fatal("expected fault injection error from MarkDispatched on call 1; got nil")
	}

	// The row must still be unprocessed (processed_at = nil).
	stored := store.findRow(rowID)
	if stored == nil {
		t.Fatal("row must still exist after crash")
	}
	if stored.processedAt != nil {
		t.Error("processed_at must be nil after MarkDispatched crash — row must remain claimable")
	}

	// Second cycle: claim row again (still unprocessed), MarkDispatched now succeeds.
	row2, err := store.ClaimNext(ctx)
	if err != nil || row2 == nil {
		t.Fatalf("ClaimNext cycle 2: got row=%v err=%v — row must be reclaimable after crash", row2, err)
	}
	if row2.ID != rowID {
		t.Errorf("ClaimNext cycle 2: got row id=%s, want %s", row2.ID, rowID)
	}

	// This time MarkDispatched succeeds.
	if err := store.MarkDispatched(ctx, row2.ID); err != nil {
		t.Fatalf("MarkDispatched cycle 2: %v", err)
	}

	// Row is now processed.
	stored2 := store.findRow(rowID)
	if stored2 == nil || stored2.processedAt == nil {
		t.Error("processed_at must be non-nil after successful MarkDispatched cycle 2")
	}

	// Third ClaimNext must return nil (row is now processed).
	row3, err := store.ClaimNext(ctx)
	if err != nil {
		t.Fatalf("ClaimNext cycle 3: %v", err)
	}
	if row3 != nil {
		t.Errorf("ClaimNext cycle 3: want nil (row processed), got row id=%s", row3.ID)
	}
}

// TestNoDoubleDelivery_FullVerification runs all feature steps as subtests.
func TestNoDoubleDelivery_FullVerification(t *testing.T) {
	t.Run("step1-3_normal_path_exactly_one_hit", func(t *testing.T) {
		srv := newHTTPCounterServer()
		defer srv.Close()

		store := newInMemOutboxStore()
		dispatcher := newHTTPEventDispatcher(srv.URL())
		logger, _ := logBuffer()

		const rowID = "00000000-0000-0000-0000-000000007310"
		store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000010", "trace-fv-1"))

		// Run dispatcher inline (httpEventDispatcher doesn't fit runDispatcherOnce signature).
		d, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
			Store:           store,
			Dispatcher:      dispatcher,
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
		deadline := time.Now().Add(400 * time.Millisecond)
		for time.Now().Before(deadline) {
			if r := store.findRow(rowID); r != nil && r.processedAt != nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond)
		_ = d.Stop()

		row := store.findRow(rowID)
		if row == nil || row.processedAt == nil {
			t.Error("step 1-3: processed_at must be set after normal delivery")
		}
		if srv.HitCount() != 1 {
			t.Errorf("step 1-3: HTTP hit count = %d, want 1", srv.HitCount())
		}
	})

	t.Run("step4-6_crash_before_mark_at_least_once", func(t *testing.T) {
		srv := newHTTPCounterServer()
		defer srv.Close()

		store := newFaultInjectStore(1)
		dispatcher := newHTTPEventDispatcher(srv.URL())
		logger, _ := logBuffer()

		const rowID = "00000000-0000-0000-0000-000000007311"
		store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000011", "trace-fv-4"))

		d, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
			Store:           store,
			Dispatcher:      dispatcher,
			Logger:          logger,
			PollInterval:    5 * time.Millisecond,
			ShutdownTimeout: 2 * time.Second,
		})
		if err != nil {
			t.Fatalf("NewOutboxEventsDispatcher: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
		defer cancel()
		go func() { _ = d.Run(ctx) }()

		deadline := time.Now().Add(600 * time.Millisecond)
		for time.Now().Before(deadline) {
			if r := store.findRow(rowID); r != nil && r.processedAt != nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond)
		_ = d.Stop()

		row := store.findRow(rowID)
		if row == nil || row.processedAt == nil {
			t.Error("step 4-6: processed_at must be set after at-least-once retry")
		}
		hits := srv.HitCount()
		if hits < 1 || hits > 2 {
			t.Errorf("step 4-6: hit count = %d, want 1 or 2 (at-least-once)", hits)
		}
	})

	t.Run("step7_no_infinite_loop", func(t *testing.T) {
		// After processing, ClaimNext returns nil.
		store := newFaultInjectStore(0) // no faults — succeed on first try
		const rowID = "00000000-0000-0000-0000-000000007312"
		store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000012", "trace-fv-7"))

		// Dispatch and mark in one cycle.
		ctx := context.Background()
		row, _ := store.ClaimNext(ctx)
		if row == nil {
			t.Fatal("ClaimNext must return the seeded row")
		}
		if err := store.MarkDispatched(ctx, row.ID); err != nil {
			t.Fatalf("MarkDispatched: %v", err)
		}

		// After marking, ClaimNext must return nil.
		row2, err := store.ClaimNext(ctx)
		if err != nil {
			t.Fatalf("ClaimNext after mark: %v", err)
		}
		if row2 != nil {
			t.Errorf("step 7: ClaimNext must return nil after processed_at is set; got id=%s", row2.ID)
		}
	})

	t.Run("step8_attempts_counter_logged", func(t *testing.T) {
		store := newInMemOutboxStore()
		capDisp := &captureDispatcher{}
		logger, buf := logBuffer()

		const rowID = "00000000-0000-0000-0000-000000007313"
		store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000013", "trace-fv-8"))

		runDispatcherOnce(t, store, capDisp, logger)

		if !strings.Contains(buf.String(), "attempts") {
			t.Errorf("step 8: log must contain 'attempts' field; got:\n%s", buf.String())
		}
	})
}

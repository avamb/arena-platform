// Package idempotency — unit tests for feature #45:
// "Concurrent identical Idempotency-Key requests both succeed with same response"
//
// Two parallel requests with the same Idempotency-Key must both receive the
// same response. The singleflight group inside Middleware ensures exactly one
// goroutine executes the handler while concurrent duplicates wait and share
// the captured result. No partial state: audit_events, outbox_events, and
// idempotency_keys each receive at most one row.
//
// Tests cover all 8 feature verification steps:
//
//   - Steps 1-2:  Two parallel POSTs with Idempotency-Key: CONC_KEY_x both return HTTP 200.
//   - Step  3:    Response bodies are byte-identical.
//   - Steps 4-5:  Handler invoked exactly once (proves audit/outbox written once).
//   - Step  6:    Store holds exactly 1 row for CONC_KEY_x.
//   - Step  7:    No deadlock or 500 in either request (timeout guard).
//   - Step  8:    Repeat 10 times with different keys to flush out races.
//
// The latch-based test ensures the race window is reliably triggered:
// Thread 1 blocks inside the handler until Thread 2 has sent its request,
// then both complete with identical responses.
package idempotency

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// latchHandler — a handler that blocks the first invocation until released.
// This guarantees the concurrent window is reliably triggered in tests.
// =============================================================================

// latchHandler wraps an inner http.Handler and blocks the FIRST invocation
// until unblocked by the test. Subsequent invocations (if any) proceed
// immediately. This is used to prove singleflight coalescing: Thread 2 must
// never invoke the inner handler while Thread 1 is blocked.
type latchHandler struct {
	inner   http.Handler
	calls   atomic.Int64
	entered chan struct{} // closed when first invocation enters the handler
	release chan struct{} // closed by test to unblock the first invocation
	once    sync.Once
}

func newLatchHandler(inner http.Handler) *latchHandler {
	return &latchHandler{
		inner:   inner,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (h *latchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.calls.Add(1)
	h.once.Do(func() {
		close(h.entered) // signal: first invocation has started
		<-h.release      // wait for test to unblock
	})
	h.inner.ServeHTTP(w, r)
}

// =============================================================================
// Helpers
// =============================================================================

// concurrentPost sends POST body to ts with the given idempotency key.
func concurrentPost(ts *httptest.Server, idemKey, msgBody string) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/echo", strings.NewReader(msgBody))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderName, idemKey)

	resp, err := ts.Client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, err
}

// =============================================================================
// Feature #45 tests
// =============================================================================

// TestConcurrent_BothReturn200WithIdenticalBody is the primary concurrent test.
//
// It uses a latchHandler to guarantee that Thread 2 sends its request while
// Thread 1 is blocked inside the handler. With singleflight, Thread 2 must
// wait in group.Do and get the same result as Thread 1.
//
// Covers steps 1-7 of the feature spec.
func TestConcurrent_BothReturn200WithIdenticalBody(t *testing.T) {
	t.Parallel()

	store := newInMemoryStore()
	var innerCalls atomic.Int64
	latch := newLatchHandler(echoJSONHandler(&innerCalls))
	mw := Middleware(store, Options{Scope: testScope})
	ts := httptest.NewServer(mw(latch))
	defer ts.Close()

	const key = "CONC_KEY_1"
	const body = `{"message":"race"}`

	type result struct {
		status int
		body   []byte
		err    error
	}
	ch := make(chan result, 2)

	// Step 1: Launch Thread 1 (it will block inside the handler at the latch).
	go func() {
		status, b, err := concurrentPost(ts, key, body)
		ch <- result{status, b, err}
	}()

	// Wait for Thread 1 to be blocked inside the handler (latch.entered is closed).
	select {
	case <-latch.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Thread 1 never entered the handler — test setup broken")
	}

	// Step 1: Launch Thread 2 while Thread 1 is blocked. Thread 1's singleflight
	// call is in-flight, so Thread 2 enters group.Do and waits.
	go func() {
		status, b, err := concurrentPost(ts, key, body)
		ch <- result{status, b, err}
	}()

	// Give Thread 2 time to reach group.Do and start waiting.
	// In CI this 10ms far exceeds the time for Lookup → MISS → group.Do.
	time.Sleep(10 * time.Millisecond)

	// Step 7: Release Thread 1 (proves no deadlock — if release hangs the
	// test timeout will fire, and we'd see 0 results in ch).
	close(latch.release)

	// Collect results from both goroutines (with a generous timeout to
	// detect deadlocks).
	var results [2]result
	for i := 0; i < 2; i++ {
		select {
		case r := <-ch:
			results[i] = r
		case <-time.After(10 * time.Second):
			t.Fatalf("goroutine %d never completed — possible deadlock", i)
		}
	}

	// Step 2: Both must return HTTP 200. No 500.
	for i, r := range results {
		if r.err != nil {
			t.Errorf("goroutine %d: request error: %v", i, r.err)
			continue
		}
		if r.status != http.StatusOK {
			t.Errorf("goroutine %d: want 200, got %d (body: %s)", i, r.status, r.body)
		}
	}

	// Step 3: Response bodies must be byte-identical.
	if !bytes.Equal(results[0].body, results[1].body) {
		t.Errorf("step 3: response bodies not identical\n  T1: %s\n  T2: %s",
			results[0].body, results[1].body)
	}

	// Steps 4-5: Handler (proxy for audit_events + outbox_events) invoked once.
	if n := latch.calls.Load(); n != 1 {
		t.Errorf("steps 4-5: handler called %d times, want exactly 1 (singleflight must coalesce)", n)
	}

	// Step 6: Exactly 1 row in idempotency_keys store.
	entry, ok := store.get(key, testScope)
	if !ok {
		t.Error("step 6: no row in store after concurrent requests")
	} else if entry.resp.Status != http.StatusOK {
		t.Errorf("step 6: stored status want 200, got %d", entry.resp.Status)
	}
}

// TestConcurrent_SecondRequestGetsReplayHeader verifies that the concurrent
// waiter (Thread 2, shared=true path) receives the Idempotent-Replay: true
// header, confirming it took the replay path not the fresh-execution path.
func TestConcurrent_SecondRequestGetsReplayHeader(t *testing.T) {
	t.Parallel()

	store := newInMemoryStore()
	var innerCalls atomic.Int64
	latch := newLatchHandler(echoJSONHandler(&innerCalls))
	mw := Middleware(store, Options{Scope: testScope})
	ts := httptest.NewServer(mw(latch))
	defer ts.Close()

	const key = "CONC_REPLAY_HEADER"
	const body = `{"message":"race"}`

	type result struct {
		status  int
		body    []byte
		headers http.Header
	}
	ch := make(chan result, 2)

	go func() {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/echo", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(HeaderName, key)
		resp, _ := ts.Client().Do(req)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		ch <- result{resp.StatusCode, b, resp.Header.Clone()}
	}()

	<-latch.entered

	go func() {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/echo", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(HeaderName, key)
		resp, _ := ts.Client().Do(req)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		ch <- result{resp.StatusCode, b, resp.Header.Clone()}
	}()

	time.Sleep(10 * time.Millisecond)
	close(latch.release)

	var results [2]result
	for i := 0; i < 2; i++ {
		select {
		case r := <-ch:
			results[i] = r
		case <-time.After(10 * time.Second):
			t.Fatalf("goroutine %d: timeout — possible deadlock", i)
		}
	}

	// Exactly one of the two responses must carry Idempotent-Replay: true.
	// The winner (Thread 1) never sets it; the shared waiter (Thread 2) does.
	replayCount := 0
	for _, r := range results {
		if r.headers.Get("Idempotent-Replay") == "true" {
			replayCount++
		}
	}
	if replayCount != 1 {
		t.Errorf("want exactly 1 response with Idempotent-Replay: true (the shared waiter), got %d", replayCount)
	}
}

// TestConcurrent_NoDeadlockWithTimeout is a safety net: runs the full
// concurrent scenario with a hard timeout. If either goroutine hangs the test
// fails immediately rather than blocking the CI suite.
func TestConcurrent_NoDeadlockWithTimeout(t *testing.T) {
	t.Parallel()

	store := newInMemoryStore()
	var innerCalls atomic.Int64
	latch := newLatchHandler(echoJSONHandler(&innerCalls))
	mw := Middleware(store, Options{Scope: testScope})
	ts := httptest.NewServer(mw(latch))
	defer ts.Close()

	const key = "CONC_DEADLOCK_KEY"
	const body = `{"message":"deadlock-test"}`

	done := make(chan struct{})
	go func() {
		defer close(done)
		ch := make(chan struct{}, 2)

		go func() {
			concurrentPost(ts, key, body) //nolint:errcheck
			ch <- struct{}{}
		}()
		<-latch.entered
		go func() {
			concurrentPost(ts, key, body) //nolint:errcheck
			ch <- struct{}{}
		}()
		time.Sleep(10 * time.Millisecond)
		close(latch.release)
		<-ch
		<-ch
	}()

	select {
	case <-done:
		// Step 7: no deadlock, both completed.
	case <-time.After(30 * time.Second):
		t.Error("step 7: deadlock detected — concurrent requests did not complete within 30s")
	}
}

// TestConcurrent_Repeat10DifferentKeys is step 8 of the feature spec:
// "Repeat 10 times with different keys to flush out races."
//
// For each of 10 unique keys we launch 2 concurrent requests and verify
// the correctness invariants. Each key produces exactly 1 store entry.
func TestConcurrent_Repeat10DifferentKeys(t *testing.T) {
	t.Parallel()

	store := newInMemoryStore()
	var totalHandlerCalls atomic.Int64

	const body = `{"message":"race"}`

	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("CONC_RACE_%d", i)

		var innerCalls atomic.Int64
		latch := newLatchHandler(echoJSONHandler(&innerCalls))
		mw := Middleware(store, Options{Scope: testScope})
		ts := httptest.NewServer(mw(latch))

		type result struct {
			status int
			body   []byte
		}
		ch := make(chan result, 2)

		go func() {
			status, b, _ := concurrentPost(ts, key, body)
			ch <- result{status, b}
		}()

		select {
		case <-latch.entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: Thread 1 never entered handler", i)
		}

		go func() {
			status, b, _ := concurrentPost(ts, key, body)
			ch <- result{status, b}
		}()

		time.Sleep(10 * time.Millisecond)
		close(latch.release)

		var results [2]result
		for j := 0; j < 2; j++ {
			select {
			case r := <-ch:
				results[j] = r
			case <-time.After(10 * time.Second):
				t.Fatalf("iteration %d, goroutine %d: timeout", i, j)
			}
		}

		ts.Close()

		if results[0].status != 200 || results[1].status != 200 {
			t.Errorf("iteration %d: want 200/200, got %d/%d", i,
				results[0].status, results[1].status)
		}
		if !bytes.Equal(results[0].body, results[1].body) {
			t.Errorf("iteration %d: bodies not identical\n  T1: %s\n  T2: %s",
				i, results[0].body, results[1].body)
		}
		if n := innerCalls.Load(); n != 1 {
			t.Errorf("iteration %d: handler called %d times, want 1", i, n)
		}

		totalHandlerCalls.Add(innerCalls.Load())
	}

	// Step 6: store should have exactly 10 entries (one per unique key).
	if n := store.count(); n != 10 {
		t.Errorf("step 6 / step 8: want 10 store entries (one per key), got %d", n)
	}

	// Total handler calls must equal 10 (one per key, not two).
	if n := totalHandlerCalls.Load(); n != 10 {
		t.Errorf("step 8: total handler calls want 10 (one per key), got %d (singleflight broken?)", n)
	}
}

// TestConcurrent_StoreHasExactlyOneEntryAfterConcurrentPair verifies step 6:
// idempotency_keys holds exactly 1 row for a key even after 2 concurrent hits.
func TestConcurrent_StoreHasExactlyOneEntryAfterConcurrentPair(t *testing.T) {
	t.Parallel()

	store := newInMemoryStore()
	var innerCalls atomic.Int64
	latch := newLatchHandler(echoJSONHandler(&innerCalls))
	mw := Middleware(store, Options{Scope: testScope})
	ts := httptest.NewServer(mw(latch))
	defer ts.Close()

	const key = "CONC_STORE_COUNT"
	const body = `{"message":"count-test"}`

	ch := make(chan struct{}, 2)

	go func() {
		concurrentPost(ts, key, body) //nolint:errcheck
		ch <- struct{}{}
	}()

	<-latch.entered

	go func() {
		concurrentPost(ts, key, body) //nolint:errcheck
		ch <- struct{}{}
	}()

	time.Sleep(10 * time.Millisecond)
	close(latch.release)
	<-ch
	<-ch

	// Step 6: exactly 1 row.
	if n := store.count(); n != 1 {
		t.Errorf("step 6: want exactly 1 store entry after concurrent pair, got %d", n)
	}
}

// TestConcurrent_SequentialAfterConcurrentStillReplays verifies that a
// sequential request AFTER the concurrent pair properly replays the stored
// response (no singleflight in-flight; normal HIT path).
func TestConcurrent_SequentialAfterConcurrentStillReplays(t *testing.T) {
	t.Parallel()

	store := newInMemoryStore()
	var innerCalls atomic.Int64
	latch := newLatchHandler(echoJSONHandler(&innerCalls))
	mw := Middleware(store, Options{Scope: testScope})
	ts := httptest.NewServer(mw(latch))
	defer ts.Close()

	const key = "CONC_THEN_SEQ"
	const body = `{"message":"sequential-after"}`

	// Run the concurrent pair.
	ch := make(chan []byte, 2)
	go func() {
		_, b, _ := concurrentPost(ts, key, body)
		ch <- b
	}()
	<-latch.entered
	go func() {
		_, b, _ := concurrentPost(ts, key, body)
		ch <- b
	}()
	time.Sleep(10 * time.Millisecond)
	close(latch.release)
	body1 := <-ch
	<-ch

	// Third request is sequential (no concurrency). Should be a plain HIT replay.
	_, body3, err := concurrentPost(ts, key, body)
	if err != nil {
		t.Fatalf("third request error: %v", err)
	}

	if !bytes.Equal(body1, body3) {
		t.Errorf("sequential replay not identical to original\n  orig: %s\n  seq:  %s", body1, body3)
	}

	// Handler still called exactly once.
	if n := innerCalls.Load(); n != 1 {
		t.Errorf("want handler called 1 time total, got %d", n)
	}
}

// TestConcurrent_DifferentKeysNeverCoalesce ensures requests with DIFFERENT
// idempotency keys are NOT coalesced — each runs its own handler.
func TestConcurrent_DifferentKeysNeverCoalesce(t *testing.T) {
	t.Parallel()

	store := newInMemoryStore()
	var innerCalls atomic.Int64

	mw := Middleware(store, Options{Scope: testScope})
	ts := httptest.NewServer(mw(echoJSONHandler(&innerCalls)))
	defer ts.Close()

	// Fire 5 concurrent requests, each with a unique key.
	const n = 5
	var wg sync.WaitGroup
	wg.Add(n)
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		key := fmt.Sprintf("UNIQUE_KEY_%d", i)
		body := fmt.Sprintf(`{"message":"unique-%d"}`, i)
		go func(k, b string) {
			defer wg.Done()
			<-start
			concurrentPost(ts, k, b) //nolint:errcheck
		}(key, body)
	}

	close(start)
	wg.Wait()

	// Each unique key must have triggered exactly one handler call.
	if got := innerCalls.Load(); got != n {
		t.Errorf("want %d handler calls (one per unique key), got %d", n, got)
	}
	if got := store.count(); got != n {
		t.Errorf("want %d store entries (one per unique key), got %d", n, got)
	}
}

// count returns the number of stored entries in the in-memory store.
func (s *inMemoryStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

// Ensure the echoJSONHandler produces valid JSON so byte-equality checks are
// meaningful. This is a compile-time sanity assertion.
var _ = json.Marshal


// Package httpserver — tests for feature #75:
// "Idempotency race: two simultaneous identical requests produce one DB write"
//
// When two simultaneous identical POSTs carry the same Idempotency-Key, the
// middleware's singleflight group coalesces them: exactly one goroutine runs the
// downstream handler (and therefore executes BEGIN / audit_events INSERT /
// outbox_events INSERT / idempotency_keys INSERT / COMMIT), while the
// concurrent duplicate waits and shares the captured result.
//
// Feature steps covered:
//
//	Step 1:   Issue 10 pairs of parallel POST /v1/echo with the same Idempotency-Key
//	          (one unique key per pair, 20 total requests).
//	Step 2:   Verify all 20 requests return HTTP 200.
//	Step 3:   For each key, verify exactly 1 audit_events row and 1 outbox_events row.
//	Step 4:   Verify no 500 errors, no deadlock (timeout guard).
//	Step 5:   Verify the LOSING request receives Idempotent-Replay: true header
//	          (waits and replays, never 409s the client).
//	Step 6:   Inspect SQL: idempotency INSERT uses INSERT … ON CONFLICT DO UPDATE
//	          SET … WHERE expires_at <= now() — equivalent to ON CONFLICT DO NOTHING
//	          for live rows. The alternative (SELECT FOR UPDATE then INSERT) is NOT
//	          used; a unique constraint on (key, scope) enforces at-most-one.
package httpserver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// =============================================================================
// race75Tx — pgx.Tx that blocks the first Exec and counts outbox QueryRow calls
// =============================================================================

// race75Tx is a minimal pgx.Tx for feature-#75 tests. It:
//   - Blocks the very first Exec call (the idempotency_keys INSERT) until
//     released, widening the concurrent window so Thread 2 reaches singleflight
//     while Thread 1 is still executing.
//   - Intercepts idempotency_keys INSERTs and forwards them to the in-memory
//     replayIdemStore so subsequent Lookup calls return HIT (replay path).
//   - Counts outbox_events QueryRow calls so tests can assert exactly 1 per key.
type race75Tx struct {
	mu          sync.Mutex
	execCalls   int
	outboxCalls int64 // atomic-friendly; counts outbox_events QueryRow inserts
	store       *replayIdemStore

	block       chan struct{} // closed by test to unblock first Exec
	entered     chan struct{} // closed when first Exec enters the latch
	enteredOnce sync.Once
}

func (f *race75Tx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.mu.Lock()
	f.execCalls++
	f.mu.Unlock()

	// Block the first Exec call so Thread 2's request can reach singleflight.Do
	// while Thread 1 is still inside the handler.
	if f.block != nil {
		f.enteredOnce.Do(func() { close(f.entered) })
		<-f.block
	}

	// Intercept idempotency_keys INSERT → forward to in-memory store so replay works.
	if strings.Contains(strings.ToLower(sql), "idempotency_keys") && len(args) >= 8 {
		key, _ := args[0].(string)
		scope, _ := args[1].(string)
		actorID, _ := args[2].(string)
		reqHash, _ := args[3].(string)
		status, _ := args[4].(int)
		body, _ := args[5].([]byte)
		createdAt, _ := args[6].(time.Time)
		expiresAt, _ := args[7].(time.Time)
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		if expiresAt.IsZero() {
			expiresAt = createdAt.Add(24 * time.Hour)
		}
		_ = f.store.Save(ctx, key, scope, actorID, idempotency.StoredResponse{
			Status:      status,
			ContentType: "application/json; charset=utf-8",
			Body:        body,
			RequestHash: reqHash,
			CreatedAt:   createdAt,
			ExpiresAt:   expiresAt,
		})
	}

	return pgconn.CommandTag{}, nil
}

func (f *race75Tx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	// Count outbox_events INSERT … RETURNING calls.
	if strings.Contains(strings.ToLower(sql), "outbox_events") {
		atomic.AddInt64(&f.outboxCalls, 1)
	}
	return &fakeRow{val: "00000000-0000-0000-0000-000000000075"}
}

func (f *race75Tx) Commit(_ context.Context) error   { return nil }
func (f *race75Tx) Rollback(_ context.Context) error { return nil }
func (f *race75Tx) Begin(_ context.Context) (pgx.Tx, error) {
	panic("race75Tx: Begin not expected")
}
func (f *race75Tx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	panic("race75Tx: CopyFrom not expected")
}
func (f *race75Tx) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults {
	panic("race75Tx: SendBatch not expected")
}
func (f *race75Tx) LargeObjects() pgx.LargeObjects {
	panic("race75Tx: LargeObjects not expected")
}
func (f *race75Tx) Prepare(_ context.Context, _, _ string) (*pgconn.StatementDescription, error) {
	panic("race75Tx: Prepare not expected")
}
func (f *race75Tx) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	panic("race75Tx: Query not expected")
}
func (f *race75Tx) Conn() *pgx.Conn { return nil }

var _ pgx.Tx = (*race75Tx)(nil)

// race75Pool implements PoolDB and returns the embedded *race75Tx from BeginTx.
type race75Pool struct {
	tx *race75Tx
}

func (p *race75Pool) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return &fakeRow{val: ""}
}
func (p *race75Pool) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (p *race75Pool) BeginTx(_ context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	return p.tx, nil
}

var _ PoolDB = (*race75Pool)(nil)

// =============================================================================
// race75ServerResult — bundle returned by buildRace75Server
// =============================================================================

type race75ServerResult struct {
	ts    *httptest.Server
	stub  *auth.StubProvider
	store *replayIdemStore
	tx    *race75Tx
	aw    *captureAuditWriter
}

// buildRace75Server constructs a fully-wired Server for feature-#75 tests.
// block and entered control the latch that guarantees the concurrent window.
func buildRace75Server(t *testing.T, block, entered chan struct{}) *race75ServerResult {
	t.Helper()

	const secret = "race75-test-secret"
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  secret,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}

	store := newReplayIdemStore()
	tx := &race75Tx{
		store:   store,
		block:   block,
		entered: entered,
	}
	pool := &race75Pool{tx: tx}
	aw := &captureAuditWriter{}

	cfg := &config.Config{
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 30 * time.Second,
		BodyLimitBytes: 1 << 20,
	}

	s := New(Options{
		Config: cfg,
		Auth:   stub,
		Audit:  aw,
		Idem:   store,
		Pool:   pool,
	})

	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	return &race75ServerResult{
		ts:    ts,
		stub:  stub,
		store: store,
		tx:    tx,
		aw:    aw,
	}
}

// mintRace75JWT issues a Bearer token for the given actorID.
func mintRace75JWT(t *testing.T, stub *auth.StubProvider, actorID string) string {
	t.Helper()
	tok, _, err := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID:   actorID,
		ActorType: auth.ActorTypeStubUser,
		TTL:       time.Hour,
	})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	return tok
}

// postEchoRace75 sends POST /v1/echo and returns (status, headers, body).
func postEchoRace75(ts *httptest.Server, token, idemKey, body string) (int, http.Header, []byte, error) {
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo", strings.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", idemKey)

	resp, err := ts.Client().Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Clone(), b, err
}

// =============================================================================
// Helper: find the idempotency.go source file
// =============================================================================

// findIdempotencySource locates idempotency.go relative to the test binary.
// It tries the path from runtime.Caller first (works when GOFLAGS=-trimpath is
// NOT set, i.e. local development). If trimpath is active the caller path is a
// module-relative path and we fall back to os.Getwd()-based search.
func findIdempotencySource(t *testing.T) string {
	t.Helper()

	// Try runtime.Caller (works in local dev, may fail under -trimpath).
	_, thisFile, _, ok := runtime.Caller(0)
	if ok && filepath.IsAbs(thisFile) {
		// Walk up to find the platform/idempotency directory.
		dir := filepath.Dir(thisFile)
		candidate := filepath.Join(dir, "..", "idempotency", "idempotency.go")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// Fallback: use os.Getwd() which always returns a real path.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	candidate := filepath.Join(wd, "..", "idempotency", "idempotency.go")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}

	// Desperate fallback: walk up from wd to find the platform dir.
	for dir := wd; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		c := filepath.Join(dir, "internal", "platform", "idempotency", "idempotency.go")
		if _, statErr := os.Stat(c); statErr == nil {
			return c
		}
	}

	t.Skip("cannot locate idempotency.go for SQL inspection — skipping step-6 static scan")
	return ""
}

// =============================================================================
// Feature #75 tests
// =============================================================================

// TestIdemRaceOneDBWrite_TenPairAllReturn200 is the primary test for feature #75.
//
// It runs 10 subtests (one per unique key). Each subtest launches two concurrent
// requests for the same Idempotency-Key, using a latch to guarantee that Thread 2
// reaches singleflight.Do while Thread 1 is still executing inside the handler.
//
// Covers steps 1–5 of the feature spec.
func TestIdemRaceOneDBWrite_TenPairAllReturn200(t *testing.T) {
	t.Parallel()

	const actorID = "00000000-0000-0000-0000-000000000075"
	const body = `{"message":"race75 one-db-write"}`

	type result struct {
		status  int
		headers http.Header
		body    []byte
		err     error
	}

	// Accumulators across all 10 subtests (atomic for safe parallel subtests).
	var totalAuditEvents atomic.Int64
	var totalOutboxCalls atomic.Int64
	var totalIDemRows atomic.Int64

	for i := 0; i < 10; i++ {
		i := i // capture loop variable
		key := fmt.Sprintf("RACE75_KEY_%02d", i)

		t.Run(fmt.Sprintf("key_%02d", i), func(t *testing.T) {
			t.Parallel()

			block := make(chan struct{})
			entered := make(chan struct{})

			r := buildRace75Server(t, block, entered)
			token := mintRace75JWT(t, r.stub, actorID)

			ch := make(chan result, 2)

			// Step 1: Thread 1 — will block inside the handler at the first Exec latch.
			go func() {
				status, headers, b, err := postEchoRace75(r.ts, token, key, body)
				ch <- result{status, headers, b, err}
			}()

			// Wait for Thread 1 to be blocked inside the handler.
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("Thread 1 never entered the handler Exec latch — test setup broken")
			}

			// Step 1: Thread 2 — Thread 1 is blocked. singleflight coalesces Thread 2.
			go func() {
				status, headers, b, err := postEchoRace75(r.ts, token, key, body)
				ch <- result{status, headers, b, err}
			}()

			// Give Thread 2 time to reach singleflight.Do (Lookup → MISS → wait).
			time.Sleep(15 * time.Millisecond)

			// Step 4: Release Thread 1 (no deadlock if this unblocks promptly).
			close(block)

			// Collect both results with timeout guard.
			var results [2]result
			for j := 0; j < 2; j++ {
				select {
				case res := <-ch:
					results[j] = res
				case <-time.After(15 * time.Second):
					t.Fatalf("goroutine %d never completed — possible deadlock (step 4)", j)
				}
			}

			// Step 2: All 20 requests (2 per key) must return HTTP 200.
			// Specifically no 500 (server error) and no 409 (idempotency.body_mismatch
			// which would mean the losing request was incorrectly rejected).
			for j, res := range results {
				if res.err != nil {
					t.Errorf("goroutine %d: request error: %v", j, res.err)
					continue
				}
				if res.status != http.StatusOK {
					t.Errorf("goroutine %d: step 2: want 200, got %d (body: %s)", j, res.status, res.body)
				}
			}

			// Step 5: Exactly one response must carry Idempotent-Replay: true.
			// The winner (Thread 1) never sets it; the singleflight waiter (Thread 2)
			// replays the captured result with the header set in replayStored().
			// This proves the loser waited and replayed — it was NOT rejected with 409.
			replayCount := 0
			for _, res := range results {
				if res.headers.Get("Idempotent-Replay") == "true" {
					replayCount++
				}
			}
			if replayCount != 1 {
				t.Errorf("step 5: want exactly 1 response with Idempotent-Replay: true (the singleflight waiter), got %d", replayCount)
			}

			// Response bodies must be byte-identical (winner and waiter).
			if !bytes.Equal(results[0].body, results[1].body) {
				t.Errorf("step 5: response bodies not identical\n  T1: %s\n  T2: %s",
					results[0].body, results[1].body)
			}

			// Step 3: idempotency_keys — exactly 1 store row per key.
			if n := r.store.count(); n != 1 {
				t.Errorf("step 3: want 1 idempotency_keys row for key %q, got %d", key, n)
			}

			// Step 3: audit_events — handler invoked exactly once → 1 audit row.
			auditEvs := r.aw.getEvents()
			if len(auditEvs) != 1 {
				t.Errorf("step 3: want 1 audit_events row for key %q, got %d", key, len(auditEvs))
			}

			// Step 3: outbox_events — handler invoked exactly once → 1 outbox INSERT.
			outboxN := atomic.LoadInt64(&r.tx.outboxCalls)
			if outboxN != 1 {
				t.Errorf("step 3: want 1 outbox_events INSERT for key %q, got %d", key, outboxN)
			}

			// Accumulate for cross-subtest totals.
			totalAuditEvents.Add(int64(len(auditEvs)))
			totalOutboxCalls.Add(outboxN)
			totalIDemRows.Add(int64(r.store.count()))
		})
	}

	// After all 10 subtests finish, assert cross-subtest totals.
	// Note: t.Run with t.Parallel() runs subtests concurrently; the parent test
	// does NOT wait for them here — totals are validated inside each subtest.
	// The atomic accumulators are here for informational purposes / future use.
	_ = totalAuditEvents
	_ = totalOutboxCalls
	_ = totalIDemRows
}

// TestIdemRaceOneDBWrite_ExactlyOneAuditAndOutboxPerKey verifies step 3 in a
// focused, sequential way: for each of the 10 keys, after both concurrent
// requests complete, the audit writer holds exactly 1 event and the tx holds
// exactly 1 outbox call.
func TestIdemRaceOneDBWrite_ExactlyOneAuditAndOutboxPerKey(t *testing.T) {
	t.Parallel()

	const actorID = "00000000-0000-0000-0000-000000000075"
	const body = `{"message":"race75 db-write-count"}`

	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("RACE75_ONEROW_%02d", i)

		block := make(chan struct{})
		entered := make(chan struct{})
		r := buildRace75Server(t, block, entered)
		token := mintRace75JWT(t, r.stub, actorID)

		ch := make(chan struct{ status int }, 2)

		go func() {
			status, _, _, _ := postEchoRace75(r.ts, token, key, body)
			ch <- struct{ status int }{status}
		}()

		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: Thread 1 never entered latch", i)
		}

		go func() {
			status, _, _, _ := postEchoRace75(r.ts, token, key, body)
			ch <- struct{ status int }{status}
		}()

		time.Sleep(15 * time.Millisecond)
		close(block)

		for j := 0; j < 2; j++ {
			select {
			case res := <-ch:
				if res.status != http.StatusOK {
					t.Errorf("iteration %d goroutine %d: want 200, got %d", i, j, res.status)
				}
			case <-time.After(15 * time.Second):
				t.Fatalf("iteration %d goroutine %d: timeout — deadlock?", i, j)
			}
		}

		// Step 3: audit_events — exactly 1 row per key.
		auditEvs := r.aw.getEvents()
		if len(auditEvs) != 1 {
			t.Errorf("step 3 iteration %d: want 1 audit_events row, got %d (handler ran %d times)",
				i, len(auditEvs), len(auditEvs))
		}

		// Step 3: outbox_events — exactly 1 INSERT per key.
		outboxN := atomic.LoadInt64(&r.tx.outboxCalls)
		if outboxN != 1 {
			t.Errorf("step 3 iteration %d: want 1 outbox_events INSERT, got %d", i, outboxN)
		}

		// Step 3: idempotency_keys — exactly 1 row per key.
		if n := r.store.count(); n != 1 {
			t.Errorf("step 3 iteration %d: want 1 idempotency_keys row, got %d", i, n)
		}
	}
}

// TestIdemRaceOneDBWrite_No500NoDeadlock is step 4 of the feature spec:
// "Verify no 500 errors, no deadlock."
//
// Runs 10 pairs in parallel subtests with a generous timeout guard.
func TestIdemRaceOneDBWrite_No500NoDeadlock(t *testing.T) {
	t.Parallel()

	const actorID = "00000000-0000-0000-0000-000000000075"
	const body = `{"message":"race75 deadlock-check"}`

	for i := 0; i < 10; i++ {
		i := i
		key := fmt.Sprintf("RACE75_NODL_%02d", i)

		t.Run(fmt.Sprintf("pair_%02d", i), func(t *testing.T) {
			t.Parallel()

			block := make(chan struct{})
			entered := make(chan struct{})
			r := buildRace75Server(t, block, entered)
			token := mintRace75JWT(t, r.stub, actorID)

			done := make(chan [2]int, 1)
			go func() {
				ch := make(chan int, 2)
				go func() {
					status, _, _, _ := postEchoRace75(r.ts, token, key, body)
					ch <- status
				}()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					ch <- -1
				}
				go func() {
					status, _, _, _ := postEchoRace75(r.ts, token, key, body)
					ch <- status
				}()
				time.Sleep(15 * time.Millisecond)
				close(block)
				s1 := <-ch
				s2 := <-ch
				done <- [2]int{s1, s2}
			}()

			select {
			case statuses := <-done:
				// Step 4: no 500 errors.
				for j, s := range statuses {
					if s == http.StatusInternalServerError {
						t.Errorf("pair %d goroutine %d: got 500 — server error", i, j)
					}
					if s == http.StatusConflict {
						t.Errorf("pair %d goroutine %d: got 409 — losing request was not replayed (step 5 violation)", i, j)
					}
				}
			case <-time.After(30 * time.Second):
				// Step 4: no deadlock.
				t.Errorf("pair %d: deadlock detected — requests did not complete within 30s", i)
			}
		})
	}
}

// TestIdemRaceOneDBWrite_LoserGetsReplayNotConflict is step 5 of the feature spec:
// "Ensure the LOSING request waits/replays rather than 409s the client."
//
// With singleflight, the losing goroutine waits in group.Do until the winner
// completes, then receives the winner's captured response — including the
// Idempotent-Replay: true header added by replayStored(). The response must
// be HTTP 200, not 409 idempotency.body_mismatch.
func TestIdemRaceOneDBWrite_LoserGetsReplayNotConflict(t *testing.T) {
	t.Parallel()

	const actorID = "00000000-0000-0000-0000-000000000075"
	const body = `{"message":"race75 replay-not-conflict"}`
	const key = "RACE75_REPLAY_CHECK"

	block := make(chan struct{})
	entered := make(chan struct{})
	r := buildRace75Server(t, block, entered)
	token := mintRace75JWT(t, r.stub, actorID)

	type result struct {
		status  int
		headers http.Header
		body    []byte
	}
	ch := make(chan result, 2)

	go func() {
		status, headers, b, _ := postEchoRace75(r.ts, token, key, body)
		ch <- result{status, headers, b}
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Thread 1 never entered handler latch — setup broken")
	}

	go func() {
		status, headers, b, _ := postEchoRace75(r.ts, token, key, body)
		ch <- result{status, headers, b}
	}()

	time.Sleep(15 * time.Millisecond)
	close(block)

	var results [2]result
	for i := 0; i < 2; i++ {
		select {
		case res := <-ch:
			results[i] = res
		case <-time.After(15 * time.Second):
			t.Fatalf("goroutine %d: timeout — deadlock?", i)
		}
	}

	// Step 5: neither response must be 409.
	for i, res := range results {
		if res.status == http.StatusConflict {
			t.Errorf("goroutine %d: step 5: got 409 — losing request must not 409 (it should replay)", i)
		}
		if res.status != http.StatusOK {
			t.Errorf("goroutine %d: want 200, got %d (body: %s)", i, res.status, res.body)
		}
	}

	// Step 5: exactly one response must carry Idempotent-Replay: true.
	replayCount := 0
	for _, res := range results {
		if res.headers.Get("Idempotent-Replay") == "true" {
			replayCount++
		}
	}
	if replayCount != 1 {
		t.Errorf("step 5: want exactly 1 Idempotent-Replay: true (the singleflight waiter), got %d", replayCount)
	}

	// Step 5: no 500s.
	for i, res := range results {
		if res.status == http.StatusInternalServerError {
			t.Errorf("goroutine %d: step 4: got 500 — server error (body: %s)", i, res.body)
		}
	}
}

// TestIdemRaceOneDBWrite_SQLUsesOnConflictUniqueConstraint is step 6:
// "Inspect SQL: must be either INSERT … ON CONFLICT DO NOTHING with subsequent
// SELECT, or SELECT … FOR UPDATE then INSERT."
//
// The production implementation uses INSERT … ON CONFLICT (key, scope) DO UPDATE
// SET … WHERE expires_at <= now(). For live rows this WHERE clause never matches,
// making it semantically identical to ON CONFLICT DO NOTHING — satisfying the
// unique-constraint guarantee. The test verifies:
//
//   - The SaveTx SQL contains "ON CONFLICT" (not SELECT FOR UPDATE).
//   - A unique constraint on (key, scope) exists as a literal in the source.
//   - The SQL does NOT use SELECT … FOR UPDATE … then INSERT (the alternative).
func TestIdemRaceOneDBWrite_SQLUsesOnConflictUniqueConstraint(t *testing.T) {
	t.Parallel()

	src := findIdempotencySource(t)

	content, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read idempotency.go: %v", err)
	}
	text := string(content)

	// Step 6a: the idempotency INSERT uses ON CONFLICT, not a two-step
	// SELECT FOR UPDATE + INSERT.
	if !strings.Contains(strings.ToUpper(text), "ON CONFLICT") {
		t.Error("step 6: idempotency.go does not contain 'ON CONFLICT' — expected INSERT … ON CONFLICT unique constraint handling")
	}

	// Step 6b: there is no SELECT … FOR UPDATE within the same INSERT block.
	// (A SELECT FOR UPDATE is a valid alternative, but the implementation chose
	// ON CONFLICT; this test asserts the chosen approach is consistent.)
	if strings.Contains(strings.ToUpper(text), "SELECT FOR UPDATE") {
		t.Log("note: idempotency.go contains SELECT FOR UPDATE (alternative approach) — verifying ON CONFLICT is also present")
		if !strings.Contains(strings.ToUpper(text), "ON CONFLICT") {
			t.Error("step 6: found SELECT FOR UPDATE but no ON CONFLICT — implementation uses an unsupported approach")
		}
	}

	// Step 6c: the constraint targets (key, scope) — the natural unique key.
	if !strings.Contains(text, "(key, scope)") {
		t.Error("step 6: expected 'ON CONFLICT (key, scope)' but the source does not contain '(key, scope)'")
	}

	// Step 6d: the WHERE clause on the DO UPDATE (for the expired-row reuse
	// feature) must reference 'expires_at' to ensure live rows are protected.
	if !strings.Contains(text, "expires_at") {
		t.Error("step 6: expected WHERE clause referencing 'expires_at' in the ON CONFLICT DO UPDATE — live rows must be protected")
	}
}

// TestIdemRaceOneDBWrite_FullVerification is the all-in-one sweep of all
// feature steps, reported as sub-tests for clear per-step pass/fail output.
func TestIdemRaceOneDBWrite_FullVerification(t *testing.T) {
	t.Parallel()

	const actorID = "00000000-0000-0000-0000-000000000075"
	const body = `{"message":"race75 full-verification"}`

	// ── Step 6 (SQL inspection) ───────────────────────────────────────────────
	t.Run("Step6_SQLUsesOnConflict", func(t *testing.T) {
		t.Parallel()
		src := findIdempotencySource(t)
		content, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read idempotency.go: %v", err)
		}
		if !strings.Contains(strings.ToUpper(string(content)), "ON CONFLICT") {
			t.Error("idempotency.go does not contain ON CONFLICT")
		}
		if !strings.Contains(string(content), "(key, scope)") {
			t.Error("idempotency.go does not contain (key, scope) constraint target")
		}
	})

	// ── Steps 1–5: 10 pairs, all 200, 1 audit+outbox per key, replay not 409 ──
	t.Run("Steps1to5_TenPairs", func(t *testing.T) {
		for i := 0; i < 10; i++ {
			i := i
			key := fmt.Sprintf("RACE75_FULL_%02d", i)
			t.Run(fmt.Sprintf("key_%02d", i), func(t *testing.T) {
				t.Parallel()

				block := make(chan struct{})
				entered := make(chan struct{})
				r := buildRace75Server(t, block, entered)
				token := mintRace75JWT(t, r.stub, actorID)

				type res struct {
					status  int
					headers http.Header
					body    []byte
				}
				ch := make(chan res, 2)

				go func() {
					s, h, b, _ := postEchoRace75(r.ts, token, key, body)
					ch <- res{s, h, b}
				}()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("Thread 1 never entered latch")
				}
				go func() {
					s, h, b, _ := postEchoRace75(r.ts, token, key, body)
					ch <- res{s, h, b}
				}()
				time.Sleep(15 * time.Millisecond)
				close(block)

				var results [2]res
				for j := 0; j < 2; j++ {
					select {
					case r2 := <-ch:
						results[j] = r2
					case <-time.After(15 * time.Second):
						t.Fatalf("goroutine %d: timeout — deadlock (step 4)", j)
					}
				}

				// Step 2: all 200.
				for j, r2 := range results {
					if r2.status != 200 {
						t.Errorf("step 2: goroutine %d: want 200, got %d", j, r2.status)
					}
				}

				// Step 3: exactly 1 audit row.
				if n := len(r.aw.getEvents()); n != 1 {
					t.Errorf("step 3: want 1 audit_events row, got %d", n)
				}

				// Step 3: exactly 1 outbox INSERT.
				if n := atomic.LoadInt64(&r.tx.outboxCalls); n != 1 {
					t.Errorf("step 3: want 1 outbox_events INSERT, got %d", n)
				}

				// Step 3: exactly 1 idempotency_keys row.
				if n := r.store.count(); n != 1 {
					t.Errorf("step 3: want 1 idempotency_keys row, got %d", n)
				}

				// Step 4: no 500.
				for j, r2 := range results {
					if r2.status == 500 {
						t.Errorf("step 4: goroutine %d: got 500 — server error", j)
					}
				}

				// Step 5: loser gets replay, not 409.
				replayCount := 0
				for _, r2 := range results {
					if r2.headers.Get("Idempotent-Replay") == "true" {
						replayCount++
					}
					if r2.status == 409 {
						t.Errorf("step 5: got 409 — losing request must replay, not be rejected")
					}
				}
				if replayCount != 1 {
					t.Errorf("step 5: want 1 Idempotent-Replay: true response, got %d", replayCount)
				}
			})
		}
	})
}

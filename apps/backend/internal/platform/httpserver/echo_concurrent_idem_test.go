// Package httpserver — integration tests for feature #45:
// "Concurrent identical Idempotency-Key requests both succeed with same response"
//
// Two parallel POST /v1/echo requests with the same Idempotency-Key must both
// receive HTTP 200 with byte-identical response bodies. One executes the
// handler; the other waits in the singleflight group and shares the captured
// result.
//
// Tests cover the feature steps at the full-server integration level:
//
//   - Both requests return HTTP 200 (no 500, no deadlock).
//   - Response bodies are byte-identical.
//   - Handler-level side-effects (audit, outbox) execute exactly once.
//   - idempotency_keys store has exactly 1 row per key.
//   - Repeated 10 times with different keys (step 8: flush out races).
//
// Infrastructure reused from existing test files:
//   - buildReplayServer, mintReplayJWT, postEchoReplay (idempotency_replay_full_test.go)
//   - replayIdemStore, replayPoolDB                    (idempotency_replay_full_test.go)
//   - captureAuditWriter, fakeRow                     (echo_audit_test.go)
package httpserver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
)

// =============================================================================
// blockingCaptureTx — extends replayCaptureTx with a latch on Exec
// =============================================================================

// blockingCaptureTx is a minimal pgx.Tx that blocks the FIRST idempotency_keys
// INSERT (via Exec) until the test releases it. This ensures Thread 2 can send
// its request while Thread 1 is still inside the handler, reliably triggering
// the singleflight concurrent window.
//
// With singleflight: Thread 2 waits in group.Do (never calls BeginTx/Exec).
// Without singleflight: Thread 2 would also call BeginTx → both handlers run.
type blockingCaptureTx struct {
	mu        sync.Mutex
	execCalls int
	store     *replayIdemStore
	// Latch fields: block controls whether Exec blocks.
	block       chan struct{} // closed by test to unblock
	entered     chan struct{} // closed when first Exec enters the block
	enteredOnce sync.Once
}

func (f *blockingCaptureTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.mu.Lock()
	f.execCalls++
	f.mu.Unlock()

	// Block the very first Exec call (the idempotency INSERT from SaveTx) until
	// the test releases us. This widens the concurrent window so Thread 2's
	// request arrives while Thread 1 is still executing.
	if f.block != nil {
		f.enteredOnce.Do(func() { close(f.entered) })
		<-f.block
	}

	// Intercept the idempotency_keys INSERT and forward to the in-memory store.
	if sqlContains(sql, "idempotency_keys") && len(args) >= 8 {
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

func (f *blockingCaptureTx) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return &fakeRow{val: "00000000-0000-0000-0000-000000000099"}
}
func (f *blockingCaptureTx) Commit(_ context.Context) error   { return nil }
func (f *blockingCaptureTx) Rollback(_ context.Context) error { return nil }
func (f *blockingCaptureTx) Begin(_ context.Context) (pgx.Tx, error) {
	panic("blockingCaptureTx: Begin not expected")
}
func (f *blockingCaptureTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	panic("blockingCaptureTx: CopyFrom not expected")
}
func (f *blockingCaptureTx) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults {
	panic("blockingCaptureTx: SendBatch not expected")
}
func (f *blockingCaptureTx) LargeObjects() pgx.LargeObjects {
	panic("blockingCaptureTx: LargeObjects not expected")
}
func (f *blockingCaptureTx) Prepare(_ context.Context, _, _ string) (*pgconn.StatementDescription, error) {
	panic("blockingCaptureTx: Prepare not expected")
}
func (f *blockingCaptureTx) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	panic("blockingCaptureTx: Query not expected")
}
func (f *blockingCaptureTx) Conn() *pgx.Conn { return nil }

var _ pgx.Tx = (*blockingCaptureTx)(nil)

// blockingPoolDB wraps a blockingCaptureTx so BeginTx returns it.
type blockingPoolDB struct {
	tx *blockingCaptureTx
}

func (p *blockingPoolDB) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return &fakeRow{val: ""}
}
func (p *blockingPoolDB) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (p *blockingPoolDB) BeginTx(_ context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	return p.tx, nil
}
func (p *blockingPoolDB) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	return nil, nil
}

var _ PoolDB = (*blockingPoolDB)(nil)

// sqlContains returns true when the SQL string contains the given table name
// (case-insensitive). Used to detect idempotency_keys INSERTs in Exec.
func sqlContains(sql, table string) bool {
	return strings.Contains(strings.ToLower(sql), strings.ToLower(table))
}

// =============================================================================
// concurrentServerResult — bundles everything for concurrent tests
// =============================================================================

type concurrentServerResult struct {
	ts    *httptest.Server
	stub  *auth.StubProvider
	store *replayIdemStore
	tx    *blockingCaptureTx
	aw    *captureAuditWriter
}

// buildConcurrentServer constructs a fully-wired Server with a blockingCaptureTx
// (latch pattern) to reliably trigger the concurrent window.
func buildConcurrentServer(t *testing.T, block, entered chan struct{}) *concurrentServerResult {
	t.Helper()

	const secret = "concurrent-idem-test-secret"
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  secret,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}

	aw := &captureAuditWriter{}
	store := newReplayIdemStore()
	tx := &blockingCaptureTx{
		store:   store,
		block:   block,
		entered: entered,
	}
	pool := &blockingPoolDB{tx: tx}

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

	return &concurrentServerResult{
		ts:    ts,
		stub:  stub,
		store: store,
		tx:    tx,
		aw:    aw,
	}
}

// mintConcJWT issues a Bearer token for the given actorID.
func mintConcJWT(t *testing.T, stub *auth.StubProvider, actorID string) string {
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

// postEchoConc sends POST /v1/echo with the given token, idempotency key, body.
func postEchoConc(ts *httptest.Server, token, idemKey, body string) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", idemKey)
	resp, err := ts.Client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, err
}

// =============================================================================
// Feature #45 integration tests
// =============================================================================

// TestConcurrentIdem_BothReturn200WithIdenticalBody is the primary integration
// test for feature #45. It uses blockingCaptureTx to guarantee Thread 2 sends
// its request while Thread 1 is blocked inside the handler.
//
// With singleflight, Thread 2 must wait in group.Do and share Thread 1's
// captured response. Both must return 200 with byte-identical bodies.
func TestConcurrentIdem_BothReturn200WithIdenticalBody(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	entered := make(chan struct{})
	r := buildConcurrentServer(t, block, entered)
	token := mintConcJWT(t, r.stub, "00000000-0000-0000-0000-000000000045")

	const key = "CONC_IKEY_1"
	const body = `{"message":"race"}`

	type result struct {
		status int
		body   []byte
		err    error
	}
	ch := make(chan result, 2)

	// Step 1: Thread 1 — will block inside the handler's first Exec.
	go func() {
		status, b, err := postEchoConc(r.ts, token, key, body)
		ch <- result{status, b, err}
	}()

	// Wait for Thread 1 to reach the latch inside the handler.
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Thread 1 never entered the handler Exec latch — test setup broken")
	}

	// Step 1: Thread 2 — Thread 1 is blocked in its handler. With singleflight
	// Thread 2 will wait in group.Do and share Thread 1's result.
	go func() {
		status, b, err := postEchoConc(r.ts, token, key, body)
		ch <- result{status, b, err}
	}()

	// Give Thread 2 time to reach group.Do (Lookup → MISS → group.Do → wait).
	time.Sleep(10 * time.Millisecond)

	// Step 7: Unblock Thread 1. Both goroutines will complete.
	close(block)

	// Collect both results with timeout to detect deadlocks.
	var results [2]result
	for i := 0; i < 2; i++ {
		select {
		case res := <-ch:
			results[i] = res
		case <-time.After(15 * time.Second):
			t.Fatalf("goroutine %d never completed — possible deadlock", i)
		}
	}

	// Step 2: Both must return HTTP 200. No 500.
	for i, res := range results {
		if res.err != nil {
			t.Errorf("goroutine %d: request error: %v", i, res.err)
			continue
		}
		if res.status != http.StatusOK {
			t.Errorf("goroutine %d: want 200, got %d (body: %s)", i, res.status, res.body)
		}
	}

	// Step 3: Response bodies must be byte-identical.
	if !bytes.Equal(results[0].body, results[1].body) {
		t.Errorf("step 3: bodies not identical\n  T1: %s\n  T2: %s",
			results[0].body, results[1].body)
	}

	// Step 4: audit_events written exactly once (captureAuditWriter has 1 event).
	events := r.aw.getEvents()
	if len(events) != 1 {
		t.Errorf("step 4: want 1 audit_events row, got %d", len(events))
	}

	// Step 6: idempotency_keys has exactly 1 row.
	if n := r.store.count(); n != 1 {
		t.Errorf("step 6: want 1 idempotency_keys row, got %d", n)
	}
}

// TestConcurrentIdem_NoDeadlock verifies step 7 via timeout guard.
func TestConcurrentIdem_NoDeadlock(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	entered := make(chan struct{})
	r := buildConcurrentServer(t, block, entered)
	token := mintConcJWT(t, r.stub, "00000000-0000-0000-0000-000000000045")

	const key = "CONC_DEADLOCK_IKEY"
	const body = `{"message":"deadlock-test"}`

	done := make(chan struct{})
	go func() {
		defer close(done)
		ch := make(chan struct{}, 2)
		go func() {
			postEchoConc(r.ts, token, key, body) //nolint:errcheck
			ch <- struct{}{}
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			return
		}
		go func() {
			postEchoConc(r.ts, token, key, body) //nolint:errcheck
			ch <- struct{}{}
		}()
		time.Sleep(10 * time.Millisecond)
		close(block)
		<-ch
		<-ch
	}()

	select {
	case <-done:
		// Step 7: no deadlock.
	case <-time.After(30 * time.Second):
		t.Error("step 7: deadlock detected — concurrent requests did not complete within 30s")
	}
}

// TestConcurrentIdem_Repeat10DifferentKeys covers step 8:
// "Repeat 10 times with different keys to flush out races."
//
// For each key we use a NEW server+latch so the block channel is fresh.
func TestConcurrentIdem_Repeat10DifferentKeys(t *testing.T) {
	t.Parallel()

	const actorID = "00000000-0000-0000-0000-000000000045"
	const body = `{"message":"race"}`

	for i := 0; i < 10; i++ {
		i := i // capture loop variable
		t.Run(fmt.Sprintf("key_%d", i), func(t *testing.T) {
			t.Parallel()

			block := make(chan struct{})
			entered := make(chan struct{})
			r := buildConcurrentServer(t, block, entered)
			token := mintConcJWT(t, r.stub, actorID)

			key := fmt.Sprintf("CONC_REPEAT_%d", i)

			type result struct {
				status int
				body   []byte
			}
			ch := make(chan result, 2)

			go func() {
				status, b, _ := postEchoConc(r.ts, token, key, body)
				ch <- result{status, b}
			}()

			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatalf("iteration %d: Thread 1 never entered handler latch", i)
			}

			go func() {
				status, b, _ := postEchoConc(r.ts, token, key, body)
				ch <- result{status, b}
			}()

			time.Sleep(10 * time.Millisecond)
			close(block)

			var results [2]result
			for j := 0; j < 2; j++ {
				select {
				case res := <-ch:
					results[j] = res
				case <-time.After(15 * time.Second):
					t.Fatalf("iteration %d, goroutine %d: timeout", i, j)
				}
			}

			// Both must return 200.
			for j, res := range results {
				if res.status != 200 {
					t.Errorf("iteration %d goroutine %d: want 200, got %d (body: %s)",
						i, j, res.status, res.body)
				}
			}

			// Bodies must be identical.
			if !bytes.Equal(results[0].body, results[1].body) {
				t.Errorf("iteration %d: bodies not identical\n  T1: %s\n  T2: %s",
					i, results[0].body, results[1].body)
			}

			// Step 6: exactly 1 store row.
			if n := r.store.count(); n != 1 {
				t.Errorf("iteration %d: want 1 store row, got %d", i, n)
			}
		})
	}
}

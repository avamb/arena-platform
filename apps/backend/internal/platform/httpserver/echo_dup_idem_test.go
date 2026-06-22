// Package httpserver — unit tests for feature #44:
// "Duplicate Idempotency-Key returns original response"
//
// Sending POST /v1/echo twice with identical Idempotency-Key and body returns
// the original response on the second call. The second call does NOT
// re-execute side effects (no new audit_events row, no new outbox_events row).
//
// Tests cover all 10 feature verification steps:
//
//   - Steps 1-2:  POST /v1/echo with Idempotency-Key: DUP_KEY_1 → HTTP 200.
//   - Step  2:    Capture response body (req1).
//   - Steps 4-5:  POST same key+body again → HTTP 200, body byte-for-byte identical to req1.
//   - Step  7:    Replay response carries Idempotent-Replay: true header (or similar marker).
//   - Step  8:    audit_events count = 1 (captureAuditWriter.getEvents() length).
//   - Step  9:    outbox_events count = 1 (dupCaptureTx.queryRowCalls == 1).
//   - Step 10:    Prometheus counter arena_idempotency_replays_total incremented by 1.
//
// Reuses from other *_test.go files in the same package:
//   - replayIdemStore, replayPoolDB  (idempotency_replay_full_test.go)
//   - captureAuditWriter, fakeRow    (echo_audit_test.go)
//
// dupCaptureTx in this file extends replayCaptureTx with a queryRowCalls
// counter so we can verify "outbox INSERT executed exactly once".
package httpserver

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
)

// =============================================================================
// dupCaptureTx — extends replayCaptureTx with outbox INSERT counting
// =============================================================================

// dupCaptureTx is a minimal pgx.Tx for feature #44 tests.
// It intercepts the idempotency INSERT (forwarding it to the in-memory store)
// and additionally counts QueryRow calls so we can assert "outbox written once".
// insertOutboxEcho calls tx.QueryRow exactly once per handler invocation; on a
// replay the handler never runs, so queryRowCalls stays at 0 for replay requests.
type dupCaptureTx struct {
	mu            sync.Mutex
	execCalls     int
	queryRowCalls int
	store         *replayIdemStore
}

func (f *dupCaptureTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.mu.Lock()
	f.execCalls++
	f.mu.Unlock()

	// Forward the idempotency_keys INSERT to the in-memory store so that the
	// second request gets a HIT from Lookup and is properly replayed.
	if strings.Contains(sql, "idempotency_keys") && len(args) >= 8 {
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

// QueryRow is called by insertOutboxEcho (once per real handler invocation).
// Counting it lets us verify that the outbox INSERT happened exactly once.
func (f *dupCaptureTx) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	f.mu.Lock()
	f.queryRowCalls++
	f.mu.Unlock()
	return &fakeRow{val: "00000000-0000-0000-0000-000000000099"}
}

func (f *dupCaptureTx) Commit(_ context.Context) error   { return nil }
func (f *dupCaptureTx) Rollback(_ context.Context) error { return nil }
func (f *dupCaptureTx) Begin(_ context.Context) (pgx.Tx, error) {
	panic("dupCaptureTx: Begin not expected in handleEcho path")
}
func (f *dupCaptureTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	panic("dupCaptureTx: CopyFrom not expected")
}
func (f *dupCaptureTx) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults {
	panic("dupCaptureTx: SendBatch not expected")
}
func (f *dupCaptureTx) LargeObjects() pgx.LargeObjects {
	panic("dupCaptureTx: LargeObjects not expected")
}
func (f *dupCaptureTx) Prepare(_ context.Context, _, _ string) (*pgconn.StatementDescription, error) {
	panic("dupCaptureTx: Prepare not expected")
}
func (f *dupCaptureTx) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	panic("dupCaptureTx: Query not expected")
}
func (f *dupCaptureTx) Conn() *pgx.Conn { return nil }

// dupPoolDB wraps dupCaptureTx so BeginTx returns it.
type dupPoolDB struct {
	tx *dupCaptureTx
}

func (p *dupPoolDB) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return &fakeRow{val: ""}
}
func (p *dupPoolDB) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (p *dupPoolDB) BeginTx(_ context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	return p.tx, nil
}

// Compile-time checks.
var _ pgx.Tx = (*dupCaptureTx)(nil)
var _ PoolDB = (*dupPoolDB)(nil)

// =============================================================================
// buildDupServer — constructs a Server wired for feature #44 tests
// =============================================================================

// dupServerResult bundles everything the caller needs for assertions.
type dupServerResult struct {
	ts       *httptest.Server
	stub     *auth.StubProvider
	store    *replayIdemStore
	aw       *captureAuditWriter
	tx       *dupCaptureTx
	metrics  *observability.Metrics
	registry *prometheus.Registry
}

// buildDupServer constructs a fully-wired Server with:
//   - StubProvider for JWT auth
//   - replayIdemStore (in-memory, thread-safe, supports TTL expiry)
//   - dupCaptureTx that intercepts idempotency INSERT AND counts outbox INSERTs
//   - captureAuditWriter (no-op SQL, captures events in memory)
//   - observability.Metrics on a fresh registry (idempotency_replays_total)
func buildDupServer(t *testing.T) *dupServerResult {
	t.Helper()

	const secret = "dup-idem-test-secret"
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  secret,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}

	aw := &captureAuditWriter{}
	store := newReplayIdemStore()
	tx := &dupCaptureTx{store: store}
	pool := &dupPoolDB{tx: tx}

	reg := prometheus.NewRegistry()
	m, err := observability.New(reg)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	cfg := &config.Config{
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
	}

	s := New(Options{
		Config:  cfg,
		Auth:    stub,
		Audit:   aw,
		Idem:    store,
		Pool:    pool,
		Metrics: m,
	})

	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	return &dupServerResult{
		ts:       ts,
		stub:     stub,
		store:    store,
		aw:       aw,
		tx:       tx,
		metrics:  m,
		registry: reg,
	}
}

// dupMintJWT issues a Bearer token for the given actorID.
func dupMintJWT(t *testing.T, stub *auth.StubProvider, actorID string) string {
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

// postEchoDup issues POST /v1/echo with the given token, idempotency key, body.
func postEchoDup(t *testing.T, ts *httptest.Server, token, idemKey, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", idemKey)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

// drainClose reads and closes the response body, returning the bytes.
func drainClose(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

// gatherCounter reads a named counter from the registry. Returns 0 when missing.
func gatherCounter(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("registry.Gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			for _, m := range mf.GetMetric() {
				if c := m.GetCounter(); c != nil {
					return c.GetValue()
				}
			}
		}
	}
	return 0
}

// outboxCallCount returns the number of tx.QueryRow calls (== outbox INSERTs).
func (r *dupServerResult) outboxCallCount() int {
	r.tx.mu.Lock()
	defer r.tx.mu.Unlock()
	return r.tx.queryRowCalls
}

// =============================================================================
// Feature #44 tests
// =============================================================================

const dupActorID44 = "00000000-0000-0000-0000-000000000044"
const dupMsgBody = `{"message":"first"}`

// TestDupIdem_FirstRequestReturns200 verifies steps 1-2:
// POST /v1/echo with Idempotency-Key: DUP_KEY_1 returns HTTP 200 on the first call.
func TestDupIdem_FirstRequestReturns200(t *testing.T) {
	r := buildDupServer(t)
	token := dupMintJWT(t, r.stub, dupActorID44)

	resp := postEchoDup(t, r.ts, token, "DUP_KEY_1", dupMsgBody)
	body := drainClose(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("step 1: want 200, got %d (body: %s)", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("step 2: want Content-Type application/json, got %q", ct)
	}
}

// TestDupIdem_SecondRequestReturnsIdenticalBody verifies steps 4-6:
// The second POST with the same key+body returns byte-for-byte identical body.
func TestDupIdem_SecondRequestReturnsIdenticalBody(t *testing.T) {
	r := buildDupServer(t)
	token := dupMintJWT(t, r.stub, dupActorID44)

	// req1: first request.
	resp1 := postEchoDup(t, r.ts, token, "DUP_KEY_1", dupMsgBody)
	body1 := drainClose(t, resp1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("req1: want 200, got %d (body: %s)", resp1.StatusCode, body1)
	}

	// req2: duplicate (same key, same body).
	resp2 := postEchoDup(t, r.ts, token, "DUP_KEY_1", dupMsgBody)
	body2 := drainClose(t, resp2)
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("step 4: req2 want 200, got %d (body: %s)", resp2.StatusCode, body2)
	}

	// Step 6: body must be byte-for-byte identical.
	if !bytes.Equal(body1, body2) {
		t.Errorf("step 6: response body not byte-identical\n  req1: %s\n  req2: %s", body1, body2)
	}
}

// TestDupIdem_SecondRequestHasReplayHeader verifies step 7:
// The second (replayed) response carries the Idempotent-Replay: true marker.
// The feature spec says "X-Idempotent-Replay: true (or similar marker)"; the
// codebase uses "Idempotent-Replay: true" which satisfies the "(or similar
// marker)" qualifier per the specification.
func TestDupIdem_SecondRequestHasReplayHeader(t *testing.T) {
	r := buildDupServer(t)
	token := dupMintJWT(t, r.stub, dupActorID44)

	// First request — must NOT carry the replay header.
	resp1 := postEchoDup(t, r.ts, token, "DUP_KEY_1", dupMsgBody)
	_ = drainClose(t, resp1)
	if got := resp1.Header.Get("Idempotent-Replay"); got != "" {
		t.Errorf("step 7 pre-cond: req1 must NOT have Idempotent-Replay header, got %q", got)
	}

	// Second request — MUST carry the replay marker.
	resp2 := postEchoDup(t, r.ts, token, "DUP_KEY_1", dupMsgBody)
	_ = drainClose(t, resp2)
	if got := resp2.Header.Get("Idempotent-Replay"); got != "true" {
		t.Errorf("step 7: req2 must have Idempotent-Replay: true, got %q", got)
	}
}

// TestDupIdem_AuditWrittenOnlyOnce verifies step 8:
// audit_events receives exactly 1 row. The idempotency middleware short-circuits
// the second request before the handler (and audit.WriteTx) runs.
func TestDupIdem_AuditWrittenOnlyOnce(t *testing.T) {
	r := buildDupServer(t)
	token := dupMintJWT(t, r.stub, dupActorID44)

	resp1 := postEchoDup(t, r.ts, token, "DUP_KEY_1", dupMsgBody)
	_ = drainClose(t, resp1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("req1: want 200, got %d", resp1.StatusCode)
	}

	// Replay request.
	resp2 := postEchoDup(t, r.ts, token, "DUP_KEY_1", dupMsgBody)
	_ = drainClose(t, resp2)

	// Step 8: captureAuditWriter must have collected exactly 1 event.
	events := r.aw.getEvents()
	if len(events) != 1 {
		t.Errorf("step 8: want 1 audit event (handler runs once), got %d", len(events))
	}
}

// TestDupIdem_OutboxWrittenOnlyOnce verifies step 9:
// outbox_events receives exactly 1 row. insertOutboxEcho calls tx.QueryRow once
// per real handler invocation. On a replay the handler is never called.
func TestDupIdem_OutboxWrittenOnlyOnce(t *testing.T) {
	r := buildDupServer(t)
	token := dupMintJWT(t, r.stub, dupActorID44)

	resp1 := postEchoDup(t, r.ts, token, "DUP_KEY_1", dupMsgBody)
	_ = drainClose(t, resp1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("req1: want 200, got %d", resp1.StatusCode)
	}

	// Replay request.
	resp2 := postEchoDup(t, r.ts, token, "DUP_KEY_1", dupMsgBody)
	_ = drainClose(t, resp2)

	// Step 9: tx.QueryRow (== insertOutboxEcho) called exactly once.
	if qr := r.outboxCallCount(); qr != 1 {
		t.Errorf("step 9: want outbox INSERT once (1 QueryRow), got %d", qr)
	}
}

// TestDupIdem_ReplayCounterIncrementedByOne verifies step 10:
// arena_idempotency_replays_total is incremented exactly once when a duplicate
// Idempotency-Key request is replayed.
func TestDupIdem_ReplayCounterIncrementedByOne(t *testing.T) {
	r := buildDupServer(t)
	token := dupMintJWT(t, r.stub, dupActorID44)

	const metricName = "arena_idempotency_replays_total"

	// Baseline: counter must be 0 before any requests.
	if baseline := gatherCounter(t, r.registry, metricName); baseline != 0 {
		t.Fatalf("step 10 pre-cond: counter must start at 0, got %v", baseline)
	}

	// First request: not a replay → counter unchanged.
	resp1 := postEchoDup(t, r.ts, token, "DUP_KEY_1", dupMsgBody)
	_ = drainClose(t, resp1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("req1: want 200, got %d", resp1.StatusCode)
	}
	if v := gatherCounter(t, r.registry, metricName); v != 0 {
		t.Errorf("step 10: counter must stay 0 after first (non-replay) request, got %v", v)
	}

	// Second request: replay → counter incremented to 1.
	resp2 := postEchoDup(t, r.ts, token, "DUP_KEY_1", dupMsgBody)
	_ = drainClose(t, resp2)
	if v := gatherCounter(t, r.registry, metricName); v != 1 {
		t.Errorf("step 10: want arena_idempotency_replays_total=1 after replay, got %v", v)
	}
}

// TestDupIdem_CounterIsAccumulative verifies the counter is cumulative across
// multiple replays (3 requests = 1 fresh + 2 replays → counter=2).
func TestDupIdem_CounterIsAccumulative(t *testing.T) {
	r := buildDupServer(t)
	token := dupMintJWT(t, r.stub, dupActorID44)

	const metricName = "arena_idempotency_replays_total"

	for i := 0; i < 3; i++ {
		resp := postEchoDup(t, r.ts, token, "DUP_KEY_1", dupMsgBody)
		_ = drainClose(t, resp)
	}

	if v := gatherCounter(t, r.registry, metricName); v != 2 {
		t.Errorf("want counter=2 after 2 replays, got %v", v)
	}
}

// TestDupIdem_FreshKeysNeverIncrementCounter ensures the counter stays at 0
// when each request uses a distinct idempotency key (no duplicates).
func TestDupIdem_FreshKeysNeverIncrementCounter(t *testing.T) {
	r := buildDupServer(t)
	token := dupMintJWT(t, r.stub, dupActorID44)

	const metricName = "arena_idempotency_replays_total"

	for _, key := range []string{"KEY_A", "KEY_B", "KEY_C"} {
		resp := postEchoDup(t, r.ts, token, key, dupMsgBody)
		_ = drainClose(t, resp)
	}

	if v := gatherCounter(t, r.registry, metricName); v != 0 {
		t.Errorf("counter must stay 0 for unique keys, got %v", v)
	}
}

// TestDupIdem_FullVerification is an end-to-end sweep of all 10 feature steps.
func TestDupIdem_FullVerification(t *testing.T) {
	r := buildDupServer(t)
	token := dupMintJWT(t, r.stub, dupActorID44)

	const metricName = "arena_idempotency_replays_total"

	// ── Step 1-2: POST /v1/echo, Idempotency-Key: DUP_KEY_FULL ──────────────
	resp1 := postEchoDup(t, r.ts, token, "DUP_KEY_FULL", dupMsgBody)
	body1 := drainClose(t, resp1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("step 1: want 200, got %d (body: %s)", resp1.StatusCode, body1)
	}
	reqID1 := resp1.Header.Get("X-Request-Id")
	t.Logf("step 2: req1 X-Request-Id=%q body=%s", reqID1, body1)

	// ── Steps 4-5: Duplicate POST (step 3 = "Wait 1s" is omitted in unit
	// tests; the in-memory store is TTL-accurate without wall-clock sleep) ────
	resp2 := postEchoDup(t, r.ts, token, "DUP_KEY_FULL", dupMsgBody)
	body2 := drainClose(t, resp2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("step 5: want 200 on replay, got %d (body: %s)", resp2.StatusCode, body2)
	}
	reqID2 := resp2.Header.Get("X-Request-Id")
	t.Logf("step 5: req2 X-Request-Id=%q body=%s", reqID2, body2)

	// ── Step 6: byte-identical body ──────────────────────────────────────────
	if !bytes.Equal(body1, body2) {
		t.Errorf("step 6: body not byte-identical\n  req1: %s\n  req2: %s", body1, body2)
	}

	// ── Step 7: Idempotent-Replay: true (or similar marker) ──────────────────
	if got := resp2.Header.Get("Idempotent-Replay"); got != "true" {
		t.Errorf("step 7: replay marker missing; Idempotent-Replay=%q", got)
	}
	if got := resp1.Header.Get("Idempotent-Replay"); got != "" {
		t.Errorf("step 7: req1 must NOT carry Idempotent-Replay, got %q", got)
	}

	// ── Step 8: audit_events count = 1 ───────────────────────────────────────
	evs := r.aw.getEvents()
	if len(evs) != 1 {
		t.Errorf("step 8: want 1 audit_events row, got %d", len(evs))
	}

	// ── Step 9: outbox_events count = 1 ──────────────────────────────────────
	if qr := r.outboxCallCount(); qr != 1 {
		t.Errorf("step 9: want 1 outbox_events row (1 tx.QueryRow call), got %d", qr)
	}

	// ── Step 10: idempotency_replays_total = 1 ────────────────────────────────
	if v := gatherCounter(t, r.registry, metricName); v != 1 {
		t.Errorf("step 10: want %s=1, got %v", metricName, v)
	}
}

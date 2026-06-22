// Package httpserver — unit tests for feature #37:
// "Idempotency replay returns identical response within TTL"
//
// These tests exercise the full idempotency lifecycle through the real
// httpserver + auth + middleware chain, without a live PostgreSQL connection.
//
// The key challenge in these tests is that POST /v1/echo calls
// idempotency.SaveTx inside the transaction (which calls tx.Exec) and then
// MarkPersisted to signal the middleware to skip its own store.Save fallback.
// To make the replay work with a unit-test setup we use a
// replayCaptureTx that intercepts the idempotency INSERT and forwards it to
// the replayIdemStore, giving us a fully functional replay store without a
// live PostgreSQL connection.
//
// Tests cover all 10 feature steps:
//
//   - Steps 1-2:  POST with key REPLAY_TEST_A → HTTP 200.
//   - Steps 3-5:  Second POST same key+body → byte-identical replay.
//   - Step  6:    Status and key headers identical; Idempotent-Replay header set.
//   - Steps 7-8:  Third POST same key, different body → 409 idempotency.body_mismatch.
//   - Step  9:    slog WARN with code='idempotency.body_mismatch' is emitted.
//   - Step 10:    Force TTL expiry via store.expireAll → next POST fresh execution.
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// =============================================================================
// replayIdemStore — in-memory idempotency.Store for replay tests
// =============================================================================

// replayEntry mirrors one idempotency_keys row.
type replayEntry struct {
	resp    idempotency.StoredResponse
	key     string
	scope   string
	actorID string
}

// replayIdemStore is a thread-safe in-memory Store whose entries can be
// manually expired via expireAll, modelling the
// "force expires_at in past by direct UPDATE" step of the feature spec.
type replayIdemStore struct {
	mu      sync.Mutex
	records map[string]replayEntry
}

func newReplayIdemStore() *replayIdemStore {
	return &replayIdemStore{records: make(map[string]replayEntry)}
}

func (s *replayIdemStore) storeKey(key, scope string) string { return key + "\x00" + scope }

func (s *replayIdemStore) Lookup(_ context.Context, key, scope string) (idempotency.StoredResponse, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.records[s.storeKey(key, scope)]
	if !ok {
		return idempotency.StoredResponse{}, false, nil
	}
	if time.Now().After(e.resp.ExpiresAt) {
		// TTL expired — treat as MISS (same as PGStore WHERE expires_at > now())
		return idempotency.StoredResponse{}, false, nil
	}
	return e.resp, true, nil
}

func (s *replayIdemStore) Save(_ context.Context, key, scope, actorID string, resp idempotency.StoredResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.storeKey(key, scope)
	if _, exists := s.records[k]; exists {
		return nil // ON CONFLICT DO NOTHING semantics
	}
	s.records[k] = replayEntry{resp: resp, key: key, scope: scope, actorID: actorID}
	return nil
}

// expireAll sets every stored entry's ExpiresAt to 24 hours in the past,
// modelling the "UPDATE idempotency_keys SET expires_at = now()-interval '1 day'"
// step in the feature spec. After expireAll, Lookup always returns MISS.
func (s *replayIdemStore) expireAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	past := time.Now().Add(-24 * time.Hour)
	for k, e := range s.records {
		e.resp.ExpiresAt = past
		s.records[k] = e
	}
}

// count returns the number of stored entries (for assertions).
func (s *replayIdemStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

// Compile-time assertion.
var _ idempotency.Store = (*replayIdemStore)(nil)

// =============================================================================
// replayCaptureTx — fakeTx that intercepts idempotency INSERT → store.Save
// =============================================================================

// replayCaptureTx is a minimal pgx.Tx implementation.
//
// When Exec receives the idempotency_keys INSERT that idempotency.SaveTx
// generates, it extracts the arguments and calls store.Save so the in-memory
// store becomes populated exactly as the real PGStore would be. Without this
// interception the middleware's auto-save is skipped (because SaveTx called
// MarkPersisted) and the store stays empty, making all Lookup calls return MISS.
type replayCaptureTx struct {
	mu        sync.Mutex
	execCalls int
	store     *replayIdemStore
}

func (f *replayCaptureTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.mu.Lock()
	f.execCalls++
	f.mu.Unlock()

	// Intercept the idempotency INSERT from idempotency.SaveTx.
	// Argument order (from SaveTx): key, scope, actorID, reqHash, status, body, createdAt, expiresAt
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

// QueryRow satisfies insertOutboxEcho which scans the generated UUID.
func (f *replayCaptureTx) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return &fakeRow{val: "00000000-0000-0000-0000-000000000099"}
}

// Commit and Rollback are no-ops in this fake.
func (f *replayCaptureTx) Commit(_ context.Context) error   { return nil }
func (f *replayCaptureTx) Rollback(_ context.Context) error { return nil }

// The following methods are never called by the handleEcho code path.
func (f *replayCaptureTx) Begin(_ context.Context) (pgx.Tx, error) {
	panic("replayCaptureTx: Begin not expected in handleEcho path")
}
func (f *replayCaptureTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	panic("replayCaptureTx: CopyFrom not expected in handleEcho path")
}
func (f *replayCaptureTx) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults {
	panic("replayCaptureTx: SendBatch not expected in handleEcho path")
}
func (f *replayCaptureTx) LargeObjects() pgx.LargeObjects {
	panic("replayCaptureTx: LargeObjects not expected in handleEcho path")
}
func (f *replayCaptureTx) Prepare(_ context.Context, _, _ string) (*pgconn.StatementDescription, error) {
	panic("replayCaptureTx: Prepare not expected in handleEcho path")
}
func (f *replayCaptureTx) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	panic("replayCaptureTx: Query not expected in handleEcho path")
}
func (f *replayCaptureTx) Conn() *pgx.Conn { return nil }

// replayPoolDB wraps replayCaptureTx so BeginTx returns it.
type replayPoolDB struct {
	tx *replayCaptureTx
}

func (p *replayPoolDB) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return &fakeRow{val: ""}
}
func (p *replayPoolDB) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (p *replayPoolDB) BeginTx(_ context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	return p.tx, nil
}

// Compile-time interface checks.
var _ pgx.Tx = (*replayCaptureTx)(nil)
var _ PoolDB = (*replayPoolDB)(nil)

// =============================================================================
// Server builder for replay tests
// =============================================================================

// buildReplayServer constructs a fully-wired Server with:
//   - StubProvider for JWT auth
//   - replayIdemStore (in-memory, supports expiry)
//   - replayCaptureTx that intercepts idempotency INSERT → store.Save
//   - captureAuditWriter (no-op SQL, captures events)
//   - optional slog.Logger to capture log output (pass nil for slog.Default())
func buildReplayServer(
	t *testing.T,
	logger *slog.Logger,
) (
	ts *httptest.Server,
	stub *auth.StubProvider,
	store *replayIdemStore,
	aw *captureAuditWriter,
) {
	t.Helper()

	const secret = "replay-test-secret-not-for-production"
	var err error
	stub, err = auth.NewStubProvider(auth.StubConfig{
		Secret:  secret,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}

	aw = &captureAuditWriter{}
	store = newReplayIdemStore()
	tx := &replayCaptureTx{store: store}
	pool := &replayPoolDB{tx: tx}

	cfg := &config.Config{
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
	}

	s := New(Options{
		Config: cfg,
		Logger: logger,
		Auth:   stub,
		Audit:  aw,
		Idem:   store,
		Pool:   pool,
	})

	ts = httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)
	return ts, stub, store, aw
}

// mintReplayJWT issues a token for the given actor UUID via the StubProvider.
func mintReplayJWT(t *testing.T, stub *auth.StubProvider, actorID string) string {
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

// postEchoReplay issues POST /v1/echo with the given token, idempotency key, and body.
func postEchoReplay(t *testing.T, ts *httptest.Server, token, idemKey, body string) *http.Response {
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

// readBodyBytes reads and closes the response body.
func readBodyBytes(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

// =============================================================================
// Feature #37 tests
// =============================================================================

// TestReplay_FirstRequestReturns200 verifies steps 1-2:
// POST /v1/echo with Idempotency-Key REPLAY_TEST_A returns HTTP 200.
func TestReplay_FirstRequestReturns200(t *testing.T) {
	ts, stub, _, _ := buildReplayServer(t, nil)

	const actorID = "00000000-0000-0000-0000-000000000042"
	token := mintReplayJWT(t, stub, actorID)

	resp := postEchoReplay(t, ts, token, "REPLAY_TEST_A", `{"message":"alpha"}`)
	body := readBodyBytes(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("step 1: want 200, got %d (body: %s)", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("step 2: want Content-Type application/json, got %q", ct)
	}
}

// TestReplay_SecondRequestReturnsIdenticalBody verifies steps 3-5:
// A second POST with the same key and body returns the same body byte-for-byte.
//
// Note: The spec says "sleep 2s" between requests. We omit this sleep in the
// unit test since our in-memory store respects TTL accurately; the 2s sleep
// in the spec is an integration-test concern to ensure real DB replication
// is complete. The TTL expiry scenario is covered separately.
func TestReplay_SecondRequestReturnsIdenticalBody(t *testing.T) {
	ts, stub, _, _ := buildReplayServer(t, nil)

	const actorID = "00000000-0000-0000-0000-000000000042"
	token := mintReplayJWT(t, stub, actorID)

	const key = "REPLAY_TEST_A"
	const msg = `{"message":"alpha"}`

	// Step 1: first POST.
	resp1 := postEchoReplay(t, ts, token, key, msg)
	body1 := readBodyBytes(t, resp1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request: want 200, got %d (body: %s)", resp1.StatusCode, body1)
	}

	// Step 4: second POST with same key and body.
	resp2 := postEchoReplay(t, ts, token, key, msg)
	body2 := readBodyBytes(t, resp2)

	// Step 5: body byte-identical.
	if !bytes.Equal(body1, body2) {
		t.Errorf("step 5: replay body not byte-identical\n  first:  %s\n  second: %s", body1, body2)
	}

	// Also verify replay header is set on second response.
	if got := resp2.Header.Get("Idempotent-Replay"); got != "true" {
		t.Errorf("step 5: replay response must have Idempotent-Replay: true, got %q", got)
	}
}

// TestReplay_SecondRequestStatusAndHeadersIdentical verifies step 6:
// The replay response carries the same status and key headers as the first
// response (excluding Idempotent-Replay which is set only on replays).
func TestReplay_SecondRequestStatusAndHeadersIdentical(t *testing.T) {
	ts, stub, _, _ := buildReplayServer(t, nil)

	const actorID = "00000000-0000-0000-0000-000000000042"
	token := mintReplayJWT(t, stub, actorID)

	const key = "REPLAY_TEST_B"
	const msg = `{"message":"beta"}`

	resp1 := postEchoReplay(t, ts, token, key, msg)
	_ = readBodyBytes(t, resp1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request: want 200, got %d", resp1.StatusCode)
	}

	resp2 := postEchoReplay(t, ts, token, key, msg)
	_ = readBodyBytes(t, resp2)

	// Step 6a: status codes must be identical.
	if resp1.StatusCode != resp2.StatusCode {
		t.Errorf("step 6: status mismatch: first=%d, replay=%d", resp1.StatusCode, resp2.StatusCode)
	}

	// Step 6b: Content-Type must match.
	ct1 := resp1.Header.Get("Content-Type")
	ct2 := resp2.Header.Get("Content-Type")
	if ct1 != ct2 {
		t.Errorf("step 6: Content-Type mismatch: first=%q, replay=%q", ct1, ct2)
	}

	// Step 6c: replay marker is present on second but NOT on first.
	if got := resp1.Header.Get("Idempotent-Replay"); got != "" {
		t.Errorf("step 6: first response must NOT have Idempotent-Replay header, got %q", got)
	}
	if got := resp2.Header.Get("Idempotent-Replay"); got != "true" {
		t.Errorf("step 6: replay response must have Idempotent-Replay: true, got %q", got)
	}
}

// TestReplay_ThirdRequestWithDifferentBodyReturns409 verifies steps 7-8:
// A POST with the same key but a different body is rejected with 409
// and error code 'idempotency.body_mismatch'.
func TestReplay_ThirdRequestWithDifferentBodyReturns409(t *testing.T) {
	ts, stub, _, _ := buildReplayServer(t, nil)

	const actorID = "00000000-0000-0000-0000-000000000042"
	token := mintReplayJWT(t, stub, actorID)

	const key = "REPLAY_TEST_C"

	// Step 1: first POST establishes the stored hash.
	resp1 := postEchoReplay(t, ts, token, key, `{"message":"alpha"}`)
	_ = readBodyBytes(t, resp1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request: want 200, got %d", resp1.StatusCode)
	}

	// Step 7: POST with different body — same key.
	resp3 := postEchoReplay(t, ts, token, key, `{"message":"different"}`)
	body3 := readBodyBytes(t, resp3)

	// Step 8: expect 409.
	if resp3.StatusCode != http.StatusConflict {
		t.Errorf("step 8: want 409 Conflict, got %d (body: %s)", resp3.StatusCode, body3)
	}

	// Step 8: verify error code is 'idempotency.body_mismatch'.
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body3, &envelope); err != nil {
		t.Fatalf("step 8: cannot parse 409 body as JSON: %v (body: %s)", err, body3)
	}
	const wantCode = "idempotency.body_mismatch"
	if envelope.Error.Code != wantCode {
		t.Errorf("step 8: want error.code=%q, got %q", wantCode, envelope.Error.Code)
	}
}

// TestReplay_ConflictSlogWarnEmitted verifies step 9:
// When a body-mismatch 409 is produced, the middleware emits a slog WARN
// record with code='idempotency.body_mismatch'.
func TestReplay_ConflictSlogWarnEmitted(t *testing.T) {
	// Capture slog output to a buffer.
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	ts, stub, _, _ := buildReplayServer(t, logger)

	const actorID = "00000000-0000-0000-0000-000000000042"
	token := mintReplayJWT(t, stub, actorID)

	const key = "REPLAY_WARN_KEY"

	// First POST: establishes the stored hash.
	resp1 := postEchoReplay(t, ts, token, key, `{"message":"original"}`)
	_ = readBodyBytes(t, resp1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request: want 200, got %d", resp1.StatusCode)
	}

	// Clear the buffer so we only capture log output from the conflict request.
	buf.Reset()

	// Step 9: POST with different body → triggers WARN.
	resp2 := postEchoReplay(t, ts, token, key, `{"message":"different_body_for_warn_test"}`)
	_ = readBodyBytes(t, resp2)

	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("step 9 pre-condition: want 409, got %d", resp2.StatusCode)
	}

	// Parse captured log lines looking for the WARN record.
	logOutput := buf.String()
	lines := strings.Split(strings.TrimSpace(logOutput), "\n")

	foundWarn := false
	for _, line := range lines {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		// slog JSON: level field is "WARN".
		level, _ := record["level"].(string)
		code, _ := record["code"].(string)
		if strings.EqualFold(level, "warn") && code == "idempotency.body_mismatch" {
			foundWarn = true
			break
		}
	}

	if !foundWarn {
		t.Errorf("step 9: no slog WARN with code='idempotency.body_mismatch' found in log output:\n%s", logOutput)
	}
}

// TestReplay_ExpiredTTLFreshExecution verifies step 10:
// After forcibly expiring all stored entries (modelling
// "UPDATE idempotency_keys SET expires_at = now()-interval '1 day'"),
// a subsequent request is treated as a fresh execution — Idempotent-Replay is
// NOT set, indicating a new handler invocation rather than a cached replay.
//
// Design choice (per feature spec "choose and document"):
//   - Expired key = MISS, not 410. The PGStore uses WHERE expires_at > now()
//     so an expired row is treated identically to a non-existent row.
//   - A fresh 200 is returned, not 410. The client can resubmit freely after TTL.
func TestReplay_ExpiredTTLFreshExecution(t *testing.T) {
	ts, stub, store, _ := buildReplayServer(t, nil)

	const actorID = "00000000-0000-0000-0000-000000000042"
	token := mintReplayJWT(t, stub, actorID)

	const key = "REPLAY_EXPIRE_KEY"
	const msg = `{"message":"expire_me"}`

	// First POST: stores the response.
	resp1 := postEchoReplay(t, ts, token, key, msg)
	body1 := readBodyBytes(t, resp1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request: want 200, got %d (body: %s)", resp1.StatusCode, body1)
	}
	// First response must NOT have Idempotent-Replay.
	if got := resp1.Header.Get("Idempotent-Replay"); got != "" {
		t.Errorf("first response must not carry Idempotent-Replay, got %q", got)
	}

	// Second POST (before expiry) must be a replay.
	resp2 := postEchoReplay(t, ts, token, key, msg)
	_ = readBodyBytes(t, resp2)
	if got := resp2.Header.Get("Idempotent-Replay"); got != "true" {
		t.Errorf("before expiry: want Idempotent-Replay: true, got %q", got)
	}

	// Step 10: force expires_at into the past (models direct DB UPDATE).
	store.expireAll()

	// Third POST: TTL expired → MISS → fresh execution → no Idempotent-Replay.
	resp3 := postEchoReplay(t, ts, token, key, msg)
	body3 := readBodyBytes(t, resp3)

	if resp3.StatusCode != http.StatusOK {
		t.Errorf("step 10: want 200 on fresh execution after TTL expiry, got %d (body: %s)",
			resp3.StatusCode, body3)
	}
	if got := resp3.Header.Get("Idempotent-Replay"); got == "true" {
		t.Errorf("step 10: after TTL expiry, response must NOT carry Idempotent-Replay" +
			" (should be fresh execution, not replay)")
	}
}

// TestReplay_StoreHasExactlyOneEntryAfterReplay verifies that replays do NOT
// insert additional rows — the store holds exactly one entry per key+scope pair
// even after multiple duplicate requests.
func TestReplay_StoreHasExactlyOneEntryAfterReplay(t *testing.T) {
	ts, stub, store, _ := buildReplayServer(t, nil)

	const actorID = "00000000-0000-0000-0000-000000000042"
	token := mintReplayJWT(t, stub, actorID)

	const key = "REPLAY_COUNT_KEY"
	const msg = `{"message":"count"}`

	for i := 0; i < 3; i++ {
		resp := postEchoReplay(t, ts, token, key, msg)
		_ = readBodyBytes(t, resp)
	}

	if n := store.count(); n != 1 {
		t.Errorf("want exactly 1 store entry after 3 identical requests, got %d", n)
	}
}

// TestReplay_DifferentKeysAreIndependent verifies that two different
// Idempotency-Keys produce independent store entries and don't interfere.
func TestReplay_DifferentKeysAreIndependent(t *testing.T) {
	ts, stub, store, _ := buildReplayServer(t, nil)

	const actorID = "00000000-0000-0000-0000-000000000042"
	token := mintReplayJWT(t, stub, actorID)

	resp1 := postEchoReplay(t, ts, token, "KEY_ALPHA", `{"message":"alpha"}`)
	body1 := readBodyBytes(t, resp1)

	resp2 := postEchoReplay(t, ts, token, "KEY_BETA", `{"message":"beta"}`)
	body2 := readBodyBytes(t, resp2)

	if bytes.Equal(body1, body2) {
		t.Error("different keys produced identical bodies — scope isolation may be broken")
	}
	if n := store.count(); n != 2 {
		t.Errorf("want 2 independent store entries, got %d", n)
	}

	// Neither first response carries the replay marker.
	if got := resp1.Header.Get("Idempotent-Replay"); got != "" {
		t.Errorf("KEY_ALPHA first response must not have Idempotent-Replay, got %q", got)
	}
	if got := resp2.Header.Get("Idempotent-Replay"); got != "" {
		t.Errorf("KEY_BETA first response must not have Idempotent-Replay, got %q", got)
	}
}

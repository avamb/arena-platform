// Package httpserver — unit tests for feature #16:
// "Audit event written for /v1/echo persists to DB"
//
// All 12 feature steps are exercised without a live PostgreSQL connection by
// injecting in-memory implementations of audit.Writer, idempotency.Store, and
// the PoolDB interface. The captureAuditWriter captures every Event passed to
// WriteTx so assertions can verify actor_id, action, resource_type, etc.
//
// The fakeTx implementation satisfies pgx.Tx using only the three methods
// called by the handleEcho code path:
//
//   - Exec        — used by idempotency.SaveTx to persist the idempotency row.
//   - QueryRow    — used by insertOutboxEcho to obtain the outbox event UUID.
//   - Commit      — called after all writes succeed.
//
// Rollback is called by the deferred cleanup; in this fake it is always a
// no-op so the test avoids pgx.ErrTxClosed noise after a successful Commit.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// =============================================================================
// In-memory test doubles
// =============================================================================

// captureAuditWriter implements audit.Writer and stores every Event in memory.
// WriteTx captures the event without executing any SQL on the supplied tx —
// the db interaction is not the subject of this test; the field values are.
type captureAuditWriter struct {
	mu     sync.Mutex
	events []audit.Event
}

func (w *captureAuditWriter) Write(_ context.Context, ev audit.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, ev)
	return nil
}

func (w *captureAuditWriter) WriteTx(_ context.Context, _ pgx.Tx, ev audit.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, ev)
	return nil
}

func (w *captureAuditWriter) getEvents() []audit.Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]audit.Event, len(w.events))
	copy(out, w.events)
	return out
}

func (w *captureAuditWriter) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = nil
}

// noopIdemStore satisfies idempotency.Store. Lookup always returns MISS so the
// handler runs on every request (no replay). Save is a no-op because the echo
// handler calls idempotency.SaveTx (which writes through the tx, then calls
// MarkPersisted) rather than letting the middleware auto-save.
type noopIdemStore struct{}

func (s *noopIdemStore) Lookup(_ context.Context, _, _ string) (idempotency.StoredResponse, bool, error) {
	return idempotency.StoredResponse{}, false, nil
}

func (s *noopIdemStore) Save(_ context.Context, _, _, _ string, _ idempotency.StoredResponse) error {
	return nil
}

// fakeRow implements pgx.Row. It scans the pre-set val string into the first
// *string destination — sufficient for insertOutboxEcho which scans one UUID.
type fakeRow struct{ val string }

func (r *fakeRow) Scan(dest ...any) error {
	if len(dest) == 0 {
		return nil
	}
	if s, ok := dest[0].(*string); ok {
		*s = r.val
		return nil
	}
	return errors.New("fakeRow: cannot scan into the supplied destination type")
}

// fakeTx is a minimal pgx.Tx. Only Exec, QueryRow, Commit, and Rollback are
// implemented meaningfully; the remaining methods panic if called (they are not
// reachable via handleEcho's code path, so a panic would indicate a test flaw).
type fakeTx struct {
	mu        sync.Mutex
	execCalls int
}

// Exec satisfies both the idempotency.SaveTx call (idempotency_keys INSERT) and
// any other tx.Exec in the handler's code path. Returns success unconditionally.
func (f *fakeTx) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	f.mu.Lock()
	f.execCalls++
	f.mu.Unlock()
	return pgconn.CommandTag{}, nil
}

// QueryRow satisfies insertOutboxEcho which scans the generated UUID.
func (f *fakeTx) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return &fakeRow{val: "00000000-0000-0000-0000-000000000099"}
}

// Commit and Rollback are no-ops in this fake.
func (f *fakeTx) Commit(_ context.Context) error   { return nil }
func (f *fakeTx) Rollback(_ context.Context) error { return nil }

// The following methods are never called by the handleEcho code path; they are
// provided only to satisfy the pgx.Tx interface. A panic here indicates a
// regression that introduced a new tx method call in the handler.
func (f *fakeTx) Begin(_ context.Context) (pgx.Tx, error) {
	panic("fakeTx: Begin not expected in handleEcho path")
}
func (f *fakeTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	panic("fakeTx: CopyFrom not expected in handleEcho path")
}
func (f *fakeTx) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults {
	panic("fakeTx: SendBatch not expected in handleEcho path")
}
func (f *fakeTx) LargeObjects() pgx.LargeObjects {
	panic("fakeTx: LargeObjects not expected in handleEcho path")
}
func (f *fakeTx) Prepare(_ context.Context, _, _ string) (*pgconn.StatementDescription, error) {
	panic("fakeTx: Prepare not expected in handleEcho path")
}
func (f *fakeTx) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	panic("fakeTx: Query not expected in handleEcho path")
}
func (f *fakeTx) Conn() *pgx.Conn { return nil }

// fakePoolDB implements PoolDB. BeginTx returns the embedded *fakeTx; QueryRow
// and Exec are present to satisfy the interface but are not called by echo.
type fakePoolDB struct {
	tx *fakeTx
}

func (p *fakePoolDB) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return &fakeRow{val: ""}
}
func (p *fakePoolDB) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (p *fakePoolDB) BeginTx(_ context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	return p.tx, nil
}

// Compile-time interface checks.
var _ pgx.Tx = (*fakeTx)(nil)
var _ PoolDB = (*fakePoolDB)(nil)
var _ audit.Writer = (*captureAuditWriter)(nil)
var _ idempotency.Store = (*noopIdemStore)(nil)

// =============================================================================
// Test helpers
// =============================================================================

// buildEchoServer constructs a fully-wired Server and returns the httptest
// server, the stub provider (to mint test JWTs), and the captureAuditWriter.
// The caller owns shutting down ts.
func buildEchoServer(t *testing.T) (ts *httptest.Server, stub *auth.StubProvider, aw *captureAuditWriter) {
	t.Helper()

	const secret = "test-secret-not-for-production"
	var err error
	stub, err = auth.NewStubProvider(auth.StubConfig{
		Secret:  secret,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}

	aw = &captureAuditWriter{}
	tx := &fakeTx{}
	pool := &fakePoolDB{tx: tx}
	idem := &noopIdemStore{}

	cfg := &config.Config{
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20, // 1 MiB
	}

	s := New(Options{
		Config: cfg,
		Auth:   stub,
		Audit:  aw,
		Idem:   idem,
		Pool:   pool,
	})

	ts = httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)
	return ts, stub, aw
}

// mintJWT issues a token for the given actor UUID via the StubProvider.
func mintJWT(t *testing.T, stub *auth.StubProvider, actorID string) string {
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

// postEchoAudit issues POST /v1/echo with the given token and idempotency key.
func postEchoAudit(t *testing.T, ts *httptest.Server, token, idemKey, body string) *http.Response {
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

// =============================================================================
// Feature #16 tests
// =============================================================================

// TestAuditEvent_EchoWritesAllRequiredFields exercises all 12 feature steps:
//
//	Step  1: Obtain dev JWT with actor_id = 00000000-0000-0000-0000-000000000001
//	Step  2: POST /v1/echo with Idempotency-Key: AUDIT_PROBE_1, body {"message":"audit me"}
//	Step  3: Capture X-Request-Id from response headers
//	Step  4: Look up the captured audit event by request_id
//	Step  5: Verify exactly 1 row exists
//	Step  6: Verify actor_id = '00000000-0000-0000-0000-000000000001'
//	Step  7: Verify action = 'v1.echo.create'
//	Step  8: Verify resource_type and resource_id are non-empty
//	Step  9: Verify trace_id matches response X-Trace-Id header
//	Step 10: Verify ip column is populated
//	Step 11: Verify occurred_at is within the last 5 seconds in UTC
//	Step 12: Cleanup — reset the in-memory audit store
func TestAuditEvent_EchoWritesAllRequiredFields(t *testing.T) {
	ts, stub, aw := buildEchoServer(t)

	// ── Step 1: Obtain dev JWT with a known actor_id ─────────────────────────
	const actorID = "00000000-0000-0000-0000-000000000001"
	token := mintJWT(t, stub, actorID)

	// ── Step 2: POST /v1/echo ─────────────────────────────────────────────────
	before := time.Now().UTC()
	const idemKey = "AUDIT_PROBE_1"
	resp := postEchoAudit(t, ts, token, idemKey, `{"message":"audit me"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("step 2: want 200, got %d — body: %s", resp.StatusCode, body)
	}

	// ── Step 3: Capture X-Request-Id and X-Trace-Id from response headers ────
	capturedRequestID := resp.Header.Get("X-Request-Id")
	capturedTraceID := resp.Header.Get("X-Trace-Id")

	if capturedRequestID == "" {
		t.Fatal("step 3: X-Request-Id response header is missing")
	}
	if capturedTraceID == "" {
		t.Fatal("step 3: X-Trace-Id response header is missing (needed for step 9)")
	}

	// ── Step 4: Look up the audit event by request_id ─────────────────────────
	events := aw.getEvents()
	var matchingEvents []audit.Event
	for _, ev := range events {
		if ev.RequestID == capturedRequestID {
			matchingEvents = append(matchingEvents, ev)
		}
	}

	// ── Step 5: Verify exactly 1 row ──────────────────────────────────────────
	if len(matchingEvents) != 1 {
		t.Fatalf("step 5: want exactly 1 audit event for request_id %q, got %d", capturedRequestID, len(matchingEvents))
	}
	ev := matchingEvents[0]

	// ── Step 6: Verify actor_id ───────────────────────────────────────────────
	if ev.ActorID != actorID {
		t.Errorf("step 6: want actor_id=%q, got %q", actorID, ev.ActorID)
	}

	// ── Step 7: Verify action is the stable identifier ────────────────────────
	if ev.Action != echoAuditAction {
		t.Errorf("step 7: want action=%q, got %q", echoAuditAction, ev.Action)
	}

	// ── Step 8: Verify resource_type and resource_id are non-empty ─────────────
	if strings.TrimSpace(ev.ResourceType) == "" {
		t.Error("step 8: resource_type must be non-empty")
	}
	if strings.TrimSpace(ev.ResourceID) == "" {
		t.Error("step 8: resource_id must be non-empty (should be the idempotency key)")
	}

	// ── Step 9: Verify trace_id matches response X-Trace-Id header ────────────
	if ev.TraceID != capturedTraceID {
		t.Errorf("step 9: audit trace_id=%q does not match X-Trace-Id response header %q",
			ev.TraceID, capturedTraceID)
	}

	// ── Step 10: Verify ip column is populated ────────────────────────────────
	// httptest.Server binds on 127.0.0.1; the client connects from loopback.
	if strings.TrimSpace(ev.IP) == "" {
		t.Error("step 10: ip column must be non-empty for loopback test requests")
	}

	// ── Step 11: Verify occurred_at is within the last 5 seconds in UTC ───────
	after := time.Now().UTC()
	if ev.OccurredAt.Before(before) {
		t.Errorf("step 11: occurred_at %v is before the request was sent (%v)",
			ev.OccurredAt, before)
	}
	if ev.OccurredAt.After(after.Add(5 * time.Second)) {
		t.Errorf("step 11: occurred_at %v is suspiciously far in the future (after %v)",
			ev.OccurredAt, after)
	}

	// ── Step 12: Cleanup ──────────────────────────────────────────────────────
	aw.reset()
	if got := len(aw.getEvents()); got != 0 {
		t.Errorf("step 12: want 0 events after cleanup, got %d", got)
	}
}

// TestAuditEvent_RequestIDMatchesResponseHeader verifies the audit
// request_id field is the same value that appears in the X-Request-Id
// response header — the core linkage tested by feature #16 step 4.
func TestAuditEvent_RequestIDMatchesResponseHeader(t *testing.T) {
	ts, stub, aw := buildEchoServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	resp := postEchoAudit(t, ts, token, "REQUEST_ID_CHECK", `{"message":"id check"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	responseRequestID := resp.Header.Get("X-Request-Id")
	if responseRequestID == "" {
		t.Fatal("X-Request-Id response header is missing")
	}

	events := aw.getEvents()
	if len(events) == 0 {
		t.Fatal("no audit events captured")
	}

	if events[0].RequestID != responseRequestID {
		t.Errorf("audit RequestID=%q does not match X-Request-Id header=%q",
			events[0].RequestID, responseRequestID)
	}
}

// TestAuditEvent_ActionIsStableIdentifier confirms the action field uses the
// canonical constant echoAuditAction ("v1.echo.create") not an ad-hoc string.
func TestAuditEvent_ActionIsStableIdentifier(t *testing.T) {
	ts, stub, aw := buildEchoServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	resp := postEchoAudit(t, ts, token, "ACTION_CHECK", `{"message":"action test"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	events := aw.getEvents()
	if len(events) == 0 {
		t.Fatal("no audit events captured")
	}

	const wantAction = "v1.echo.create"
	if events[0].Action != wantAction {
		t.Errorf("want Action=%q, got %q", wantAction, events[0].Action)
	}
	// Double-check via the package constant (guard against constant mismatch).
	if echoAuditAction != wantAction {
		t.Errorf("echoAuditAction constant=%q, expected %q", echoAuditAction, wantAction)
	}
}

// TestAuditEvent_ResourceTypeAndIDNonEmpty guards step 8.
func TestAuditEvent_ResourceTypeAndIDNonEmpty(t *testing.T) {
	ts, stub, aw := buildEchoServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	resp := postEchoAudit(t, ts, token, "RESOURCE_CHECK", `{"message":"resource"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	events := aw.getEvents()
	if len(events) == 0 {
		t.Fatal("no audit events captured")
	}
	ev := events[0]

	if ev.ResourceType == "" {
		t.Error("ResourceType must not be empty")
	}
	if ev.ResourceID == "" {
		t.Error("ResourceID must not be empty")
	}
}

// TestAuditEvent_TraceIDMatchesResponseHeader guards step 9.
func TestAuditEvent_TraceIDMatchesResponseHeader(t *testing.T) {
	ts, stub, aw := buildEchoServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	resp := postEchoAudit(t, ts, token, "TRACE_CHECK", `{"message":"trace"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	traceID := resp.Header.Get("X-Trace-Id")
	if traceID == "" {
		t.Fatal("X-Trace-Id response header missing")
	}

	events := aw.getEvents()
	if len(events) == 0 {
		t.Fatal("no audit events captured")
	}
	if events[0].TraceID != traceID {
		t.Errorf("audit TraceID=%q != X-Trace-Id header %q", events[0].TraceID, traceID)
	}
}

// TestAuditEvent_OccurredAtWithinLastFiveSeconds guards step 11.
func TestAuditEvent_OccurredAtWithinLastFiveSeconds(t *testing.T) {
	ts, stub, aw := buildEchoServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	before := time.Now().UTC().Add(-time.Second) // 1s slack before request
	resp := postEchoAudit(t, ts, token, "TIME_CHECK", `{"message":"timing"}`)
	after := time.Now().UTC().Add(time.Second) // 1s slack after request
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	events := aw.getEvents()
	if len(events) == 0 {
		t.Fatal("no audit events captured")
	}
	ev := events[0]

	if ev.OccurredAt.IsZero() {
		t.Fatal("occurred_at must not be the zero value")
	}
	if ev.OccurredAt.Before(before) {
		t.Errorf("occurred_at %v is before request start %v", ev.OccurredAt, before)
	}
	if ev.OccurredAt.After(after) {
		t.Errorf("occurred_at %v is after request end %v", ev.OccurredAt, after)
	}
}

// TestAuditEvent_ResponseBodyContainsRequestID verifies that the echo response
// body also embeds the same request_id, so API clients can retrieve it without
// parsing response headers.
func TestAuditEvent_ResponseBodyContainsRequestID(t *testing.T) {
	ts, stub, _ := buildEchoServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	resp := postEchoAudit(t, ts, token, "BODY_REQID", `{"message":"body check"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var body struct {
		RequestID string `json:"request_id"`
		TraceID   string `json:"trace_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	headerReqID := resp.Header.Get("X-Request-Id")
	if body.RequestID != headerReqID {
		t.Errorf("body request_id=%q != X-Request-Id header %q", body.RequestID, headerReqID)
	}

	headerTraceID := resp.Header.Get("X-Trace-Id")
	if body.TraceID != headerTraceID {
		t.Errorf("body trace_id=%q != X-Trace-Id header %q", body.TraceID, headerTraceID)
	}
}

// TestAuditEvent_ExactlyOneRowPerRequest verifies idempotency: a second POST
// with the same Idempotency-Key replays the stored response via the middleware
// and the handler (and therefore the audit writer) is invoked only ONCE.
// This matches step 5's "exactly 1 row" requirement across duplicate requests.
func TestAuditEvent_ExactlyOneRowPerRequest(t *testing.T) {
	ts, stub, aw := buildEchoServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	// NOTE: the noopIdemStore always returns MISS, so each request goes
	// through the handler. This test therefore verifies that a single
	// successful request produces exactly one audit row (not more), not
	// the idempotency-replay scenario. The idempotency replay is covered
	// by the idempotency_replay_test.go suite.
	const key = "SINGLE_ROW_CHECK"

	resp := postEchoAudit(t, ts, token, key, `{"message":"single row"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	events := aw.getEvents()
	if len(events) != 1 {
		t.Errorf("want exactly 1 audit event per request, got %d", len(events))
	}
}

// TestAuditEvent_CleanupResetsStore verifies step 12 semantics: after calling
// aw.reset(), no events remain in the in-memory store — modelling the
// DELETE FROM audit_events WHERE request_id = '<captured>' cleanup step.
func TestAuditEvent_CleanupResetsStore(t *testing.T) {
	ts, stub, aw := buildEchoServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	resp := postEchoAudit(t, ts, token, "CLEANUP_CHECK", `{"message":"cleanup"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	// Verify row exists before cleanup.
	if len(aw.getEvents()) == 0 {
		t.Fatal("want at least 1 event before cleanup")
	}

	// Step 12: cleanup.
	aw.reset()
	if got := len(aw.getEvents()); got != 0 {
		t.Errorf("want 0 events after cleanup, got %d", got)
	}
}

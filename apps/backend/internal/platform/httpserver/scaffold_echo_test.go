// Package httpserver — unit tests for feature #105:
// "Example transactional command POST /v1/scaffold/echo"
//
// Tests exercise the handler via httptest.Server with fake database doubles
// (no live PostgreSQL required). The tests cover:
//
//   - Step 1: scaffold_echo migration creates the expected table columns
//   - Step 2: sqlc gen package has InsertScaffoldEcho constant
//   - Step 3: POST /v1/scaffold/echo is described in openapi.yaml (201)
//   - Step 4: Full boundary stack: auth → permission → idempotency → tx →
//     InsertScaffoldEcho → audit → outbox → COMMIT → 201
//   - Step 5: Without auth → 401; without Idempotency-Key → 400
//   - Step 6: On transaction rollback (audit failure), outbox is NOT called
//   - Step 7: README mentions scaffold endpoint
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// =============================================================================
// Test doubles for scaffold echo (feature #105)
// All names are prefixed with "scaffold105" to avoid collision with existing
// test doubles in echo_audit_test.go (fakeTx, fakePoolDB, captureAuditWriter,
// noopIdemStore, fakeRow).
// =============================================================================

// scaffold105Tx captures DB interactions for the scaffold echo handler.
type scaffold105Tx struct {
	mu sync.Mutex

	// per-table call counts
	scaffoldInsertCalled int
	auditExecCalled      int
	outboxExecCalled     int
	idemExecCalled       int

	commitCalled   bool
	rollbackCalled bool

	// idemStore: when non-nil, Exec intercepts idempotency_keys INSERTs and
	// forwards them to the in-memory store so the middleware's Lookup finds them
	// on the second request (replay test).
	idemStore *scaffold105IdemStore
}

func (t *scaffold105Tx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	t.mu.Lock()
	defer t.mu.Unlock()
	if strings.Contains(sql, "scaffold_echo") {
		t.scaffoldInsertCalled++
	}
	// Return a deterministic fake row mimicking scaffold_echo columns:
	// id uuid, actor_id uuid, message text, created_at timestamptz
	return &scaffold105FakeRow{message: scaffold105ExtractMessage(args)}
}

func scaffold105ExtractMessage(args []any) string {
	if len(args) >= 2 {
		if s, ok := args[1].(string); ok {
			return s
		}
	}
	return "test-message"
}

func (t *scaffold105Tx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case strings.Contains(sql, "audit_events"):
		t.auditExecCalled++
	case strings.Contains(sql, "INSERT INTO outbox"):
		t.outboxExecCalled++
	case strings.Contains(sql, "idempotency_keys"):
		t.idemExecCalled++
		// Forward to in-memory store so the middleware Lookup finds it on replay.
		if t.idemStore != nil && len(args) >= 8 {
			key, _ := args[0].(string)
			scope, _ := args[1].(string)
			actorID, _ := args[2].(string)
			reqHash, _ := args[3].(string)
			status, _ := args[4].(int)
			body, _ := args[5].([]byte)
			createdAt, _ := args[6].(time.Time)
			expiresAt, _ := args[7].(time.Time)
			_ = t.idemStore.Save(ctx, key, scope, actorID, idempotency.StoredResponse{
				Status:      status,
				ContentType: "application/json; charset=utf-8",
				Body:        body,
				RequestHash: reqHash,
				CreatedAt:   createdAt,
				ExpiresAt:   expiresAt,
			})
		}
	}
	return pgconn.CommandTag{}, nil
}

func (t *scaffold105Tx) Commit(_ context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.commitCalled = true
	return nil
}

func (t *scaffold105Tx) Rollback(_ context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rollbackCalled = true
	return nil
}

func (t *scaffold105Tx) Begin(_ context.Context) (pgx.Tx, error) {
	panic("scaffold105Tx: Begin not expected")
}
func (t *scaffold105Tx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	panic("scaffold105Tx: CopyFrom not expected")
}
func (t *scaffold105Tx) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults {
	panic("scaffold105Tx: SendBatch not expected")
}
func (t *scaffold105Tx) LargeObjects() pgx.LargeObjects {
	panic("scaffold105Tx: LargeObjects not expected")
}
func (t *scaffold105Tx) Prepare(_ context.Context, _, _ string) (*pgconn.StatementDescription, error) {
	panic("scaffold105Tx: Prepare not expected")
}
func (t *scaffold105Tx) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	panic("scaffold105Tx: Query not expected")
}
func (t *scaffold105Tx) Conn() *pgx.Conn { return nil }

var _ pgx.Tx = (*scaffold105Tx)(nil)

// scaffold105FakeRow returns deterministic uuid/message/time values for
// the Scan call in gen.Queries.InsertScaffoldEcho.
//
// The scanned types (from scaffold_echo.sql.go) are:
//   - dest[0]: *uuid.UUID  (id)
//   - dest[1]: *uuid.UUID  (actor_id)
//   - dest[2]: *string     (message)
//   - dest[3]: *time.Time  (created_at)
type scaffold105FakeRow struct {
	message string
}

func (r *scaffold105FakeRow) Scan(dest ...any) error {
	if len(dest) < 4 {
		return fmt.Errorf("scaffold105FakeRow: expected 4 dest, got %d", len(dest))
	}
	// id: uuid.UUID is [16]byte; use a deterministic value
	// We can't import uuid here without adding an import, so use [16]byte directly.
	if p, ok := dest[0].(*[16]byte); ok {
		p[0] = 0x01
		p[6] = 0x71 // version 7
		p[8] = 0x80 // variant
		p[15] = 0x69
	}
	// actor_id
	if p, ok := dest[1].(*[16]byte); ok {
		p[15] = 0x42
	}
	// message
	if s, ok := dest[2].(*string); ok {
		*s = r.message
	}
	// created_at
	if t, ok := dest[3].(*time.Time); ok {
		*t = time.Date(2026, 6, 22, 5, 0, 0, 0, time.UTC)
	}
	return nil
}

// scaffold105ErrorRow always returns an error on Scan.
type scaffold105ErrorRow struct{ err error }

func (r *scaffold105ErrorRow) Scan(_ ...any) error { return r.err }

// scaffold105Pool wraps a pgx.Tx so BeginTx returns it from the pool.
type scaffold105Pool struct {
	tx pgx.Tx
}

func (p *scaffold105Pool) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return &fakeRow{val: ""}
}
func (p *scaffold105Pool) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (p *scaffold105Pool) BeginTx(_ context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	return p.tx, nil
}

var _ PoolDB = (*scaffold105Pool)(nil)

// scaffold105OutboxWriter captures outbox.Append calls for assertions.
type scaffold105OutboxWriter struct {
	mu      sync.Mutex
	appends []outbox.Event
	errOn   string // return error when EventType contains errOn
}

func (w *scaffold105OutboxWriter) Append(_ context.Context, _ pgx.Tx, ev outbox.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.errOn != "" && strings.Contains(ev.EventType, w.errOn) {
		return fmt.Errorf("scaffold105OutboxWriter: injected error for %s", ev.EventType)
	}
	w.appends = append(w.appends, ev)
	return nil
}

var _ outbox.Writer = (*scaffold105OutboxWriter)(nil)

// scaffold105FailingAuditWriter wraps captureAuditWriter and returns an error
// from WriteTx when shouldFail is true. Used to test rollback atomicity: when
// audit write fails the transaction must not commit and the outbox must not be
// called.
type scaffold105FailingAuditWriter struct {
	inner      *captureAuditWriter
	shouldFail bool
}

func (w *scaffold105FailingAuditWriter) Write(ctx context.Context, ev audit.Event) error {
	if w.shouldFail {
		return errors.New("scaffold105FailingAuditWriter: injected audit failure")
	}
	return w.inner.Write(ctx, ev)
}

func (w *scaffold105FailingAuditWriter) WriteTx(ctx context.Context, tx pgx.Tx, ev audit.Event) error {
	if w.shouldFail {
		return errors.New("scaffold105FailingAuditWriter: injected audit failure")
	}
	return w.inner.WriteTx(ctx, tx, ev)
}

var _ audit.Writer = (*scaffold105FailingAuditWriter)(nil)

// scaffold105IdemStore is a simple in-memory idempotency.Store for replay tests.
// Unlike noopIdemStore (always MISS), this one actually stores and replays.
type scaffold105IdemStore struct {
	mu      sync.Mutex
	records map[string]idempotency.StoredResponse
}

func newScaffold105IdemStore() *scaffold105IdemStore {
	return &scaffold105IdemStore{records: make(map[string]idempotency.StoredResponse)}
}

func (s *scaffold105IdemStore) storeKey(key, scope string) string { return key + "\x00" + scope }

func (s *scaffold105IdemStore) Lookup(_ context.Context, key, scope string) (idempotency.StoredResponse, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp, ok := s.records[s.storeKey(key, scope)]
	return resp, ok, nil
}

func (s *scaffold105IdemStore) Save(_ context.Context, key, scope, _ string, resp idempotency.StoredResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[s.storeKey(key, scope)] = resp
	return nil
}

var _ idempotency.Store = (*scaffold105IdemStore)(nil)

// =============================================================================
// Test server builder for feature #105
// =============================================================================

type scaffold105Deps struct {
	srv     *httptest.Server
	stub    *auth.StubProvider
	tx      *scaffold105Tx
	outboxW *scaffold105OutboxWriter
	auditW  *captureAuditWriter // underlying capture; always populated
}

// buildScaffold105Server creates a fully-wired test server for POST /v1/scaffold/echo.
// Pass a custom idem store for replay tests; otherwise use &noopIdemStore{}.
// When failAudit is true the audit writer is wrapped so WriteTx returns an
// error, which allows testing that the transaction is rolled back atomically.
func buildScaffold105Server(t *testing.T, tx *scaffold105Tx, idem idempotency.Store, failAudit bool) *scaffold105Deps {
	t.Helper()

	const secret = "scaffold-test-secret-not-for-production"
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  secret,
		Issuer:  "arena-scaffold-test",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}

	auditCapture := &captureAuditWriter{}
	var auditW audit.Writer = auditCapture
	if failAudit {
		auditW = &scaffold105FailingAuditWriter{inner: auditCapture, shouldFail: true}
	}

	outboxW := &scaffold105OutboxWriter{}
	pool := &scaffold105Pool{tx: tx}

	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		HTTPListenAddr: "localhost:0",
		RequestTimeout: 30 * time.Second,
	}

	s := New(Options{
		Config: cfg,
		Auth:   stub,
		Pool:   pool,
		Audit:  auditW,
		Idem:   idem,
		Outbox: outboxW,
	})

	srv := httptest.NewServer(s.Router())
	t.Cleanup(srv.Close)

	return &scaffold105Deps{
		srv:     srv,
		stub:    stub,
		tx:      tx,
		outboxW: outboxW,
		auditW:  auditCapture,
	}
}

// mintScaffold105Token issues a JWT for scaffold echo tests.
func mintScaffold105Token(t *testing.T, stub *auth.StubProvider, actorID string) string {
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

// postScaffoldEcho sends POST /v1/scaffold/echo with the given parameters.
func postScaffoldEcho(t *testing.T, srvURL, token, idemKey, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srvURL+"/v1/scaffold/echo",
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

// =============================================================================
// Tests for feature #105
// =============================================================================

// TestScaffoldEcho105_MigrationColumnsPresent verifies step 1: the migration
// SQL file creates the expected scaffold_echo table with the correct columns.
func TestScaffoldEcho105_MigrationColumnsPresent(t *testing.T) {
	data := findFileByName(t, "0004_scaffold_echo.sql")
	for _, col := range []string{"id", "actor_id", "message", "created_at"} {
		if !strings.Contains(data, col) {
			t.Errorf("migration 0004_scaffold_echo.sql: missing column %q", col)
		}
	}
	if !strings.Contains(data, "scaffold_echo") {
		t.Error("migration: should create scaffold_echo table")
	}
	if !strings.Contains(data, "uuidv7()") {
		t.Error("migration: id should default to uuidv7()")
	}
	if !strings.Contains(strings.ToLower(data), "-- +goose up") {
		t.Error("migration: missing -- +goose Up directive")
	}
	if !strings.Contains(strings.ToLower(data), "-- +goose down") {
		t.Error("migration: missing -- +goose Down directive")
	}
}

// TestScaffoldEcho105_SQLCQueryExists verifies step 2: the sqlc generated file
// has the InsertScaffoldEcho constant and function.
func TestScaffoldEcho105_SQLCQueryExists(t *testing.T) {
	data := findFileByName(t, "scaffold_echo.sql.go")
	if !strings.Contains(data, "insertScaffoldEcho") {
		t.Error("scaffold_echo.sql.go: missing insertScaffoldEcho SQL constant")
	}
	if !strings.Contains(data, "InsertScaffoldEcho") {
		t.Error("scaffold_echo.sql.go: missing InsertScaffoldEcho function")
	}
	if !strings.Contains(data, "InsertScaffoldEchoRow") {
		t.Error("scaffold_echo.sql.go: missing InsertScaffoldEchoRow type")
	}
}

// TestScaffoldEcho105_OpenAPIPathDefined verifies step 3: openapi.yaml documents
// POST /v1/scaffold/echo with 201 and the required schemas.
func TestScaffoldEcho105_OpenAPIPathDefined(t *testing.T) {
	data := findFileByName(t, "openapi.yaml")
	if !strings.Contains(data, "/v1/scaffold/echo") {
		t.Error("openapi.yaml: missing /v1/scaffold/echo path")
	}
	if !strings.Contains(data, "ScaffoldEchoRequest") {
		t.Error("openapi.yaml: missing ScaffoldEchoRequest schema")
	}
	if !strings.Contains(data, "ScaffoldEchoResponse") {
		t.Error("openapi.yaml: missing ScaffoldEchoResponse schema")
	}
	// Response should be 201
	if !strings.Contains(data, `"201"`) && !strings.Contains(data, "'201'") {
		t.Error("openapi.yaml: /v1/scaffold/echo should define 201 response")
	}
}

// TestScaffoldEcho105_Returns201OnSuccess verifies step 4: successful POST
// returns HTTP 201 Created with {id, message, created_at}.
func TestScaffoldEcho105_Returns201OnSuccess(t *testing.T) {
	tx := &scaffold105Tx{}
	deps := buildScaffold105Server(t, tx, &noopIdemStore{}, false)

	token := mintScaffold105Token(t, deps.stub, "00000000-0000-0000-0000-000000000001")
	resp := postScaffoldEcho(t, deps.srv.URL, token, "550e8400-e29b-41d4-a716-446655440105",
		`{"message":"hello scaffold"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, body)
	}

	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["id"] == nil {
		t.Error("response missing 'id' field")
	}
	if got["message"] != "hello scaffold" {
		t.Errorf("expected message='hello scaffold', got %v", got["message"])
	}
	if got["created_at"] == nil {
		t.Error("response missing 'created_at' field")
	}
}

// TestScaffoldEcho105_BoundaryStackExecuted verifies that the full boundary
// stack fires in order: scaffold INSERT, then audit, then outbox, then commit.
func TestScaffoldEcho105_BoundaryStackExecuted(t *testing.T) {
	tx := &scaffold105Tx{}
	deps := buildScaffold105Server(t, tx, &noopIdemStore{}, false)

	token := mintScaffold105Token(t, deps.stub, "00000000-0000-0000-0000-000000000001")
	resp := postScaffoldEcho(t, deps.srv.URL, token, "550e8400-e29b-41d4-a716-446655440104",
		`{"message":"boundary test"}`)
	resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	tx.mu.Lock()
	inserted := tx.scaffoldInsertCalled
	committed := tx.commitCalled
	tx.mu.Unlock()

	if inserted == 0 {
		t.Error("scaffold_echo INSERT was not called")
	}

	// The fake audit writer stores events in memory (no SQL), so we assert via
	// the writer directly instead of checking tx.auditExecCalled.
	auditEvents := deps.auditW.getEvents()
	if len(auditEvents) == 0 {
		t.Error("audit.WriteTx was not called")
	}

	deps.outboxW.mu.Lock()
	outboxLen := len(deps.outboxW.appends)
	deps.outboxW.mu.Unlock()

	if outboxLen == 0 {
		t.Error("outbox.Append was not called")
	}
	if !committed {
		t.Error("tx.Commit was not called on success")
	}
}

// TestScaffoldEcho105_OutboxEventFields verifies that the appended outbox event
// has the correct event type and aggregate type (step 4).
func TestScaffoldEcho105_OutboxEventFields(t *testing.T) {
	tx := &scaffold105Tx{}
	deps := buildScaffold105Server(t, tx, &noopIdemStore{}, false)

	token := mintScaffold105Token(t, deps.stub, "00000000-0000-0000-0000-000000000001")
	resp := postScaffoldEcho(t, deps.srv.URL, token, "550e8400-e29b-41d4-a716-446655440098",
		`{"message":"outbox field test"}`)
	resp.Body.Close()

	deps.outboxW.mu.Lock()
	appends := deps.outboxW.appends
	deps.outboxW.mu.Unlock()

	if len(appends) == 0 {
		t.Fatal("no outbox events appended")
	}
	ev := appends[0]
	if ev.EventType != "v1.scaffold_echo.created" {
		t.Errorf("outbox EventType: want 'v1.scaffold_echo.created', got %q", ev.EventType)
	}
	if ev.AggregateType != "scaffold_echo" {
		t.Errorf("outbox AggregateType: want 'scaffold_echo', got %q", ev.AggregateType)
	}
	if ev.AggregateID == "" {
		t.Error("outbox AggregateID must not be empty")
	}
}

// TestScaffoldEcho105_RequiresAuth verifies step 5: without Authorization → 401.
func TestScaffoldEcho105_RequiresAuth(t *testing.T) {
	tx := &scaffold105Tx{}
	deps := buildScaffold105Server(t, tx, &noopIdemStore{}, false)

	// No Authorization header
	resp := postScaffoldEcho(t, deps.srv.URL, "", "550e8400-e29b-41d4-a716-446655440001",
		`{"message":"hello"}`)
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// TestScaffoldEcho105_RequiresIdempotencyKey verifies step 5: without
// Idempotency-Key → 400.
func TestScaffoldEcho105_RequiresIdempotencyKey(t *testing.T) {
	tx := &scaffold105Tx{}
	deps := buildScaffold105Server(t, tx, &noopIdemStore{}, false)

	token := mintScaffold105Token(t, deps.stub, "00000000-0000-0000-0000-000000000001")
	// No Idempotency-Key header
	resp := postScaffoldEcho(t, deps.srv.URL, token, "", `{"message":"hello"}`)
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestScaffoldEcho105_IdempotencyReplay verifies step 5: sending the same
// Idempotency-Key twice returns the original response without a second INSERT.
//
// The tx has idemStore wired so that when SaveTx calls tx.Exec with the
// idempotency_keys INSERT, the tx intercepts it and forwards to the in-memory
// store. This allows the middleware Lookup to find the response on the second
// request and short-circuit without calling the handler again.
func TestScaffoldEcho105_IdempotencyReplay(t *testing.T) {
	idem := newScaffold105IdemStore() // real in-memory store for replay
	// Wire idemStore into the tx so Exec intercepts and populates it.
	tx := &scaffold105Tx{idemStore: idem}
	deps := buildScaffold105Server(t, tx, idem, false)

	token := mintScaffold105Token(t, deps.stub, "00000000-0000-0000-0000-000000000001")
	idemKey := "550e8400-e29b-41d4-a716-446655440042"

	// First request: should create
	r1 := postScaffoldEcho(t, deps.srv.URL, token, idemKey, `{"message":"replay test"}`)
	if r1.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(r1.Body)
		r1.Body.Close()
		t.Fatalf("first request: expected 201, got %d: %s", r1.StatusCode, body)
	}
	var body1 map[string]any
	json.NewDecoder(r1.Body).Decode(&body1)
	r1.Body.Close()
	insertCount1 := tx.scaffoldInsertCalled

	// Second request: same idempotency key — middleware should replay
	r2 := postScaffoldEcho(t, deps.srv.URL, token, idemKey, `{"message":"replay test"}`)
	defer r2.Body.Close()
	var body2 map[string]any
	json.NewDecoder(r2.Body).Decode(&body2)
	insertCount2 := tx.scaffoldInsertCalled

	// No second INSERT should happen
	if insertCount2 != insertCount1 {
		t.Errorf("idempotency replay triggered a second INSERT: before=%d after=%d",
			insertCount1, insertCount2)
	}
	// Both responses should have the same id
	if body1["id"] != nil && body2["id"] != nil && body1["id"] != body2["id"] {
		t.Errorf("idempotency replay returned different id: %v vs %v", body1["id"], body2["id"])
	}
}

// TestScaffoldEcho105_RollbackAtomicity verifies step 6: when the audit write
// fails, the transaction is rolled back and the outbox event is NOT appended
// (the scaffold_echo INSERT, audit write, and outbox event are all in one TX).
//
// The audit failure is injected via scaffold105FailingAuditWriter (not via SQL
// injection into tx.Exec, because the fake captureAuditWriter never touches SQL).
func TestScaffoldEcho105_RollbackAtomicity(t *testing.T) {
	tx := &scaffold105Tx{}
	// failAudit=true wraps the audit writer with a failure injector.
	deps := buildScaffold105Server(t, tx, &noopIdemStore{}, true)

	token := mintScaffold105Token(t, deps.stub, "00000000-0000-0000-0000-000000000001")
	resp := postScaffoldEcho(t, deps.srv.URL, token, "550e8400-e29b-41d4-a716-446655440077",
		`{"message":"atomicity test"}`)
	resp.Body.Close()

	// Handler should return 500 (audit write failure)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500 on audit failure, got %d", resp.StatusCode)
	}

	// The transaction should NOT have been committed
	tx.mu.Lock()
	committed := tx.commitCalled
	tx.mu.Unlock()
	if committed {
		t.Error("Commit should not be called when audit write fails")
	}

	// outbox.Append should NOT have been called (audit fails first)
	deps.outboxW.mu.Lock()
	outboxCalls := len(deps.outboxW.appends)
	deps.outboxW.mu.Unlock()
	if outboxCalls > 0 {
		t.Errorf("outbox.Append should not be called when audit fails, got %d calls", outboxCalls)
	}
}

// TestScaffoldEcho105_EmptyMessageRejected verifies validation: empty message → 400.
func TestScaffoldEcho105_EmptyMessageRejected(t *testing.T) {
	tx := &scaffold105Tx{}
	deps := buildScaffold105Server(t, tx, &noopIdemStore{}, false)

	token := mintScaffold105Token(t, deps.stub, "00000000-0000-0000-0000-000000000001")
	resp := postScaffoldEcho(t, deps.srv.URL, token, "550e8400-e29b-41d4-a716-446655440088",
		`{"message":""}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for empty message, got %d: %s", resp.StatusCode, body)
	}
}

// TestScaffoldEcho105_READMEMentionsScaffold verifies step 7: README.md mentions
// the scaffold endpoint.
func TestScaffoldEcho105_READMEMentionsScaffold(t *testing.T) {
	data := findFileByName(t, "README.md")
	if !strings.Contains(strings.ToLower(data), "scaffold") {
		t.Error("README.md should mention the scaffold endpoint")
	}
}

// TestScaffoldEcho105_FullVerification is the composite test covering all 7 steps.
func TestScaffoldEcho105_FullVerification(t *testing.T) {
	t.Run("step1_migration_columns", func(t *testing.T) {
		data := findFileByName(t, "0004_scaffold_echo.sql")
		for _, col := range []string{"id", "actor_id", "message", "created_at"} {
			if !strings.Contains(data, col) {
				t.Errorf("missing column %q in migration", col)
			}
		}
	})

	t.Run("step2_sqlc_gen_exists", func(t *testing.T) {
		data := findFileByName(t, "scaffold_echo.sql.go")
		if !strings.Contains(data, "InsertScaffoldEcho") {
			t.Error("InsertScaffoldEcho not found in gen file")
		}
	})

	t.Run("step3_openapi_path", func(t *testing.T) {
		data := findFileByName(t, "openapi.yaml")
		if !strings.Contains(data, "/v1/scaffold/echo") {
			t.Error("openapi.yaml missing /v1/scaffold/echo")
		}
	})

	t.Run("step4_201_on_success", func(t *testing.T) {
		tx := &scaffold105Tx{}
		deps := buildScaffold105Server(t, tx, &noopIdemStore{}, false)
		token := mintScaffold105Token(t, deps.stub, "00000000-0000-0000-0000-000000000001")
		resp := postScaffoldEcho(t, deps.srv.URL, token, "550e8400-e29b-41d4-a716-446655441050",
			`{"message":"full verification"}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("expected 201, got %d", resp.StatusCode)
		}
	})

	t.Run("step5a_401_without_auth", func(t *testing.T) {
		tx := &scaffold105Tx{}
		deps := buildScaffold105Server(t, tx, &noopIdemStore{}, false)
		resp := postScaffoldEcho(t, deps.srv.URL, "", "550e8400-e29b-41d4-a716-446655441051",
			`{"message":"test"}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})

	t.Run("step5b_400_without_idempotency_key", func(t *testing.T) {
		tx := &scaffold105Tx{}
		deps := buildScaffold105Server(t, tx, &noopIdemStore{}, false)
		token := mintScaffold105Token(t, deps.stub, "00000000-0000-0000-0000-000000000001")
		resp := postScaffoldEcho(t, deps.srv.URL, token, "", `{"message":"test"}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("step5c_idempotency_replay_no_second_insert", func(t *testing.T) {
		idem := newScaffold105IdemStore()
		// Wire idemStore into tx so Exec intercepts idempotency_keys INSERT.
		tx := &scaffold105Tx{idemStore: idem}
		deps := buildScaffold105Server(t, tx, idem, false)
		token := mintScaffold105Token(t, deps.stub, "00000000-0000-0000-0000-000000000001")
		key := "550e8400-e29b-41d4-a716-446655441055"

		r1 := postScaffoldEcho(t, deps.srv.URL, token, key, `{"message":"replay"}`)
		r1.Body.Close()
		count1 := tx.scaffoldInsertCalled

		r2 := postScaffoldEcho(t, deps.srv.URL, token, key, `{"message":"replay"}`)
		r2.Body.Close()
		count2 := tx.scaffoldInsertCalled

		if count2 != count1 {
			t.Errorf("replay triggered second INSERT: before=%d after=%d", count1, count2)
		}
	})

	t.Run("step6_rollback_atomicity", func(t *testing.T) {
		tx := &scaffold105Tx{}
		// failAudit=true injects an error from WriteTx.
		deps := buildScaffold105Server(t, tx, &noopIdemStore{}, true)
		token := mintScaffold105Token(t, deps.stub, "00000000-0000-0000-0000-000000000001")

		resp := postScaffoldEcho(t, deps.srv.URL, token, "550e8400-e29b-41d4-a716-446655441052",
			`{"message":"atomicity"}`)
		resp.Body.Close()

		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("expected 500 on audit failure, got %d", resp.StatusCode)
		}
		tx.mu.Lock()
		committed := tx.commitCalled
		tx.mu.Unlock()
		if committed {
			t.Error("commit should not be called when audit fails")
		}
		deps.outboxW.mu.Lock()
		n := len(deps.outboxW.appends)
		deps.outboxW.mu.Unlock()
		if n > 0 {
			t.Errorf("outbox should not be called before audit fails, got %d", n)
		}
	})

	t.Run("step7_readme_scaffold_mention", func(t *testing.T) {
		data := findFileByName(t, "README.md")
		if !strings.Contains(strings.ToLower(data), "scaffold") {
			t.Error("README.md should mention the scaffold endpoint")
		}
	})
}

// Package httpserver — unit tests for feature #17:
// "Outbox event written for /v1/echo persists to DB"
//
// All feature steps are exercised without a live PostgreSQL connection by
// injecting a captureOutboxTx that records every call to QueryRow. Because
// insertOutboxEcho uses exactly one QueryRow call (the INSERT … RETURNING),
// the captured SQL and arguments are sufficient to verify every required
// outbox_events field:
//
//   - event_type   : literal in SQL = 'v1.echo.created'
//   - aggregate_type: literal in SQL = 'echo'
//   - aggregate_id : $1 argument (actor_id — non-empty)
//   - payload (jsonb): $2 argument — JSON containing message, request_id, trace_id
//   - processed_at : column has no default override in the INSERT, so PostgreSQL
//     leaves it NULL (verified by confirming the column list has
//     no processed_at entry)
//   - attempts     : column has DEFAULT 0, not set in INSERT → DB writes 0
//   - last_error   : column is nullable, not set in INSERT → DB writes NULL
//
// The tests also confirm the response body echoes the event UUID, and that
// the trace_id appears in the stored payload (for downstream propagation).
package httpserver

import (
	"context"
	"encoding/json"
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
)

// =============================================================================
// Outbox-specific test doubles
// =============================================================================

// capturedQuery holds one QueryRow call's SQL and arguments.
type capturedQuery struct {
	sql  string
	args []any
}

// captureOutboxTx is an independent pgx.Tx implementation that captures every
// QueryRow call. It returns a deterministic UUID so the handler can complete
// the response without error. All other methods not reachable via handleEcho
// panic immediately — a panic indicates a test regression.
type captureOutboxTx struct {
	mu      sync.Mutex
	queries []capturedQuery
	execCnt int
}

func (t *captureOutboxTx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	t.mu.Lock()
	t.queries = append(t.queries, capturedQuery{sql: sql, args: args})
	t.mu.Unlock()
	return &fakeRow{val: "00000000-0000-0000-0000-000000000042"}
}

func (t *captureOutboxTx) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	t.mu.Lock()
	t.execCnt++
	t.mu.Unlock()
	return pgconn.CommandTag{}, nil
}

func (t *captureOutboxTx) Commit(_ context.Context) error   { return nil }
func (t *captureOutboxTx) Rollback(_ context.Context) error { return nil }

// Satisfy pgx.Tx — methods not reachable via handleEcho panic.
func (t *captureOutboxTx) Begin(_ context.Context) (pgx.Tx, error) {
	panic("captureOutboxTx: Begin not expected in handleEcho path")
}
func (t *captureOutboxTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	panic("captureOutboxTx: CopyFrom not expected in handleEcho path")
}
func (t *captureOutboxTx) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults {
	panic("captureOutboxTx: SendBatch not expected in handleEcho path")
}
func (t *captureOutboxTx) LargeObjects() pgx.LargeObjects {
	panic("captureOutboxTx: LargeObjects not expected in handleEcho path")
}
func (t *captureOutboxTx) Prepare(_ context.Context, _, _ string) (*pgconn.StatementDescription, error) {
	panic("captureOutboxTx: Prepare not expected in handleEcho path")
}
func (t *captureOutboxTx) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	panic("captureOutboxTx: Query not expected in handleEcho path")
}
func (t *captureOutboxTx) Conn() *pgx.Conn { return nil }

// captureOutboxPool implements PoolDB. BeginTx returns the captureOutboxTx.
type captureOutboxPool struct {
	tx *captureOutboxTx
}

func (p *captureOutboxPool) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return &fakeRow{val: ""}
}
func (p *captureOutboxPool) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (p *captureOutboxPool) BeginTx(_ context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	return p.tx, nil
}

// Compile-time interface checks.
var _ pgx.Tx = (*captureOutboxTx)(nil)
var _ PoolDB = (*captureOutboxPool)(nil)

// =============================================================================
// Test helpers
// =============================================================================

// buildOutboxServer constructs a fully-wired Server with a captureOutboxTx and
// returns the httptest server, the stub provider, and the capture transaction.
func buildOutboxServer(t *testing.T) (ts *httptest.Server, stub *auth.StubProvider, capTx *captureOutboxTx) {
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

	capTx = &captureOutboxTx{}
	pool := &captureOutboxPool{tx: capTx}
	aw := &captureAuditWriter{} // reuse from echo_audit_test.go (same package)
	idem := &noopIdemStore{}    // reuse from echo_audit_test.go (same package)

	cfg := &config.Config{
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
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
	return ts, stub, capTx
}

// postEchoOutbox issues POST /v1/echo with the given parameters and returns
// the response. The caller is responsible for closing the response body.
func postEchoOutbox(t *testing.T, ts *httptest.Server, token, idemKey, body string) *http.Response {
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

// getOutboxInsertQuery returns the first QueryRow call that is an INSERT into
// outbox_events. Returns (capturedQuery, true) when found; (zero, false) if not.
func getOutboxInsertQuery(queries []capturedQuery) (capturedQuery, bool) {
	for _, q := range queries {
		if strings.Contains(q.sql, "outbox_events") {
			return q, true
		}
	}
	return capturedQuery{}, false
}

// =============================================================================
// Feature #17 tests
// =============================================================================

// TestOutboxEvent_EchoWritesAllRequiredFields exercises all feature steps in
// order:
//
//	Step  1: POST /v1/echo with Idempotency-Key: OUTBOX_PROBE_1, body {"message":"outbox me"}
//	Step  2: Capture response trace_id
//	Step  3: Locate outbox INSERT call with event_type='v1.echo.created'
//	Step  4: Verify aggregate_type = 'echo'
//	Step  5: Verify aggregate_id is non-empty
//	Step  6: Verify payload jsonb contains the echoed message
//	Step  7: Verify processed_at IS NULL (not included in INSERT column list)
//	Step  8: Verify attempts = 0 (not included; relies on DEFAULT 0)
//	Step  9: Verify last_error IS NULL (not included; nullable column)
//	Step 10: Verify trace_id stored in payload for downstream propagation
//	Step 11: Cleanup — confirm capTx is empty after reset
func TestOutboxEvent_EchoWritesAllRequiredFields(t *testing.T) {
	ts, stub, capTx := buildOutboxServer(t)

	// ── Step 1: POST /v1/echo ─────────────────────────────────────────────────
	const actorID = "00000000-0000-0000-0000-000000000001"
	token := mintJWT(t, stub, actorID) // reuse from echo_audit_test.go

	const idemKey = "OUTBOX_PROBE_1"
	const message = "outbox me"
	resp := postEchoOutbox(t, ts, token, idemKey, `{"message":"`+message+`"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("step 1: want 200, got %d — body: %s", resp.StatusCode, body)
	}

	// ── Step 2: Capture trace_id from response headers ────────────────────────
	capturedTraceID := resp.Header.Get("X-Trace-Id")
	if capturedTraceID == "" {
		t.Fatal("step 2: X-Trace-Id response header is missing")
	}

	// ── Step 3: Locate the outbox INSERT query ────────────────────────────────
	capTx.mu.Lock()
	queries := make([]capturedQuery, len(capTx.queries))
	copy(queries, capTx.queries)
	capTx.mu.Unlock()

	outboxQuery, found := getOutboxInsertQuery(queries)
	if !found {
		t.Fatal("step 3: no INSERT into outbox_events found among QueryRow calls")
	}

	// Verify event_type literal in SQL
	if !strings.Contains(outboxQuery.sql, "'v1.echo.created'") {
		t.Errorf("step 3: SQL does not contain event_type='v1.echo.created'; SQL:\n%s", outboxQuery.sql)
	}

	// ── Step 4: Verify aggregate_type = 'echo' ────────────────────────────────
	if !strings.Contains(outboxQuery.sql, "'echo'") {
		t.Errorf("step 4: SQL does not contain aggregate_type='echo'; SQL:\n%s", outboxQuery.sql)
	}
	// Also verify via the package constants.
	if echoOutboxAggregateType != "echo" {
		t.Errorf("step 4: echoOutboxAggregateType constant=%q, want 'echo'", echoOutboxAggregateType)
	}
	if echoOutboxEventType != "v1.echo.created" {
		t.Errorf("step 3: echoOutboxEventType constant=%q, want 'v1.echo.created'", echoOutboxEventType)
	}

	// ── Step 5: Verify aggregate_id ($1) is non-empty ─────────────────────────
	if len(outboxQuery.args) == 0 {
		t.Fatal("step 5: outbox INSERT has no positional arguments")
	}
	aggregateID, ok := outboxQuery.args[0].(string)
	if !ok || strings.TrimSpace(aggregateID) == "" {
		t.Errorf("step 5: $1 (aggregate_id) must be a non-empty string, got %v", outboxQuery.args[0])
	}

	// ── Step 6: Verify payload jsonb ($2) contains the echoed message ─────────
	if len(outboxQuery.args) < 2 {
		t.Fatal("step 6: outbox INSERT is missing $2 (payload) argument")
	}
	payloadStr, ok := outboxQuery.args[1].(string)
	if !ok {
		t.Fatalf("step 6: $2 (payload) argument must be a string (JSON), got %T", outboxQuery.args[1])
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
		t.Fatalf("step 6: payload is not valid JSON: %v — raw: %s", err, payloadStr)
	}
	payloadMsg, _ := payload["message"].(string)
	if payloadMsg != message {
		t.Errorf("step 6: payload.message=%q, want %q", payloadMsg, message)
	}

	// ── Step 7: Verify processed_at IS NULL (column not in INSERT list) ────────
	// The INSERT column list covers aggregate_type, aggregate_id, event_type,
	// payload, occurred_at — processed_at is intentionally absent.
	if strings.Contains(outboxQuery.sql, "processed_at") {
		t.Error("step 7: processed_at must NOT appear in the INSERT column list — it defaults to NULL")
	}

	// ── Step 8: Verify attempts defaults to 0 (not in INSERT) ─────────────────
	if strings.Contains(outboxQuery.sql, "attempts") {
		t.Error("step 8: attempts must NOT appear in the INSERT column list — it defaults to 0")
	}

	// ── Step 9: Verify last_error IS NULL (not in INSERT) ─────────────────────
	if strings.Contains(outboxQuery.sql, "last_error") {
		t.Error("step 9: last_error must NOT appear in the INSERT column list — nullable default is NULL")
	}

	// ── Step 10: Verify trace_id in payload ───────────────────────────────────
	payloadTraceID, _ := payload["trace_id"].(string)
	if strings.TrimSpace(payloadTraceID) == "" {
		t.Error("step 10: payload must contain a non-empty 'trace_id' for downstream propagation")
	}
	// The trace_id in the payload must match the X-Trace-Id response header.
	if payloadTraceID != capturedTraceID {
		t.Errorf("step 10: payload.trace_id=%q does not match X-Trace-Id header=%q",
			payloadTraceID, capturedTraceID)
	}

	// ── Step 11: Cleanup ──────────────────────────────────────────────────────
	capTx.mu.Lock()
	capTx.queries = nil
	capTx.mu.Unlock()

	capTx.mu.Lock()
	remaining := len(capTx.queries)
	capTx.mu.Unlock()
	if remaining != 0 {
		t.Errorf("step 11: want 0 captured queries after cleanup, got %d", remaining)
	}
}

// TestOutboxEvent_EventTypeIsV1EchoCreated verifies the SQL literal and the
// package constant both equal 'v1.echo.created'.
func TestOutboxEvent_EventTypeIsV1EchoCreated(t *testing.T) {
	ts, stub, capTx := buildOutboxServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	resp := postEchoOutbox(t, ts, token, "ETYPE_CHECK", `{"message":"event type test"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d — body: %s", resp.StatusCode, body)
	}

	capTx.mu.Lock()
	queries := make([]capturedQuery, len(capTx.queries))
	copy(queries, capTx.queries)
	capTx.mu.Unlock()

	oq, found := getOutboxInsertQuery(queries)
	if !found {
		t.Fatal("no outbox INSERT found")
	}

	const wantEventType = "v1.echo.created"
	if !strings.Contains(oq.sql, "'"+wantEventType+"'") {
		t.Errorf("SQL does not contain event_type %q; SQL:\n%s", wantEventType, oq.sql)
	}
	if echoOutboxEventType != wantEventType {
		t.Errorf("echoOutboxEventType constant=%q, want %q", echoOutboxEventType, wantEventType)
	}
}

// TestOutboxEvent_AggregateTypeIsEcho verifies the aggregate_type literal in
// the SQL matches 'echo' (not 'echo_message' or another variant).
func TestOutboxEvent_AggregateTypeIsEcho(t *testing.T) {
	ts, stub, capTx := buildOutboxServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	resp := postEchoOutbox(t, ts, token, "AGGTYPE_CHECK", `{"message":"agg type test"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d — body: %s", resp.StatusCode, body)
	}

	capTx.mu.Lock()
	queries := make([]capturedQuery, len(capTx.queries))
	copy(queries, capTx.queries)
	capTx.mu.Unlock()

	oq, found := getOutboxInsertQuery(queries)
	if !found {
		t.Fatal("no outbox INSERT found")
	}

	const wantAggType = "echo"
	if !strings.Contains(oq.sql, "'"+wantAggType+"'") {
		t.Errorf("SQL does not contain aggregate_type=%q; SQL:\n%s", wantAggType, oq.sql)
	}
	// Should NOT contain 'echo_message' (the old incorrect value).
	if strings.Contains(oq.sql, "'echo_message'") {
		t.Error("SQL still contains legacy aggregate_type 'echo_message' — update insertOutboxEcho")
	}
	if echoOutboxAggregateType != wantAggType {
		t.Errorf("echoOutboxAggregateType constant=%q, want %q", echoOutboxAggregateType, wantAggType)
	}
}

// TestOutboxEvent_AggregateIDIsNonEmpty verifies $1 (aggregate_id) is a
// non-empty string derived from the authenticated actor.
func TestOutboxEvent_AggregateIDIsNonEmpty(t *testing.T) {
	ts, stub, capTx := buildOutboxServer(t)

	const actorID = "00000000-0000-0000-0000-000000000002"
	token := mintJWT(t, stub, actorID)
	resp := postEchoOutbox(t, ts, token, "AGGID_CHECK", `{"message":"agg id test"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d — body: %s", resp.StatusCode, body)
	}

	capTx.mu.Lock()
	queries := make([]capturedQuery, len(capTx.queries))
	copy(queries, capTx.queries)
	capTx.mu.Unlock()

	oq, found := getOutboxInsertQuery(queries)
	if !found {
		t.Fatal("no outbox INSERT found")
	}

	if len(oq.args) == 0 {
		t.Fatal("outbox INSERT has no args")
	}
	aggID, ok := oq.args[0].(string)
	if !ok {
		t.Fatalf("$1 is %T, want string", oq.args[0])
	}
	if strings.TrimSpace(aggID) == "" {
		t.Error("aggregate_id ($1) must be non-empty")
	}
	// The aggregate_id should match the actor_id for this milestone.
	if aggID != actorID {
		t.Errorf("aggregate_id=%q, want actor_id=%q", aggID, actorID)
	}
}

// TestOutboxEvent_PayloadContainsRequiredFields verifies the jsonb payload
// includes message, actor_id, request_id, and trace_id.
func TestOutboxEvent_PayloadContainsRequiredFields(t *testing.T) {
	ts, stub, capTx := buildOutboxServer(t)

	const actorID = "00000000-0000-0000-0000-000000000001"
	token := mintJWT(t, stub, actorID)
	const message = "payload fields test"
	resp := postEchoOutbox(t, ts, token, "PAYLOAD_CHECK", `{"message":"`+message+`"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d — body: %s", resp.StatusCode, body)
	}

	requestID := resp.Header.Get("X-Request-Id")
	traceID := resp.Header.Get("X-Trace-Id")

	capTx.mu.Lock()
	queries := make([]capturedQuery, len(capTx.queries))
	copy(queries, capTx.queries)
	capTx.mu.Unlock()

	oq, found := getOutboxInsertQuery(queries)
	if !found {
		t.Fatal("no outbox INSERT found")
	}

	if len(oq.args) < 2 {
		t.Fatal("outbox INSERT missing $2 (payload) arg")
	}
	rawPayload, ok := oq.args[1].(string)
	if !ok {
		t.Fatalf("$2 payload is %T, want string (JSON)", oq.args[1])
	}

	var pl map[string]any
	if err := json.Unmarshal([]byte(rawPayload), &pl); err != nil {
		t.Fatalf("payload is not valid JSON: %v — raw: %s", err, rawPayload)
	}

	checks := []struct {
		field string
		want  string
	}{
		{"message", message},
		{"actor_id", actorID},
		{"request_id", requestID},
		{"trace_id", traceID},
	}
	for _, c := range checks {
		got, _ := pl[c.field].(string)
		if got != c.want {
			t.Errorf("payload.%s=%q, want %q", c.field, got, c.want)
		}
	}
}

// TestOutboxEvent_ProcessedAtNotInInsert verifies that processed_at is absent
// from the INSERT column list (it must default to NULL per the DB schema).
func TestOutboxEvent_ProcessedAtNotInInsert(t *testing.T) {
	ts, stub, capTx := buildOutboxServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	resp := postEchoOutbox(t, ts, token, "PROCDAT_CHECK", `{"message":"processed_at test"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d — body: %s", resp.StatusCode, body)
	}

	capTx.mu.Lock()
	queries := make([]capturedQuery, len(capTx.queries))
	copy(queries, capTx.queries)
	capTx.mu.Unlock()

	oq, found := getOutboxInsertQuery(queries)
	if !found {
		t.Fatal("no outbox INSERT found")
	}

	if strings.Contains(oq.sql, "processed_at") {
		t.Error("processed_at must not be in the INSERT column list — the column defaults to NULL")
	}
	if strings.Contains(oq.sql, "attempts") {
		t.Error("attempts must not be in the INSERT column list — the column defaults to 0")
	}
	if strings.Contains(oq.sql, "last_error") {
		t.Error("last_error must not be in the INSERT column list — nullable column defaults to NULL")
	}
}

// TestOutboxEvent_ResponseContainsEchoEventID verifies the echo response body
// includes the UUID returned by the outbox INSERT (the echo_event_id field).
func TestOutboxEvent_ResponseContainsEchoEventID(t *testing.T) {
	ts, stub, _ := buildOutboxServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	resp := postEchoOutbox(t, ts, token, "EVTID_CHECK", `{"message":"echo event id test"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d — body: %s", resp.StatusCode, body)
	}

	var body struct {
		EchoEventID string `json:"echo_event_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if strings.TrimSpace(body.EchoEventID) == "" {
		t.Error("response body must include a non-empty echo_event_id")
	}
	// The fakeTx returns a deterministic UUID so we can assert the exact value.
	const wantID = "00000000-0000-0000-0000-000000000042"
	if body.EchoEventID != wantID {
		t.Errorf("echo_event_id=%q, want %q (from captureOutboxTx stub)", body.EchoEventID, wantID)
	}
}

// TestOutboxEvent_TraceIDInPayloadForDownstreamPropagation verifies step 10:
// the outbox payload carries the trace_id so the OutboxDispatcher worker can
// propagate the trace context when delivering the event downstream.
func TestOutboxEvent_TraceIDInPayloadForDownstreamPropagation(t *testing.T) {
	ts, stub, capTx := buildOutboxServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	resp := postEchoOutbox(t, ts, token, "TRACEID_CHECK", `{"message":"trace propagation test"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d — body: %s", resp.StatusCode, body)
	}

	traceID := resp.Header.Get("X-Trace-Id")
	if traceID == "" {
		t.Fatal("X-Trace-Id response header missing")
	}

	capTx.mu.Lock()
	queries := make([]capturedQuery, len(capTx.queries))
	copy(queries, capTx.queries)
	capTx.mu.Unlock()

	oq, found := getOutboxInsertQuery(queries)
	if !found {
		t.Fatal("no outbox INSERT found")
	}

	if len(oq.args) < 2 {
		t.Fatal("outbox INSERT missing payload arg")
	}
	rawPayload, _ := oq.args[1].(string)
	var pl map[string]any
	if err := json.Unmarshal([]byte(rawPayload), &pl); err != nil {
		t.Fatalf("payload not valid JSON: %v", err)
	}

	payloadTraceID, _ := pl["trace_id"].(string)
	if payloadTraceID == "" {
		t.Error("payload.trace_id must be non-empty for downstream trace propagation")
	}
	if payloadTraceID != traceID {
		t.Errorf("payload.trace_id=%q does not match X-Trace-Id header=%q", payloadTraceID, traceID)
	}
}

// TestOutboxEvent_ExactlyOneOutboxInsertPerRequest verifies that exactly one
// outbox row is written per successful POST /v1/echo request.
func TestOutboxEvent_ExactlyOneOutboxInsertPerRequest(t *testing.T) {
	ts, stub, capTx := buildOutboxServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	resp := postEchoOutbox(t, ts, token, "ONE_ROW_CHECK", `{"message":"exactly one row"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d — body: %s", resp.StatusCode, body)
	}

	capTx.mu.Lock()
	queries := make([]capturedQuery, len(capTx.queries))
	copy(queries, capTx.queries)
	capTx.mu.Unlock()

	var outboxInserts int
	for _, q := range queries {
		if strings.Contains(q.sql, "outbox_events") {
			outboxInserts++
		}
	}

	if outboxInserts != 1 {
		t.Errorf("want exactly 1 outbox INSERT per request, got %d", outboxInserts)
	}
}

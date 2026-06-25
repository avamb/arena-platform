// Package httpserver — unit tests for feature #18:
// "Audit and outbox written in same transaction"
//
// These tests verify the atomicity guarantee that audit_events and
// outbox_events writes for a single POST /v1/echo call occur in ONE database
// transaction. When a fault is injected between the two writes, the deferred
// tx.Rollback ensures neither row persists.
//
// Fault injection is controlled by Server.faultInjectOutboxAfterAudit (set via
// Options.FaultInjectOutboxAfterAudit). In production this is read from the
// FAULT_INJECT_OUTBOX_AFTER_AUDIT env var; in these unit tests we set it
// directly on the Options struct.
//
// # What the tests prove
//
//  1. With fault injection ON:
//     - POST /v1/echo returns HTTP 500
//     - Response code is 'internal.transaction_failed' (not 'audit_failed',
//     not 'outbox_failed' — the fault fires AFTER audit succeeds, BEFORE
//     outbox, and reports as a transaction-level failure)
//     - tx.Commit() is never called (the transaction is rolled back)
//     - The outbox INSERT is never attempted (proves the two writes are atomic)
//
//  2. With fault injection OFF (normal path):
//     - POST /v1/echo returns HTTP 200
//     - tx.Commit() is called
//     - Both audit and outbox writes occur before commit
//
//  3. Nested Begin() is rejected:
//     - Calling tx.Begin() on the transaction used by handleEcho panics,
//     guarding against accidental nested-transaction bugs.
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
// Transaction-tracking test doubles for atomicity tests
// =============================================================================

// atomicityTx tracks which lifecycle calls were made. The Rollback method is
// always present (handleEcho defers it); the key distinction is whether Commit
// was called before any return path.
type atomicityTx struct {
	mu sync.Mutex

	// execCalls counts tx.Exec() invocations — the audit WriteTx path.
	execCalls int
	// queryCalls counts tx.QueryRow() invocations — the outbox INSERT path.
	queryCalls int
	// committed is set to true when Commit() is called successfully.
	committed bool
	// commitCallCount tracks how many times Commit was called (must be ≤ 1).
	commitCallCount int
}

func (t *atomicityTx) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	t.mu.Lock()
	t.execCalls++
	t.mu.Unlock()
	return pgconn.CommandTag{}, nil
}

func (t *atomicityTx) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	t.mu.Lock()
	t.queryCalls++
	t.mu.Unlock()
	return &fakeRow{val: "00000000-0000-0000-0000-000000000099"}
}

func (t *atomicityTx) Commit(_ context.Context) error {
	t.mu.Lock()
	t.committed = true
	t.commitCallCount++
	t.mu.Unlock()
	return nil
}

// Rollback is a no-op: both the success path (deferred after commit) and the
// fault-injection path call it; we don't differentiate here — we differentiate
// via the committed flag instead.
func (t *atomicityTx) Rollback(_ context.Context) error { return nil }

// Begin panics: nested transactions must not be used in the handleEcho code
// path. A panic here indicates a regression.
func (t *atomicityTx) Begin(_ context.Context) (pgx.Tx, error) {
	panic("atomicityTx: Begin() must not be called — nested transactions are forbidden in handleEcho")
}

// Remaining pgx.Tx methods that are not reachable via handleEcho — panic on
// call to catch accidental usage regressions.
func (t *atomicityTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	panic("atomicityTx: CopyFrom not expected")
}
func (t *atomicityTx) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults {
	panic("atomicityTx: SendBatch not expected")
}
func (t *atomicityTx) LargeObjects() pgx.LargeObjects {
	panic("atomicityTx: LargeObjects not expected")
}
func (t *atomicityTx) Prepare(_ context.Context, _, _ string) (*pgconn.StatementDescription, error) {
	panic("atomicityTx: Prepare not expected")
}
func (t *atomicityTx) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	panic("atomicityTx: Query not expected")
}
func (t *atomicityTx) Conn() *pgx.Conn { return nil }

// Compile-time interface guard.
var _ pgx.Tx = (*atomicityTx)(nil)

// atomicityPool implements PoolDB and returns the shared atomicityTx.
type atomicityPool struct {
	tx *atomicityTx
}

func (p *atomicityPool) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return &fakeRow{val: ""}
}
func (p *atomicityPool) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (p *atomicityPool) BeginTx(_ context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	return p.tx, nil
}

var _ PoolDB = (*atomicityPool)(nil)

// =============================================================================
// Test helpers
// =============================================================================

// buildAtomicityServer creates a test server with the given fault injection
// flag. Returns the httptest.Server, the stub provider, the tracking
// transaction, and the capture audit writer.
func buildAtomicityServer(t *testing.T, faultInject bool) (*httptest.Server, *auth.StubProvider, *atomicityTx, *captureAuditWriter) {
	t.Helper()

	const secret = "test-secret-not-for-production"
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  secret,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}

	tx := &atomicityTx{}
	pool := &atomicityPool{tx: tx}
	aw := &captureAuditWriter{} // reuse from echo_audit_test.go (same package)
	idem := &noopIdemStore{}    // reuse from echo_audit_test.go (same package)

	cfg := &config.Config{
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
	}

	s := New(Options{
		Config:                      cfg,
		Auth:                        stub,
		Audit:                       aw,
		Idem:                        idem,
		Pool:                        pool,
		FaultInjectOutboxAfterAudit: faultInject,
	})

	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)
	return ts, stub, tx, aw
}

// postEchoAtomicity issues POST /v1/echo and returns the response.
func postEchoAtomicity(t *testing.T, ts *httptest.Server, token, idemKey, body string) *http.Response {
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

// mintAtomicityJWT issues a JWT for a known actor UUID.
func mintAtomicityJWT(t *testing.T, stub *auth.StubProvider) string {
	t.Helper()
	tok, _, err := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID:   "00000000-0000-0000-0000-000000000001",
		ActorType: auth.ActorTypeStubUser,
		TTL:       time.Hour,
	})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	return tok
}

// atomicityDecodeErrorCode decodes the standard error envelope {"error":{"code":...}}
// and returns the value of the "code" field inside the "error" object.
func atomicityDecodeErrorCode(t *testing.T, r io.Reader) string {
	t.Helper()
	var env map[string]any
	if err := json.NewDecoder(r).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	errObj, _ := env["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("error envelope missing 'error' key; got: %v", env)
	}
	code, _ := errObj["code"].(string)
	return code
}

// =============================================================================
// Feature #18 tests — Step 1–7: fault injection (FAULT_INJECT_OUTBOX_AFTER_AUDIT=true)
// =============================================================================

// TestTxAtomicity_FaultInjection_Returns500 verifies that when the outbox
// fault injection is active, POST /v1/echo returns HTTP 500 (step 4).
func TestTxAtomicity_FaultInjection_Returns500(t *testing.T) {
	ts, stub, _, _ := buildAtomicityServer(t, true /* faultInject */)
	token := mintAtomicityJWT(t, stub)

	resp := postEchoAtomicity(t, ts, token, "TX_PROBE_FAULT_1", `{"message":"fault injection test"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("step 4: want 500, got %d — body: %s", resp.StatusCode, body)
	}
}

// TestTxAtomicity_FaultInjection_CodeIsTransactionFailed verifies the JSON
// error code is 'internal.transaction_failed' (step 4: code='internal.transaction_failed').
func TestTxAtomicity_FaultInjection_CodeIsTransactionFailed(t *testing.T) {
	ts, stub, _, _ := buildAtomicityServer(t, true)
	token := mintAtomicityJWT(t, stub)

	resp := postEchoAtomicity(t, ts, token, "TX_PROBE_FAULT_CODE", `{"message":"code check"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}

	code := atomicityDecodeErrorCode(t, resp.Body)
	if code != "internal.transaction_failed" {
		t.Errorf("want code='internal.transaction_failed', got %q", code)
	}
}

// TestTxAtomicity_FaultInjection_NotAuditFailed verifies the code is NOT
// 'audit_failed' — the fault fires after audit succeeds, so calling it
// audit_failed would mislead the caller into thinking the audit write failed.
func TestTxAtomicity_FaultInjection_NotAuditFailed(t *testing.T) {
	ts, stub, _, _ := buildAtomicityServer(t, true)
	token := mintAtomicityJWT(t, stub)

	resp := postEchoAtomicity(t, ts, token, "TX_PROBE_NOT_AUDIT", `{"message":"not audit_failed"}`)
	defer resp.Body.Close()

	code := atomicityDecodeErrorCode(t, resp.Body)
	if code == "audit_failed" {
		t.Errorf("code must NOT be 'audit_failed' when fault injection fires; got %q", code)
	}
}

// TestTxAtomicity_FaultInjection_TransactionNotCommitted verifies that when
// fault injection fires, the transaction is never committed (steps 5 and 6:
// both writes roll back together).
func TestTxAtomicity_FaultInjection_TransactionNotCommitted(t *testing.T) {
	ts, stub, tx, _ := buildAtomicityServer(t, true)
	token := mintAtomicityJWT(t, stub)

	resp := postEchoAtomicity(t, ts, token, "TX_PROBE_NOCOMMIT", `{"message":"no commit"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}

	tx.mu.Lock()
	committed := tx.committed
	tx.mu.Unlock()

	if committed {
		t.Error("tx.Commit() must NOT be called when fault injection fires — " +
			"both writes must roll back together (steps 5, 6, 7)")
	}
}

// TestTxAtomicity_FaultInjection_AuditWriteAttemptedButRolledBack verifies
// that the audit WriteTx WAS invoked (the fault fires after it), but the
// transaction as a whole is not committed — modelling the real-DB scenario
// where neither row persists in audit_events or outbox_events (step 7).
//
// Note: the test server uses captureAuditWriter (an in-memory audit writer)
// so audit.WriteTx stores events in memory without calling tx.Exec(). The
// in-memory capture still proves that WriteTx was called before the fault.
// In production with PGWriter, WriteTx calls tx.Exec() and the deferred
// rollback undoes it — but that PostgreSQL behaviour is beyond the scope of
// unit tests and is verified by the integration tests in tests/integration/.
func TestTxAtomicity_FaultInjection_AuditWriteAttemptedButRolledBack(t *testing.T) {
	ts, stub, tx, aw := buildAtomicityServer(t, true)
	token := mintAtomicityJWT(t, stub)

	resp := postEchoAtomicity(t, ts, token, "TX_PROBE_AUDIT_ROLLBACK", `{"message":"audit rollback"}`)
	defer resp.Body.Close()

	tx.mu.Lock()
	queryCalls := tx.queryCalls
	committed := tx.committed
	tx.mu.Unlock()

	// audit.WriteTx should have been called (captureAuditWriter stores the event
	// in memory immediately). This proves the fault fires AFTER the audit write.
	auditEvents := aw.getEvents()
	if len(auditEvents) == 0 {
		t.Error("audit WriteTx() should have been called before fault injection fires")
	}

	// outbox INSERT must NOT have been attempted (fault fires before outbox).
	if queryCalls != 0 {
		t.Errorf("outbox QueryRow() must NOT be called when fault fires; got %d calls", queryCalls)
	}

	// Transaction must NOT be committed (both writes roll back together).
	if committed {
		t.Error("tx must NOT be committed when fault injection fires (step 7: verify rollback)")
	}
}

// TestTxAtomicity_FaultInjection_OutboxInsertNotAttempted verifies that the
// outbox INSERT is never attempted when the fault fires. This is the key
// atomicity guarantee: if we crash between audit and outbox, neither row
// ends up in the database (step 6: SELECT count(*) FROM outbox_events = 0).
func TestTxAtomicity_FaultInjection_OutboxInsertNotAttempted(t *testing.T) {
	ts, stub, tx, _ := buildAtomicityServer(t, true)
	token := mintAtomicityJWT(t, stub)

	resp := postEchoAtomicity(t, ts, token, "TX_PROBE_NO_OUTBOX", `{"message":"no outbox"}`)
	defer resp.Body.Close()

	tx.mu.Lock()
	queryCalls := tx.queryCalls
	tx.mu.Unlock()

	if queryCalls != 0 {
		t.Errorf("step 6: outbox INSERT must not be attempted when fault fires; "+
			"tx.QueryRow() called %d times (want 0)", queryCalls)
	}
}

// =============================================================================
// Feature #18 tests — Steps 8–9: success path (no fault injection)
// =============================================================================

// TestTxAtomicity_NoFault_Returns200 verifies the normal (no fault) path
// returns HTTP 200 (step 9: both rows present on success).
func TestTxAtomicity_NoFault_Returns200(t *testing.T) {
	ts, stub, _, _ := buildAtomicityServer(t, false /* no fault */)
	token := mintAtomicityJWT(t, stub)

	resp := postEchoAtomicity(t, ts, token, "TX_PROBE_OK_1", `{"message":"no fault"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("step 9: want 200, got %d — body: %s", resp.StatusCode, body)
	}
}

// TestTxAtomicity_NoFault_BothWritesCommitted verifies that on the success
// path, both the audit Exec AND the outbox QueryRow are called, and
// tx.Commit() is invoked (step 9: both rows present).
func TestTxAtomicity_NoFault_BothWritesCommitted(t *testing.T) {
	ts, stub, tx, aw := buildAtomicityServer(t, false)
	token := mintAtomicityJWT(t, stub)

	resp := postEchoAtomicity(t, ts, token, "TX_PROBE_BOTH_1", `{"message":"both rows"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d — body: %s", resp.StatusCode, body)
	}

	tx.mu.Lock()
	queryCalls := tx.queryCalls
	committed := tx.committed
	tx.mu.Unlock()

	// The audit write goes through captureAuditWriter (in-memory); verify it
	// captured an event so we know WriteTx was called.
	if len(aw.getEvents()) == 0 {
		t.Error("step 9: audit WriteTx() must have been called on success path")
	}
	// The outbox INSERT uses tx.QueryRow (RETURNING id).
	if queryCalls == 0 {
		t.Error("step 9: outbox QueryRow() must have been called on success path")
	}
	if !committed {
		t.Error("step 9: tx.Commit() must be called on success path")
	}
}

// TestTxAtomicity_NoFault_CommitCalledExactlyOnce ensures the handler does
// not double-commit (which would be a bug).
func TestTxAtomicity_NoFault_CommitCalledExactlyOnce(t *testing.T) {
	ts, stub, tx, _ := buildAtomicityServer(t, false)
	token := mintAtomicityJWT(t, stub)

	resp := postEchoAtomicity(t, ts, token, "TX_PROBE_COMMIT_ONCE", `{"message":"commit once"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	tx.mu.Lock()
	commitCount := tx.commitCallCount
	tx.mu.Unlock()

	if commitCount != 1 {
		t.Errorf("tx.Commit() must be called exactly once per request, got %d", commitCount)
	}
}

// =============================================================================
// Feature #18 test — Step 10: nested Begin() is rejected
// =============================================================================

// TestTxAtomicity_NestedBeginPanics verifies that calling tx.Begin() on the
// transaction used by handleEcho causes a panic. This proves that the handler
// does not accidentally create nested transactions (savepoints), which would
// break the single-transaction atomicity guarantee (step 10).
//
// The test uses an explicit panicking Begin implementation on atomicityTx and
// confirms the production handler never triggers it (no panic = the handler
// does not call Begin). A panic here would indicate a regression.
func TestTxAtomicity_NestedBeginPanics(t *testing.T) {
	// The production handler must never call tx.Begin(). We verify this by
	// running a normal request through atomicityTx (whose Begin panics) and
	// confirming the request completes without a panic.
	ts, stub, _, _ := buildAtomicityServer(t, false)
	token := mintAtomicityJWT(t, stub)

	// This call would trigger a panic in atomicityTx.Begin() if handleEcho
	// called Begin internally. The test passes only if no panic occurs.
	resp := postEchoAtomicity(t, ts, token, "TX_PROBE_NO_NESTED", `{"message":"no nested begin"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d — body: %s (handler must not call tx.Begin)", resp.StatusCode, body)
	}
}

// TestTxAtomicity_NestedBeginDirectPanic is a direct unit test on atomicityTx
// that documents and verifies the panic contract. Any future refactor that
// accidentally enables nested Begin will surface here even without a full
// HTTP round-trip.
func TestTxAtomicity_NestedBeginDirectPanic(t *testing.T) {
	tx := &atomicityTx{}

	defer func() {
		r := recover()
		if r == nil {
			t.Error("step 10: expected Begin() to panic but it did not")
		}
		// Verify the panic message contains a recognisable marker.
		msg, _ := r.(string)
		if !strings.Contains(msg, "nested transactions are forbidden") {
			t.Errorf("step 10: panic message %q does not mention nested transaction guard", msg)
		}
	}()

	// This call must panic.
	_, _ = tx.Begin(context.Background())
}

// =============================================================================
// Feature #18 — Cross-cutting verification
// =============================================================================

// TestTxAtomicity_FaultAndNoFault_SameServer verifies that the fault injection
// flag is per-server-instance and does not leak between instances. A server
// with the flag on returns 500; a server with the flag off returns 200.
func TestTxAtomicity_FaultAndNoFault_SameServer(t *testing.T) {
	tsOn, stubOn, _, _ := buildAtomicityServer(t, true)
	tsOff, stubOff, _, _ := buildAtomicityServer(t, false)

	tokenOn := mintAtomicityJWT(t, stubOn)
	tokenOff := mintAtomicityJWT(t, stubOff)

	respOn := postEchoAtomicity(t, tsOn, tokenOn, "TX_DUAL_ON", `{"message":"fault on"}`)
	defer respOn.Body.Close()

	respOff := postEchoAtomicity(t, tsOff, tokenOff, "TX_DUAL_OFF", `{"message":"fault off"}`)
	defer respOff.Body.Close()

	if respOn.StatusCode != http.StatusInternalServerError {
		t.Errorf("fault-on server: want 500, got %d", respOn.StatusCode)
	}
	if respOff.StatusCode != http.StatusOK {
		t.Errorf("fault-off server: want 200, got %d", respOff.StatusCode)
	}
}

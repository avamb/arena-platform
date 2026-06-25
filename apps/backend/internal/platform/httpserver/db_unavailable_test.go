// db_unavailable_test.go verifies feature #32:
// "DB unreachable returns 503 with retry-after"
//
// When pgx pool can't acquire a connection within the request timeout (DB
// outage), handlers return 503 with code='dependency.database_unavailable'
// and a Retry-After header. The readiness probe (/readyz) also returns 503
// while the liveness probe (/healthz) continues to return 200 (liveness is
// never coupled to DB state).
//
// All tests in this file run without a live PostgreSQL connection. DB outage is
// simulated by:
//   - dbDownPool: a PoolDB whose BeginTx always returns a fake connection error.
//   - failingReadinessProbe: a ReadinessProbe whose Ping always returns an error.
//   - recoveringReadinessProbe: a ReadinessProbe whose Ping alternates between
//     failing and succeeding (controlled by an atomic flag).
//
// Feature steps covered:
//
//	Step 1 (stop postgres / setup):    dbDownPool + failingReadinessProbe.
//	Step 3 (POST /v1/echo → 503):      TestDBUnavailable_EchoReturns503.
//	Step 3 (code field):               TestDBUnavailable_EchoCodeIsDatabaseUnavailable.
//	Step 4 (Retry-After header):       TestDBUnavailable_EchoHasRetryAfterHeader.
//	Step 4 (Retry-After is numeric):   TestDBUnavailable_RetryAfterIsNumeric.
//	Step 4 (Retry-After > 0):          TestDBUnavailable_RetryAfterIsPositive.
//	Step 5 (GET /readyz → 503):        TestDBUnavailable_ReadyzReturns503WhenDBDown.
//	Step 5 (readyz body code):         TestDBUnavailable_ReadyzBodyShowsDBCheck.
//	Step 6 (GET /healthz → 200):       TestDBUnavailable_HealthzStillReturns200.
//	Step 7 (recovery: readyz → 200):   TestDBUnavailable_ReadyzReturns200AfterRecovery.
//	Extra  (envelope structure):       TestDBUnavailable_EchoEnvelopeHasRequiredFields.
//	Extra  (no stack in body):         TestDBUnavailable_EchoBodyHasNoStackTrace.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// =============================================================================
// Test doubles for DB-down scenario
// =============================================================================

// dbDownPool implements PoolDB and simulates a PostgreSQL pool that cannot
// acquire a connection (e.g. the server is stopped). BeginTx always returns a
// realistic-looking connection refused error. QueryRow and Exec panic to catch
// accidental usage when the pool is "down" — the handler should always bail out
// after BeginTx fails without touching the pool further.
type dbDownPool struct {
	// beginErr is the error returned by BeginTx. If nil, a default connection
	// error is used. Set in tests that need a specific error type.
	beginErr error
}

func (p *dbDownPool) err() error {
	if p.beginErr != nil {
		return p.beginErr
	}
	return errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")
}

func (p *dbDownPool) BeginTx(_ context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	return nil, p.err()
}

func (p *dbDownPool) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	panic("dbDownPool: QueryRow must not be called when DB is down")
}

func (p *dbDownPool) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	panic("dbDownPool: Exec must not be called when DB is down")
}

var _ PoolDB = (*dbDownPool)(nil)

// failingReadinessProbe implements ReadinessProbe and always fails its Ping.
// Used to model the database readiness probe when the DB is unreachable.
type failingReadinessProbe struct {
	name string
	err  error
}

func (p *failingReadinessProbe) ProbeName() string { return p.name }
func (p *failingReadinessProbe) Ping(_ context.Context) error {
	if p.err != nil {
		return p.err
	}
	return errors.New("connection refused")
}

var _ ReadinessProbe = (*failingReadinessProbe)(nil)

// succeedingReadinessProbe implements ReadinessProbe and always succeeds.
// Used to verify that /readyz returns 200 when all dependencies are healthy.
type succeedingReadinessProbe struct {
	name string
}

func (p *succeedingReadinessProbe) ProbeName() string            { return p.name }
func (p *succeedingReadinessProbe) Ping(_ context.Context) error { return nil }

var _ ReadinessProbe = (*succeedingReadinessProbe)(nil)

// recoveringReadinessProbe implements ReadinessProbe with a controllable
// health state. Call setHealthy(true) to make subsequent Ping calls succeed.
type recoveringReadinessProbe struct {
	name    string
	healthy atomic.Bool
}

func (p *recoveringReadinessProbe) ProbeName() string { return p.name }
func (p *recoveringReadinessProbe) Ping(_ context.Context) error {
	if p.healthy.Load() {
		return nil
	}
	return errors.New("connection refused")
}

func (p *recoveringReadinessProbe) setHealthy(v bool) { p.healthy.Store(v) }

var _ ReadinessProbe = (*recoveringReadinessProbe)(nil)

// =============================================================================
// Test server builders
// =============================================================================

// buildDBDownEchoServer builds a test HTTP server whose DB pool always returns
// a connection error. Auth, idempotency, and audit are all in-memory fakes so
// only the BeginTx call reaches the dbDownPool. Returns the httptest.Server and
// a valid JWT token for POST /v1/echo.
func buildDBDownEchoServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()

	const secret = "test-secret-not-for-production"
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  secret,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}

	tok, _, err := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID:   "00000000-0000-0000-0000-000000000001",
		ActorType: auth.ActorTypeStubUser,
		TTL:       time.Hour,
	})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	cfg := &config.Config{
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
	}

	pool := &dbDownPool{}
	s := New(Options{
		Config: cfg,
		Auth:   stub,
		Audit:  &captureAuditWriter{},
		Idem:   &noopIdemStore{},
		Pool:   pool,
		// Probe failing so /readyz also reflects DB state.
		Probes: []ReadinessProbe{
			&failingReadinessProbe{name: "database"},
		},
	})

	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)
	return ts, tok
}

// postEchoDBDown issues POST /v1/echo with a valid JWT and idempotency key.
// Returns the *http.Response; caller must close Body.
func postEchoDBDown(t *testing.T, ts *httptest.Server, token string) *http.Response {
	t.Helper()
	body := strings.NewReader(`{"message":"db down test"}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/v1/echo", body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "DB_DOWN_TEST_IDEM_KEY_001")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

// decodeDBDownEnvelope decodes the JSON error envelope from the response body.
// Closes resp.Body.
func decodeDBDownEnvelope(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("body is not valid JSON: %v\nbody: %s", err, b)
	}
	return env
}

// =============================================================================
// Step 3 — POST /v1/echo returns HTTP 503 when DB is down
// =============================================================================

// TestDBUnavailable_EchoReturns503 verifies that POST /v1/echo returns HTTP 503
// Service Unavailable when the database pool cannot acquire a connection.
func TestDBUnavailable_EchoReturns503(t *testing.T) {
	ts, tok := buildDBDownEchoServer(t)

	resp := postEchoDBDown(t, ts, tok)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("step 3: expected HTTP 503, got %d — body: %s", resp.StatusCode, body)
	}
}

// TestDBUnavailable_EchoCodeIsDatabaseUnavailable verifies that the JSON error
// envelope code is exactly 'dependency.database_unavailable' (step 3).
func TestDBUnavailable_EchoCodeIsDatabaseUnavailable(t *testing.T) {
	ts, tok := buildDBDownEchoServer(t)

	resp := postEchoDBDown(t, ts, tok)
	env := decodeDBDownEnvelope(t, resp)

	errObj, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("envelope missing 'error' object; got: %#v", env)
	}
	code, _ := errObj["code"].(string)
	if code != "dependency.database_unavailable" {
		t.Errorf("step 3: expected code 'dependency.database_unavailable', got %q", code)
	}
}

// TestDBUnavailable_EchoCodeFollowsDottedNamespace verifies the code uses the
// dotted-namespace convention (contains at least one dot).
func TestDBUnavailable_EchoCodeFollowsDottedNamespace(t *testing.T) {
	ts, tok := buildDBDownEchoServer(t)

	resp := postEchoDBDown(t, ts, tok)
	env := decodeDBDownEnvelope(t, resp)

	errObj := env["error"].(map[string]any)
	code, _ := errObj["code"].(string)
	if !strings.Contains(code, ".") {
		t.Errorf("error code must follow dotted-namespace format, got %q", code)
	}
}

// =============================================================================
// Step 4 — Retry-After header is present (and numeric/positive)
// =============================================================================

// TestDBUnavailable_EchoHasRetryAfterHeader verifies that the 503 response
// carries a non-empty Retry-After header (step 4).
func TestDBUnavailable_EchoHasRetryAfterHeader(t *testing.T) {
	ts, tok := buildDBDownEchoServer(t)

	resp := postEchoDBDown(t, ts, tok)
	defer resp.Body.Close()

	ra := resp.Header.Get("Retry-After")
	if strings.TrimSpace(ra) == "" {
		t.Error("step 4: Retry-After header must be present on 503 response but was empty/missing")
	}
}

// TestDBUnavailable_RetryAfterIsNumeric verifies that Retry-After contains a
// valid integer (seconds form, per RFC 9110 §10.2.3).
func TestDBUnavailable_RetryAfterIsNumeric(t *testing.T) {
	ts, tok := buildDBDownEchoServer(t)

	resp := postEchoDBDown(t, ts, tok)
	defer resp.Body.Close()

	ra := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if _, err := strconv.Atoi(ra); err != nil {
		t.Errorf("step 4: Retry-After must be a decimal integer (seconds), got %q", ra)
	}
}

// TestDBUnavailable_RetryAfterIsPositive verifies that the Retry-After value
// is a positive integer (at least 1 second) so clients have a meaningful
// backoff period.
func TestDBUnavailable_RetryAfterIsPositive(t *testing.T) {
	ts, tok := buildDBDownEchoServer(t)

	resp := postEchoDBDown(t, ts, tok)
	defer resp.Body.Close()

	ra := strings.TrimSpace(resp.Header.Get("Retry-After"))
	n, err := strconv.Atoi(ra)
	if err != nil {
		t.Fatalf("Retry-After is not an integer: %q", ra)
	}
	if n <= 0 {
		t.Errorf("step 4: Retry-After must be positive; got %d", n)
	}
}

// =============================================================================
// Step 5 — GET /readyz returns 503 when DB probe fails
// =============================================================================

// TestDBUnavailable_ReadyzReturns503WhenDBDown verifies that GET /readyz
// returns HTTP 503 when the database readiness probe fails (step 5).
func TestDBUnavailable_ReadyzReturns503WhenDBDown(t *testing.T) {
	ts, _ := buildDBDownEchoServer(t)

	resp, err := ts.Client().Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("step 5: expected HTTP 503 from /readyz when DB is down, got %d — body: %s",
			resp.StatusCode, body)
	}
}

// TestDBUnavailable_ReadyzBodyShowsDBCheck verifies that the /readyz response
// body includes a "checks" map whose "database" entry is not "ok" (step 5).
func TestDBUnavailable_ReadyzBodyShowsDBCheck(t *testing.T) {
	ts, _ := buildDBDownEchoServer(t)

	resp, err := ts.Client().Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /readyz body: %v", err)
	}

	checks, ok := body["checks"].(map[string]any)
	if !ok {
		t.Fatalf("step 5: /readyz body missing 'checks' map; got: %#v", body)
	}

	dbStatus, present := checks["database"]
	if !present {
		t.Fatalf("step 5: /readyz 'checks' map missing 'database' key; got: %#v", checks)
	}
	if dbStatus == "ok" {
		t.Errorf("step 5: /readyz database check must NOT be 'ok' when DB is down; got %q", dbStatus)
	}
}

// TestDBUnavailable_ReadyzStatusIsNotReady verifies that the status field in
// the /readyz body is "not_ready" when a probe fails.
func TestDBUnavailable_ReadyzStatusIsNotReady(t *testing.T) {
	ts, _ := buildDBDownEchoServer(t)

	resp, err := ts.Client().Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /readyz body: %v", err)
	}

	status, _ := body["status"].(string)
	if status != "not_ready" {
		t.Errorf("step 5: /readyz status must be 'not_ready' when DB is down; got %q", status)
	}
}

// =============================================================================
// Step 6 — GET /healthz returns 200 (liveness not coupled to DB)
// =============================================================================

// TestDBUnavailable_HealthzStillReturns200 verifies that GET /healthz returns
// HTTP 200 even when the database readiness probe fails (step 6). Liveness must
// never depend on external dependencies — only process-level health matters.
func TestDBUnavailable_HealthzStillReturns200(t *testing.T) {
	ts, _ := buildDBDownEchoServer(t)

	resp, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("step 6: expected HTTP 200 from /healthz when DB is down, got %d — body: %s",
			resp.StatusCode, body)
	}
}

// TestDBUnavailable_HealthzBodyIsOK verifies that the /healthz body reports
// status "ok" even when the database is down (liveness is unconditional).
func TestDBUnavailable_HealthzBodyIsOK(t *testing.T) {
	ts, _ := buildDBDownEchoServer(t)

	resp, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /healthz body: %v", err)
	}

	status, _ := body["status"].(string)
	if status != "ok" {
		t.Errorf("step 6: /healthz body status must be 'ok' even when DB is down; got %q", status)
	}
}

// =============================================================================
// Step 7 — Recovery: GET /readyz returns 200 once DB probe recovers
// =============================================================================

// TestDBUnavailable_ReadyzReturns200AfterRecovery verifies that once the
// database probe starts returning nil errors (DB recovered), GET /readyz
// returns HTTP 200 again (step 7). Uses a recoveringReadinessProbe whose
// health state can be toggled mid-test.
func TestDBUnavailable_ReadyzReturns200AfterRecovery(t *testing.T) {
	probe := &recoveringReadinessProbe{name: "database"}
	// Start unhealthy.
	probe.setHealthy(false)

	const secret = "test-secret-not-for-production"
	stub, err := auth.NewStubProvider(auth.StubConfig{Secret: secret, Enabled: true})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}

	cfg := &config.Config{
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
	}

	s := New(Options{
		Config: cfg,
		Auth:   stub,
		Audit:  &captureAuditWriter{},
		Idem:   &noopIdemStore{},
		Pool:   &dbDownPool{},
		Probes: []ReadinessProbe{probe},
	})

	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	// Verify /readyz returns 503 while DB is down.
	respDown, err := ts.Client().Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz (down): %v", err)
	}
	io.Copy(io.Discard, respDown.Body)
	respDown.Body.Close()

	if respDown.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("step 7 (pre-recovery): expected 503, got %d", respDown.StatusCode)
	}

	// Simulate DB recovery.
	probe.setHealthy(true)

	// Verify /readyz returns 200 after recovery.
	respUp, err := ts.Client().Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz (recovered): %v", err)
	}
	defer respUp.Body.Close()

	if respUp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(respUp.Body)
		t.Errorf("step 7 (post-recovery): expected 200 after DB recovery, got %d — body: %s",
			respUp.StatusCode, body)
	}
}

// TestDBUnavailable_ReadyzRecoveredBodyStatusIsReady verifies that the /readyz
// body status is "ready" after the probe recovers.
func TestDBUnavailable_ReadyzRecoveredBodyStatusIsReady(t *testing.T) {
	probe := &recoveringReadinessProbe{name: "database"}
	probe.setHealthy(true) // Start healthy.

	const secret = "test-secret-not-for-production"
	stub, err := auth.NewStubProvider(auth.StubConfig{Secret: secret, Enabled: true})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}

	cfg := &config.Config{
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
	}

	s := New(Options{
		Config: cfg,
		Auth:   stub,
		Audit:  &captureAuditWriter{},
		Idem:   &noopIdemStore{},
		Pool:   &dbDownPool{}, // pool still "down" — only readyz probe is recovered
		Probes: []ReadinessProbe{probe},
	})

	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("step 7: expected 200 after probe recovery, got %d — body: %s", resp.StatusCode, body)
	}

	var body map[string]any
	// Re-read the body from the response we already have... but it was closed.
	// We need to re-fetch — let's use a separate GET.
	resp2, err := ts.Client().Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz (body check): %v", err)
	}
	defer resp2.Body.Close()
	if err := json.NewDecoder(resp2.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	status, _ := body["status"].(string)
	if status != "ready" {
		t.Errorf("step 7: /readyz status must be 'ready' after recovery; got %q", status)
	}
}

// =============================================================================
// Extra — standard envelope fields present on 503
// =============================================================================

// TestDBUnavailable_EchoEnvelopeHasRequiredFields verifies that the 503 error
// envelope carries all four required fields (code, message, request_id, trace_id).
func TestDBUnavailable_EchoEnvelopeHasRequiredFields(t *testing.T) {
	ts, tok := buildDBDownEchoServer(t)

	resp := postEchoDBDown(t, ts, tok)
	env := decodeDBDownEnvelope(t, resp)

	errObj, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("envelope missing 'error' object; got: %#v", env)
	}
	for _, field := range []string{"code", "message", "request_id", "trace_id"} {
		if _, present := errObj[field]; !present {
			t.Errorf("error envelope missing required field %q", field)
		}
	}
}

// TestDBUnavailable_EchoBodyHasNoStackTrace verifies that the 503 response body
// does not include a goroutine stack trace (security: don't leak internals to
// clients in non-development environments).
func TestDBUnavailable_EchoBodyHasNoStackTrace(t *testing.T) {
	ts, tok := buildDBDownEchoServer(t)

	resp := postEchoDBDown(t, ts, tok)
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	bodyStr := string(b)
	// A goroutine stack trace always starts with "goroutine " followed by a
	// number. Its presence in the response body indicates a stack leak.
	if strings.Contains(bodyStr, "goroutine ") {
		t.Error("503 response body must not contain a goroutine stack trace")
	}
}

// TestDBUnavailable_EchoMessageMentionsDatabase verifies that the error message
// gives a human-readable hint that the database is the root cause.
func TestDBUnavailable_EchoMessageMentionsDatabase(t *testing.T) {
	ts, tok := buildDBDownEchoServer(t)

	resp := postEchoDBDown(t, ts, tok)
	env := decodeDBDownEnvelope(t, resp)

	errObj := env["error"].(map[string]any)
	msg, _ := errObj["message"].(string)
	if !strings.Contains(strings.ToLower(msg), "database") &&
		!strings.Contains(strings.ToLower(msg), "unavailable") {
		t.Errorf("error message should mention 'database' or 'unavailable'; got %q", msg)
	}
}

// TestDBUnavailable_EchoContentTypeIsJSON verifies that the 503 response
// carries Content-Type: application/json (same contract as all other error
// responses in the project).
func TestDBUnavailable_EchoContentTypeIsJSON(t *testing.T) {
	ts, tok := buildDBDownEchoServer(t)

	resp := postEchoDBDown(t, ts, tok)
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected Content-Type application/json on 503, got %q", ct)
	}
}

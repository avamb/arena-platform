// trace_id_propagation_test.go verifies feature #62:
// "trace_id propagated to logs and error envelope"
//
// OpenTelemetry trace_id is captured per request and surfaced in error
// envelopes and slog output, enabling end-to-end debugging.
//
// Steps covered in this file:
//  1. GET /v1/info — response body includes trace_id; X-Trace-Id header present
//  2. POST /v1/echo with bad JSON — error envelope includes trace_id field
//  3. trace_id captured from error response is non-empty
//  4. slog records for a request carry trace_id= on multiple log lines
//     (request start, handler error, request end)
//  5. trace_id is hex-encoded 32 chars (W3C Trace Context format)
//
// Step 6 (incoming W3C traceparent header → server continues the trace)
// is covered by router_traceparent_test.go in the httpadapter package since
// it requires injecting an explicit propagator + tracer into the router.
package httpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// =============================================================================
// Test infrastructure
// =============================================================================

// captureHandler is a slog.Handler that captures every log record as a JSON
// line into a bytes.Buffer. It wraps a real JSON slog.Handler so the output
// format is identical to production — callers can parse lines as JSON objects.
type captureHandler struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	base slog.Handler
}

func newCaptureHandler() *captureHandler {
	h := &captureHandler{}
	// Write JSON records into h.buf via a real JSON handler so all attributes
	// (including those pre-attached via With) are serialised correctly.
	h.base = slog.NewJSONHandler(&h.buf, &slog.HandlerOptions{
		Level: slog.LevelDebug, // capture everything
	})
	return h
}

func (h *captureHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *captureHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.base.Handle(ctx, r)
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &captureHandler{base: h.base.WithAttrs(attrs), buf: bytes.Buffer{}}
}

func (h *captureHandler) WithGroup(name string) slog.Handler {
	return &captureHandler{base: h.base.WithGroup(name), buf: bytes.Buffer{}}
}

// lines returns all captured log lines up to this point.
func (h *captureHandler) lines() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	raw := h.buf.String()
	if raw == "" {
		return nil
	}
	var out []string
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// buildTraceTestServer returns a running httptest.Server backed by a fully-
// wired httpserver.Server, plus a logger whose output goes to cap so tests
// can inspect slog records emitted during request processing.
//
// The server is wired with:
//   - A fakePoolDB (no real Postgres — tests are unit-level)
//   - A captureAuditWriter (already defined in echo_audit_test.go)
//   - A noopIdemStore (already defined in echo_audit_test.go)
//   - The StubProvider for JWT auth
func buildTraceTestServer(t *testing.T) (ts *httptest.Server, stub *auth.StubProvider, cap *captureHandler) {
	t.Helper()

	cap = newCaptureHandler()
	logger := slog.New(cap)

	const secret = "trace-test-secret"
	var err error
	stub, err = auth.NewStubProvider(auth.StubConfig{
		Secret:  secret,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}

	aw := &captureAuditWriter{}
	tx := &fakeTx{}
	pool := &fakePoolDB{tx: tx}
	idem := &noopIdemStore{}

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
		Idem:   idem,
		Pool:   pool,
	})

	ts = httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)
	return ts, stub, cap
}

// getInfoResponse sends GET /v1/info and returns the parsed JSON body.
func getInfoResponse(t *testing.T, baseURL string) map[string]any {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/v1/info", nil)
	if err != nil {
		t.Fatalf("build GET /v1/info request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /v1/info body: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("/v1/info response is not JSON: %v\nbody: %s", err, body)
	}
	return m
}

// is32HexChars returns true iff s is exactly 32 lowercase hex characters — the
// W3C Trace Context format for a 128-bit trace identifier.
func is32HexChars(s string) bool {
	if len(s) != 32 {
		return false
	}
	matched, _ := regexp.MatchString(`^[0-9a-f]{32}$`, s)
	return matched
}

// =============================================================================
// Step 1 — GET /v1/info response body includes trace_id
// =============================================================================

// TestTraceID_InfoResponseBodyHasTraceIDField verifies step 1a: the GET /v1/info
// response JSON body contains a "trace_id" field (not absent, not null).
func TestTraceID_InfoResponseBodyHasTraceIDField(t *testing.T) {
	t.Parallel()
	ts, _, _ := buildTraceTestServer(t)

	body := getInfoResponse(t, ts.URL)

	rawTraceID, ok := body["trace_id"]
	if !ok {
		t.Fatal("GET /v1/info response JSON must include 'trace_id' field")
	}
	traceID, ok := rawTraceID.(string)
	if !ok {
		t.Fatalf("trace_id must be a string, got %T: %v", rawTraceID, rawTraceID)
	}
	if traceID == "" {
		t.Fatal("GET /v1/info trace_id must be non-empty")
	}
}

// TestTraceID_InfoResponseBodyTraceIDMatchesHeader verifies step 1b:
// the trace_id field in the response body equals the X-Trace-Id response
// header. They must refer to the same identifier — one in the envelope for
// machine consumers, one in the header for HTTP-layer tools.
func TestTraceID_InfoResponseBodyTraceIDMatchesHeader(t *testing.T) {
	t.Parallel()
	ts, _, _ := buildTraceTestServer(t)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/v1/info", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer resp.Body.Close()

	headerTraceID := resp.Header.Get("X-Trace-Id")
	if headerTraceID == "" {
		t.Fatal("X-Trace-Id response header must be non-empty")
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /v1/info response: %v", err)
	}
	bodyTraceID, _ := body["trace_id"].(string)

	if bodyTraceID == "" {
		t.Fatal("response body trace_id must be non-empty")
	}
	if bodyTraceID != headerTraceID {
		t.Errorf("body trace_id=%q != X-Trace-Id header=%q; they must be the same identifier",
			bodyTraceID, headerTraceID)
	}
}

// =============================================================================
// Step 2 — POST /v1/echo with bad JSON → error envelope includes trace_id
// =============================================================================

// TestTraceID_ErrorEnvelopeIncludesTraceID verifies step 2: when POST /v1/echo
// receives a malformed JSON body, the 400 error envelope carries a non-empty
// trace_id field — not an empty string and not the zero value.
func TestTraceID_ErrorEnvelopeIncludesTraceID(t *testing.T) {
	t.Parallel()
	ts, stub, _ := buildTraceTestServer(t)
	token := mintJWT(t, stub, "user-trace-001")

	resp := doEchoRequest(t, ts.URL, token, "{bad json")
	env := decodeErrorEnvelope(t, resp)

	errObj, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("envelope missing 'error' object; got %#v", env)
	}
	rawTraceID, present := errObj["trace_id"]
	if !present {
		t.Fatal("error envelope must contain 'trace_id' field")
	}
	traceID, ok := rawTraceID.(string)
	if !ok {
		t.Fatalf("error.trace_id must be a string, got %T", rawTraceID)
	}
	if traceID == "" {
		t.Fatal("error.trace_id must be non-empty in the error envelope")
	}
}

// TestTraceID_ErrorEnvelopeTraceIDMatchesResponseHeader verifies step 2+3:
// the trace_id inside the error envelope matches the X-Trace-Id response
// header on the same response. This lets clients correlate the body-level error
// with server-side logs using either the header or the body field.
func TestTraceID_ErrorEnvelopeTraceIDMatchesResponseHeader(t *testing.T) {
	t.Parallel()
	ts, stub, _ := buildTraceTestServer(t)
	token := mintJWT(t, stub, "user-trace-002")

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader("{bad json"),
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "trace-test-key-bad-json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	defer resp.Body.Close()

	headerTraceID := resp.Header.Get("X-Trace-Id")
	if headerTraceID == "" {
		t.Fatal("X-Trace-Id response header must be non-empty")
	}

	var env map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	errObj, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("envelope missing 'error' object")
	}
	bodyTraceID, _ := errObj["trace_id"].(string)

	if bodyTraceID == "" {
		t.Fatal("error.trace_id in body must be non-empty")
	}
	if bodyTraceID != headerTraceID {
		t.Errorf("error.trace_id=%q != X-Trace-Id header=%q", bodyTraceID, headerTraceID)
	}
}

// =============================================================================
// Step 3 — Capture trace_id from error response (non-empty, stable)
// =============================================================================

// TestTraceID_CapturedTraceIDIsNonEmpty verifies step 3: the trace_id
// extracted from an error response is a non-empty string that can be used as
// a search key in the slog output.
func TestTraceID_CapturedTraceIDIsNonEmpty(t *testing.T) {
	t.Parallel()
	ts, stub, _ := buildTraceTestServer(t)
	token := mintJWT(t, stub, "user-trace-003")

	resp := doEchoRequest(t, ts.URL, token, "{bad json")
	env := decodeErrorEnvelope(t, resp)

	errObj, _ := env["error"].(map[string]any)
	traceID, _ := errObj["trace_id"].(string)

	if traceID == "" {
		t.Fatal("captured trace_id from error response must be non-empty")
	}
	t.Logf("captured trace_id: %s", traceID)
}

// =============================================================================
// Step 4 — slog records for a request carry trace_id on multiple log lines
// =============================================================================

// TestTraceID_SlogLinesCarryTraceID verifies step 4: after a request completes,
// multiple slog JSON lines in the captured output carry a "trace_id" field that
// matches the X-Trace-Id response header. At minimum the "http request start"
// and "http request end" log lines must carry the trace_id.
func TestTraceID_SlogLinesCarryTraceID(t *testing.T) {
	t.Parallel()
	ts, stub, cap := buildTraceTestServer(t)
	token := mintJWT(t, stub, "user-trace-004")

	// Make the request using a simple approach via ts.Client()
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader("{bad json"),
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "trace-slog-test-key")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	defer resp.Body.Close()

	headerTraceID := resp.Header.Get("X-Trace-Id")
	if headerTraceID == "" {
		t.Fatal("X-Trace-Id response header must be non-empty")
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("drain body: %v", err)
	}

	// Parse all captured slog JSON lines and count how many carry the trace_id.
	lines := cap.lines()
	if len(lines) == 0 {
		t.Fatal("no slog output captured — logger may not have been wired into the server")
	}

	linesWithTraceID := 0
	for _, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		// logging.FromContext enriches the logger with trace_id, so it appears as
		// a top-level field in the JSON record.
		if tid, ok := rec[logging.FieldTraceID].(string); ok && tid == headerTraceID {
			linesWithTraceID++
		}
	}

	// Minimum expectation: "http request start" and "http request end" both carry
	// trace_id, giving us ≥2 lines per request.
	if linesWithTraceID < 2 {
		t.Errorf("expected ≥2 slog lines with trace_id=%q, found %d\nAll lines:\n%s",
			headerTraceID, linesWithTraceID, strings.Join(lines, "\n"))
	}
}

// TestTraceID_SlogRequestStartCarriesTraceID verifies step 4a: the
// "http request start" slog record carries the request's trace_id.
func TestTraceID_SlogRequestStartCarriesTraceID(t *testing.T) {
	t.Parallel()
	ts, _, cap := buildTraceTestServer(t)

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodGet, ts.URL+"/v1/info", nil,
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer resp.Body.Close()

	headerTraceID := resp.Header.Get("X-Trace-Id")
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("drain body: %v", err)
	}

	// Find the "http request start" record and check its trace_id.
	var foundStart bool
	for _, line := range cap.lines() {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		msg, _ := rec["msg"].(string)
		if msg != "http request start" {
			continue
		}
		foundStart = true
		tid, _ := rec[logging.FieldTraceID].(string)
		if tid == "" {
			t.Error("'http request start' log record is missing trace_id field")
		} else if tid != headerTraceID {
			t.Errorf("'http request start' trace_id=%q != X-Trace-Id=%q", tid, headerTraceID)
		}
	}
	if !foundStart {
		t.Error("no 'http request start' slog record found in captured output")
	}
}

// TestTraceID_SlogRequestEndCarriesTraceID verifies step 4b: the
// "http request end" slog record carries the same trace_id as the response
// header, completing the per-request log bracket.
func TestTraceID_SlogRequestEndCarriesTraceID(t *testing.T) {
	t.Parallel()
	ts, _, cap := buildTraceTestServer(t)

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodGet, ts.URL+"/v1/info", nil,
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer resp.Body.Close()

	headerTraceID := resp.Header.Get("X-Trace-Id")
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("drain body: %v", err)
	}

	var foundEnd bool
	for _, line := range cap.lines() {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		msg, _ := rec["msg"].(string)
		if msg != "http request end" {
			continue
		}
		foundEnd = true
		tid, _ := rec[logging.FieldTraceID].(string)
		if tid == "" {
			t.Error("'http request end' log record is missing trace_id field")
		} else if tid != headerTraceID {
			t.Errorf("'http request end' trace_id=%q != X-Trace-Id=%q", tid, headerTraceID)
		}
	}
	if !foundEnd {
		t.Error("no 'http request end' slog record found in captured output")
	}
}

// TestTraceID_SlogTraceIDConsistentWithinRequest verifies step 4 (consistency):
// all slog records produced during a single request carry the SAME trace_id.
// This is critical — if log lines from a single request show different trace_ids
// the slog filter "trace_id=<captured>" would miss some records.
func TestTraceID_SlogTraceIDConsistentWithinRequest(t *testing.T) {
	t.Parallel()
	ts, stub, cap := buildTraceTestServer(t)
	token := mintJWT(t, stub, "user-trace-005")

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader("{bad json"),
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "trace-consistency-key")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	defer resp.Body.Close()

	headerTraceID := resp.Header.Get("X-Trace-Id")
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("drain body: %v", err)
	}

	// Collect all distinct trace_ids seen in slog output for this request.
	seen := make(map[string]int)
	for _, line := range cap.lines() {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if tid, ok := rec[logging.FieldTraceID].(string); ok && tid != "" {
			seen[tid]++
		}
	}

	if len(seen) == 0 {
		t.Fatal("no slog records with trace_id found")
	}
	if len(seen) > 1 {
		t.Errorf("inconsistent trace_ids in slog output: %v — all records for a request must share the same trace_id", seen)
	}

	// The single observed trace_id must match the response header.
	for tid := range seen {
		if tid != headerTraceID {
			t.Errorf("slog trace_id=%q != X-Trace-Id header=%q", tid, headerTraceID)
		}
	}
}

// =============================================================================
// Step 5 — trace_id is hex-encoded 32 chars (W3C Trace Context format)
// =============================================================================

// TestTraceID_TraceIDIs32LowercaseHexChars verifies step 5: the trace_id
// value must be exactly 32 lowercase hexadecimal characters — the W3C Trace
// Context v1 format for a 128-bit trace identifier. No dashes, no uppercase,
// no truncation.
func TestTraceID_TraceIDIs32LowercaseHexChars(t *testing.T) {
	t.Parallel()
	ts, _, _ := buildTraceTestServer(t)

	body := getInfoResponse(t, ts.URL)

	traceID, _ := body["trace_id"].(string)
	if !is32HexChars(traceID) {
		t.Errorf("trace_id=%q must be exactly 32 lowercase hex chars (W3C format); len=%d", traceID, len(traceID))
	}
}

// TestTraceID_ErrorEnvelopeTraceIDIs32HexChars verifies step 5 on the error
// path: the trace_id in an error envelope is also exactly 32 hex chars.
func TestTraceID_ErrorEnvelopeTraceIDIs32HexChars(t *testing.T) {
	t.Parallel()
	ts, stub, _ := buildTraceTestServer(t)
	token := mintJWT(t, stub, "user-trace-006")

	resp := doEchoRequest(t, ts.URL, token, "{bad json")
	env := decodeErrorEnvelope(t, resp)

	errObj, _ := env["error"].(map[string]any)
	traceID, _ := errObj["trace_id"].(string)

	if !is32HexChars(traceID) {
		t.Errorf("error.trace_id=%q must be exactly 32 lowercase hex chars; len=%d", traceID, len(traceID))
	}
}

// TestTraceID_HeaderTraceIDIs32HexChars verifies step 5 on the X-Trace-Id
// response header: the header value is also exactly 32 hex chars, consistent
// with the body.
func TestTraceID_HeaderTraceIDIs32HexChars(t *testing.T) {
	t.Parallel()
	ts, _, _ := buildTraceTestServer(t)

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodGet, ts.URL+"/v1/info", nil,
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("drain body: %v", err)
	}

	traceID := resp.Header.Get("X-Trace-Id")
	if !is32HexChars(traceID) {
		t.Errorf("X-Trace-Id=%q must be exactly 32 lowercase hex chars; len=%d", traceID, len(traceID))
	}
}

// TestTraceID_SlogTraceIDIs32HexChars verifies step 5 in the slog output:
// every trace_id field found in the JSON slog records is a 32-char hex string.
func TestTraceID_SlogTraceIDIs32HexChars(t *testing.T) {
	t.Parallel()
	ts, _, cap := buildTraceTestServer(t)

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodGet, ts.URL+"/v1/info", nil,
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("drain body: %v", err)
	}

	// Parse all slog lines and verify the trace_id format when present.
	var checked int
	for _, line := range cap.lines() {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		tid, ok := rec[logging.FieldTraceID].(string)
		if !ok || tid == "" {
			continue
		}
		checked++
		if !is32HexChars(tid) {
			t.Errorf("slog record trace_id=%q is not 32 lowercase hex chars; line: %s", tid, line)
		}
	}
	if checked == 0 {
		t.Error("no slog records with trace_id were found to verify the format")
	}
}

// TestTraceID_FullVerification is a single sub-test sweep that exercises all
// five feature steps in sequence, using the trace_id captured in step 2 as the
// search key for step 4. This mirrors the exact flow described in the feature
// specification.
func TestTraceID_FullVerification(t *testing.T) {
	ts, stub, cap := buildTraceTestServer(t)
	token := mintJWT(t, stub, "user-trace-full")

	// -------------------------------------------------------------------------
	// Step 1: GET /v1/info — trace_id in response body.
	// -------------------------------------------------------------------------
	t.Run("step1_info_has_trace_id", func(t *testing.T) {
		body := getInfoResponse(t, ts.URL)
		traceID, _ := body["trace_id"].(string)
		if traceID == "" {
			t.Error("GET /v1/info body must include non-empty trace_id")
		}
	})

	// -------------------------------------------------------------------------
	// Step 2+3: POST /v1/echo with bad JSON — error envelope has trace_id.
	// -------------------------------------------------------------------------
	var capturedTraceID string
	t.Run("step2_error_envelope_has_trace_id", func(t *testing.T) {
		resp := doEchoRequest(t, ts.URL, token, "{bad json again")
		env := decodeErrorEnvelope(t, resp)
		errObj, _ := env["error"].(map[string]any)
		capturedTraceID, _ = errObj["trace_id"].(string)
		if capturedTraceID == "" {
			t.Error("error.trace_id must be non-empty")
		}
	})
	if capturedTraceID == "" {
		t.Fatal("cannot proceed: no trace_id captured from step 2")
	}

	// -------------------------------------------------------------------------
	// Step 4: Search slog for entries with trace_id=<captured>.
	// -------------------------------------------------------------------------
	t.Run("step4_slog_has_trace_id", func(t *testing.T) {
		count := 0
		for _, line := range cap.lines() {
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				continue
			}
			if tid, _ := rec[logging.FieldTraceID].(string); tid == capturedTraceID {
				count++
			}
		}
		if count < 2 {
			t.Errorf("expected ≥2 slog lines with trace_id=%q, found %d", capturedTraceID, count)
		}
	})

	// -------------------------------------------------------------------------
	// Step 5: trace_id is hex-encoded 32 chars (W3C format).
	// -------------------------------------------------------------------------
	t.Run("step5_trace_id_is_32_hex_chars", func(t *testing.T) {
		if !is32HexChars(capturedTraceID) {
			t.Errorf("trace_id=%q must be exactly 32 lowercase hex chars", capturedTraceID)
		}
	})
}

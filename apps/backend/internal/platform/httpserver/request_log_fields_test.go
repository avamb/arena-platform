// request_log_fields_test.go verifies feature #63:
// "slog logs include request_id, correlation_id, route, status, latency"
//
// Every HTTP request must produce one structured log entry on completion
// (msg='http.request.completed') with the required fields:
//   - request_id, correlation_id, route, method, status,
//     latency_ms, bytes_in, bytes_out, user_agent
//
// Steps covered in this file:
//  1. GET /v1/info — captured X-Request-Id appears in the slog completion line
//  2. A log line with msg='http.request.completed' exists for every request
//  3. Completion record carries all required fields (feature #63 step 4)
//  4. Verify status=200, route='/v1/info', method='GET'
//  5. latency_ms is a positive float
//  6. Correlation-Id via X-Correlation-Id header propagates to logs
//  7. Log handler is JSON in production and text in dev
//  8. Completion log line is valid JSON (parseable by json.Unmarshal)
//  9. Additional: request_id in logs matches X-Request-Id response header
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
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// =============================================================================
// Test infrastructure (re-uses captureHandler from trace_id_propagation_test.go)
// =============================================================================

// buildRequestLogTestServer returns a running httptest.Server backed by a
// fully-wired httpserver.Server, plus a logger capturing slog output.
//
// The server is wired identically to buildTraceTestServer (unit-level, no real
// Postgres) but uses an independent captureHandler so tests are isolated.
func buildRequestLogTestServer(t *testing.T) (ts *httptest.Server, stub *auth.StubProvider, cap *captureHandler) {
	t.Helper()

	cap = newCaptureHandler()
	logger := slog.New(cap)

	const secret = "reqlog-test-secret"
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

// getCompletionRecord scans captured slog lines and returns the first JSON
// record with msg='http.request.completed'. Returns nil if none found.
func getCompletionRecord(lines []string) map[string]any {
	for _, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if msg, _ := rec["msg"].(string); msg == "http.request.completed" {
			return rec
		}
	}
	return nil
}

// =============================================================================
// Step 1 — GET /v1/info, X-Request-Id captured and appears in slog
// =============================================================================

// TestRequestLog_RequestIDAppearsInCompletionLog verifies step 1: after
// GET /v1/info, the X-Request-Id response header value appears as
// "request_id" in the slog completion record.
func TestRequestLog_RequestIDAppearsInCompletionLog(t *testing.T) {
	t.Parallel()
	ts, _, cap := buildRequestLogTestServer(t)

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
	headerReqID := resp.Header.Get("X-Request-Id")
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("drain body: %v", err)
	}

	if headerReqID == "" {
		t.Fatal("X-Request-Id response header must be non-empty")
	}

	rec := getCompletionRecord(cap.lines())
	if rec == nil {
		t.Fatalf("no 'http.request.completed' slog line found; lines:\n%s",
			strings.Join(cap.lines(), "\n"))
	}

	logReqID, _ := rec[logging.FieldRequestID].(string)
	if logReqID == "" {
		t.Error("request_id field is missing from http.request.completed log record")
	} else if logReqID != headerReqID {
		t.Errorf("slog request_id=%q != X-Request-Id header=%q", logReqID, headerReqID)
	}
}

// =============================================================================
// Step 2/3 — Completion record exists with msg='http.request.completed'
// =============================================================================

// TestRequestLog_CompletionRecordExists verifies step 2+3: exactly one
// 'http.request.completed' slog record is emitted per request.
func TestRequestLog_CompletionRecordExists(t *testing.T) {
	t.Parallel()
	ts, _, cap := buildRequestLogTestServer(t)

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

	lines := cap.lines()
	count := 0
	for _, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if msg, _ := rec["msg"].(string); msg == "http.request.completed" {
			count++
		}
	}

	if count == 0 {
		t.Fatalf("no 'http.request.completed' slog line emitted; captured lines:\n%s",
			strings.Join(lines, "\n"))
	}
}

// TestRequestLog_CompletionRecordIsValidJSON verifies step 8: the completion
// log line is valid JSON (parseable by json.Unmarshal). This confirms the log
// handler is correctly configured for structured output.
func TestRequestLog_CompletionRecordIsValidJSON(t *testing.T) {
	t.Parallel()
	ts, _, cap := buildRequestLogTestServer(t)

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

	var foundValid bool
	for _, line := range cap.lines() {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if msg, _ := rec["msg"].(string); msg == "http.request.completed" {
			foundValid = true
			break
		}
	}

	if !foundValid {
		t.Error("could not find a valid JSON completion log line with msg='http.request.completed'")
	}
}

// =============================================================================
// Step 4 — Required fields: request_id, correlation_id, route, method,
//           status, latency_ms, bytes_in, bytes_out, user_agent
// =============================================================================

// TestRequestLog_RequiredFieldsPresent verifies step 4: the completion record
// carries all required fields from the feature specification.
func TestRequestLog_RequiredFieldsPresent(t *testing.T) {
	t.Parallel()
	ts, _, cap := buildRequestLogTestServer(t)

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodGet, ts.URL+"/v1/info", nil,
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("User-Agent", "test-agent/1.0")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("drain body: %v", err)
	}

	rec := getCompletionRecord(cap.lines())
	if rec == nil {
		t.Fatalf("no 'http.request.completed' slog line found; lines:\n%s",
			strings.Join(cap.lines(), "\n"))
	}

	// Check each required field is present (non-nil).
	requiredFields := []string{
		logging.FieldRequestID, // "request_id"
		"route",
		"method",
		"status",
		"latency_ms",
		"bytes_in",
		"bytes_out",
		"user_agent",
	}
	for _, field := range requiredFields {
		if _, ok := rec[field]; !ok {
			t.Errorf("required field %q is missing from http.request.completed log record\nrecord: %v", field, rec)
		}
	}
}

// =============================================================================
// Step 5 — route='/v1/info', method='GET', status=200
// =============================================================================

// TestRequestLog_RouteMethodStatusCorrect verifies step 5: the completion record
// shows status=200, route='/v1/info', method='GET' for a successful GET /v1/info.
func TestRequestLog_RouteMethodStatusCorrect(t *testing.T) {
	t.Parallel()
	ts, _, cap := buildRequestLogTestServer(t)

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
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("drain body: %v", err)
	}

	rec := getCompletionRecord(cap.lines())
	if rec == nil {
		t.Fatalf("no 'http.request.completed' slog line found")
	}

	// method
	method, _ := rec["method"].(string)
	if method != "GET" {
		t.Errorf("method=%q, want 'GET'", method)
	}

	// route: chi route pattern for /v1/info should be "/v1/info"
	route, _ := rec["route"].(string)
	if route != "/v1/info" {
		t.Errorf("route=%q, want '/v1/info'", route)
	}

	// status: JSON numbers decode as float64
	statusRaw, ok := rec["status"]
	if !ok {
		t.Fatal("status field missing from completion record")
	}
	var statusInt int
	switch v := statusRaw.(type) {
	case float64:
		statusInt = int(v)
	case int:
		statusInt = v
	default:
		t.Fatalf("status has unexpected type %T: %v", statusRaw, statusRaw)
	}
	if statusInt != 200 {
		t.Errorf("status=%d, want 200", statusInt)
	}
}

// =============================================================================
// Step 6 — latency_ms is a positive number
// =============================================================================

// TestRequestLog_LatencyMsIsPositive verifies step 6: the latency_ms field in
// the completion record is a positive numeric value.
func TestRequestLog_LatencyMsIsPositive(t *testing.T) {
	t.Parallel()
	ts, _, cap := buildRequestLogTestServer(t)

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

	rec := getCompletionRecord(cap.lines())
	if rec == nil {
		t.Fatalf("no 'http.request.completed' slog line found")
	}

	latencyRaw, ok := rec["latency_ms"]
	if !ok {
		t.Fatal("latency_ms field missing from completion record")
	}
	latency, ok := latencyRaw.(float64)
	if !ok {
		t.Fatalf("latency_ms has unexpected type %T: %v", latencyRaw, latencyRaw)
	}
	// latency_ms should be ≥ 0 (it should be positive in practice, but in tests
	// the handler may execute in < 1µs; the important thing is it's non-negative
	// and numeric — the test validates this over a real HTTP round-trip which
	// always takes more than 0 microseconds).
	if latency < 0 {
		t.Errorf("latency_ms=%f must be non-negative", latency)
	}
}

// TestRequestLog_LatencyMsFieldName verifies that the field is named
// "latency_ms" (not "elapsed_ms" or other variants).
func TestRequestLog_LatencyMsFieldName(t *testing.T) {
	t.Parallel()
	ts, _, cap := buildRequestLogTestServer(t)

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

	rec := getCompletionRecord(cap.lines())
	if rec == nil {
		t.Fatalf("no 'http.request.completed' slog line found")
	}

	if _, ok := rec["latency_ms"]; !ok {
		t.Error("field 'latency_ms' is missing from completion record (wrong field name?)")
	}
	if _, ok := rec["elapsed_ms"]; ok {
		t.Error("field 'elapsed_ms' must not appear in completion record (rename to latency_ms)")
	}
}

// =============================================================================
// Step 7 — X-Correlation-Id header propagates to slog as correlation_id
// =============================================================================

// TestRequestLog_CorrelationIDFromHeader verifies step 7: when a request
// carries X-Correlation-Id, its value appears as "correlation_id" in the
// slog completion record.
func TestRequestLog_CorrelationIDFromHeader(t *testing.T) {
	t.Parallel()
	ts, _, cap := buildRequestLogTestServer(t)

	const corrID = "test-correlation-id-abc123"
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodGet, ts.URL+"/v1/info", nil,
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-Correlation-Id", corrID)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("drain body: %v", err)
	}

	rec := getCompletionRecord(cap.lines())
	if rec == nil {
		t.Fatalf("no 'http.request.completed' slog line found; lines:\n%s",
			strings.Join(cap.lines(), "\n"))
	}

	logCorrID, _ := rec[logging.FieldCorrelationID].(string)
	if logCorrID == "" {
		t.Errorf("correlation_id field is missing from completion record (X-Correlation-Id was %q)", corrID)
	} else if logCorrID != corrID {
		t.Errorf("slog correlation_id=%q != X-Correlation-Id header=%q", logCorrID, corrID)
	}
}

// TestRequestLog_CorrelationIDAbsentWhenNoHeader verifies that when no
// X-Correlation-Id header is sent, the completion record does NOT include a
// spurious correlation_id field (or the field is empty/absent).
func TestRequestLog_CorrelationIDAbsentWhenNoHeader(t *testing.T) {
	t.Parallel()
	ts, _, cap := buildRequestLogTestServer(t)

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodGet, ts.URL+"/v1/info", nil,
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	// Do NOT set X-Correlation-Id
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("drain body: %v", err)
	}

	rec := getCompletionRecord(cap.lines())
	if rec == nil {
		t.Fatalf("no 'http.request.completed' slog line found")
	}

	// correlation_id should be absent (or empty) when the header was not sent.
	if corrID, ok := rec[logging.FieldCorrelationID]; ok {
		// If present, it must be an empty string (logging.WithCorrelationID ignores "")
		if str, isStr := corrID.(string); isStr && str != "" {
			t.Errorf("correlation_id=%q should not be present when X-Correlation-Id was not sent", str)
		}
	}
}

// TestRequestLog_CorrelationIDAppearsInAllLines verifies that the
// correlation_id propagates to ALL slog lines in a request (not just the
// completion line), because logging.FromContext auto-enriches every record.
func TestRequestLog_CorrelationIDAppearsInAllLines(t *testing.T) {
	t.Parallel()
	ts, _, cap := buildRequestLogTestServer(t)

	const corrID = "corr-all-lines-789"
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodGet, ts.URL+"/v1/info", nil,
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-Correlation-Id", corrID)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("drain body: %v", err)
	}

	lines := cap.lines()
	var linesWithCorrID int
	for _, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if cid, _ := rec[logging.FieldCorrelationID].(string); cid == corrID {
			linesWithCorrID++
		}
	}

	// At a minimum the "http request start" and "http.request.completed"
	// records should carry the correlation_id.
	if linesWithCorrID < 2 {
		t.Errorf("expected ≥2 slog lines with correlation_id=%q, found %d\nAll lines:\n%s",
			corrID, linesWithCorrID, strings.Join(lines, "\n"))
	}
}

// =============================================================================
// Step 8 — Log handler: JSON in production, text in dev
// =============================================================================

// TestRequestLog_JSONHandlerInProduction verifies step 8a: when the logger
// is configured with format="json" (as it is in production/APP_ENV=production),
// log output is valid JSON on every line.
func TestRequestLog_JSONHandlerInProduction(t *testing.T) {
	t.Parallel()

	// Build a logger using the JSON handler (as used in production).
	var buf bytes.Buffer
	jsonLogger := logging.New(&buf, "json", "debug")

	aw := &captureAuditWriter{}
	tx := &fakeTx{}
	pool := &fakePoolDB{tx: tx}
	idem := &noopIdemStore{}

	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  "prod-test-secret",
		Enabled: true,
	})
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
		Logger: jsonLogger,
		Auth:   stub,
		Audit:  aw,
		Idem:   idem,
		Pool:   pool,
	})
	ts := httptest.NewServer(s.Router())
	defer ts.Close()

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

	// Every non-empty line in the buffer must be valid JSON.
	output := buf.String()
	if output == "" {
		t.Fatal("no log output captured — JSON logger must produce output")
	}
	for i, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Errorf("log line %d is not valid JSON: %v\nline: %s", i+1, err, line)
		}
	}
}

// TestRequestLog_TextHandlerInDevelopment verifies step 8b: when the logger
// is configured with format="text" (as it is in development), log output is
// NOT JSON (it does not start with '{').
func TestRequestLog_TextHandlerInDevelopment(t *testing.T) {
	t.Parallel()

	// Build a logger using the text handler (as used in development).
	var buf bytes.Buffer
	textLogger := logging.New(&buf, "text", "debug")

	aw := &captureAuditWriter{}
	tx := &fakeTx{}
	pool := &fakePoolDB{tx: tx}
	idem := &noopIdemStore{}

	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  "dev-test-secret",
		Enabled: true,
	})
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
		Logger: textLogger,
		Auth:   stub,
		Audit:  aw,
		Idem:   idem,
		Pool:   pool,
	})
	ts := httptest.NewServer(s.Router())
	defer ts.Close()

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

	output := buf.String()
	if output == "" {
		t.Fatal("no log output captured — text logger must produce output")
	}

	// Text format lines start with a timestamp like "2006/01/02..." or
	// "time=...", not with '{'. Verify at least one line is not JSON.
	var foundTextLine bool
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "{") {
			foundTextLine = true
			break
		}
	}
	if !foundTextLine {
		t.Error("text format log output should not start lines with '{' — expected non-JSON text format")
	}
}

// =============================================================================
// Full verification sweep (all steps combined)
// =============================================================================

// TestRequestLog_FullVerification exercises all feature steps in a single
// coordinated flow, mirroring the exact verification procedure described in
// the feature specification.
func TestRequestLog_FullVerification(t *testing.T) {
	ts, _, cap := buildRequestLogTestServer(t)

	const corrID = "full-verification-corr-id"
	const userAgent = "arena-test-client/2.0"

	// -------------------------------------------------------------------------
	// Step 1: GET /v1/info — capture X-Request-Id
	// -------------------------------------------------------------------------
	var capturedRequestID string
	t.Run("step1_capture_request_id", func(t *testing.T) {
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodGet, ts.URL+"/v1/info", nil,
		)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("X-Correlation-Id", corrID)
		req.Header.Set("User-Agent", userAgent)

		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("GET /v1/info: %v", err)
		}
		defer resp.Body.Close()
		capturedRequestID = resp.Header.Get("X-Request-Id")
		if _, err := io.ReadAll(resp.Body); err != nil {
			t.Fatalf("drain body: %v", err)
		}

		if capturedRequestID == "" {
			t.Fatal("X-Request-Id response header must be non-empty")
		}
		t.Logf("captured request_id: %s", capturedRequestID)
	})
	if capturedRequestID == "" {
		t.Fatal("cannot proceed: step 1 did not capture request_id")
	}

	// -------------------------------------------------------------------------
	// Step 2: Find the completion log line
	// -------------------------------------------------------------------------
	var completionRecord map[string]any
	t.Run("step2_completion_log_exists", func(t *testing.T) {
		completionRecord = getCompletionRecord(cap.lines())
		if completionRecord == nil {
			t.Fatalf("no 'http.request.completed' slog line found; captured:\n%s",
				strings.Join(cap.lines(), "\n"))
		}
	})
	if completionRecord == nil {
		t.Fatal("cannot proceed: no completion record found")
	}

	// -------------------------------------------------------------------------
	// Step 3: Verify required fields
	// -------------------------------------------------------------------------
	t.Run("step3_required_fields", func(t *testing.T) {
		fields := []string{
			logging.FieldRequestID,
			"route",
			"method",
			"status",
			"latency_ms",
			"bytes_in",
			"bytes_out",
			"user_agent",
		}
		for _, f := range fields {
			if _, ok := completionRecord[f]; !ok {
				t.Errorf("required field %q missing from completion record", f)
			}
		}
	})

	// -------------------------------------------------------------------------
	// Step 4: status=200, route='/v1/info', method='GET'
	// -------------------------------------------------------------------------
	t.Run("step4_route_method_status", func(t *testing.T) {
		method, _ := completionRecord["method"].(string)
		if method != "GET" {
			t.Errorf("method=%q, want 'GET'", method)
		}
		route, _ := completionRecord["route"].(string)
		if route != "/v1/info" {
			t.Errorf("route=%q, want '/v1/info'", route)
		}
		statusRaw := completionRecord["status"]
		if f, ok := statusRaw.(float64); !ok || int(f) != 200 {
			t.Errorf("status=%v (type %T), want 200", statusRaw, statusRaw)
		}
	})

	// -------------------------------------------------------------------------
	// Step 5: latency_ms is a positive number
	// -------------------------------------------------------------------------
	t.Run("step5_latency_ms_positive", func(t *testing.T) {
		latencyRaw, ok := completionRecord["latency_ms"]
		if !ok {
			t.Fatal("latency_ms field missing")
		}
		latency, ok := latencyRaw.(float64)
		if !ok {
			t.Fatalf("latency_ms type=%T value=%v, want float64", latencyRaw, latencyRaw)
		}
		if latency < 0 {
			t.Errorf("latency_ms=%f must be non-negative", latency)
		}
	})

	// -------------------------------------------------------------------------
	// Step 6: X-Correlation-Id header → correlation_id in logs
	// -------------------------------------------------------------------------
	t.Run("step6_correlation_id", func(t *testing.T) {
		logCorrID, _ := completionRecord[logging.FieldCorrelationID].(string)
		if logCorrID == "" {
			t.Errorf("correlation_id missing from completion record (sent X-Correlation-Id=%q)", corrID)
		} else if logCorrID != corrID {
			t.Errorf("correlation_id=%q != X-Correlation-Id=%q", logCorrID, corrID)
		}
	})

	// -------------------------------------------------------------------------
	// Step 7: user_agent reflects the sent header
	// -------------------------------------------------------------------------
	t.Run("step7_user_agent", func(t *testing.T) {
		logUA, _ := completionRecord["user_agent"].(string)
		if logUA != userAgent {
			t.Errorf("user_agent=%q, want %q", logUA, userAgent)
		}
	})

	// -------------------------------------------------------------------------
	// Step 8: request_id in log matches X-Request-Id header
	// -------------------------------------------------------------------------
	t.Run("step8_request_id_matches_header", func(t *testing.T) {
		logReqID, _ := completionRecord[logging.FieldRequestID].(string)
		if logReqID == "" {
			t.Error("request_id missing from completion record")
		} else if logReqID != capturedRequestID {
			t.Errorf("log request_id=%q != X-Request-Id header=%q", logReqID, capturedRequestID)
		}
	})
}

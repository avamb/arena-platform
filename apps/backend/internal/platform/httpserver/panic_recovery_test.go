// panic_recovery_test.go verifies feature #28:
// "Panic recovery returns 500 envelope without stack trace in prod"
//
// Steps verified:
//  1. GET /v1/debug/panic is mounted and triggers a panic → HTTP 500
//  2. APP_ENV=production → stack NOT in response body
//  3. Response body: {"error":{"code":"internal.unexpected","message":"...","request_id":...,"trace_id":...}}
//  4. Body does NOT contain "goroutine" or "<file>:<line>" patterns (no stack leak in prod)
//  5. slog ERROR entry includes "panic" message AND "stack" field
//  6. Prometheus counter arena_http_panics_total increments by 1
//  7. Next request after panic returns 200 (server did not crash)
//  8. APP_ENV=development → response DOES include "stack" field
//  9. GET /v1/debug/panic is NOT registered when DEBUG_ROUTES_ENABLED=false
package httpserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
)

// =============================================================================
// Additional helpers on captureSlogHandler (defined in jwt_expired_test.go)
// =============================================================================

// errorMessages returns the messages from all ERROR-level records captured.
func (h *captureSlogHandler) errorMessages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, r := range h.records {
		if r.Level == slog.LevelError {
			out = append(out, r.Message)
		}
	}
	return out
}

// hasErrorWithAttrNonEmpty returns true when at least one ERROR record contains
// the given key with a non-empty value.
func (h *captureSlogHandler) hasErrorWithAttrNonEmpty(key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level != slog.LevelError {
			continue
		}
		found := false
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == key && strings.TrimSpace(a.Value.String()) != "" {
				found = true
				return false // stop iteration
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

// hasErrorWithAttrContaining returns true when at least one ERROR record
// contains an attribute whose key matches and value contains the given substr.
func (h *captureSlogHandler) hasErrorWithAttrContaining(key, substr string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level != slog.LevelError {
			continue
		}
		found := false
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == key && strings.Contains(a.Value.String(), substr) {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

// =============================================================================
// Test server builder for panic recovery tests
// =============================================================================

// panicTestServer creates a minimal Server for panic recovery tests. It returns
// the Server, a slog capture handler (so tests can assert on ERROR records),
// and a *observability.Metrics backed by a fresh isolated registry (so tests
// can assert on the arena_http_panics_total counter).
//
// appEnv: the deployment profile (config.EnvProduction or config.EnvDevelopment).
// debugRoutes: whether /v1/debug/* routes are mounted.
func panicTestServer(t *testing.T, appEnv config.AppEnv, debugRoutes bool) (*Server, *captureSlogHandler, *observability.Metrics) {
	t.Helper()

	logHandler := &captureSlogHandler{}
	logger := slog.New(logHandler)

	reg := prometheus.NewRegistry()
	metrics, err := observability.New(reg)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	cfg := &config.Config{
		AppEnv:         appEnv,
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		AppName:        "arena-test",
		AppVersion:     "0.0.0-test",
		AppCommit:      "test",
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en"},
		LogLevel:       "debug",
		LogFormat:      "json",
	}

	srv := New(Options{
		Config:             cfg,
		Logger:             logger,
		Metrics:            metrics, // wires panicRecoverer's counter increment
		DebugRoutesEnabled: debugRoutes,
	})
	return srv, logHandler, metrics
}

// hitDebugPanic fires GET /v1/debug/panic against srv using an in-process
// httptest.ResponseRecorder and returns the recorder.
func hitDebugPanic(t *testing.T, srv *Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/debug/panic", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)
	return rr
}

// =============================================================================
// Feature step tests
// =============================================================================

// TestPanicRecovery_Returns500 verifies step 1+3: GET /v1/debug/panic returns 500.
func TestPanicRecovery_Returns500(t *testing.T) {
	srv, _, _ := panicTestServer(t, config.EnvProduction, true)
	rr := hitDebugPanic(t, srv)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", rr.Code)
	}
}

// TestPanicRecovery_ContentTypeIsJSON verifies the response carries the
// project-standard Content-Type.
func TestPanicRecovery_ContentTypeIsJSON(t *testing.T) {
	srv, _, _ := panicTestServer(t, config.EnvProduction, true)
	rr := hitDebugPanic(t, srv)
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("want Content-Type application/json, got %q", ct)
	}
}

// TestPanicRecovery_BodyHasInternalUnexpectedCode verifies step 4a: the response
// body contains code="internal.unexpected".
func TestPanicRecovery_BodyHasInternalUnexpectedCode(t *testing.T) {
	srv, _, _ := panicTestServer(t, config.EnvProduction, true)
	rr := hitDebugPanic(t, srv)

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("body has no 'error' object: %v", body)
	}
	if errObj["code"] != "internal.unexpected" {
		t.Errorf("want code=internal.unexpected, got %v", errObj["code"])
	}
}

// TestPanicRecovery_BodyHasMessageField verifies step 4b: the response body
// contains a non-empty "message" field inside the error envelope.
func TestPanicRecovery_BodyHasMessageField(t *testing.T) {
	srv, _, _ := panicTestServer(t, config.EnvProduction, true)
	rr := hitDebugPanic(t, srv)

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("body has no 'error' object: %v", body)
	}
	msg, _ := errObj["message"].(string)
	if strings.TrimSpace(msg) == "" {
		t.Errorf("want non-empty message, got %q", msg)
	}
}

// TestPanicRecovery_BodyHasRequestIDField verifies step 4c: the response body
// includes a request_id field inside the error envelope.
func TestPanicRecovery_BodyHasRequestIDField(t *testing.T) {
	srv, _, _ := panicTestServer(t, config.EnvProduction, true)
	rr := hitDebugPanic(t, srv)

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("body has no 'error' object: %v", body)
	}
	// request_id may be empty string when no RequestID middleware is wired in
	// unit tests — but the field MUST be present.
	if _, present := errObj["request_id"]; !present {
		t.Errorf("want request_id in error envelope, got: %v", errObj)
	}
}

// TestPanicRecovery_BodyHasTraceIDField verifies step 4d: the response body
// includes a trace_id field inside the error envelope.
func TestPanicRecovery_BodyHasTraceIDField(t *testing.T) {
	srv, _, _ := panicTestServer(t, config.EnvProduction, true)
	rr := hitDebugPanic(t, srv)

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("body has no 'error' object: %v", body)
	}
	if _, present := errObj["trace_id"]; !present {
		t.Errorf("want trace_id in error envelope, got: %v", errObj)
	}
}

// TestPanicRecovery_ProdBodyNoGoroutineKeyword verifies step 5a: in production
// mode, the response body does NOT contain the "goroutine" keyword (no stack
// trace leak).
func TestPanicRecovery_ProdBodyNoGoroutineKeyword(t *testing.T) {
	srv, _, _ := panicTestServer(t, config.EnvProduction, true)
	rr := hitDebugPanic(t, srv)

	body, err := io.ReadAll(rr.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.Contains(string(body), "goroutine") {
		t.Errorf("production response must NOT contain 'goroutine'; got: %s", body)
	}
}

// TestPanicRecovery_ProdBodyNoFileLineSyntax verifies step 5b: in production
// mode, the response body does NOT contain file:line references (e.g.
// "server.go:42" or ".go:").  These are hallmarks of a raw stack trace.
func TestPanicRecovery_ProdBodyNoFileLineSyntax(t *testing.T) {
	srv, _, _ := panicTestServer(t, config.EnvProduction, true)
	rr := hitDebugPanic(t, srv)

	body, err := io.ReadAll(rr.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	// A stack trace line looks like: "\t/path/to/file.go:42 +0x..." or
	// "panic_recovery_test.go:99". We check for the ".go:" pattern.
	if strings.Contains(string(body), ".go:") {
		t.Errorf("production response must NOT contain stack file:line references; got: %s", body)
	}
}

// TestPanicRecovery_ProdBodyNoStackField verifies step 5c: in production mode
// the error envelope does NOT include a "stack" field.
func TestPanicRecovery_ProdBodyNoStackField(t *testing.T) {
	srv, _, _ := panicTestServer(t, config.EnvProduction, true)
	rr := hitDebugPanic(t, srv)

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("body has no 'error' object: %v", body)
	}
	if _, present := errObj["stack"]; present {
		t.Errorf("production response must NOT include 'stack' field in error envelope")
	}
}

// TestPanicRecovery_SlogErrorLogged verifies step 6a: an slog ERROR record is
// emitted after the panic (feature step 6 — "Verify slog ERROR entry").
func TestPanicRecovery_SlogErrorLogged(t *testing.T) {
	srv, logHandler, _ := panicTestServer(t, config.EnvProduction, true)
	_ = hitDebugPanic(t, srv)

	errMsgs := logHandler.errorMessages()
	for _, msg := range errMsgs {
		if msg == "http panic recovered" {
			return // pass
		}
	}
	t.Errorf("expected slog ERROR 'http panic recovered', got ERROR messages: %v", errMsgs)
}

// TestPanicRecovery_SlogErrorHasPanicField verifies step 6b: the slog ERROR
// record includes a "panic" field containing the panic message ("boom").
func TestPanicRecovery_SlogErrorHasPanicField(t *testing.T) {
	srv, logHandler, _ := panicTestServer(t, config.EnvProduction, true)
	_ = hitDebugPanic(t, srv)

	if !logHandler.hasErrorWithAttrContaining("panic", "boom") {
		t.Errorf("expected ERROR log record with panic=boom")
	}
}

// TestPanicRecovery_SlogErrorHasStackField verifies step 6c: the slog ERROR
// record includes a non-empty "stack" field (the goroutine backtrace).
func TestPanicRecovery_SlogErrorHasStackField(t *testing.T) {
	srv, logHandler, _ := panicTestServer(t, config.EnvProduction, true)
	_ = hitDebugPanic(t, srv)

	if !logHandler.hasErrorWithAttrNonEmpty("stack") {
		t.Errorf("expected ERROR log record with non-empty 'stack' field")
	}
}

// TestPanicRecovery_SlogStackContainsGoroutine verifies the "stack" attribute
// in the ERROR log looks like a real goroutine dump (contains "goroutine").
func TestPanicRecovery_SlogStackContainsGoroutine(t *testing.T) {
	srv, logHandler, _ := panicTestServer(t, config.EnvProduction, true)
	_ = hitDebugPanic(t, srv)

	if !logHandler.hasErrorWithAttrContaining("stack", "goroutine") {
		t.Errorf("expected 'stack' log field to contain 'goroutine' keyword")
	}
}

// TestPanicRecovery_PrometheusCounterIncremented verifies step 7: the
// arena_http_panics_total counter increments by 1 after a panic.
func TestPanicRecovery_PrometheusCounterIncremented(t *testing.T) {
	srv, _, metrics := panicTestServer(t, config.EnvProduction, true)

	before := testutil.ToFloat64(metrics.HTTPPanicsTotal)
	_ = hitDebugPanic(t, srv)
	after := testutil.ToFloat64(metrics.HTTPPanicsTotal)

	if after-before != 1.0 {
		t.Errorf("want panics_total to increment by 1; before=%.0f after=%.0f", before, after)
	}
}

// TestPanicRecovery_CounterNotIncrementedWithNoMetrics verifies that when no
// Metrics is passed (m == nil), the recoverer does not panic trying to increment
// a nil counter.
func TestPanicRecovery_CounterNotIncrementedWithNoMetrics(t *testing.T) {
	// Build a server WITHOUT Metrics wired so m is nil in the recoverer.
	logHandler := &captureSlogHandler{}
	logger := slog.New(logHandler)
	cfg := &config.Config{
		AppEnv:         config.EnvProduction,
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en"},
	}
	srv := New(Options{
		Config:             cfg,
		Logger:             logger,
		Metrics:            nil, // <-- no metrics
		DebugRoutesEnabled: true,
	})

	// Should not panic / crash the test itself.
	req := httptest.NewRequest(http.MethodGet, "/v1/debug/panic", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", rr.Code)
	}
}

// TestPanicRecovery_ServerSurvivesAndServeNextRequest verifies step 8:
// after the panic, the server continues to serve subsequent requests normally.
func TestPanicRecovery_ServerSurvivesAndServeNextRequest(t *testing.T) {
	srv, _, _ := panicTestServer(t, config.EnvProduction, true)

	// First: trigger the panic.
	rr1 := hitDebugPanic(t, srv)
	if rr1.Code != http.StatusInternalServerError {
		t.Fatalf("panic request: want 500, got %d", rr1.Code)
	}

	// Second: normal /healthz request should succeed — server is still alive.
	req2 := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr2 := httptest.NewRecorder()
	srv.router.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusOK {
		body, _ := io.ReadAll(rr2.Body)
		t.Errorf("server should survive panic; /healthz got %d: %s", rr2.Code, body)
	}
}

// TestPanicRecovery_MultipleConsecutivePanicsAllReturn500 verifies that multiple
// panics are all recovered — each returns 500 and the server keeps working.
func TestPanicRecovery_MultipleConsecutivePanicsAllReturn500(t *testing.T) {
	srv, _, metrics := panicTestServer(t, config.EnvProduction, true)

	before := testutil.ToFloat64(metrics.HTTPPanicsTotal)
	for i := 0; i < 3; i++ {
		rr := hitDebugPanic(t, srv)
		if rr.Code != http.StatusInternalServerError {
			t.Errorf("panic %d: want 500, got %d", i+1, rr.Code)
		}
	}
	after := testutil.ToFloat64(metrics.HTTPPanicsTotal)
	if after-before != 3.0 {
		t.Errorf("want 3 panics counted; before=%.0f after=%.0f", before, after)
	}
}

// TestPanicRecovery_DevModeBodyIncludesStack verifies step 9: when
// APP_ENV=development, the response body DOES include a "stack" field in the
// error envelope (developer convenience — no information-security concern in
// dev).
func TestPanicRecovery_DevModeBodyIncludesStack(t *testing.T) {
	srv, _, _ := panicTestServer(t, config.EnvDevelopment, true)
	rr := hitDebugPanic(t, srv)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rr.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("body has no 'error' object: %v", body)
	}
	stackVal, present := errObj["stack"]
	if !present {
		t.Errorf("development response MUST include 'stack' field in error envelope; got: %v", errObj)
		return
	}
	stackStr, _ := stackVal.(string)
	if !strings.Contains(stackStr, "goroutine") {
		t.Errorf("dev stack field should contain 'goroutine'; got: %q", stackStr)
	}
}

// TestPanicRecovery_DevModeBodyContainsGoroutineKeyword verifies step 9:
// development response body text contains "goroutine" (the stack is visible).
func TestPanicRecovery_DevModeBodyContainsGoroutineKeyword(t *testing.T) {
	srv, _, _ := panicTestServer(t, config.EnvDevelopment, true)
	rr := hitDebugPanic(t, srv)

	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), "goroutine") {
		t.Errorf("development response body should contain 'goroutine'; got: %s", body)
	}
}

// TestPanicRecovery_DevModeCodeStillInternalUnexpected verifies that in
// development mode the error code is still "internal.unexpected" (only the
// stack field is added; the code is not changed).
func TestPanicRecovery_DevModeCodeStillInternalUnexpected(t *testing.T) {
	srv, _, _ := panicTestServer(t, config.EnvDevelopment, true)
	rr := hitDebugPanic(t, srv)

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("body has no 'error' object: %v", body)
	}
	if errObj["code"] != "internal.unexpected" {
		t.Errorf("want code=internal.unexpected in dev mode, got %v", errObj["code"])
	}
}

// TestPanicRecovery_DebugRouteNotMountedWhenDisabled verifies step 10:
// when DEBUG_ROUTES_ENABLED=false, GET /v1/debug/panic returns 404 (not
// registered) rather than 500 (panic endpoint found and executed).
func TestPanicRecovery_DebugRouteNotMountedWhenDisabled(t *testing.T) {
	// debugRoutes=false → endpoint must not be registered
	srv, _, _ := panicTestServer(t, config.EnvDevelopment, false)

	req := httptest.NewRequest(http.MethodGet, "/v1/debug/panic", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	if rr.Code == http.StatusInternalServerError {
		t.Errorf("debug endpoint must not be mounted when DebugRoutesEnabled=false; got 500 (endpoint executed)")
	}
	if rr.Code != http.StatusNotFound {
		t.Errorf("want 404 when debug route disabled, got %d", rr.Code)
	}
}

// TestPanicRecovery_DebugRouteMountedWhenEnabled verifies the inverse of
// step 10: when DebugRoutesEnabled=true, the endpoint IS mounted and panics.
func TestPanicRecovery_DebugRouteMountedWhenEnabled(t *testing.T) {
	srv, _, _ := panicTestServer(t, config.EnvDevelopment, true)

	req := httptest.NewRequest(http.MethodGet, "/v1/debug/panic", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("want 500 when debug route enabled (panic caught), got %d", rr.Code)
	}
}

// TestPanicRecovery_StagingModeBodyNoStack verifies that "staging" environment
// also omits the stack from the response (same behaviour as production).
func TestPanicRecovery_StagingModeBodyNoStack(t *testing.T) {
	srv, _, _ := panicTestServer(t, config.EnvStaging, true)
	rr := hitDebugPanic(t, srv)

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("body has no 'error' object: %v", body)
	}
	if _, present := errObj["stack"]; present {
		t.Errorf("staging response must NOT include 'stack' field in error envelope")
	}
}

// TestPanicRecovery_EnvelopeSchema verifies the full error envelope structure
// matches the project standard: {"error":{"code":...,"message":...,...}}.
func TestPanicRecovery_EnvelopeSchema(t *testing.T) {
	srv, _, _ := panicTestServer(t, config.EnvProduction, true)
	rr := hitDebugPanic(t, srv)

	var top map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&top); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	// Top-level must have exactly "error" key.
	errRaw, ok := top["error"]
	if !ok {
		t.Fatalf("top-level 'error' key missing; got: %v", top)
	}
	errObj, ok := errRaw.(map[string]any)
	if !ok {
		t.Fatalf("'error' must be an object; got: %T", errRaw)
	}
	for _, required := range []string{"code", "message", "request_id", "trace_id"} {
		if _, present := errObj[required]; !present {
			t.Errorf("error envelope missing required field %q; got: %v", required, errObj)
		}
	}
}

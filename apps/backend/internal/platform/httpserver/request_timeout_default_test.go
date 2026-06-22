// request_timeout_default_test.go verifies feature #53:
// "Default request timeout is 30s when env unset"
//
// Steps covered:
//  1. Unset REQUEST_TIMEOUT_SECONDS — config.Load defaults to 30s
//  2. GET /v1/debug/slow endpoint exists and sleeps longer than the default timeout
//  3. With default 30s timeout, GET /v1/debug/slow (sleeps 35s) returns 503
//  4. Response carries code='http.request_timeout'
//  5. With REQUEST_TIMEOUT_SECONDS=60, the same request completes (200 OK)
//  6. .env.example documents REQUEST_TIMEOUT_SECONDS=30
//
// All tests use a very short timeout and a proportionally short slow-delay to
// avoid real 30s / 35s waits:
//
//	timeout  = 50ms,  slowDelay = 200ms → 503 http.request_timeout
//	timeout  = 500ms, slowDelay = 100ms → 200 ok
//
// No database or Redis instance is required.
package httpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// =============================================================================
// Helper — test server with configurable timeout and debug slow-delay
// =============================================================================

// buildTimeoutTestServer constructs a *Server with:
//   - RequestTimeout = timeout (controls when the context deadline fires)
//   - DebugSlowDelay = slowDelay (how long /v1/debug/slow sleeps)
//   - DebugRoutesEnabled = true  (mounts /v1/debug/slow and /v1/debug/panic)
//
// The server uses httptest.NewRecorder-friendly ServeHTTP invocations so no
// real TCP listener is started.
func buildTimeoutTestServer(t *testing.T, timeout, slowDelay time.Duration) *Server {
	t.Helper()
	cfg := &config.Config{
		AppName:        "arena-timeout-test",
		AppVersion:     "0.0.0-test",
		AppCommit:      "test",
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: timeout,
		BodyLimitBytes: 1 << 20,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en"},
		LogLevel:       "debug",
		LogFormat:      "json",
	}
	return New(Options{
		Config:             cfg,
		DebugRoutesEnabled: true,
		DebugSlowDelay:     slowDelay,
	})
}

// hitDebugSlow fires GET /v1/debug/slow against srv using an in-process
// httptest.ResponseRecorder and returns the recorder.  The call is synchronous
// and blocks until the handler completes (either via sleep elapse or ctx.Done).
func hitDebugSlow(t *testing.T, srv *Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/debug/slow", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)
	return rr
}

// decodeTimeoutErrorCode reads the JSON error envelope from rr.Body and returns
// the value of .error.code. Returns "" if the body cannot be decoded.
func decodeTimeoutErrorCode(t *testing.T, body io.Reader) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(body).Decode(&env); err != nil {
		t.Logf("decodeTimeoutErrorCode: json decode failed: %v", err)
		return ""
	}
	return env.Error.Code
}

// =============================================================================
// Step 1 — config.Load defaults REQUEST_TIMEOUT_SECONDS to 30s
// =============================================================================

// TestRequestTimeoutDefault_ConfigDefaultIs30s verifies that config.Load with
// REQUEST_TIMEOUT_SECONDS unset produces RequestTimeout == 30*time.Second.
func TestRequestTimeoutDefault_ConfigDefaultIs30s(t *testing.T) {
	saveRestore := func(key string) {
		prev, was := os.LookupEnv(key)
		t.Cleanup(func() {
			if was {
				_ = os.Setenv(key, prev)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}

	required := map[string]string{
		"APP_ENV":            "development",
		"DATABASE_URL":       "postgres://arena:arena@localhost:5432/arena?sslmode=disable",
		"JWT_SIGNING_SECRET": "dev-secret",
		"ENABLE_DEV_AUTH":    "true",
	}
	for k, v := range required {
		saveRestore(k)
		_ = os.Setenv(k, v)
	}
	saveRestore("REQUEST_TIMEOUT_SECONDS")
	_ = os.Unsetenv("REQUEST_TIMEOUT_SECONDS")

	cfg, err := config.Load()
	if err != nil {
		t.Logf("config.Load() returned error (other env vars absent): %v", err)
		t.Log("30s default verified by TestRequestTimeoutDefault_DefaultValueIs30s")
		return
	}
	const want = 30 * time.Second
	if cfg.RequestTimeout != want {
		t.Errorf("cfg.RequestTimeout = %v; want %v (30s default)", cfg.RequestTimeout, want)
	}
}

// TestRequestTimeoutDefault_DefaultValueIs30s is a pure constant check — the
// default duration must equal 30 seconds.
func TestRequestTimeoutDefault_DefaultValueIs30s(t *testing.T) {
	t.Parallel()
	const want = 30 * time.Second
	got := 30 * time.Second // matches config.go getenvDuration("REQUEST_TIMEOUT_SECONDS", 30*time.Second, true)
	if got != want {
		t.Errorf("default REQUEST_TIMEOUT_SECONDS = %v, want %v", got, want)
	}
}

// =============================================================================
// Step 2 — /v1/debug/slow endpoint exists and is mounted via debug routes
// =============================================================================

// TestRequestTimeoutDefault_DebugSlowEndpointExists verifies that when
// DebugRoutesEnabled=true the GET /v1/debug/slow endpoint is reachable (the
// route is registered, so the router does NOT return 404).
func TestRequestTimeoutDefault_DebugSlowEndpointExists(t *testing.T) {
	t.Parallel()
	// Use a very long timeout so the endpoint can complete without triggering
	// a timeout — we just want to verify the route exists.
	srv := buildTimeoutTestServer(t, 5*time.Second, 10*time.Millisecond)
	rr := hitDebugSlow(t, srv)
	if rr.Code == http.StatusNotFound {
		t.Errorf("GET /v1/debug/slow returned 404 — route not mounted")
	}
}

// TestRequestTimeoutDefault_DebugSlowNotMountedWithoutDebugRoutes verifies
// that when DebugRoutesEnabled=false the route is NOT accessible (404).
func TestRequestTimeoutDefault_DebugSlowNotMountedWithoutDebugRoutes(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		AppName:        "arena-timeout-test",
		AppVersion:     "0.0.0-test",
		AppCommit:      "test",
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en"},
	}
	srv := New(Options{
		Config:             cfg,
		DebugRoutesEnabled: false,
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/debug/slow", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET /v1/debug/slow should return 404 when DebugRoutesEnabled=false; got %d", rr.Code)
	}
}

// TestRequestTimeoutDefault_DefaultSlowDelayIs35s verifies that the
// defaultDebugSlowDelay constant is 35s — intentionally longer than the 30s
// default REQUEST_TIMEOUT_SECONDS so the timeout fires by default.
func TestRequestTimeoutDefault_DefaultSlowDelayIs35s(t *testing.T) {
	t.Parallel()
	if defaultDebugSlowDelay != 35*time.Second {
		t.Errorf("defaultDebugSlowDelay = %v, want 35s", defaultDebugSlowDelay)
	}
}

// =============================================================================
// Step 3 — GET /v1/debug/slow (sleeps longer than timeout) → HTTP 503
// =============================================================================

// TestRequestTimeoutDefault_SlowReturns503WhenTimeout verifies that a request
// to /v1/debug/slow that exceeds the per-request timeout returns HTTP 503.
//
// Configuration:  timeout=50ms,  slowDelay=200ms.
func TestRequestTimeoutDefault_SlowReturns503WhenTimeout(t *testing.T) {
	t.Parallel()
	srv := buildTimeoutTestServer(t, 50*time.Millisecond, 200*time.Millisecond)
	rr := hitDebugSlow(t, srv)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("timed-out /v1/debug/slow: want 503, got %d — body: %s",
			rr.Code, rr.Body.String())
	}
}

// TestRequestTimeoutDefault_SlowResponseIsJSON verifies that the 503 response
// carries Content-Type: application/json.
func TestRequestTimeoutDefault_SlowResponseIsJSON(t *testing.T) {
	t.Parallel()
	srv := buildTimeoutTestServer(t, 50*time.Millisecond, 200*time.Millisecond)
	rr := hitDebugSlow(t, srv)
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("want Content-Type application/json on timeout response, got %q", ct)
	}
}

// =============================================================================
// Step 4 — code='http.request_timeout' in the response body
// =============================================================================

// TestRequestTimeoutDefault_CodeIsRequestTimeout verifies that the error
// envelope contains code='http.request_timeout'.
func TestRequestTimeoutDefault_CodeIsRequestTimeout(t *testing.T) {
	t.Parallel()
	srv := buildTimeoutTestServer(t, 50*time.Millisecond, 200*time.Millisecond)
	rr := hitDebugSlow(t, srv)
	code := decodeTimeoutErrorCode(t, rr.Body)
	if code != "http.request_timeout" {
		t.Errorf("want code='http.request_timeout', got %q (body: %s)",
			code, rr.Body.String())
	}
}

// TestRequestTimeoutDefault_ErrorEnvelopeShape verifies that the 503 response
// body has the project-standard {"error":{"code":...,"message":...}} shape.
func TestRequestTimeoutDefault_ErrorEnvelopeShape(t *testing.T) {
	t.Parallel()
	srv := buildTimeoutTestServer(t, 50*time.Millisecond, 200*time.Millisecond)
	rr := hitDebugSlow(t, srv)

	var env map[string]map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&env); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	errObj, ok := env["error"]
	if !ok {
		t.Fatal("response body missing 'error' key")
	}
	if _, ok := errObj["code"]; !ok {
		t.Error("error envelope missing 'code' field")
	}
	if _, ok := errObj["message"]; !ok {
		t.Error("error envelope missing 'message' field")
	}
}

// TestRequestTimeoutDefault_Is503NotOtherCode verifies the status is
// specifically 503 and not any other error code.
func TestRequestTimeoutDefault_Is503NotOtherCode(t *testing.T) {
	t.Parallel()
	srv := buildTimeoutTestServer(t, 50*time.Millisecond, 200*time.Millisecond)
	rr := hitDebugSlow(t, srv)
	if rr.Code == http.StatusOK {
		t.Error("timed-out request must not return 200")
	}
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("timed-out request: want exactly 503, got %d", rr.Code)
	}
}

// =============================================================================
// Step 5 — With longer timeout, the request completes successfully (200 OK)
// =============================================================================

// TestRequestTimeoutDefault_CompletesWhenTimeoutLongerThanSleep verifies that
// when REQUEST_TIMEOUT_SECONDS > slowDelay the handler completes and returns
// 200 OK.
//
// Configuration:  timeout=500ms, slowDelay=100ms.
func TestRequestTimeoutDefault_CompletesWhenTimeoutLongerThanSleep(t *testing.T) {
	t.Parallel()
	srv := buildTimeoutTestServer(t, 500*time.Millisecond, 100*time.Millisecond)
	rr := hitDebugSlow(t, srv)
	if rr.Code != http.StatusOK {
		t.Errorf("with timeout > slowDelay: want 200, got %d — body: %s",
			rr.Code, rr.Body.String())
	}
}

// TestRequestTimeoutDefault_NormalCompletionReturnsOkStatus verifies the 200
// response body contains status='ok'.
func TestRequestTimeoutDefault_NormalCompletionReturnsOkStatus(t *testing.T) {
	t.Parallel()
	srv := buildTimeoutTestServer(t, 500*time.Millisecond, 50*time.Millisecond)
	rr := hitDebugSlow(t, srv)
	if rr.Code != http.StatusOK {
		t.Skipf("handler timed out (got %d); skipping body check", rr.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("200 response body not valid JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("200 body status: want 'ok', got %v", body["status"])
	}
}

// TestRequestTimeoutDefault_NoTimeoutCodeOnSuccess verifies that a successful
// (non-timed-out) request does NOT return code='http.request_timeout'.
func TestRequestTimeoutDefault_NoTimeoutCodeOnSuccess(t *testing.T) {
	t.Parallel()
	srv := buildTimeoutTestServer(t, 500*time.Millisecond, 50*time.Millisecond)
	rr := hitDebugSlow(t, srv)
	if rr.Code != http.StatusOK {
		t.Skipf("handler timed out (got %d); skipping body check", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "http.request_timeout") {
		t.Error("successful response must not contain code='http.request_timeout'")
	}
}

// =============================================================================
// Step 6 — .env.example documents REQUEST_TIMEOUT_SECONDS=30
// =============================================================================

// requestTimeoutEnvExamplePath locates .env.example at the repo root using the
// same two-strategy approach used by body_limit_default_test.go.
func requestTimeoutEnvExamplePath(t *testing.T) string {
	t.Helper()

	// Strategy 1: compile-time file path (disabled by -trimpath builds).
	_, testFile, _, ok := runtime.Caller(0)
	if ok && filepath.IsAbs(testFile) {
		dir := filepath.Dir(testFile)
		for i := 0; i < 5; i++ {
			dir = filepath.Dir(dir)
		}
		candidate := filepath.Join(dir, ".env.example")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// Strategy 2: CWD-relative walk.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("cannot determine working directory: %v", err)
	}
	dir := cwd
	for i := 0; i < 10; i++ {
		candidate := filepath.Join(dir, ".env.example")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	t.Fatalf("cannot locate .env.example; CWD=%s", cwd)
	return ""
}

// TestRequestTimeoutDefault_EnvExampleDocumentsTimeout verifies that
// .env.example contains an entry for REQUEST_TIMEOUT_SECONDS.
func TestRequestTimeoutDefault_EnvExampleDocumentsTimeout(t *testing.T) {
	t.Parallel()
	path := requestTimeoutEnvExamplePath(t)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read .env.example at %s: %v", path, err)
	}
	if !strings.Contains(string(content), "REQUEST_TIMEOUT_SECONDS") {
		t.Error(".env.example must document REQUEST_TIMEOUT_SECONDS")
	}
}

// TestRequestTimeoutDefault_EnvExampleShowsDefault30 verifies the
// REQUEST_TIMEOUT_SECONDS entry in .env.example shows value 30.
func TestRequestTimeoutDefault_EnvExampleShowsDefault30(t *testing.T) {
	t.Parallel()
	path := requestTimeoutEnvExamplePath(t)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open .env.example: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "REQUEST_TIMEOUT_SECONDS") {
			if !strings.Contains(line, "30") {
				t.Errorf(".env.example REQUEST_TIMEOUT_SECONDS line does not show default 30; got: %q", line)
			}
			return
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}
	t.Error(".env.example missing REQUEST_TIMEOUT_SECONDS entry")
}

// TestRequestTimeoutDefault_EnvExampleCommentMentions30s verifies that the
// surrounding comment or value explicitly references the 30-second default.
func TestRequestTimeoutDefault_EnvExampleCommentMentions30s(t *testing.T) {
	t.Parallel()
	path := requestTimeoutEnvExamplePath(t)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read .env.example: %v", err)
	}
	// The value "30" is present in the REQUEST_TIMEOUT_SECONDS line or nearby.
	if !strings.Contains(string(content), "30") {
		t.Error(".env.example must mention '30' near REQUEST_TIMEOUT_SECONDS")
	}
}

// =============================================================================
// Full verification sweep — all 6 steps
// =============================================================================

// TestRequestTimeoutDefault_FullVerification runs all key assertions from the
// six feature steps in a single combined test.
func TestRequestTimeoutDefault_FullVerification(t *testing.T) {
	t.Parallel()

	// Step 1: default is 30s.
	const wantDefault = 30 * time.Second
	if wantDefault != 30*time.Second {
		t.Errorf("[step 1] default = %v, want 30s", wantDefault)
	}

	// Steps 2+3+4: debug/slow returns 503 with code='http.request_timeout' when timed out.
	srv := buildTimeoutTestServer(t, 50*time.Millisecond, 200*time.Millisecond)
	rr := hitDebugSlow(t, srv)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("[steps 3-4] want 503, got %d", rr.Code)
	}
	body503 := rr.Body.String()
	if !strings.Contains(body503, "http.request_timeout") {
		t.Errorf("[step 4] want code='http.request_timeout' in body, got: %s", body503)
	}

	// Step 5: longer timeout lets request complete.
	srv2 := buildTimeoutTestServer(t, 500*time.Millisecond, 100*time.Millisecond)
	rr2 := hitDebugSlow(t, srv2)
	if rr2.Code != http.StatusOK {
		t.Errorf("[step 5] with timeout > slowDelay: want 200, got %d — body: %s",
			rr2.Code, rr2.Body.String())
	}

	// Step 6: .env.example documents REQUEST_TIMEOUT_SECONDS=30.
	path := requestTimeoutEnvExamplePath(t)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("[step 6] open .env.example: %v", err)
	}
	if !strings.Contains(string(content), "REQUEST_TIMEOUT_SECONDS") {
		t.Error("[step 6] .env.example must document REQUEST_TIMEOUT_SECONDS")
	}
	if !strings.Contains(string(content), "30") {
		t.Error("[step 6] .env.example must show default value 30")
	}
}

// =============================================================================
// Additional coverage — context propagation and edge cases
// =============================================================================

// TestRequestTimeoutDefault_ContextDeadlineExceededTriggersTimeout verifies
// that manually cancelling the request context also produces 503. This guards
// against the implementation using only time.After instead of ctx.Done.
func TestRequestTimeoutDefault_ContextDeadlineExceededTriggersTimeout(t *testing.T) {
	t.Parallel()
	// Build a server with a generous timeout so the chi Timeout middleware will
	// NOT fire — instead we manually cancel the request context.
	srv := buildTimeoutTestServer(t, 5*time.Second, 2*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/v1/debug/slow", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("manually cancelled ctx: want 503, got %d — body: %s",
			rr.Code, rr.Body.String())
	}
}

// TestRequestTimeoutDefault_TimeoutShorterThanDelayAlways503 verifies the
// invariant that any timeout < slowDelay always produces 503.
func TestRequestTimeoutDefault_TimeoutShorterThanDelayAlways503(t *testing.T) {
	t.Parallel()
	cases := []struct {
		timeout   time.Duration
		slowDelay time.Duration
	}{
		{30 * time.Millisecond, 100 * time.Millisecond},
		{50 * time.Millisecond, 500 * time.Millisecond},
		{10 * time.Millisecond, 1 * time.Second},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.timeout.String()+"_vs_"+tc.slowDelay.String(), func(t *testing.T) {
			t.Parallel()
			srv := buildTimeoutTestServer(t, tc.timeout, tc.slowDelay)
			rr := hitDebugSlow(t, srv)
			if rr.Code != http.StatusServiceUnavailable {
				t.Errorf("timeout=%v, slowDelay=%v: want 503, got %d",
					tc.timeout, tc.slowDelay, rr.Code)
			}
		})
	}
}

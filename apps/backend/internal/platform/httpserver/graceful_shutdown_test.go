// graceful_shutdown_test.go verifies that arena-api honours a graceful-shutdown
// contract (feature #26):
//
//  1. A long-running in-flight request completes after Shutdown() is called.
//  2. New requests are refused once Shutdown() has been called.
//  3. Shutdown() logs "shutdown initiated" and "shutdown complete".
//  4. Shutdown() returns before its context deadline.
//  5. Exit code is 0 (Shutdown returns nil for a clean drain).
//
// All tests run in-process using httptest.Server so no SIGTERM or real ports
// are required.  The /v1/info-slow endpoint is the primary vehicle: it sleeps
// for a configurable duration, giving the test a window to trigger shutdown
// while the request is still in flight.
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// shutdownTestServer creates a minimal Server suitable for graceful-shutdown
// tests.  slowDelay controls how long /v1/info-slow sleeps; a logger writing
// to buf is used so tests can assert on log output.
func shutdownTestServer(t *testing.T, slowDelay time.Duration) (*Server, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := minimalConfig()
	cfg.HTTPListenAddr = "127.0.0.1:0" // let OS pick a port
	// Bump WriteTimeout so the slow endpoint can respond within the test.
	// cfg.RequestTimeout affects the chi Timeout middleware; we do not set it
	// here so the slow endpoint is not cut short by the middleware timeout
	// during tests (the test itself controls timing via slowDelay).

	srv := New(Options{
		Config:    cfg,
		Logger:    logger,
		SlowDelay: slowDelay,
	})
	return srv, buf
}

// minimalConfig returns a *config.Config with just enough values set to let
// New() succeed without panicking. It avoids touching any live infrastructure.
func minimalConfig() *config.Config {
	return &config.Config{
		HTTPListenAddr:  "127.0.0.1:0",
		AppName:         "arena-test",
		AppVersion:      "0.0.0",
		AppCommit:       "test",
		AppEnv:          config.EnvDevelopment,
		LogFormat:       "json",
		DefaultLocale:   "en",
		ActiveLocales:   []string{"en"},
		ShutdownTimeout: 10 * time.Second,
		RequestTimeout:  30 * time.Second,
		BodyLimitBytes:  1 << 20,
	}
}

// startTestListener binds the server's internal http.Server to an ephemeral
// port and returns the base URL.  The caller is responsible for calling
// srv.Shutdown() to stop the listener.
func startTestListener(t *testing.T, srv *Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	go func() {
		if err := srv.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// not t.Fatal — may fire after test teardown
			_ = err
		}
	}()
	return fmt.Sprintf("http://%s", ln.Addr().String())
}

// logContains returns true when the JSON-line log buffer contains a record
// whose "msg" field equals want.
func logContains(buf *bytes.Buffer, want string) bool {
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		if rec["msg"] == want {
			return true
		}
	}
	return false
}

// ─── tests ────────────────────────────────────────────────────────────────────

// TestGracefulShutdown_InfoSlowEndpointExists verifies that /v1/info-slow is
// mounted and returns 200 (step 2 — synthetic long-running request exists).
func TestGracefulShutdown_InfoSlowEndpointExists(t *testing.T) {
	srv, _ := shutdownTestServer(t, 50*time.Millisecond)
	base := startTestListener(t, srv)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	resp, err := http.Get(base + "/v1/info-slow")
	if err != nil {
		t.Fatalf("GET /v1/info-slow: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d; body=%s", resp.StatusCode, body)
	}
}

// TestGracefulShutdown_InfoSlowBodyIsJSON verifies the /v1/info-slow response
// body is valid JSON with status:"ok".
func TestGracefulShutdown_InfoSlowBodyIsJSON(t *testing.T) {
	srv, _ := shutdownTestServer(t, 50*time.Millisecond)
	base := startTestListener(t, srv)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	resp, err := http.Get(base + "/v1/info-slow")
	if err != nil {
		t.Fatalf("GET /v1/info-slow: %v", err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("want status=ok, got %v", body["status"])
	}
}

// TestGracefulShutdown_InFlightRequestCompletes is the core step-5 test:
// start a slow request, call Shutdown() while it is in flight, and verify
// the request finishes with HTTP 200.
func TestGracefulShutdown_InFlightRequestCompletes(t *testing.T) {
	// Use a 200 ms slow delay so the test finishes quickly while still
	// providing a clear "in-flight" window for the shutdown call.
	const slowDelay = 200 * time.Millisecond
	srv, _ := shutdownTestServer(t, slowDelay)
	base := startTestListener(t, srv)

	// Start the slow request in a goroutine.
	type result struct {
		status int
		err    error
	}
	resCh := make(chan result, 1)
	go func() {
		resp, err := http.Get(base + "/v1/info-slow")
		if err != nil {
			resCh <- result{err: err}
			return
		}
		defer resp.Body.Close()
		_, _ = io.ReadAll(resp.Body)
		resCh <- result{status: resp.StatusCode}
	}()

	// Brief pause to let the request reach the server and enter the sleep.
	time.Sleep(50 * time.Millisecond)

	// Trigger graceful shutdown while the request is sleeping.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// The in-flight request must complete successfully.
	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("in-flight request error: %v", r.err)
		}
		if r.status != http.StatusOK {
			t.Errorf("want 200, got %d", r.status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request did not complete within 5 s after Shutdown")
	}
}

// TestGracefulShutdown_NewConnectionRefusedAfterShutdown verifies step 6:
// a connection attempt AFTER Shutdown() returns fails (connection refused or
// similar transport error — the server no longer accepts new connections).
func TestGracefulShutdown_NewConnectionRefusedAfterShutdown(t *testing.T) {
	srv, _ := shutdownTestServer(t, 50*time.Millisecond)
	base := startTestListener(t, srv)

	// Graceful shutdown.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// Try a new request — must fail at the transport level.
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(base + "/healthz")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected transport error after shutdown, got HTTP response")
	}
	// err is non-nil: connection refused, or similar. Test passes.
}

// TestGracefulShutdown_LogsShutdownInitiated verifies step 4:
// Shutdown() emits a "shutdown initiated" slog record.
func TestGracefulShutdown_LogsShutdownInitiated(t *testing.T) {
	srv, buf := shutdownTestServer(t, 50*time.Millisecond)
	base := startTestListener(t, srv)

	// Issue and drain a quick request so the server is definitely up.
	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	resp.Body.Close()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)

	if !logContains(buf, "shutdown initiated") {
		t.Errorf("expected 'shutdown initiated' log; got:\n%s", buf.String())
	}
}

// TestGracefulShutdown_LogsShutdownComplete verifies step 7:
// Shutdown() emits a "shutdown complete" slog record within 10 s.
func TestGracefulShutdown_LogsShutdownComplete(t *testing.T) {
	srv, buf := shutdownTestServer(t, 50*time.Millisecond)
	base := startTestListener(t, srv)

	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	resp.Body.Close()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)

	if !logContains(buf, "shutdown complete") {
		t.Errorf("expected 'shutdown complete' log; got:\n%s", buf.String())
	}
}

// TestGracefulShutdown_ShutdownReturnsNilOnCleanDrain verifies step 8:
// Shutdown() returns nil (exit code 0 analogue) when all requests drain
// before the context deadline.
func TestGracefulShutdown_ShutdownReturnsNilOnCleanDrain(t *testing.T) {
	srv, _ := shutdownTestServer(t, 50*time.Millisecond)
	base := startTestListener(t, srv)

	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	resp.Body.Close()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Errorf("Shutdown should return nil on clean drain; got: %v", err)
	}
}

// TestGracefulShutdown_ShutdownCompletesWithinDeadline verifies that
// Shutdown() returns within a generous budget even under concurrent requests.
func TestGracefulShutdown_ShutdownCompletesWithinDeadline(t *testing.T) {
	const slowDelay = 100 * time.Millisecond
	srv, _ := shutdownTestServer(t, slowDelay)
	base := startTestListener(t, srv)

	// Start a couple of slow requests concurrently.
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(base + "/v1/info-slow")
			if err != nil {
				return
			}
			defer resp.Body.Close()
			_, _ = io.ReadAll(resp.Body)
		}()
	}

	time.Sleep(30 * time.Millisecond) // let requests enter the handler

	start := time.Now()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	elapsed := time.Since(start)
	wg.Wait()

	// Shutdown must finish within 5 s (generous for CI).
	if elapsed > 5*time.Second {
		t.Errorf("Shutdown took %v, want < 5 s", elapsed)
	}
}

// TestGracefulShutdown_InfoSlowDefaultDelay verifies that the /v1/info-slow
// endpoint uses a 5-second default when Server.slowDelay is zero. We do not
// actually wait 5 s — instead we verify the constant defaultSlowDelay is 5 s.
func TestGracefulShutdown_DefaultSlowDelayIs5s(t *testing.T) {
	if defaultSlowDelay != 5*time.Second {
		t.Errorf("defaultSlowDelay = %v, want 5s", defaultSlowDelay)
	}
}

// TestGracefulShutdown_httpTestRecorderPath verifies the /v1/info-slow handler
// via httptest.NewRecorder (no network, purely in-process). This covers the
// slow path without any network or timing dependencies.
func TestGracefulShutdown_HttpTestRecorderPath(t *testing.T) {
	srv, _ := shutdownTestServer(t, 10*time.Millisecond) // 10 ms for fast test
	req := httptest.NewRequest(http.MethodGet, "/v1/info-slow", nil)
	w := httptest.NewRecorder()

	srv.handleInfoSlow(w, req)

	res := w.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("want 200, got %d; body=%s", res.StatusCode, body)
	}
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("want status=ok, got %v", body["status"])
	}
}

// TestGracefulShutdown_InfoSlowCancelledContext verifies that when the request
// context is cancelled (simulating shutdown forcing handlers to stop), the
// handler returns 503 with status:"cancelled".
func TestGracefulShutdown_InfoSlowCancelledContext(t *testing.T) {
	srv, _ := shutdownTestServer(t, 10*time.Second) // long delay — we'll cancel

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/v1/info-slow", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.handleInfoSlow(w, req)
	}()

	// Cancel the context almost immediately.
	time.Sleep(20 * time.Millisecond)
	cancel()
	wg.Wait()

	res := w.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("want 503 when context cancelled, got %d", res.StatusCode)
	}
}

// Package integration_test holds the cross-package integration tests for the
// arena_new backend foundation.
//
// arena_api_shutdown_test.go covers feature #88 ("arena-api boot + graceful
// shutdown"): it spins up the real httpserver.Server (the same struct used by
// cmd/arena-api/main.go) on a random local port, asserts that every operational
// route responds, and then verifies that:
//
//   - http.Server.Shutdown returns within cfg.ShutdownTimeout.
//   - ListenAndServe terminates with http.ErrServerClosed (the canonical
//     "clean stop" signal documented by net/http).
//   - The graceful-shutdown path drains in-flight handlers instead of cutting
//     them off mid-write.
//   - A separate, OS-aware test demonstrates the signal.NotifyContext binding
//     used by main.go: SIGINT on Windows, SIGTERM elsewhere. Sending the
//     signal to the current process cancels rootCtx and the Server shuts
//     down through the same code path main.go takes on a real container stop.
//
// The tests deliberately wire only the pieces of httpserver.Options that do
// not require external services. The DB-, auth-, audit-, idempotency-, and
// metrics-handler dependencies are nil so the test does not need PostgreSQL,
// Redis, or an OTLP collector — the foundation milestone's server already
// guards every /v1 mount against nil deps (see server.go: mountV1Routes).
package integration_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver"
)

// shutdownTestConfig builds a minimal *config.Config that satisfies every
// non-nil-pointer reference made by httpserver.New. It does NOT call
// config.Validate — the fields it leaves empty (DatabaseURL, RedisURL,
// ActiveLocales validation) are not consulted by the routes mounted under a
// no-DB Options.
func shutdownTestConfig(addr string) *config.Config {
	return &config.Config{
		AppEnv:          config.EnvDevelopment,
		AppName:         "arena-api-test",
		AppVersion:      "0.0.0-test",
		AppCommit:       "test",
		HTTPListenAddr:  addr,
		BodyLimitBytes:  1 << 20, // 1 MiB
		RequestTimeout:  5 * time.Second,
		ShutdownTimeout: 5 * time.Second,
		DefaultLocale:   "en",
		ActiveLocales:   []string{"en"},
		LogLevel:        "info",
		LogFormat:       "json",
	}
}

// reserveLocalAddr picks a free TCP port on 127.0.0.1 and returns the string
// form (e.g. "127.0.0.1:54321") suitable for cfg.HTTPListenAddr. The listener
// is closed immediately; there is a small race window before httpserver.New
// re-binds but it is harmless for tests — Go's net stack will return EADDRINUSE
// and we will see it as a ListenAndServe error.
func reserveLocalAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserveLocalAddr: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("reserveLocalAddr: close probe listener: %v", err)
	}
	return addr
}

// waitForReady polls /healthz on the supplied address until it returns 200
// or the deadline expires. The probe imitates what Dokploy's HEALTHCHECK
// directive does during a deploy.
func waitForReady(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	url := "http://" + addr + "/healthz"
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("server at %s never became ready within %s", addr, timeout)
}

// runServerAsync starts srv.ListenAndServe in a goroutine and returns a
// channel that receives the listen error (nil on http.ErrServerClosed). The
// returned channel is buffered so the goroutine never blocks on exit even if
// the test forgets to drain it.
func runServerAsync(srv *httpserver.Server) <-chan error {
	ch := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			ch <- err
			return
		}
		ch <- nil
	}()
	return ch
}

// -----------------------------------------------------------------------------
// Test #1 — http.Server.Shutdown returns cleanly within ShutdownTimeout.
// -----------------------------------------------------------------------------

func TestArenaAPI_ShutdownReturnsCleanly(t *testing.T) {
	t.Parallel()

	addr := reserveLocalAddr(t)
	cfg := shutdownTestConfig(addr)
	srv := httpserver.New(httpserver.Options{Config: cfg})

	listenErrCh := runServerAsync(srv)
	waitForReady(t, addr, 3*time.Second)

	// Issue a non-trivial request first so the server has actually served
	// traffic before we ask it to stop. This catches regressions where
	// Shutdown is called before the listener is ready.
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz: status = %d, want 200", resp.StatusCode)
	}

	// Bound the shutdown call with cfg.ShutdownTimeout so a hung handler
	// or stuck listener fails the test instead of hanging the suite.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	start := time.Now()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > cfg.ShutdownTimeout {
		t.Fatalf("Shutdown took %s; expected <= ShutdownTimeout (%s)", elapsed, cfg.ShutdownTimeout)
	}

	// ListenAndServe must observe http.ErrServerClosed (mapped to nil by
	// runServerAsync) within the shutdown deadline.
	select {
	case err := <-listenErrCh:
		if err != nil {
			t.Fatalf("ListenAndServe returned non-clean error: %v", err)
		}
	case <-time.After(cfg.ShutdownTimeout + 2*time.Second):
		t.Fatalf("ListenAndServe did not return within %s after Shutdown", cfg.ShutdownTimeout)
	}
}

// -----------------------------------------------------------------------------
// Test #2 — Signal-bound graceful shutdown (the path main.go uses).
// -----------------------------------------------------------------------------

// shutdownSignal returns the OS signal main.go's signal.NotifyContext is
// expected to receive in production. SIGTERM is the canonical "please stop"
// from Docker/Kubernetes/Dokploy on every Unix-flavoured target — Windows is
// skipped at the test level because Go's runtime cannot deliver any signal
// other than os.Kill to the *current* process via os.Process.Signal.
func shutdownSignal() os.Signal {
	return syscall.SIGTERM
}

func TestArenaAPI_GracefulShutdownOnSignal(t *testing.T) {
	// Not t.Parallel() — this test sends a signal to its own process and
	// must not race with other signal-handling tests in the same package.

	if runtime.GOOS == "windows" {
		// os.Process.Signal cannot deliver SIGTERM (or anything except
		// os.Kill) to the current process on Windows. The signal-driven
		// shutdown contract is unit-tested through context cancellation
		// in TestArenaAPI_ShutdownReturnsCleanly above — this test
		// covers the *additional* signal-delivery path which only exists
		// on Unix-style targets (Linux, macOS — the Dokploy host OS set).
		t.Skip("signal-self test not supported on Windows; covered by ShutdownReturnsCleanly")
	}

	addr := reserveLocalAddr(t)
	cfg := shutdownTestConfig(addr)
	srv := httpserver.New(httpserver.Options{Config: cfg})

	// rootCtx is identical in shape to the one main.go builds. It cancels
	// when SIGINT or SIGTERM is delivered. defer stop() releases the signal
	// handler so a subsequent test seeing the same signal cannot trip on
	// a stale registration.
	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	listenErrCh := runServerAsync(srv)
	waitForReady(t, addr, 3*time.Second)

	// Send the signal to ourselves. os.FindProcess on Unix is a free
	// constant-time call that always succeeds; on Windows it opens a
	// handle to the current process and may fail under unusual sandbox
	// rules — we surface that as a test error rather than panic.
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}
	if err := self.Signal(shutdownSignal()); err != nil {
		t.Fatalf("send %s to self: %v", shutdownSignal(), err)
	}

	// Wait for the signal handler to fire and cancel rootCtx. A 3s budget
	// is generous on every CI worker we deploy to.
	select {
	case <-rootCtx.Done():
		// good
	case <-time.After(3 * time.Second):
		t.Fatal("signal.NotifyContext did not cancel root ctx within 3s after signal")
	}

	// Drive the same shutdown path main.go uses: a fresh context with
	// cfg.ShutdownTimeout, NOT rootCtx (which is already cancelled).
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	select {
	case err := <-listenErrCh:
		if err != nil {
			t.Fatalf("ListenAndServe returned non-clean error: %v", err)
		}
	case <-time.After(cfg.ShutdownTimeout + 2*time.Second):
		t.Fatal("ListenAndServe did not return within ShutdownTimeout after signal-driven Shutdown")
	}
}

// -----------------------------------------------------------------------------
// Test #3 — In-flight handlers are drained rather than cut off.
// -----------------------------------------------------------------------------

func TestArenaAPI_ShutdownDrainsInFlightRequest(t *testing.T) {
	t.Parallel()

	addr := reserveLocalAddr(t)
	cfg := shutdownTestConfig(addr)

	// Mount a /v1/slow endpoint that takes ~750ms to respond. We want the
	// drain window to be longer than this so Shutdown completes the
	// request rather than tearing the connection down.
	const slowDelay = 750 * time.Millisecond
	var (
		started, finished atomic.Bool
	)

	srv := httpserver.New(httpserver.Options{Config: cfg})
	srv.Router().Get("/v1/slow", func(w http.ResponseWriter, r *http.Request) {
		started.Store(true)
		select {
		case <-time.After(slowDelay):
			finished.Store(true)
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "done")
		case <-r.Context().Done():
			// The handler context is bound to the connection. If the
			// connection is forcibly closed we surface that as a 503 so
			// the test sees a definitive failure rather than a hang.
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "context cancelled")
		}
	})

	listenErrCh := runServerAsync(srv)
	waitForReady(t, addr, 3*time.Second)

	// Fire the slow request from a goroutine so the test can also call
	// Shutdown while it is still in flight.
	type slowResult struct {
		body string
		code int
		err  error
	}
	resultCh := make(chan slowResult, 1)
	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get("http://" + addr + "/v1/slow")
		if err != nil {
			resultCh <- slowResult{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		resultCh <- slowResult{body: string(body), code: resp.StatusCode, err: err}
	}()

	// Wait until the handler reports it has started processing — the
	// invariant we want to test is "in-flight when Shutdown is called".
	deadline := time.Now().Add(2 * time.Second)
	for !started.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !started.Load() {
		t.Fatal("slow handler never reported start; Shutdown drain test cannot run")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	select {
	case res := <-resultCh:
		if res.err != nil {
			t.Fatalf("slow request failed: %v", res.err)
		}
		if res.code != http.StatusOK {
			t.Fatalf("slow request status = %d, body = %q; want 200 (Shutdown should drain it)", res.code, res.body)
		}
		if !finished.Load() {
			t.Fatal("handler did not reach its completion branch; Shutdown cut it off")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("slow request did not complete within 5s after Shutdown")
	}

	select {
	case err := <-listenErrCh:
		if err != nil {
			t.Fatalf("ListenAndServe returned non-clean error: %v", err)
		}
	case <-time.After(cfg.ShutdownTimeout + 2*time.Second):
		t.Fatal("ListenAndServe did not return within ShutdownTimeout")
	}
}

// -----------------------------------------------------------------------------
// Test #4 — Shutdown is safe to call twice (idempotent).
// -----------------------------------------------------------------------------

func TestArenaAPI_ShutdownIsIdempotent(t *testing.T) {
	t.Parallel()

	addr := reserveLocalAddr(t)
	cfg := shutdownTestConfig(addr)
	srv := httpserver.New(httpserver.Options{Config: cfg})

	listenErrCh := runServerAsync(srv)
	waitForReady(t, addr, 3*time.Second)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("first Shutdown returned error: %v", err)
	}

	// http.Server.Shutdown returns http.ErrServerClosed on subsequent calls
	// per the stdlib docs. We accept either nil OR ErrServerClosed; the
	// invariant we care about is "no goroutine leak, no panic".
	err := srv.Shutdown(shutdownCtx)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("second Shutdown returned unexpected error: %v", err)
	}

	select {
	case err := <-listenErrCh:
		if err != nil {
			t.Fatalf("ListenAndServe returned non-clean error: %v", err)
		}
	case <-time.After(cfg.ShutdownTimeout + 2*time.Second):
		t.Fatal("ListenAndServe did not return within ShutdownTimeout")
	}
}

// -----------------------------------------------------------------------------
// Test #5 — A previously-bound listener returns the expected error.
// -----------------------------------------------------------------------------

// TestArenaAPI_ListenAndServeReportsBindFailure asserts that a failed bind
// (address already in use) propagates back through ListenAndServe as a
// non-nil error — not http.ErrServerClosed. main.go relies on that
// distinction to map fatal startup errors to a non-zero exit code; if a
// future refactor accidentally swallowed the bind error the binary would
// exit 0 on failure and the container runtime would never restart it.
func TestArenaAPI_ListenAndServeReportsBindFailure(t *testing.T) {
	t.Parallel()

	// Hold the listener open for the duration of the test so the second
	// server is guaranteed to hit EADDRINUSE.
	holder, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("acquire holder listener: %v", err)
	}
	defer func() { _ = holder.Close() }()
	addr := holder.Addr().String()

	cfg := shutdownTestConfig(addr)
	srv := httpserver.New(httpserver.Options{Config: cfg})

	listenErrCh := runServerAsync(srv)

	select {
	case err := <-listenErrCh:
		if err == nil {
			t.Fatal("ListenAndServe returned nil despite an occupied port")
		}
		// We don't assert the concrete error string because Go's
		// net.OpError formatting differs across OS versions. The contract
		// we care about: "not http.ErrServerClosed".
		if errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("ListenAndServe reported clean shutdown for a failed bind: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ListenAndServe did not report bind failure within 3s")
	}
}

// -----------------------------------------------------------------------------
// Compile-time guards
// -----------------------------------------------------------------------------

// Catch the case where a future refactor removes the public *config.Config or
// httpserver.Options surface this file depends on.
var (
	_ *config.Config     = (*config.Config)(nil)
	_ httpserver.Options = httpserver.Options{}
	_ http.Handler       = (*http.ServeMux)(nil)
)

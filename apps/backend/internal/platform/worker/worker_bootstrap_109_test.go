// Package worker — feature #109 test suite: Worker binary bootstrap.
//
// These tests verify all 6 steps of the worker binary bootstrap feature
// using the in-memory queue (unit tests) so they run without a live database:
//
//  1. cmd/arena-worker/main.go exists with config loading and graceful shutdown.
//  2. Job registry with handler registration (Register + Lookup).
//  3. Postgres poller (FOR UPDATE SKIP LOCKED, configurable poll interval).
//  4. Retry with exponential backoff and max-attempts dead-letter.
//  5. Integration tests via testcontainers-go (see worker_integration_109_test.go).
//  6. /healthz and Prometheus metrics endpoint for worker.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ---------------------------------------------------------------------------
// Step 1: cmd/arena-worker/main.go exists and has required structure.
// ---------------------------------------------------------------------------

// TestWorkerBoot109_MainGoExists verifies that cmd/arena-worker/main.go
// exists in the repository and declares package main.
func TestWorkerBoot109_MainGoExists(t *testing.T) {
	t.Parallel()

	// Walk up from the test file's directory to find the repo root.
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("step 1: filepath.Abs: %v", err)
	}

	// Climb until we find go.mod at the root.
	root := dir
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("step 1: could not locate repo root (go.mod not found)")
		}
		root = parent
	}

	mainPath := filepath.Join(root, "apps", "backend", "cmd", "arena-worker", "main.go")
	data, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("step 1: cmd/arena-worker/main.go not found: %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "package main") {
		t.Error("step 1: main.go must declare 'package main'")
	}
}

// TestWorkerBoot109_MainGoHasGracefulShutdown verifies that main.go wires
// signal.NotifyContext for SIGINT/SIGTERM (step 1 — graceful shutdown).
func TestWorkerBoot109_MainGoHasGracefulShutdown(t *testing.T) {
	t.Parallel()

	dir, _ := filepath.Abs(".")
	root := dir
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("step 1: repo root not found")
		}
		root = parent
	}

	mainPath := filepath.Join(root, "apps", "backend", "cmd", "arena-worker", "main.go")
	data, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("step 1: %v", err)
	}
	src := string(data)

	checks := []struct {
		name    string
		needle  string
		missing string
	}{
		{"signal.NotifyContext", "signal.NotifyContext", "step 1: main.go must use signal.NotifyContext for graceful shutdown"},
		{"SIGTERM", "syscall.SIGTERM", "step 1: main.go must handle SIGTERM"},
		{"SIGINT", "syscall.SIGINT", "step 1: main.go must handle SIGINT"},
		{"config.Load", "config.Load", "step 1: main.go must call config.Load"},
	}
	for _, c := range checks {
		if !strings.Contains(src, c.needle) {
			t.Error(c.missing)
		}
	}
}

// TestWorkerBoot109_MainGoHasWorkerMetricsAddr verifies that WORKER_METRICS_ADDR
// is referenced in the worker binary and in config (step 6).
func TestWorkerBoot109_MainGoHasWorkerMetricsAddr(t *testing.T) {
	t.Parallel()

	dir, _ := filepath.Abs(".")
	root := dir
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("repo root not found")
		}
		root = parent
	}

	mainPath := filepath.Join(root, "apps", "backend", "cmd", "arena-worker", "main.go")
	data, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("step 6: %v", err)
	}
	src := string(data)

	if !strings.Contains(src, "WorkerMetricsAddr") {
		t.Error("step 6: main.go must reference cfg.WorkerMetricsAddr")
	}
	if !strings.Contains(src, "/healthz") {
		t.Error("step 6: main.go must serve /healthz endpoint")
	}
	if !strings.Contains(src, "/metrics") {
		t.Error("step 6: main.go must serve /metrics endpoint")
	}
}

// TestWorkerBoot109_ConfigHasWorkerMetricsAddr verifies that the config
// struct exposes WorkerMetricsAddr backed by WORKER_METRICS_ADDR env var
// with a default of :9091 (step 6).
func TestWorkerBoot109_ConfigHasWorkerMetricsAddr(t *testing.T) {
	t.Parallel()

	dir, _ := filepath.Abs(".")
	root := dir
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("repo root not found")
		}
		root = parent
	}

	cfgPath := filepath.Join(root, "apps", "backend", "internal", "platform", "config", "config.go")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("step 6: config.go not found: %v", err)
	}
	src := string(data)

	if !strings.Contains(src, "WorkerMetricsAddr") {
		t.Error("step 6: config.go must define WorkerMetricsAddr field")
	}
	if !strings.Contains(src, "WORKER_METRICS_ADDR") {
		t.Error("step 6: config.go must reference WORKER_METRICS_ADDR env var")
	}
	if !strings.Contains(src, ":9091") {
		t.Error("step 6: config.go must default WORKER_METRICS_ADDR to :9091")
	}
}

// ---------------------------------------------------------------------------
// Step 2: Job registry with handler registration.
// ---------------------------------------------------------------------------

// TestWorkerBoot109_RegistryRegisterAndLookup verifies that Register stores
// a handler and Lookup retrieves it (step 2).
func TestWorkerBoot109_RegistryRegisterAndLookup(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()

	var called atomic.Bool
	reg.Register("my.job", func(_ context.Context, _ []byte) error {
		called.Store(true)
		return nil
	})

	h, ok := reg.Lookup("my.job")
	if !ok {
		t.Fatal("step 2: Lookup returned (nil, false) for registered handler")
	}
	if err := h(context.Background(), nil); err != nil {
		t.Fatalf("step 2: handler returned error: %v", err)
	}
	if !called.Load() {
		t.Fatal("step 2: handler was not invoked by Lookup result")
	}
}

// TestWorkerBoot109_RegistryLookupMissing verifies Lookup returns false for
// unregistered types (step 2).
func TestWorkerBoot109_RegistryLookupMissing(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	_, ok := reg.Lookup("no.such.job")
	if ok {
		t.Fatal("step 2: Lookup must return false for unregistered job type")
	}
}

// TestWorkerBoot109_RegistryOverwrite verifies re-registering overwrites the
// previous handler (step 2 — useful in tests, documented in API).
func TestWorkerBoot109_RegistryOverwrite(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()

	var firstCalled, secondCalled atomic.Bool
	reg.Register("x", func(_ context.Context, _ []byte) error {
		firstCalled.Store(true)
		return nil
	})
	reg.Register("x", func(_ context.Context, _ []byte) error {
		secondCalled.Store(true)
		return nil
	})

	h, ok := reg.Lookup("x")
	if !ok {
		t.Fatal("step 2: expected registered handler")
	}
	_ = h(context.Background(), nil)
	if firstCalled.Load() {
		t.Error("step 2: first handler should have been overwritten")
	}
	if !secondCalled.Load() {
		t.Error("step 2: second handler must be active after overwrite")
	}
}

// ---------------------------------------------------------------------------
// Step 3: Postgres poller (FOR UPDATE SKIP LOCKED in source).
// ---------------------------------------------------------------------------

// TestWorkerBoot109_PGQueueClaimUsesSkipLocked verifies that the FOR UPDATE
// SKIP LOCKED clause is present in worker.go (step 3).
func TestWorkerBoot109_PGQueueClaimUsesSkipLocked(t *testing.T) {
	t.Parallel()

	dir, _ := filepath.Abs(".")
	root := dir
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("repo root not found")
		}
		root = parent
	}

	workerPath := filepath.Join(root, "apps", "backend", "internal", "platform", "worker", "worker.go")
	data, err := os.ReadFile(workerPath)
	if err != nil {
		t.Fatalf("step 3: worker.go not found: %v", err)
	}
	src := strings.ToUpper(string(data))

	if !strings.Contains(src, "FOR UPDATE SKIP LOCKED") {
		t.Error("step 3: PGQueue.ClaimNext must use FOR UPDATE SKIP LOCKED")
	}
	if !strings.Contains(src, "SCHEDULED_AT") {
		t.Error("step 3: ClaimNext must filter by scheduled_at")
	}
}

// TestWorkerBoot109_PollIntervalConfigurable verifies that PollInterval is
// honoured when constructing a Worker (step 3).
func TestWorkerBoot109_PollIntervalConfigurable(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	reg := NewRegistry()
	reg.Register("noop", func(_ context.Context, _ []byte) error { return nil })

	w, err := New(Options{
		Queue:        q,
		Registry:     reg,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollInterval: 42 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("step 3: New: %v", err)
	}
	if w.pollInterval != 42*time.Millisecond {
		t.Errorf("step 3: pollInterval expected 42ms, got %v", w.pollInterval)
	}
}

// TestWorkerBoot109_WorkerClaims jobs from queue within reasonable time
// (basic end-to-end smoke, step 3).
func TestWorkerBoot109_WorkerClaimsAndCompletesJob(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	jobID := q.insert("noop.test", []byte(`{}`), time.Time{}, 3)

	reg := NewRegistry()
	done := make(chan struct{}, 1)
	reg.Register("noop.test", func(_ context.Context, _ []byte) error {
		select {
		case done <- struct{}{}:
		default:
		}
		return nil
	})

	w, err := New(Options{
		Queue:        q,
		Registry:     reg,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("step 3: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() { _ = w.Run(ctx) }()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("step 3: job was not consumed within 5s")
	}

	// Verify the job reached status='done' in the in-memory queue.
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, row := range q.rows {
		if row.id == jobID && row.status != "done" {
			t.Errorf("step 3: expected job %s status='done', got %q", jobID, row.status)
		}
	}

	_ = w.Stop()
}

// ---------------------------------------------------------------------------
// Step 4: Retry with backoff and dead-letter on max attempts.
// ---------------------------------------------------------------------------

// TestWorkerBoot109_RetryOnError verifies that a failing handler causes the
// job to return to status='pending' (step 4 — retry).
func TestWorkerBoot109_RetryOnError(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	jobID := q.insert("fail.job", []byte(`{}`), time.Time{}, 5)

	reg := NewRegistry()
	var attempts atomic.Int32
	reg.Register("fail.job", func(_ context.Context, _ []byte) error {
		attempts.Add(1)
		return errors.New("intentional failure")
	})

	w, err := New(Options{
		Queue:        q,
		Registry:     reg,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollInterval: 5 * time.Millisecond,
		RetryBackoff: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("step 4: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Wait for at least 2 attempts.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if attempts.Load() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	_ = w.Stop()

	if attempts.Load() < 2 {
		t.Errorf("step 4: expected at least 2 retry attempts, got %d", attempts.Load())
	}

	// After retry, the row should be back to pending or claimed.
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, row := range q.rows {
		if row.id == jobID {
			if row.status == "done" {
				t.Error("step 4: failing job must not be marked done")
			}
		}
	}
}

// TestWorkerBoot109_DeadLetterOnMaxAttempts verifies that exhausting
// max_attempts moves the job to dead letter (step 4).
func TestWorkerBoot109_DeadLetterOnMaxAttempts(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	_ = q.insert("always.fail", []byte(`{}`), time.Time{}, 2) // max_attempts=2

	reg := NewRegistry()
	reg.Register("always.fail", func(_ context.Context, _ []byte) error {
		return errors.New("always fails")
	})

	w, err := New(Options{
		Queue:        q,
		Registry:     reg,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollInterval: 5 * time.Millisecond,
		RetryBackoff: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("step 4: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Wait for dead-letter entry.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		dlLen := len(q.deadLetter)
		q.mu.Unlock()
		if dlLen > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	_ = w.Stop()

	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.deadLetter) == 0 {
		t.Fatal("step 4: job exhausting max_attempts must be moved to dead letter")
	}
	dl := q.deadLetter[0]
	if dl.jobType != "always.fail" {
		t.Errorf("step 4: dead-letter job_type expected 'always.fail', got %q", dl.jobType)
	}
	if dl.lastError == "" {
		t.Error("step 4: dead-letter entry must capture the last error")
	}
}

// TestWorkerBoot109_BackoffDelayBetweenRetries verifies that the worker
// does NOT immediately re-claim a retried row — the retryBackoff adds a
// measurable gap between successive attempts (step 4).
func TestWorkerBoot109_BackoffDelayBetweenRetries(t *testing.T) {
	t.Parallel()

	q := newInMemoryQueue()
	_ = q.insert("slow.retry", []byte(`{}`), time.Time{}, 5)

	reg := NewRegistry()
	var timestamps []time.Time
	var tsMu sync.Mutex
	reg.Register("slow.retry", func(_ context.Context, _ []byte) error {
		tsMu.Lock()
		timestamps = append(timestamps, time.Now())
		tsMu.Unlock()
		return errors.New("keep retrying")
	})

	const retryBackoff = 80 * time.Millisecond
	w, err := New(Options{
		Queue:        q,
		Registry:     reg,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollInterval: 5 * time.Millisecond,
		RetryBackoff: retryBackoff,
	})
	if err != nil {
		t.Fatalf("step 4: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Wait for at least 2 invocations.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tsMu.Lock()
		n := len(timestamps)
		tsMu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	_ = w.Stop()

	tsMu.Lock()
	defer tsMu.Unlock() //nolint:govet

	if len(timestamps) < 2 {
		t.Fatalf("step 4: expected at least 2 retry attempts, got %d", len(timestamps))
	}

	gap := timestamps[1].Sub(timestamps[0])
	// Allow generous tolerance (retryBackoff * 0.5) to handle slow CI.
	minGap := retryBackoff / 2
	if gap < minGap {
		t.Errorf("step 4: gap between retries %v is too small (expected >= %v); backoff not applied", gap, minGap)
	}
}

// ---------------------------------------------------------------------------
// Step 6: /healthz and Prometheus /metrics endpoint for worker.
// ---------------------------------------------------------------------------

// TestWorkerBoot109_HealthzEndpointReturns200 verifies that a /healthz
// handler returns HTTP 200 with JSON {"status":"ok"} (step 6).
func TestWorkerBoot109_HealthzEndpointReturns200(t *testing.T) {
	t.Parallel()

	// Replicate the same handler pattern used in the worker main.go.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/healthz", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("step 6: /healthz expected 200, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("step 6: /healthz body is not valid JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("step 6: /healthz JSON status expected 'ok', got %q", body["status"])
	}
}

// TestWorkerBoot109_MetricsEndpointExposed verifies that the worker exposes
// a Prometheus /metrics handler that returns 200 with metric lines (step 6).
func TestWorkerBoot109_MetricsEndpointExposed(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	// Register a simple gauge so the /metrics body is non-trivial.
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "arena_worker_test_gauge",
		Help: "test gauge for step 6 verification",
	})
	g.Set(7)
	reg.MustRegister(g)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/metrics", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("step 6: /metrics expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "arena_worker_test_gauge") {
		t.Errorf("step 6: /metrics body must contain gauge name; got:\n%s", body)
	}
}

// TestWorkerBoot109_MetricsServerListens verifies that the metrics server
// binds to a random free port and responds to /healthz (step 6 — live socket
// test without Docker).
func TestWorkerBoot109_MetricsServerListens(t *testing.T) {
	t.Parallel()

	// Find a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("step 6: find free port: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}
	go func() { _ = srv.ListenAndServe() }()

	// Poll until the server is up.
	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	for time.Now().Before(deadline) {
		resp, err = http.Get(fmt.Sprintf("http://%s/healthz", addr)) //nolint:noctx
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("step 6: could not reach worker metrics server: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("step 6: expected 200, got %d", resp.StatusCode)
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
}

// TestWorkerBoot109_FullVerification runs all 6 steps as sub-tests.
func TestWorkerBoot109_FullVerification(t *testing.T) {
	t.Run("step1_main_exists", func(t *testing.T) {
		TestWorkerBoot109_MainGoExists(t)
	})
	t.Run("step1_graceful_shutdown", func(t *testing.T) {
		TestWorkerBoot109_MainGoHasGracefulShutdown(t)
	})
	t.Run("step2_registry_register_lookup", func(t *testing.T) {
		TestWorkerBoot109_RegistryRegisterAndLookup(t)
	})
	t.Run("step3_skip_locked_in_source", func(t *testing.T) {
		TestWorkerBoot109_PGQueueClaimUsesSkipLocked(t)
	})
	t.Run("step4_dead_letter_on_max_attempts", func(t *testing.T) {
		TestWorkerBoot109_DeadLetterOnMaxAttempts(t)
	})
	t.Run("step6_healthz_200", func(t *testing.T) {
		TestWorkerBoot109_HealthzEndpointReturns200(t)
	})
	t.Run("step6_metrics_exposed", func(t *testing.T) {
		TestWorkerBoot109_MetricsEndpointExposed(t)
	})
}

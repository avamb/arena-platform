// Package httpserver — unit tests for feature #14:
// "Operational endpoints not under /v1"
//
// Health, readiness, and metrics endpoints must live at top-level so
// orchestrators (Dokploy, Kubernetes) can probe them without version coupling.
//
// Covered steps:
//
//  1. GET /healthz              → 200
//  2. GET /readyz               → 200 when no probes configured
//  3. GET /metrics              → 200 text/plain (when handler is wired)
//  4. GET /v1/healthz           → 404 (operational endpoints not duplicated)
//  5. Dockerfile HEALTHCHECK references /healthz not /v1/healthz
package httpserver

import (
	"encoding/json"
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

// buildOperationalTestServer creates a minimal Server whose only goal is to
// exercise the operational-route mounts. No auth, no pool, no idempotency —
// only what is needed to mount /healthz, /readyz, and optionally /metrics.
// Test doubles (captureAuditWriter, noopIdemStore, fakePoolDB, fakeTx) are
// defined in echo_audit_test.go which belongs to the same package.
func buildOperationalTestServer(t *testing.T, extraOpts ...func(*Options)) *Server {
	t.Helper()

	cfg := &config.Config{
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
	}
	opts := Options{Config: cfg}
	for _, fn := range extraOpts {
		fn(&opts)
	}
	return New(opts)
}

// doRequest issues a request against the server's router using httptest and
// returns the response recorder.
func doRequest(srv *Server, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)
	return rr
}

// =============================================================================
// Step 1 — GET /healthz → 200
// =============================================================================

// TestOperational_HealthzReturns200 verifies the liveness probe is mounted at
// the top-level path /healthz and returns 200 {"status":"ok"}.
func TestOperational_HealthzReturns200(t *testing.T) {
	t.Parallel()
	srv := buildOperationalTestServer(t)

	rr := doRequest(srv, http.MethodGet, "/healthz")

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /healthz: status = %d, want 200", rr.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("GET /healthz: decode JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("GET /healthz: body[\"status\"] = %v, want \"ok\"", body["status"])
	}
}

// TestOperational_HealthzContentTypeIsJSON verifies that /healthz returns a
// JSON Content-Type header.
func TestOperational_HealthzContentTypeIsJSON(t *testing.T) {
	t.Parallel()
	srv := buildOperationalTestServer(t)

	rr := doRequest(srv, http.MethodGet, "/healthz")

	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Fatalf("GET /healthz: Content-Type = %q, want application/json", ct)
	}
}

// =============================================================================
// Step 2 — GET /readyz → 200 when no probes
// =============================================================================

// TestOperational_ReadyzReturns200WhenNoProbes verifies that /readyz is mounted
// at the top-level path and returns 200 when no probes are configured.
func TestOperational_ReadyzReturns200WhenNoProbes(t *testing.T) {
	t.Parallel()
	srv := buildOperationalTestServer(t)

	rr := doRequest(srv, http.MethodGet, "/readyz")

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /readyz: status = %d, want 200", rr.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("GET /readyz: decode JSON: %v", err)
	}
	if body["status"] != "ready" {
		t.Fatalf("GET /readyz: body[\"status\"] = %v, want \"ready\"", body["status"])
	}
}

// TestOperational_ReadyzHasChecksKey verifies that /readyz always includes a
// "checks" map in its response (empty when no probes registered).
func TestOperational_ReadyzHasChecksKey(t *testing.T) {
	t.Parallel()
	srv := buildOperationalTestServer(t)

	rr := doRequest(srv, http.MethodGet, "/readyz")

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("GET /readyz: decode JSON: %v", err)
	}
	if _, ok := body["checks"]; !ok {
		t.Fatalf("GET /readyz: body missing \"checks\" key; got %v", body)
	}
}

// =============================================================================
// Step 3 — GET /metrics → 200 text/plain
// =============================================================================

// TestOperational_MetricsReturns200WhenHandlerWired verifies that /metrics
// returns 200 when a MetricsHandler is supplied.
func TestOperational_MetricsReturns200WhenHandlerWired(t *testing.T) {
	t.Parallel()

	// Minimal Prometheus-like handler that returns a text/plain body.
	metricsHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("# HELP go_goroutines Number of goroutines.\n"))
	})

	srv := buildOperationalTestServer(t, func(o *Options) {
		o.MetricsHandler = metricsHandler
	})

	rr := doRequest(srv, http.MethodGet, "/metrics")

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /metrics: status = %d, want 200", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Fatalf("GET /metrics: Content-Type = %q, want text/plain", ct)
	}
	if !strings.Contains(rr.Body.String(), "# HELP") {
		t.Fatalf("GET /metrics: body does not contain Prometheus # HELP comment; got %q", rr.Body.String())
	}
}

// TestOperational_MetricsReturns404WhenNotWired verifies that GET /metrics
// returns 404 when no MetricsHandler is configured (route is not mounted).
func TestOperational_MetricsReturns404WhenNotWired(t *testing.T) {
	t.Parallel()
	srv := buildOperationalTestServer(t) // no MetricsHandler

	rr := doRequest(srv, http.MethodGet, "/metrics")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("GET /metrics (no handler): status = %d, want 404", rr.Code)
	}
}

// =============================================================================
// Step 4 — GET /v1/healthz → 404 (not duplicated under /v1)
// =============================================================================

// TestOperational_HealthzNotUnderV1 is the canonical step 4 verification:
// operational endpoints must NOT be duplicated under the /v1 prefix. Probing
// GET /v1/healthz must return 404 so orchestrators that use the un-prefixed
// paths do not accidentally pick up versioned paths.
func TestOperational_HealthzNotUnderV1(t *testing.T) {
	t.Parallel()
	srv := buildOperationalTestServer(t)

	rr := doRequest(srv, http.MethodGet, "/v1/healthz")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("GET /v1/healthz: status = %d, want 404 (must not be duplicated under /v1)", rr.Code)
	}
}

// TestOperational_ReadyzNotUnderV1 verifies that /readyz is also absent from
// the /v1 prefix — the same constraint applies to the readiness probe.
func TestOperational_ReadyzNotUnderV1(t *testing.T) {
	t.Parallel()
	srv := buildOperationalTestServer(t)

	rr := doRequest(srv, http.MethodGet, "/v1/readyz")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("GET /v1/readyz: status = %d, want 404 (must not be duplicated under /v1)", rr.Code)
	}
}

// TestOperational_MetricsNotUnderV1 verifies that the /metrics path is absent
// from the /v1 prefix (even when a MetricsHandler is wired).
func TestOperational_MetricsNotUnderV1(t *testing.T) {
	t.Parallel()

	metricsHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
	})

	srv := buildOperationalTestServer(t, func(o *Options) {
		o.MetricsHandler = metricsHandler
	})

	// /metrics is mounted at top level; /v1/metrics must not exist.
	rr := doRequest(srv, http.MethodGet, "/v1/metrics")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("GET /v1/metrics: status = %d, want 404 (must not be duplicated under /v1)", rr.Code)
	}
}

// TestOperational_V1HealthzResponseIsJSONErrorEnvelope verifies that when
// /v1/healthz returns 404, the body follows the standard JSON error envelope
// (feature #12) rather than chi's default plain-text "404 page not found\n".
func TestOperational_V1HealthzResponseIsJSONErrorEnvelope(t *testing.T) {
	t.Parallel()
	srv := buildOperationalTestServer(t)

	rr := doRequest(srv, http.MethodGet, "/v1/healthz")

	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Fatalf("GET /v1/healthz: Content-Type = %q, want application/json error envelope", ct)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("GET /v1/healthz: body is not valid JSON: %v", err)
	}

	// Standard error envelope from feature #12 has an "error" key.
	if _, hasError := body["error"]; !hasError {
		t.Fatalf("GET /v1/healthz: body missing \"error\" key in JSON envelope; got %v", body)
	}
}

// =============================================================================
// Step 5 — Dockerfile HEALTHCHECK references /healthz not /v1/healthz
// =============================================================================

// dockerfileContent reads the project Dockerfile by walking up from this
// source file's location. Returns (content, path, ok).
func dockerfileContent(t *testing.T) (string, string, bool) {
	t.Helper()

	// runtime.Caller(0) gives the absolute path to this source file at test
	// compile time. We navigate up 5 levels to reach the repo root:
	//   operational_not_v1_test.go
	//   → httpserver
	//   → platform
	//   → internal
	//   → backend
	//   → apps
	//   → repo root (Dockerfile lives here)
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", "", false
	}

	// Walk up 6 directories from the source file location.
	dir := filepath.Dir(thisFile) // httpserver/
	for i := 0; i < 5; i++ {
		dir = filepath.Dir(dir)
	}
	dockerfilePath := filepath.Join(dir, "Dockerfile")

	b, err := os.ReadFile(dockerfilePath)
	if err != nil {
		// Fallback: try searching relative to current working directory.
		candidates := []string{
			"../../../../../Dockerfile",
			"../../../../../../Dockerfile",
		}
		for _, c := range candidates {
			if b2, err2 := os.ReadFile(c); err2 == nil {
				abs, _ := filepath.Abs(c)
				return string(b2), abs, true
			}
		}
		return "", "", false
	}
	return string(b), dockerfilePath, true
}

// TestOperational_DockerfileHEALTHCHECKUsesHealthzNotV1 verifies that:
//   - The Dockerfile contains a HEALTHCHECK directive.
//   - The HEALTHCHECK references the arena-healthcheck binary (which internally
//     calls /healthz, not /v1/healthz).
//   - The Dockerfile does NOT reference /v1/healthz anywhere in a HEALTHCHECK
//     context — orchestrators must probe /healthz at the top level.
func TestOperational_DockerfileHEALTHCHECKUsesHealthzNotV1(t *testing.T) {
	content, path, ok := dockerfileContent(t)
	if !ok {
		t.Fatal("could not locate Dockerfile relative to test source; check directory walk")
	}
	t.Logf("Dockerfile path: %s", path)

	if !strings.Contains(content, "HEALTHCHECK") {
		t.Error("Dockerfile does not contain a HEALTHCHECK directive")
	}

	// The HEALTHCHECK must use the arena-healthcheck binary, which internally
	// performs GET /healthz. A shell-based `curl /healthz` would also be
	// acceptable but would not work in distroless — the binary is required.
	if !strings.Contains(content, "arena-healthcheck") {
		t.Error("Dockerfile HEALTHCHECK does not reference arena-healthcheck binary")
	}

	// Negative assertion: the Dockerfile must NOT hardcode /v1/healthz as the
	// probe target. Such a path would break every orchestrator that expects the
	// standard /healthz path.
	if strings.Contains(content, "/v1/healthz") {
		t.Error("Dockerfile contains /v1/healthz — operational endpoints must not be accessed via the versioned prefix")
	}
}

// TestOperational_ArenaHealthcheckBinaryCallsHealthzPath verifies (via source
// inspection) that the arena-healthcheck binary targets /healthz and not any
// other path. This is a contract test: if a developer accidentally changes
// the health-check binary to call /v1/healthz, this test catches it.
func TestOperational_ArenaHealthcheckBinaryCallsHealthzPath(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) returned ok=false")
	}

	// Navigate from httpserver/ up to repo root, then into the binary.
	dir := filepath.Dir(thisFile)
	for i := 0; i < 5; i++ {
		dir = filepath.Dir(dir)
	}
	mainPath := filepath.Join(dir, "apps", "backend", "cmd", "arena-healthcheck", "main.go")

	src, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("could not read arena-healthcheck/main.go at %s: %v", mainPath, err)
	}
	text := string(src)

	// The binary must reference "/healthz".
	if !strings.Contains(text, `"/healthz"`) {
		t.Error("arena-healthcheck/main.go does not reference \"/healthz\"")
	}

	// It must NOT reference "/v1/healthz".
	if strings.Contains(text, "/v1/healthz") {
		t.Error("arena-healthcheck/main.go references /v1/healthz — probe must use top-level /healthz")
	}
}

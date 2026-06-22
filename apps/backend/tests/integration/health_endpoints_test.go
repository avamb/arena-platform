// health_endpoints_test.go — integration tests for feature #91
// "Health endpoints /healthz /readyz /metrics"
//
// Covered steps:
//
//  1. GET /healthz returns 200 {status:"ok"} without dependencies.
//  2. GET /readyz uses ReadinessProbe interface; returns 200/{checks:{…}} on
//     success or 503 when a probe fails.
//  3. GET /metrics proxies to the Prometheus handler (200, text/plain body).
//  4. /healthz is referenced in the Dockerfile HEALTHCHECK (verified by
//     reading the Dockerfile in TestHealthEndpoints_DockerfileHEALTHCHECK).
//  5. Both /healthz and /readyz return 200 on a fresh server (no probes).
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
)

// -----------------------------------------------------------------------------
// Helpers (shared with arena_api_shutdown_test.go in the same package)
// -----------------------------------------------------------------------------

// healthTestConfig is a minimal config that satisfies httpserver.New without
// any external services (no DB, no Redis, no OTLP).
func healthTestConfig(addr string) *config.Config {
	return &config.Config{
		AppEnv:          config.EnvDevelopment,
		AppName:         "arena-api-test",
		AppVersion:      "0.0.0-test",
		AppCommit:       "test",
		HTTPListenAddr:  addr,
		BodyLimitBytes:  1 << 20,
		RequestTimeout:  5 * time.Second,
		ShutdownTimeout: 5 * time.Second,
		DefaultLocale:   "en",
		ActiveLocales:   []string{"en"},
		LogLevel:        "info",
		LogFormat:       "json",
	}
}

// startHealthServer builds an httpserver.Server with the supplied options,
// starts it in a background goroutine, waits up to 3 s for /healthz to
// accept connections, and registers t.Cleanup to shut it down.
// Returns the base URL ("http://127.0.0.1:<port>").
func startHealthServer(t *testing.T, opts httpserver.Options) string {
	t.Helper()
	addr := reserveLocalAddr(t) // defined in arena_api_shutdown_test.go
	opts.Config = healthTestConfig(addr)
	srv := httpserver.New(opts)

	listenErrCh := runServerAsync(srv) // defined in arena_api_shutdown_test.go
	waitForReady(t, addr, 3*time.Second)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-listenErrCh
	})
	return "http://" + addr
}

// getJSON issues a GET to url, decodes the JSON body into dest, and returns
// the HTTP status code.
func getJSON(t *testing.T, url string, dest any) int {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	body, rerr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if rerr != nil {
		t.Fatalf("GET %s: read body: %v", url, rerr)
	}
	if err := json.Unmarshal(body, dest); err != nil {
		t.Fatalf("GET %s: decode JSON %q: %v", url, string(body), err)
	}
	return resp.StatusCode
}

// getRaw issues a GET and returns status + body string.
func getRaw(t *testing.T, url string) (int, string) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// -----------------------------------------------------------------------------
// Test doubles
// -----------------------------------------------------------------------------

// okProbe is a ReadinessProbe whose Ping always returns nil ("ok").
type okProbe struct{ name string }

func (p *okProbe) ProbeName() string              { return p.name }
func (p *okProbe) Ping(_ context.Context) error   { return nil }

var _ httpserver.ReadinessProbe = (*okProbe)(nil)

// failProbe is a ReadinessProbe whose Ping always returns an error.
type failProbe struct {
	name string
	msg  string
}

func (p *failProbe) ProbeName() string              { return p.name }
func (p *failProbe) Ping(_ context.Context) error   { return errors.New(p.msg) }

var _ httpserver.ReadinessProbe = (*failProbe)(nil)

// -----------------------------------------------------------------------------
// Step 1 — GET /healthz
// -----------------------------------------------------------------------------

// TestHealthz_Returns200WithStatusOk verifies the liveness probe:
// GET /healthz → 200 {"status":"ok"} with no dependencies.
func TestHealthz_Returns200WithStatusOk(t *testing.T) {
	t.Parallel()
	base := startHealthServer(t, httpserver.Options{})

	var body map[string]any
	status := getJSON(t, base+"/healthz", &body)

	if status != http.StatusOK {
		t.Fatalf("/healthz: status = %d, want 200", status)
	}
	if body["status"] != "ok" {
		t.Fatalf("/healthz: body[\"status\"] = %v, want \"ok\"", body["status"])
	}
}

// TestHealthz_NeverDependsOnDB verifies that /healthz is a pure liveness
// probe and returns 200 regardless of whether DB is configured.
func TestHealthz_NeverDependsOnDB(t *testing.T) {
	t.Parallel()
	// Even with a failing probe registered, /healthz should still be 200.
	base := startHealthServer(t, httpserver.Options{
		Probes: []httpserver.ReadinessProbe{
			&failProbe{name: "database", msg: "simulated outage"},
		},
	})

	status, _ := getRaw(t, base+"/healthz")
	if status != http.StatusOK {
		t.Fatalf("/healthz with failing probe: status = %d, want 200", status)
	}
}

// -----------------------------------------------------------------------------
// Step 2 — GET /readyz via ReadinessProbe interface
// -----------------------------------------------------------------------------

// TestReadyz_Returns200WhenNoProbesRegistered verifies that /readyz with zero
// probes always returns 200 {"status":"ready","checks":{}}.
// This is the "fresh server" case from feature step 5.
func TestReadyz_Returns200WhenNoProbesRegistered(t *testing.T) {
	t.Parallel()
	base := startHealthServer(t, httpserver.Options{})

	var body map[string]any
	status := getJSON(t, base+"/readyz", &body)

	if status != http.StatusOK {
		t.Fatalf("/readyz (no probes): status = %d, want 200", status)
	}
	if body["status"] != "ready" {
		t.Fatalf("/readyz (no probes): body[\"status\"] = %v, want \"ready\"", body["status"])
	}
	checks, ok := body["checks"]
	if !ok {
		t.Fatalf("/readyz: body missing \"checks\" key; body = %v", body)
	}
	if _, isMap := checks.(map[string]any); !isMap {
		t.Fatalf("/readyz: body[\"checks\"] is %T, want map", checks)
	}
}

// TestReadyz_Returns200WhenAllProbesPass verifies that /readyz returns 200
// with each probe's name mapped to "ok".
func TestReadyz_Returns200WhenAllProbesPass(t *testing.T) {
	t.Parallel()
	base := startHealthServer(t, httpserver.Options{
		Probes: []httpserver.ReadinessProbe{
			&okProbe{name: "database"},
			&okProbe{name: "redis"},
		},
	})

	var body map[string]any
	status := getJSON(t, base+"/readyz", &body)

	if status != http.StatusOK {
		t.Fatalf("/readyz (all pass): status = %d, want 200", status)
	}
	if body["status"] != "ready" {
		t.Fatalf("/readyz (all pass): body[\"status\"] = %v, want \"ready\"", body["status"])
	}

	checks, _ := body["checks"].(map[string]any)
	if checks["database"] != "ok" {
		t.Fatalf("/readyz: checks[\"database\"] = %v, want \"ok\"", checks["database"])
	}
	if checks["redis"] != "ok" {
		t.Fatalf("/readyz: checks[\"redis\"] = %v, want \"ok\"", checks["redis"])
	}
}

// TestReadyz_Returns503WhenAProbeFailsChecksMap verifies that /readyz returns
// 503 {"status":"not_ready","checks":{...}} when any probe returns an error.
func TestReadyz_Returns503WhenAProbeFailsChecksMap(t *testing.T) {
	t.Parallel()
	const errMsg = "connection refused"
	base := startHealthServer(t, httpserver.Options{
		Probes: []httpserver.ReadinessProbe{
			&okProbe{name: "redis"},
			&failProbe{name: "database", msg: errMsg},
		},
	})

	var body map[string]any
	status := getJSON(t, base+"/readyz", &body)

	if status != http.StatusServiceUnavailable {
		t.Fatalf("/readyz (one fail): status = %d, want 503", status)
	}
	if body["status"] != "not_ready" {
		t.Fatalf("/readyz (one fail): body[\"status\"] = %v, want \"not_ready\"", body["status"])
	}

	checks, _ := body["checks"].(map[string]any)
	if checks["redis"] != "ok" {
		t.Fatalf("/readyz: checks[\"redis\"] = %v, want \"ok\"", checks["redis"])
	}
	if checks["database"] != errMsg {
		t.Fatalf("/readyz: checks[\"database\"] = %v, want %q", checks["database"], errMsg)
	}
}

// TestReadyz_LegacyDBPingerProbeAppears verifies that when Options.DB is set,
// the probe appears in the checks map under the key "database" — backward
// compatibility with the database.Pool Pinger path used by main.go.
func TestReadyz_LegacyDBPingerProbeAppears(t *testing.T) {
	t.Parallel()

	// Use a Pinger stub that reports healthy.
	pinger := &healthyPinger{}
	base := startHealthServer(t, httpserver.Options{
		DB: pinger,
	})

	var body map[string]any
	status := getJSON(t, base+"/readyz", &body)

	if status != http.StatusOK {
		t.Fatalf("/readyz (legacy pinger): status = %d, want 200", status)
	}
	checks, _ := body["checks"].(map[string]any)
	if checks["database"] != "ok" {
		t.Fatalf("/readyz (legacy pinger): checks[\"database\"] = %v, want \"ok\"", checks["database"])
	}
}

// healthyPinger satisfies httpserver.Pinger and always reports IsHealthy=true.
type healthyPinger struct{}

func (p *healthyPinger) IsHealthy() bool { return true }
func (p *healthyPinger) LastError() string { return "" }

var _ httpserver.Pinger = (*healthyPinger)(nil)

// TestReadyz_LegacyDBPingerDown verifies that a failing Pinger causes 503
// with the database entry in checks.
func TestReadyz_LegacyDBPingerDown(t *testing.T) {
	t.Parallel()
	const errMsg = "ping failed: EOF"

	pinger := &unhealthyPinger{msg: errMsg}
	base := startHealthServer(t, httpserver.Options{
		DB: pinger,
	})

	var body map[string]any
	status := getJSON(t, base+"/readyz", &body)

	if status != http.StatusServiceUnavailable {
		t.Fatalf("/readyz (legacy pinger down): status = %d, want 503", status)
	}
	checks, _ := body["checks"].(map[string]any)
	if checks["database"] != errMsg {
		t.Fatalf("/readyz: checks[\"database\"] = %v, want %q", checks["database"], errMsg)
	}
}

// unhealthyPinger satisfies httpserver.Pinger and always reports IsHealthy=false.
type unhealthyPinger struct{ msg string }

func (p *unhealthyPinger) IsHealthy() bool { return false }
func (p *unhealthyPinger) LastError() string { return p.msg }

var _ httpserver.Pinger = (*unhealthyPinger)(nil)

// -----------------------------------------------------------------------------
// Step 3 — GET /metrics
// -----------------------------------------------------------------------------

// TestMetrics_Returns200WithPrometheusText verifies that /metrics proxies to
// the Prometheus handler and returns 200 with text/plain body.
func TestMetrics_Returns200WithPrometheusText(t *testing.T) {
	t.Parallel()

	m := observability.MustNew(nil)
	base := startHealthServer(t, httpserver.Options{
		Metrics:        m,
		MetricsHandler: m.Handler(),
	})

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(base + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics: status = %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Fatalf("/metrics: Content-Type = %q, want text/plain", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "# HELP") {
		t.Fatalf("/metrics: body does not contain Prometheus # HELP comment; got %q", string(body)[:200])
	}
}

// TestMetrics_NotMountedWhenHandlerIsNil verifies that when no MetricsHandler
// is supplied, GET /metrics returns 404 (route not mounted) rather than
// panicking or returning 500.
func TestMetrics_NotMountedWhenHandlerIsNil(t *testing.T) {
	t.Parallel()
	base := startHealthServer(t, httpserver.Options{}) // no MetricsHandler

	status, _ := getRaw(t, base+"/metrics")
	if status != http.StatusNotFound {
		t.Fatalf("/metrics (no handler): status = %d, want 404", status)
	}
}

// -----------------------------------------------------------------------------
// Step 4 — Dockerfile HEALTHCHECK references /healthz
// -----------------------------------------------------------------------------

// TestHealthEndpoints_DockerfileHEALTHCHECK verifies that the Dockerfile
// contains a HEALTHCHECK directive that references the arena-healthcheck
// binary (which in turn calls /healthz). This is a documentation + contract
// test: the Dockerfile is the delivery artefact checked into the repository.
func TestHealthEndpoints_DockerfileHEALTHCHECK(t *testing.T) {
	// Resolve the Dockerfile relative to the module root. The integration
	// tests run from their package directory, so we walk up 4 levels:
	// tests/integration → tests → backend → apps → repo root.
	paths := []string{
		"../../../../Dockerfile",
		"../../../../../Dockerfile", // one extra level in case of test runner CWD variance
	}

	var content []byte
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err == nil {
			content = b
			break
		}
	}
	if content == nil {
		t.Fatal("could not find Dockerfile relative to integration test package; check the path walk")
	}

	text := string(content)

	if !strings.Contains(text, "HEALTHCHECK") {
		t.Error("Dockerfile does not contain a HEALTHCHECK directive")
	}
	if !strings.Contains(text, "arena-healthcheck") {
		t.Error("Dockerfile HEALTHCHECK does not reference arena-healthcheck binary")
	}
}

// -----------------------------------------------------------------------------
// Step 5 — Both endpoints return 200 on a fresh server
// -----------------------------------------------------------------------------

// TestHealthEndpoints_FreshServerBothReturn200 is the canonical "step 5" test:
// start a brand-new server with no probes and verify /healthz + /readyz are
// both 200.
func TestHealthEndpoints_FreshServerBothReturn200(t *testing.T) {
	t.Parallel()
	base := startHealthServer(t, httpserver.Options{}) // no DB, no probes

	tests := []struct {
		path string
	}{
		{"/healthz"},
		{"/readyz"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			status, _ := getRaw(t, base+tc.path)
			if status != http.StatusOK {
				t.Fatalf("GET %s: status = %d, want 200 on fresh server", tc.path, status)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Compile-time guards
// -----------------------------------------------------------------------------

var (
	_ httpserver.ReadinessProbe = (*okProbe)(nil)
	_ httpserver.ReadinessProbe = (*failProbe)(nil)
	_ httpserver.Pinger         = (*healthyPinger)(nil)
	_ httpserver.Pinger         = (*unhealthyPinger)(nil)
)

// readiness_probe_registry_112_test.go — feature #112 tests.
//
// Feature: "Readiness probe registry"
// Description: Extensible ReadinessProbe interface; register DB, Redis,
// outbox-lag probes; /readyz aggregates and returns 503 if any fails.
//
// Integration scenarios verified (per feature spec):
//   - "kill DB → /readyz returns 503"           (TestReadinessProbe112_DBProbeFailsReturns503)
//   - "restore → /readyz 200"                   (TestReadinessProbe112_DBRestoreReturns200)
//   - "outbox backlog above threshold → 503"    (TestReadinessProbe112_OutboxLagAboveThreshold503)
//
// All tests in this file are hermetic: they wire mock probes into a test
// httpserver.Server; no live PostgreSQL, Redis, or network calls are made.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	redisadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/redis"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// =============================================================================
// Compile-time interface guards (Steps 1–4)
// =============================================================================

// TestReadinessProbe112_InterfaceGuards confirms at compile time that all
// three ReadinessProbe implementations satisfy the interface contract.
// Failures surface as compile errors, not runtime panics.
func TestReadinessProbe112_InterfaceGuards(t *testing.T) {
	// Compile-time assertions via var _ pattern.
	var _ ReadinessProbe = (*redisadapter.RedisPingProbe)(nil)
	var _ ReadinessProbe = (*outbox.OutboxLagProbe)(nil)
	// postgres.PingProbe is verified in adapters/postgres/pool_test.go.
	t.Log("all ReadinessProbe interface guards pass")
}

// =============================================================================
// Test doubles (httpserver-package-level) for feature #112
// =============================================================================

// alwaysOKRedisPinger implements redisadapter.RedisPinger and always returns nil.
type alwaysOKRedisPinger struct{}

func (a *alwaysOKRedisPinger) Ping(_ context.Context) error { return nil }

var _ redisadapter.RedisPinger = (*alwaysOKRedisPinger)(nil)

// alwaysErrRedisPinger implements redisadapter.RedisPinger and always returns
// the configured error.
type alwaysErrRedisPinger struct{ err error }

func (a *alwaysErrRedisPinger) Ping(_ context.Context) error { return a.err }

var _ redisadapter.RedisPinger = (*alwaysErrRedisPinger)(nil)

// fakeOutboxCounter implements outbox.LagCounter for test control.
type fakeOutboxCounter struct {
	count int64
	err   error
}

func (f *fakeOutboxCounter) CountUndispatched(_ context.Context) (int64, error) {
	return f.count, f.err
}

var _ outbox.LagCounter = (*fakeOutboxCounter)(nil)

// =============================================================================
// Test server builder
// =============================================================================

// buildProbeTestServer builds a minimal httpserver.Server wired with the
// supplied probes and returns an httptest.Server serving its routes.
func buildProbeTestServer(t *testing.T, probes []ReadinessProbe) *httptest.Server {
	t.Helper()
	cfg := &config.Config{
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
	}
	srv := New(Options{Config: cfg, Probes: probes})
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return ts
}

// decodeReadyzBody decodes the /readyz JSON body and returns the status
// string and the checks map.
func decodeReadyzBody(t *testing.T, ts *httptest.Server) (statusCode int, status string, checks map[string]any) {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /readyz body: %v", err)
	}
	statusStr, _ := body["status"].(string)
	checksMap, _ := body["checks"].(map[string]any)
	return resp.StatusCode, statusStr, checksMap
}

// =============================================================================
// Step 2 — DB ping probe
// =============================================================================

// TestReadinessProbe112_DBProbePassesWhenHealthy verifies that a healthy DB
// probe contributes "ok" to the /readyz checks map and that the overall
// status is 200 "ready".
func TestReadinessProbe112_DBProbePassesWhenHealthy(t *testing.T) {
	t.Parallel()
	// Use the succeedingReadinessProbe defined in db_unavailable_test.go.
	probe := &succeedingReadinessProbe{name: "database"}
	ts := buildProbeTestServer(t, []ReadinessProbe{probe})

	code, status, checks := decodeReadyzBody(t, ts)
	if code != http.StatusOK {
		t.Errorf("status code = %d, want 200", code)
	}
	if status != "ready" {
		t.Errorf("status = %q, want \"ready\"", status)
	}
	if checks["database"] != "ok" {
		t.Errorf("checks[\"database\"] = %v, want \"ok\"", checks["database"])
	}
}

// TestReadinessProbe112_DBProbeFailsReturns503 simulates killing the DB
// (feature spec: "kill DB, /readyz returns 503").
func TestReadinessProbe112_DBProbeFailsReturns503(t *testing.T) {
	t.Parallel()
	const errMsg = "dial tcp: connection refused"
	// Use the failingReadinessProbe defined in db_unavailable_test.go.
	probe := &failingReadinessProbe{
		name: "database",
		err:  errors.New(errMsg),
	}
	ts := buildProbeTestServer(t, []ReadinessProbe{probe})

	code, status, checks := decodeReadyzBody(t, ts)
	if code != http.StatusServiceUnavailable {
		t.Errorf("status code = %d, want 503", code)
	}
	if status != "not_ready" {
		t.Errorf("status = %q, want \"not_ready\"", status)
	}
	if checks["database"] == "ok" {
		t.Error("checks[\"database\"] must not be \"ok\" when DB is down")
	}
}

// TestReadinessProbe112_DBRestoreReturns200 simulates DB recovery (feature
// spec: "restore, /readyz 200").
func TestReadinessProbe112_DBRestoreReturns200(t *testing.T) {
	t.Parallel()
	// Use the recoveringReadinessProbe defined in db_unavailable_test.go.
	probe := &recoveringReadinessProbe{name: "database"}
	probe.setHealthy(false) // start unhealthy

	ts := buildProbeTestServer(t, []ReadinessProbe{probe})

	// Phase 1: DB is down → 503.
	code1, _, _ := decodeReadyzBody(t, ts)
	if code1 != http.StatusServiceUnavailable {
		t.Errorf("phase 1: code = %d, want 503", code1)
	}

	// Simulate recovery.
	probe.setHealthy(true)

	// Phase 2: DB is restored → 200.
	code2, status2, _ := decodeReadyzBody(t, ts)
	if code2 != http.StatusOK {
		t.Errorf("phase 2: code = %d, want 200", code2)
	}
	if status2 != "ready" {
		t.Errorf("phase 2: status = %q, want \"ready\"", status2)
	}
}

// =============================================================================
// Step 3 — Redis ping probe
// =============================================================================

// TestReadinessProbe112_RedisProbePasses verifies that a healthy Redis probe
// contributes "ok" to the /readyz checks map.
func TestReadinessProbe112_RedisProbePasses(t *testing.T) {
	t.Parallel()
	probe := redisadapter.NewRedisPingProbe(&alwaysOKRedisPinger{}, "redis")
	ts := buildProbeTestServer(t, []ReadinessProbe{probe})

	code, status, checks := decodeReadyzBody(t, ts)
	if code != http.StatusOK {
		t.Errorf("status code = %d, want 200", code)
	}
	if status != "ready" {
		t.Errorf("status = %q, want \"ready\"", status)
	}
	if checks["redis"] != "ok" {
		t.Errorf("checks[\"redis\"] = %v, want \"ok\"", checks["redis"])
	}
}

// TestReadinessProbe112_RedisProbeFailsReturns503 verifies that a failing
// Redis probe causes /readyz to return 503 with "not_ready".
func TestReadinessProbe112_RedisProbeFailsReturns503(t *testing.T) {
	t.Parallel()
	probe := redisadapter.NewRedisPingProbe(
		&alwaysErrRedisPinger{err: errors.New("redis: connection refused")},
		"redis",
	)
	ts := buildProbeTestServer(t, []ReadinessProbe{probe})

	code, status, checks := decodeReadyzBody(t, ts)
	if code != http.StatusServiceUnavailable {
		t.Errorf("status code = %d, want 503", code)
	}
	if status != "not_ready" {
		t.Errorf("status = %q, want \"not_ready\"", status)
	}
	if checks["redis"] == "ok" {
		t.Error("checks[\"redis\"] must not be \"ok\" when Redis is down")
	}
}

// TestReadinessProbe112_RedisProbeName verifies the probe name appears in the
// /readyz checks map under the configured key.
func TestReadinessProbe112_RedisProbeName(t *testing.T) {
	t.Parallel()
	probe := redisadapter.NewRedisPingProbe(&alwaysOKRedisPinger{}, "session-store")
	ts := buildProbeTestServer(t, []ReadinessProbe{probe})

	_, _, checks := decodeReadyzBody(t, ts)
	if _, found := checks["session-store"]; !found {
		t.Errorf("checks map missing \"session-store\" key; got %v", checks)
	}
}

// =============================================================================
// Step 4 — Outbox-lag probe
// =============================================================================

// TestReadinessProbe112_OutboxLagBelowThresholdPasses verifies that a backlog
// below the threshold produces a healthy /readyz response.
func TestReadinessProbe112_OutboxLagBelowThresholdPasses(t *testing.T) {
	t.Parallel()
	probe := outbox.NewOutboxLagProbe(&fakeOutboxCounter{count: 50}, 100, "outbox")
	ts := buildProbeTestServer(t, []ReadinessProbe{probe})

	code, status, checks := decodeReadyzBody(t, ts)
	if code != http.StatusOK {
		t.Errorf("status code = %d, want 200", code)
	}
	if status != "ready" {
		t.Errorf("status = %q, want \"ready\"", status)
	}
	if checks["outbox"] != "ok" {
		t.Errorf("checks[\"outbox\"] = %v, want \"ok\"", checks["outbox"])
	}
}

// TestReadinessProbe112_OutboxLagAboveThreshold503 covers the feature spec
// scenario "outbox backlog above threshold → 503".
func TestReadinessProbe112_OutboxLagAboveThreshold503(t *testing.T) {
	t.Parallel()
	probe := outbox.NewOutboxLagProbe(&fakeOutboxCounter{count: 150}, 100, "outbox")
	ts := buildProbeTestServer(t, []ReadinessProbe{probe})

	code, status, checks := decodeReadyzBody(t, ts)
	if code != http.StatusServiceUnavailable {
		t.Errorf("status code = %d, want 503 (backlog=150 >= threshold=100)", code)
	}
	if status != "not_ready" {
		t.Errorf("status = %q, want \"not_ready\"", status)
	}
	if checks["outbox"] == "ok" {
		t.Error("checks[\"outbox\"] must not be \"ok\" when backlog exceeds threshold")
	}
}

// TestReadinessProbe112_OutboxLagAtThreshold503 verifies the edge case where
// backlog exactly equals the threshold (inclusive failure).
func TestReadinessProbe112_OutboxLagAtThreshold503(t *testing.T) {
	t.Parallel()
	probe := outbox.NewOutboxLagProbe(&fakeOutboxCounter{count: 100}, 100, "outbox")
	ts := buildProbeTestServer(t, []ReadinessProbe{probe})

	code, _, _ := decodeReadyzBody(t, ts)
	if code != http.StatusServiceUnavailable {
		t.Errorf("status code = %d, want 503 (backlog==threshold==100)", code)
	}
}

// =============================================================================
// Step 5 — Multi-probe aggregation (/readyz checks map)
// =============================================================================

// TestReadinessProbe112_AllProbesPassReturns200 verifies that DB + Redis +
// outbox probes all passing produces a single 200 "ready" response with all
// three keys set to "ok".
func TestReadinessProbe112_AllProbesPassReturns200(t *testing.T) {
	t.Parallel()
	probes := []ReadinessProbe{
		&succeedingReadinessProbe{name: "database"},
		redisadapter.NewRedisPingProbe(&alwaysOKRedisPinger{}, "redis"),
		outbox.NewOutboxLagProbe(&fakeOutboxCounter{count: 0}, 100, "outbox"),
	}
	ts := buildProbeTestServer(t, probes)

	code, status, checks := decodeReadyzBody(t, ts)
	if code != http.StatusOK {
		t.Errorf("status code = %d, want 200 (all probes passing)", code)
	}
	if status != "ready" {
		t.Errorf("status = %q, want \"ready\"", status)
	}
	for _, name := range []string{"database", "redis", "outbox"} {
		if checks[name] != "ok" {
			t.Errorf("checks[%q] = %v, want \"ok\"", name, checks[name])
		}
	}
}

// TestReadinessProbe112_OneFailureAmongManyReturns503 verifies that a single
// failing probe causes the aggregate /readyz to return 503 even when all
// other probes pass. The failing probe's error message must appear in checks.
func TestReadinessProbe112_OneFailureAmongManyReturns503(t *testing.T) {
	t.Parallel()
	const redisErr = "ECONNREFUSED"
	probes := []ReadinessProbe{
		&succeedingReadinessProbe{name: "database"},
		redisadapter.NewRedisPingProbe(
			&alwaysErrRedisPinger{err: errors.New(redisErr)},
			"redis",
		),
		outbox.NewOutboxLagProbe(&fakeOutboxCounter{count: 0}, 100, "outbox"),
	}
	ts := buildProbeTestServer(t, probes)

	code, status, checks := decodeReadyzBody(t, ts)
	if code != http.StatusServiceUnavailable {
		t.Errorf("status code = %d, want 503 (redis probe failing)", code)
	}
	if status != "not_ready" {
		t.Errorf("status = %q, want \"not_ready\"", status)
	}
	// Healthy probes must still report "ok".
	if checks["database"] != "ok" {
		t.Errorf("checks[\"database\"] = %v, want \"ok\"", checks["database"])
	}
	if checks["outbox"] != "ok" {
		t.Errorf("checks[\"outbox\"] = %v, want \"ok\"", checks["outbox"])
	}
	// Failing probe must not report "ok".
	if checks["redis"] == "ok" {
		t.Error("checks[\"redis\"] must not be \"ok\" when Redis probe fails")
	}
}

// TestReadinessProbe112_ChecksMapHasAllKeys verifies that the /readyz body
// includes all registered probe names as keys in the checks map, regardless
// of pass/fail outcome.
func TestReadinessProbe112_ChecksMapHasAllKeys(t *testing.T) {
	t.Parallel()
	probes := []ReadinessProbe{
		&succeedingReadinessProbe{name: "database"},
		redisadapter.NewRedisPingProbe(
			&alwaysErrRedisPinger{err: errors.New("down")},
			"redis",
		),
		outbox.NewOutboxLagProbe(&fakeOutboxCounter{count: 500}, 100, "outbox"),
	}
	ts := buildProbeTestServer(t, probes)

	_, _, checks := decodeReadyzBody(t, ts)
	for _, name := range []string{"database", "redis", "outbox"} {
		if _, found := checks[name]; !found {
			t.Errorf("checks map missing key %q; got %v", name, checks)
		}
	}
}

// =============================================================================
// Step 6 — Integration scenario: /healthz stays 200 when probes fail
// =============================================================================

// TestReadinessProbe112_HealthzUnaffectedByProbeFailures verifies that the
// liveness probe /healthz always returns 200 even when all readiness probes
// are failing. Liveness and readiness are independent by design.
func TestReadinessProbe112_HealthzUnaffectedByProbeFailures(t *testing.T) {
	t.Parallel()
	probes := []ReadinessProbe{
		&failingReadinessProbe{name: "database", err: errors.New("refused")},
		redisadapter.NewRedisPingProbe(
			&alwaysErrRedisPinger{err: errors.New("refused")},
			"redis",
		),
		outbox.NewOutboxLagProbe(&fakeOutboxCounter{count: 9999}, 100, "outbox"),
	}
	ts := buildProbeTestServer(t, probes)

	resp, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200 (must be unaffected by readiness probes)", resp.StatusCode)
	}
}

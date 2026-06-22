// metrics_endpoint_test.go verifies feature #42:
// "Direct call to /metrics succeeds without auth"
//
// The six feature steps are:
//
//  1. GET /metrics → 200 (no auth header needed)
//  2. Content-Type: text/plain; version=0.0.4; charset=utf-8
//  3. Body contains 'go_goroutines' (standard Go runtime metric)
//  4. Body contains 'http_requests_total' (custom arena metric)
//  5. Body contains '# HELP' and '# TYPE' lines for every metric
//  6. No Authorization header was needed to reach the endpoint
//
// All steps are verified with in-process httptest helpers wired to the
// REAL observability.Metrics so the scrape output is genuine Prometheus
// text-format (not a stub). No external PostgreSQL or network is required.
package httpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/prometheus/client_golang/prometheus"
)

// =============================================================================
// helpers
// =============================================================================

// buildMetricsTestServer constructs a Server wired with a real
// *observability.Metrics backed by a fresh Prometheus registry.
// The registry receives the standard Go runtime + process collectors
// (registered inside observability.New) so go_goroutines is always present.
//
// It also pre-increments arena_http_requests_total once so the custom counter
// has at least one sample and will appear in the scrape output even before any
// real HTTP request passes through the prometheusMiddleware.
func buildMetricsTestServer(t *testing.T) (*httptest.Server, *observability.Metrics) {
	t.Helper()

	reg := prometheus.NewRegistry()
	m, err := observability.New(reg)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	// Pre-seed one observation so arena_http_requests_total appears in the
	// scrape output (Prometheus vectors without any label-set are omitted from
	// Gather() output unless at least one combination has been observed).
	m.HTTPRequestsTotal.WithLabelValues("GET", "/healthz", "200").Inc()

	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		AppName:        "arena-api-test",
		AppVersion:     "0.0.0-test",
		AppCommit:      "test",
		HTTPListenAddr: "127.0.0.1:0",
		BodyLimitBytes: 1 << 20,
		RequestTimeout: 5 * time.Second,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
		LogLevel:       "info",
		LogFormat:      "json",
	}

	srv := New(Options{
		Config:         cfg,
		Metrics:        m,
		MetricsHandler: m.Handler(),
	})

	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return ts, m
}

// getMetrics issues GET /metrics to ts without any Authorization header and
// returns the response. The caller is responsible for closing resp.Body.
func getMetrics(t *testing.T, ts *httptest.Server) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/metrics", nil)
	if err != nil {
		t.Fatalf("build GET /metrics request: %v", err)
	}
	// Explicitly make sure no Authorization header is sent — this is the
	// "no auth needed" contract for step 6.
	req.Header.Del("Authorization")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	return resp
}

// =============================================================================
// Step 1 — GET /metrics → 200
// =============================================================================

// TestMetricsEndpoint_Returns200 verifies that /metrics responds 200 with no
// Authorization header. This is the primary liveness check for the endpoint.
func TestMetricsEndpoint_Returns200(t *testing.T) {
	t.Parallel()
	ts, _ := buildMetricsTestServer(t)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics: status = %d, want 200", resp.StatusCode)
	}
}

// =============================================================================
// Step 2 — Content-Type: text/plain; version=0.0.4; charset=utf-8
// =============================================================================

// TestMetricsEndpoint_ContentTypeIsPlainText verifies that the response carries
// the canonical Prometheus text-exposition Content-Type header. The format is
// documented in the Prometheus exposition formats specification:
//
//	text/plain; version=0.0.4; charset=utf-8
//
// The promhttp.HandlerFor handler uses this value unless the client sends an
// Accept header that triggers OpenMetrics negotiation. Our request sends no
// Accept header, so the legacy text format is always selected.
func TestMetricsEndpoint_ContentTypeIsPlainText(t *testing.T) {
	t.Parallel()
	ts, _ := buildMetricsTestServer(t)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("GET /metrics Content-Type = %q, want prefix \"text/plain\"", ct)
	}
	if !strings.Contains(ct, "version=0.0.4") {
		t.Errorf("GET /metrics Content-Type = %q, want to contain \"version=0.0.4\"", ct)
	}
	if !strings.Contains(ct, "charset=utf-8") {
		t.Errorf("GET /metrics Content-Type = %q, want to contain \"charset=utf-8\"", ct)
	}
}

// TestMetricsEndpoint_ContentTypeExact verifies the Content-Type value
// matches the canonical Prometheus text-exposition format. Newer versions of
// prometheus/client_golang may append additional parameters such as
// "; escaping=values" (introduced in client_golang ≥ 1.20 for UTF-8 label
// escaping negotiation). We therefore check that the required parameters are
// present as a prefix rather than requiring an exact byte-for-byte match,
// which would be too fragile against library upgrades.
func TestMetricsEndpoint_ContentTypeExact(t *testing.T) {
	t.Parallel()
	ts, _ := buildMetricsTestServer(t)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	const wantPrefix = "text/plain; version=0.0.4; charset=utf-8"
	if !strings.HasPrefix(ct, wantPrefix) {
		t.Errorf("GET /metrics Content-Type = %q, want prefix %q", ct, wantPrefix)
	}
}

// =============================================================================
// Step 3 — Body contains 'go_goroutines' (standard Go runtime metric)
// =============================================================================

// TestMetricsEndpoint_BodyContainsGoGoroutines verifies that the scrape output
// includes the standard Go runtime 'go_goroutines' gauge. This metric is
// registered by collectors.NewGoCollector inside observability.New, confirming
// that the Go runtime collector is wired into the arena registry and that the
// /metrics endpoint exposes it in the text-format scrape output.
func TestMetricsEndpoint_BodyContainsGoGoroutines(t *testing.T) {
	t.Parallel()
	ts, _ := buildMetricsTestServer(t)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET /metrics: read body: %v", err)
	}

	if !strings.Contains(string(body), "go_goroutines") {
		t.Errorf("GET /metrics body does not contain 'go_goroutines'; excerpt:\n%s", truncate(string(body), 500))
	}
}

// TestMetricsEndpoint_GoGoroutinesHasHELPLine verifies that the standard
// go_goroutines metric is accompanied by a '# HELP go_goroutines' comment
// line, confirming it is a properly registered collector and not a bare
// value injected without metadata.
func TestMetricsEndpoint_GoGoroutinesHasHELPLine(t *testing.T) {
	t.Parallel()
	ts, _ := buildMetricsTestServer(t)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET /metrics: read body: %v", err)
	}

	if !strings.Contains(string(body), "# HELP go_goroutines") {
		t.Errorf("GET /metrics body missing '# HELP go_goroutines' line; excerpt:\n%s", truncate(string(body), 500))
	}
}

// =============================================================================
// Step 4 — Body contains 'http_requests_total' (custom arena metric)
// =============================================================================

// TestMetricsEndpoint_BodyContainsHTTPRequestsTotal verifies that the scrape
// output contains the custom arena_http_requests_total counter. The metric is
// registered by observability.New with the "arena_http" subsystem and is
// pre-seeded with one observation in buildMetricsTestServer so it appears in
// the Gather output.
func TestMetricsEndpoint_BodyContainsHTTPRequestsTotal(t *testing.T) {
	t.Parallel()
	ts, _ := buildMetricsTestServer(t)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET /metrics: read body: %v", err)
	}

	// The full metric name is arena_http_requests_total; the feature spec uses
	// the substring 'http_requests_total' which unambiguously matches.
	if !strings.Contains(string(body), "http_requests_total") {
		t.Errorf("GET /metrics body does not contain 'http_requests_total'; excerpt:\n%s", truncate(string(body), 500))
	}
}

// TestMetricsEndpoint_HTTPRequestsTotalHasHELPLine verifies that the custom
// arena_http_requests_total metric carries a '# HELP' comment line in the
// scrape output, confirming the metric is properly documented.
func TestMetricsEndpoint_HTTPRequestsTotalHasHELPLine(t *testing.T) {
	t.Parallel()
	ts, _ := buildMetricsTestServer(t)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET /metrics: read body: %v", err)
	}

	if !strings.Contains(string(body), "# HELP arena_http_requests_total") {
		t.Errorf("GET /metrics body missing '# HELP arena_http_requests_total'; excerpt:\n%s", truncate(string(body), 500))
	}
}

// TestMetricsEndpoint_HTTPRequestsTotalHasTYPELine verifies that the custom
// counter carries a '# TYPE ... counter' line, confirming it is classified
// correctly in the text-format output.
func TestMetricsEndpoint_HTTPRequestsTotalHasTYPELine(t *testing.T) {
	t.Parallel()
	ts, _ := buildMetricsTestServer(t)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET /metrics: read body: %v", err)
	}

	if !strings.Contains(string(body), "# TYPE arena_http_requests_total counter") {
		t.Errorf("GET /metrics body missing '# TYPE arena_http_requests_total counter'; excerpt:\n%s", truncate(string(body), 500))
	}
}

// =============================================================================
// Step 5 — '# HELP' and '# TYPE' lines for every arena metric
// =============================================================================

// TestMetricsEndpoint_AllArenaMetricsHaveHELPLines verifies that each baseline
// arena metric registered by observability.New carries a '# HELP' comment line
// in the scrape output. The Prometheus text format requires this for every
// metric family; its absence would indicate a metric registered without Help
// text, which is a violation of the project's observability contract.
func TestMetricsEndpoint_AllArenaMetricsHaveHELPLines(t *testing.T) {
	t.Parallel()
	ts, m := buildMetricsTestServer(t)

	// Pre-seed all vectors so they appear in the Gather output.
	m.HTTPRequestDuration.WithLabelValues("GET", "/healthz", "200").Observe(0.001)
	m.DBPoolConnections.WithLabelValues("acquired").Set(0)
	m.WorkerJobsLagSeconds.WithLabelValues("default").Set(0)
	m.OutboxBacklog.Set(0)
	m.HTTPPanicsTotal.Add(0)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET /metrics: read body: %v", err)
	}
	bodyStr := string(body)

	wantHELP := []string{
		"# HELP arena_http_request_duration_seconds",
		"# HELP arena_http_requests_total",
		"# HELP arena_db_pool_connections",
		"# HELP arena_worker_jobs_lag_seconds",
		"# HELP arena_outbox_backlog",
		"# HELP arena_http_panics_total",
	}
	for _, want := range wantHELP {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("GET /metrics body missing %q", want)
		}
	}
}

// TestMetricsEndpoint_AllArenaMetricsHaveTYPELines verifies that each baseline
// arena metric carries a '# TYPE' comment line in the scrape output, confirming
// that Prometheus can correctly classify and display each metric family.
func TestMetricsEndpoint_AllArenaMetricsHaveTYPELines(t *testing.T) {
	t.Parallel()
	ts, m := buildMetricsTestServer(t)

	// Pre-seed all vectors so they surface in Gather output.
	m.HTTPRequestDuration.WithLabelValues("GET", "/healthz", "200").Observe(0.001)
	m.DBPoolConnections.WithLabelValues("acquired").Set(0)
	m.WorkerJobsLagSeconds.WithLabelValues("default").Set(0)
	m.OutboxBacklog.Set(0)
	m.HTTPPanicsTotal.Add(0)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET /metrics: read body: %v", err)
	}
	bodyStr := string(body)

	wantTYPE := []string{
		"# TYPE arena_http_request_duration_seconds histogram",
		"# TYPE arena_http_requests_total counter",
		"# TYPE arena_db_pool_connections gauge",
		"# TYPE arena_worker_jobs_lag_seconds gauge",
		"# TYPE arena_outbox_backlog gauge",
		"# TYPE arena_http_panics_total counter",
	}
	for _, want := range wantTYPE {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("GET /metrics body missing %q", want)
		}
	}
}

// TestMetricsEndpoint_BodyHasHELPAndTYPEForGoGoroutines checks that the
// standard go_goroutines runtime metric also carries # HELP and # TYPE lines,
// confirming that the Go collector is properly wired and that both metadata
// lines are emitted.
func TestMetricsEndpoint_BodyHasHELPAndTYPEForGoGoroutines(t *testing.T) {
	t.Parallel()
	ts, _ := buildMetricsTestServer(t)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET /metrics: read body: %v", err)
	}
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "# HELP go_goroutines") {
		t.Errorf("GET /metrics body missing '# HELP go_goroutines'")
	}
	if !strings.Contains(bodyStr, "# TYPE go_goroutines gauge") {
		t.Errorf("GET /metrics body missing '# TYPE go_goroutines gauge'")
	}
}

// =============================================================================
// Step 6 — No Authorization header needed
// =============================================================================

// TestMetricsEndpoint_NoAuthHeaderNeeded verifies that /metrics does not
// return 401 Unauthorized or 403 Forbidden when no Authorization header is
// present. The endpoint is intentionally unauthenticated for the foundation
// milestone (network-level restriction is enforced at the Dokploy layer).
func TestMetricsEndpoint_NoAuthHeaderNeeded(t *testing.T) {
	t.Parallel()
	ts, _ := buildMetricsTestServer(t)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		t.Fatal("GET /metrics (no auth): got 401 — endpoint must be unauthenticated")
	case http.StatusForbidden:
		t.Fatal("GET /metrics (no auth): got 403 — endpoint must be unauthenticated")
	}
}

// TestMetricsEndpoint_NoSetCookieHeader verifies that the scrape endpoint
// does not set a session cookie in the response. The /metrics path is consumed
// by Prometheus, not browsers; session cookies would be a privacy leak.
func TestMetricsEndpoint_NoSetCookieHeader(t *testing.T) {
	t.Parallel()
	ts, _ := buildMetricsTestServer(t)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	if v := resp.Header.Get("Set-Cookie"); v != "" {
		t.Errorf("GET /metrics set Set-Cookie header: %q — scrape endpoint must not initiate sessions", v)
	}
}

// TestMetricsEndpoint_NoWWWAuthenticateHeader verifies that the scrape
// endpoint does not return a WWW-Authenticate challenge header. Prometheus
// scrapers do not handle HTTP authentication challenges; their presence would
// cause silent scrape failures.
func TestMetricsEndpoint_NoWWWAuthenticateHeader(t *testing.T) {
	t.Parallel()
	ts, _ := buildMetricsTestServer(t)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	if v := resp.Header.Get("WWW-Authenticate"); v != "" {
		t.Errorf("GET /metrics returned WWW-Authenticate header: %q — endpoint must not challenge unauthenticated callers", v)
	}
}

// =============================================================================
// Summary test — all six steps in one sweep
// =============================================================================

// TestMetricsEndpoint_FullVerification is a single sweep that exercises all
// six feature steps in sequence, making it the canonical acceptance test for
// feature #42.
func TestMetricsEndpoint_FullVerification(t *testing.T) {
	t.Parallel()
	ts, m := buildMetricsTestServer(t)

	// Pre-seed all custom metrics so they surface in the scrape output.
	m.HTTPRequestDuration.WithLabelValues("GET", "/v1/info", "200").Observe(0.005)
	m.DBPoolConnections.WithLabelValues("acquired").Set(1)
	m.WorkerJobsLagSeconds.WithLabelValues("default").Set(0)
	m.OutboxBacklog.Set(0)
	m.HTTPPanicsTotal.Add(0)

	// Step 6 — no auth header in request.
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/metrics", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	// Verify no Authorization header is present.
	if req.Header.Get("Authorization") != "" {
		t.Fatal("test bug: request carries Authorization header")
	}

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	// Step 1 — 200 OK.
	if resp.StatusCode != http.StatusOK {
		t.Errorf("step 1 — status = %d, want 200", resp.StatusCode)
	}

	// Step 2 — Content-Type: text/plain; version=0.0.4; charset=utf-8.
	// Newer prometheus/client_golang versions may append "; escaping=values"
	// so we check for the required prefix rather than an exact string match.
	ct := resp.Header.Get("Content-Type")
	const wantCTPrefix = "text/plain; version=0.0.4; charset=utf-8"
	if !strings.HasPrefix(ct, wantCTPrefix) {
		t.Errorf("step 2 — Content-Type = %q, want prefix %q", ct, wantCTPrefix)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	bodyStr := string(body)

	// Step 3 — go_goroutines present (standard Go runtime metric).
	if !strings.Contains(bodyStr, "go_goroutines") {
		t.Errorf("step 3 — body missing 'go_goroutines'")
	}

	// Step 4 — http_requests_total present (custom arena metric).
	if !strings.Contains(bodyStr, "http_requests_total") {
		t.Errorf("step 4 — body missing 'http_requests_total'")
	}

	// Step 5 — # HELP and # TYPE lines for every arena metric.
	expectedPairs := [][2]string{
		{"# HELP arena_http_request_duration_seconds", "# TYPE arena_http_request_duration_seconds histogram"},
		{"# HELP arena_http_requests_total", "# TYPE arena_http_requests_total counter"},
		{"# HELP arena_db_pool_connections", "# TYPE arena_db_pool_connections gauge"},
		{"# HELP arena_worker_jobs_lag_seconds", "# TYPE arena_worker_jobs_lag_seconds gauge"},
		{"# HELP arena_outbox_backlog", "# TYPE arena_outbox_backlog gauge"},
		{"# HELP arena_http_panics_total", "# TYPE arena_http_panics_total counter"},
		{"# HELP go_goroutines", "# TYPE go_goroutines gauge"},
	}
	for _, pair := range expectedPairs {
		if !strings.Contains(bodyStr, pair[0]) {
			t.Errorf("step 5 — body missing HELP line: %s", pair[0])
		}
		if !strings.Contains(bodyStr, pair[1]) {
			t.Errorf("step 5 — body missing TYPE line: %s", pair[1])
		}
	}

	// Step 6 — confirm no auth challenge was returned.
	if resp.StatusCode == http.StatusUnauthorized {
		t.Error("step 6 — got 401; endpoint must be unauthenticated")
	}
	if resp.Header.Get("WWW-Authenticate") != "" {
		t.Errorf("step 6 — WWW-Authenticate returned: %q", resp.Header.Get("WWW-Authenticate"))
	}
}

// =============================================================================
// helpers
// =============================================================================

// truncate returns the first n characters of s, appending "…" if truncated.
// Used in test error messages to avoid flooding the test output with very
// long Prometheus scrape bodies.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

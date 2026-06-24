// metrics_test.go covers the Prometheus metrics scaffold for feature #87.
//
// Test goals:
//
//   - All baseline collectors required by the spec (http_request_duration_seconds
//     histogram, http_requests_total counter, db_pool_connections gauge,
//     worker_jobs_lag_seconds gauge, outbox_backlog gauge) are registered after
//     calling New, without panicking, and surfaced via the registry.
//
//   - Handler() returns 200 with a non-empty body when scraped (the spec's
//     "unit test: /metrics returns 200 with non-empty output" requirement).
//
//   - New is idempotent across repeated calls on the same registry (so test
//     fixtures and dev hot-reloads don't panic with AlreadyRegisteredError).
//
//   - Typed-field observations (HTTPRequestDuration.WithLabelValues, etc.)
//     surface on Gather and on the /metrics scrape so middleware can rely on
//     the labels documented at the top of metrics.go.
//
// Pure stdlib + Prometheus client tests — no external dependencies, no
// network I/O. Safe to run under `go test -race ./...`.
package observability

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// expectedMetricNames lists the fully-qualified metric names every call to
// New(registry) must register. The slice is the contract between this
// package and downstream middleware / dashboards.
var expectedMetricNames = []string{
	"arena_http_request_duration_seconds", // histogram (suffix _bucket/_count/_sum)
	"arena_http_requests_total",
	"arena_db_pool_connections",
	"arena_worker_jobs_lag_seconds",
	"arena_outbox_backlog",
}

func TestNew_RegistersAllBaselineCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()

	m, err := New(reg)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if m == nil {
		t.Fatal("New returned nil *Metrics")
	}
	if m.Registry() != reg {
		t.Error("Registry() did not return the registry we passed in")
	}

	// Force at least one sample on every Vec so they surface in Gather output
	// (vectors without an observation are not returned by Gather).
	m.HTTPRequestDuration.WithLabelValues("GET", "/healthz", "200").Observe(0.01)
	m.HTTPRequestsTotal.WithLabelValues("GET", "/healthz", "200").Inc()
	m.DBPoolConnections.WithLabelValues("acquired").Set(3)
	m.WorkerJobsLagSeconds.WithLabelValues("default").Set(0)
	m.OutboxBacklog.Set(0)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("registry.Gather() failed: %v", err)
	}

	got := make(map[string]struct{}, len(families))
	for _, f := range families {
		got[f.GetName()] = struct{}{}
	}

	for _, want := range expectedMetricNames {
		if _, ok := got[want]; !ok {
			t.Errorf("expected metric %q to be registered, but Gather() did not return it", want)
		}
	}
}

func TestNew_IsIdempotentOnSharedRegistry(t *testing.T) {
	reg := prometheus.NewRegistry()

	if _, err := New(reg); err != nil {
		t.Fatalf("first New: %v", err)
	}
	// The second call MUST NOT return an error and MUST NOT panic with
	// prometheus.AlreadyRegisteredError. Inside metrics.go we explicitly fall
	// through on AlreadyRegisteredError to keep the constructor idempotent.
	if _, err := New(reg); err != nil {
		t.Fatalf("second New on the same registry returned: %v", err)
	}
}

func TestNew_NilRegistryAllocatesFresh(t *testing.T) {
	m, err := New(nil)
	if err != nil {
		t.Fatalf("New(nil) returned error: %v", err)
	}
	if m == nil {
		t.Fatal("New(nil) returned nil *Metrics")
	}
	if m.Registry() == nil {
		t.Fatal("New(nil) did not allocate a registry")
	}
}

// TestMustNew_DoesNotPanicOnFreshRegistry ensures the panic-on-error
// constructor is safe in the common case (used in main()).
func TestMustNew_DoesNotPanicOnFreshRegistry(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("MustNew panicked on fresh registry: %v", r)
		}
	}()
	m := MustNew(prometheus.NewRegistry())
	if m == nil {
		t.Fatal("MustNew returned nil *Metrics")
	}
}

// TestHandler_ReturnsHTTP200WithNonEmptyBody is the headline assertion from
// the feature-#87 spec ("/metrics returns 200 with non-empty output").
func TestHandler_ReturnsHTTP200WithNonEmptyBody(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := New(reg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Push at least one observation so the scrape output contains business
	// metrics in addition to the always-emitted runtime metrics.
	m.HTTPRequestDuration.WithLabelValues("GET", "/healthz", "200").Observe(0.01)
	m.HTTPRequestsTotal.WithLabelValues("GET", "/healthz", "200").Inc()
	m.DBPoolConnections.WithLabelValues("acquired").Set(1)
	m.WorkerJobsLagSeconds.WithLabelValues("default").Set(0)
	m.OutboxBacklog.Set(0)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)

	m.Handler().ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want %d", resp.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("scrape body was empty")
	}

	bodyStr := string(body)
	for _, want := range expectedMetricNames {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("scrape body missing expected metric name %q", want)
		}
	}
}

// TestHandlerFor_AcceptsBareRegistry verifies the package-level HandlerFor
// helper which lets callers that hold only a *prometheus.Registry expose a
// scrape endpoint without going through *Metrics.
func TestHandlerFor_AcceptsBareRegistry(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Subsystem: "test",
		Name:      "synthetic_total",
		Help:      "Synthetic counter used by HandlerFor_AcceptsBareRegistry.",
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)

	HandlerFor(reg).ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want %d", resp.StatusCode, http.StatusOK)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "arena_test_synthetic_total") {
		t.Errorf("scrape did not contain the synthetic counter; body=%q", string(body))
	}
}

// TestMetrics_ObservationsPropagateToScrape walks the full path used by
// real middleware: observe a histogram + increment a counter + set a gauge,
// then scrape via the handler and confirm the values are reflected.
func TestMetrics_ObservationsPropagateToScrape(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := New(reg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	m.HTTPRequestDuration.WithLabelValues("POST", "/v1/echo", "201").Observe(0.123)
	m.HTTPRequestsTotal.WithLabelValues("POST", "/v1/echo", "201").Add(7)
	m.DBPoolConnections.WithLabelValues("acquired").Set(11)
	m.WorkerJobsLagSeconds.WithLabelValues("emails").Set(42)
	m.OutboxBacklog.Set(99)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()

	// Each substring uniquely identifies the value we just emitted, so a
	// regression in any vector immediately shows up here.
	wantSubstrings := []string{
		`arena_http_requests_total{method="POST",route="/v1/echo",status="201"} 7`,
		`arena_db_pool_connections{state="acquired"} 11`,
		`arena_worker_jobs_lag_seconds{queue="emails"} 42`,
		`arena_outbox_backlog 99`,
		// The histogram emits <name>_count / <name>_sum lines; check _count is 1
		// for the route we observed once.
		`arena_http_request_duration_seconds_count{method="POST",route="/v1/echo",status="201"} 1`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q\n---BODY---\n%s", want, body)
		}
	}
}

// TestDefaultHTTPDurationBuckets_AreOrderedAndPositive guards the contract
// documented at the top of metrics.go ("typical web-API range from 1 ms to
// 10 s"). Prometheus rejects out-of-order or non-positive buckets at
// registration time; this test makes the failure mode obvious rather than
// surface inside Prometheus internals.
func TestDefaultHTTPDurationBuckets_AreOrderedAndPositive(t *testing.T) {
	if len(DefaultHTTPDurationBuckets) == 0 {
		t.Fatal("DefaultHTTPDurationBuckets is empty")
	}
	var prev float64
	for i, b := range DefaultHTTPDurationBuckets {
		if b <= 0 {
			t.Errorf("bucket[%d] must be > 0, got %v", i, b)
		}
		if i > 0 && !(b > prev) {
			t.Errorf("bucket[%d]=%v is not strictly greater than previous %v", i, b, prev)
		}
		prev = b
	}
}

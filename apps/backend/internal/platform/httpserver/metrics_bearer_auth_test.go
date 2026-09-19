// metrics_bearer_auth_test.go verifies the METRICS_BEARER_TOKEN guard added
// alongside the ops watchdog (Part 3 of the watchdog task): when
// METRICS_BEARER_TOKEN is set, GET /metrics without a matching
// "Authorization: Bearer <token>" header must be rejected with 401; when it
// is unset, behaviour is unchanged (see metrics_endpoint_test.go, feature
// #42, for the no-auth contract).
package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
)

// buildMetricsAuthTestServer is buildMetricsTestServer plus an explicit
// MetricsBearerToken, so these tests can exercise the gated path without
// disturbing the unauthenticated-by-default suite in metrics_endpoint_test.go.
func buildMetricsAuthTestServer(t *testing.T, token string) *httptest.Server {
	t.Helper()

	reg := prometheus.NewRegistry()
	m, err := observability.New(reg)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}
	m.HTTPRequestsTotal.WithLabelValues("GET", "/healthz", "200").Inc()

	cfg := &config.Config{
		AppEnv:             config.EnvDevelopment,
		AppName:            "arena-api-test",
		AppVersion:         "0.0.0-test",
		AppCommit:          "test",
		HTTPListenAddr:     "127.0.0.1:0",
		BodyLimitBytes:     1 << 20,
		RequestTimeout:     5 * time.Second,
		DefaultLocale:      "en",
		ActiveLocales:      []string{"en", "ru"},
		LogLevel:           "info",
		LogFormat:          "json",
		MetricsBearerToken: token,
	}

	srv := New(Options{
		Config:         cfg,
		Metrics:        m,
		MetricsHandler: m.Handler(),
	})

	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return ts
}

func TestMetricsBearerAuth_TokenSet_NoHeader_Returns401(t *testing.T) {
	t.Parallel()
	ts := buildMetricsAuthTestServer(t, "s3cr3t-token")

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/metrics", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestMetricsBearerAuth_TokenSet_WrongToken_Returns401(t *testing.T) {
	t.Parallel()
	ts := buildMetricsAuthTestServer(t, "s3cr3t-token")

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/metrics", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestMetricsBearerAuth_TokenSet_CorrectToken_Returns200(t *testing.T) {
	t.Parallel()
	ts := buildMetricsAuthTestServer(t, "s3cr3t-token")

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/metrics", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer s3cr3t-token")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestMetricsBearerAuth_TokenUnset_NoHeader_Returns200(t *testing.T) {
	t.Parallel()
	ts := buildMetricsAuthTestServer(t, "")

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/metrics", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (METRICS_BEARER_TOKEN unset preserves prior unauthenticated behaviour)", resp.StatusCode)
	}
}

func TestRequireMetricsBearerToken_EmptyTokenReturnsHandlerUnwrapped(t *testing.T) {
	t.Parallel()
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	wrapped := RequireMetricsBearerToken("", inner)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418 (empty token must pass through unwrapped)", rr.Code)
	}
}

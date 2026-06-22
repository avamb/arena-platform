// anonymous_access_test.go verifies feature #9:
// "Anonymous access allowed on public endpoints"
//
// Endpoints declared public (/healthz, /readyz, /metrics, GET /v1/info) must
// respond 200 without any Authorization header.  The five feature steps are:
//
//  1. GET /healthz with no headers         → 200 {"status":"ok"}
//  2. GET /readyz  with no headers         → 200 when DB probe is passing
//  3. GET /metrics with no headers         → 200 Prometheus text exposition
//  4. GET /v1/info with no headers         → 200, active_locale resolved to "en"
//  5. None of the above set Set-Cookie or leak Authorization in the response
//
// All five steps are verified entirely with in-process httptest helpers;
// no external PostgreSQL, Redis, or OTLP collector is required.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// =============================================================================
// Helpers
// =============================================================================

// anonTestConfig builds the minimum *config.Config that lets the Server
// resolve the locale negotiation chain (DefaultLocale + ActiveLocales are
// consulted by handleInfo; RequestTimeout / BodyLimitBytes are wired into
// the chi adapter's middleware).
func anonTestConfig() *config.Config {
	return &config.Config{
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
}

// passingProbe is a ReadinessProbe whose Ping always returns nil ("ok").
// It is used in step 2 to simulate an operational database.
type passingProbe struct{ probeName string }

func (p *passingProbe) ProbeName() string            { return p.probeName }
func (p *passingProbe) Ping(_ context.Context) error { return nil }

var _ ReadinessProbe = (*passingProbe)(nil)

// buildAnonTestServer constructs a minimal Server that mounts:
//   - /healthz, /readyz  (always)
//   - /metrics           (when metricsHandler is non-nil)
//   - /v1/info           (always, no DB pool needed — falls back to static metadata)
//
// No auth stub, no pool, no audit, no idempotency — the /v1/echo and dev-token
// routes are therefore not mounted. Only the public-endpoint surfaces are tested.
func buildAnonTestServer(t *testing.T, opts ...func(*Options)) *Server {
	t.Helper()
	cfg := anonTestConfig()
	o := Options{Config: cfg}
	for _, fn := range opts {
		fn(&o)
	}
	// Ensure readiness probes are available when the caller adds them via opts.
	return New(o)
}

// requestAnon issues method+path against srv with NO headers (simulates an
// anonymous client that has no Authorization, Cookie, or Accept-Language
// headers set). Returns the recorded response.
func requestAnon(srv *Server, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	// Verify no default headers sneak in from the test framework.
	req.Header.Del("Authorization")
	req.Header.Del("Cookie")
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)
	return rr
}

// assertNoSetCookie fails the test if the response contains a Set-Cookie
// header. Public probes must not initiate a session for anonymous callers.
func assertNoSetCookie(t *testing.T, rr *httptest.ResponseRecorder, endpoint string) {
	t.Helper()
	if v := rr.Header().Get("Set-Cookie"); v != "" {
		t.Errorf("%s anonymous response set Set-Cookie header: %q", endpoint, v)
	}
}

// assertNoAuthorizationEcho fails the test if the response contains an
// Authorization header. Responses must never echo the request's credentials
// back to the caller.
func assertNoAuthorizationEcho(t *testing.T, rr *httptest.ResponseRecorder, endpoint string) {
	t.Helper()
	if v := rr.Header().Get("Authorization"); v != "" {
		t.Errorf("%s response echoes Authorization header back to client: %q", endpoint, v)
	}
}

// =============================================================================
// Step 1 — GET /healthz with no headers → 200 {"status":"ok"}
// =============================================================================

// TestAnonymousAccess_HealthzReturns200 verifies that the liveness probe
// responds 200 without any Authorization header being supplied.
func TestAnonymousAccess_HealthzReturns200(t *testing.T) {
	t.Parallel()
	srv := buildAnonTestServer(t)

	rr := requestAnon(srv, http.MethodGet, "/healthz")

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /healthz (anon): status = %d, want 200", rr.Code)
	}
}

// TestAnonymousAccess_HealthzBodyStatusOk verifies that the response body
// contains {"status":"ok"} — the exact contract documented in the feature spec.
func TestAnonymousAccess_HealthzBodyStatusOk(t *testing.T) {
	t.Parallel()
	srv := buildAnonTestServer(t)

	rr := requestAnon(srv, http.MethodGet, "/healthz")

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("GET /healthz (anon): decode JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("GET /healthz (anon): body[\"status\"] = %v, want \"ok\"", body["status"])
	}
}

// TestAnonymousAccess_HealthzNoAuthHeaderRequired asserts that the endpoint
// does NOT return 401 or 403 when accessed without credentials — confirming it
// is entirely outside the auth middleware chain.
func TestAnonymousAccess_HealthzNoAuthRequired(t *testing.T) {
	t.Parallel()
	srv := buildAnonTestServer(t)

	rr := requestAnon(srv, http.MethodGet, "/healthz")

	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("GET /healthz (anon): got 401 — endpoint should not require auth")
	}
	if rr.Code == http.StatusForbidden {
		t.Fatalf("GET /healthz (anon): got 403 — endpoint should not require auth")
	}
}

// =============================================================================
// Step 2 — GET /readyz with no headers → 200 when DB probe is passing
// =============================================================================

// TestAnonymousAccess_ReadyzReturns200WithPassingProbe verifies that /readyz
// returns 200 without an Authorization header when all registered probes pass.
func TestAnonymousAccess_ReadyzReturns200WithPassingProbe(t *testing.T) {
	t.Parallel()
	srv := buildAnonTestServer(t, func(o *Options) {
		o.Probes = []ReadinessProbe{
			&passingProbe{probeName: "database"},
		}
	})

	rr := requestAnon(srv, http.MethodGet, "/readyz")

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /readyz (anon, passing probe): status = %d, want 200", rr.Code)
	}
}

// TestAnonymousAccess_ReadyzBodyStatusReady verifies that the response body
// carries {"status":"ready"} when the DB probe is passing.
func TestAnonymousAccess_ReadyzBodyStatusReady(t *testing.T) {
	t.Parallel()
	srv := buildAnonTestServer(t, func(o *Options) {
		o.Probes = []ReadinessProbe{
			&passingProbe{probeName: "database"},
		}
	})

	rr := requestAnon(srv, http.MethodGet, "/readyz")

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("GET /readyz (anon): decode JSON: %v", err)
	}
	if body["status"] != "ready" {
		t.Fatalf("GET /readyz (anon): body[\"status\"] = %v, want \"ready\"", body["status"])
	}
}

// TestAnonymousAccess_ReadyzNoProbesAlsoReturns200 verifies that /readyz with
// no probes registered (vacuously healthy) also returns 200 anonymously.
func TestAnonymousAccess_ReadyzNoProbesAlsoReturns200(t *testing.T) {
	t.Parallel()
	srv := buildAnonTestServer(t) // no probes

	rr := requestAnon(srv, http.MethodGet, "/readyz")

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /readyz (anon, no probes): status = %d, want 200", rr.Code)
	}
}

// TestAnonymousAccess_ReadyzNoAuthRequired asserts /readyz does not return
// 401 or 403 to an anonymous caller — it is outside the auth middleware.
func TestAnonymousAccess_ReadyzNoAuthRequired(t *testing.T) {
	t.Parallel()
	srv := buildAnonTestServer(t)

	rr := requestAnon(srv, http.MethodGet, "/readyz")

	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("GET /readyz (anon): got 401 — endpoint should not require auth")
	}
	if rr.Code == http.StatusForbidden {
		t.Fatalf("GET /readyz (anon): got 403 — endpoint should not require auth")
	}
}

// =============================================================================
// Step 3 — GET /metrics with no headers → 200 Prometheus text
// =============================================================================

// metricsStubHandler returns a bare-bones Prometheus text-format response.
// It is used by Step 3 tests instead of the real Prometheus registry so that
// the httpserver package does not import the observability package in tests.
var metricsStubHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("# HELP go_goroutines Number of goroutines that currently exist.\n# TYPE go_goroutines gauge\ngo_goroutines 4\n"))
})

// TestAnonymousAccess_MetricsReturns200 verifies that /metrics responds 200
// without an Authorization header when a MetricsHandler is wired.
func TestAnonymousAccess_MetricsReturns200(t *testing.T) {
	t.Parallel()
	srv := buildAnonTestServer(t, func(o *Options) {
		o.MetricsHandler = metricsStubHandler
	})

	rr := requestAnon(srv, http.MethodGet, "/metrics")

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /metrics (anon): status = %d, want 200", rr.Code)
	}
}

// TestAnonymousAccess_MetricsBodyIsPrometheusText verifies that the response
// contains Prometheus text-format content (the "# HELP" marker).
func TestAnonymousAccess_MetricsBodyIsPrometheusText(t *testing.T) {
	t.Parallel()
	srv := buildAnonTestServer(t, func(o *Options) {
		o.MetricsHandler = metricsStubHandler
	})

	rr := requestAnon(srv, http.MethodGet, "/metrics")

	body := rr.Body.String()
	if !strings.Contains(body, "# HELP") {
		t.Fatalf("GET /metrics (anon): body does not contain Prometheus # HELP comment; got %q", body[:min(len(body), 300)])
	}
}

// TestAnonymousAccess_MetricsNoAuthRequired confirms that /metrics does not
// require credentials (no 401/403 for an anonymous caller).
func TestAnonymousAccess_MetricsNoAuthRequired(t *testing.T) {
	t.Parallel()
	srv := buildAnonTestServer(t, func(o *Options) {
		o.MetricsHandler = metricsStubHandler
	})

	rr := requestAnon(srv, http.MethodGet, "/metrics")

	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("GET /metrics (anon): got 401 — endpoint should not require auth")
	}
	if rr.Code == http.StatusForbidden {
		t.Fatalf("GET /metrics (anon): got 403 — endpoint should not require auth")
	}
}

// =============================================================================
// Step 4 — GET /v1/info with no headers → 200, active_locale = "en"
// =============================================================================

// TestAnonymousAccess_InfoReturns200 verifies that /v1/info responds 200
// without an Authorization header being supplied by the caller.
func TestAnonymousAccess_InfoReturns200(t *testing.T) {
	t.Parallel()
	srv := buildAnonTestServer(t)

	rr := requestAnon(srv, http.MethodGet, "/v1/info")

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /v1/info (anon): status = %d, want 200", rr.Code)
	}
}

// TestAnonymousAccess_InfoLocaleDefaultsToEn verifies that when no
// Accept-Language header is sent, the response carries active_locale = "en"
// (the configured DefaultLocale). This is the canonical step 4 check.
func TestAnonymousAccess_InfoLocaleDefaultsToEn(t *testing.T) {
	t.Parallel()
	srv := buildAnonTestServer(t)

	rr := requestAnon(srv, http.MethodGet, "/v1/info")

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /v1/info (anon): status = %d, want 200", rr.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("GET /v1/info (anon): decode JSON: %v", err)
	}

	activeLocale, ok := body["active_locale"].(string)
	if !ok {
		t.Fatalf("GET /v1/info (anon): active_locale field missing or not a string; body = %v", body)
	}
	if activeLocale != "en" {
		t.Fatalf("GET /v1/info (anon): active_locale = %q, want \"en\" (default locale)", activeLocale)
	}
}

// TestAnonymousAccess_InfoNoAuthRequired confirms that /v1/info does not
// require credentials — it must return 200, never 401 or 403.
func TestAnonymousAccess_InfoNoAuthRequired(t *testing.T) {
	t.Parallel()
	srv := buildAnonTestServer(t)

	rr := requestAnon(srv, http.MethodGet, "/v1/info")

	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("GET /v1/info (anon): got 401 — endpoint should not require auth")
	}
	if rr.Code == http.StatusForbidden {
		t.Fatalf("GET /v1/info (anon): got 403 — endpoint should not require auth")
	}
}

// =============================================================================
// Step 5 — None of the public endpoints set Set-Cookie or echo Authorization
// =============================================================================

// publicEndpoints enumerates every endpoint that must be accessible
// anonymously. The test table is used by both the Set-Cookie and the
// Authorization-echo assertions below.
var publicEndpoints = []struct {
	name   string
	method string
	path   string
}{
	{"healthz", http.MethodGet, "/healthz"},
	{"readyz", http.MethodGet, "/readyz"},
	{"v1/info", http.MethodGet, "/v1/info"},
}

// TestAnonymousAccess_NoneSetSetCookie verifies that none of the public
// endpoints set a Set-Cookie header in their anonymous response. Setting a
// cookie on a liveness/readiness probe would be a privacy/security issue and
// would confuse monitoring agents that aggregate cookie state.
func TestAnonymousAccess_NoneSetSetCookie(t *testing.T) {
	t.Parallel()
	srv := buildAnonTestServer(t, func(o *Options) {
		o.Probes = []ReadinessProbe{&passingProbe{probeName: "database"}}
		o.MetricsHandler = metricsStubHandler
	})

	// Test all public endpoints including /metrics.
	endpoints := append(publicEndpoints, struct {
		name   string
		method string
		path   string
	}{"metrics", http.MethodGet, "/metrics"})

	for _, ep := range endpoints {
		ep := ep
		t.Run(ep.name, func(t *testing.T) {
			t.Parallel()
			rr := requestAnon(srv, ep.method, ep.path)
			assertNoSetCookie(t, rr, ep.path)
		})
	}
}

// TestAnonymousAccess_NoneEchoAuthorization verifies that none of the public
// endpoints echo an Authorization header back to the caller. Echoing
// credentials in a response header is a security vulnerability (leaks tokens
// to any intermediate proxy or log aggregator that captures response headers).
func TestAnonymousAccess_NoneEchoAuthorization(t *testing.T) {
	t.Parallel()
	srv := buildAnonTestServer(t, func(o *Options) {
		o.Probes = []ReadinessProbe{&passingProbe{probeName: "database"}}
		o.MetricsHandler = metricsStubHandler
	})

	// Include /metrics in the echo check.
	endpoints := append(publicEndpoints, struct {
		name   string
		method string
		path   string
	}{"metrics", http.MethodGet, "/metrics"})

	for _, ep := range endpoints {
		ep := ep
		t.Run(ep.name, func(t *testing.T) {
			t.Parallel()
			rr := requestAnon(srv, ep.method, ep.path)
			assertNoAuthorizationEcho(t, rr, ep.path)
		})
	}
}

// TestAnonymousAccess_PublicEndpoints200Summary is a single sweep test that
// exercises all four public endpoints in one table and asserts each returns
// 200. It serves as the canonical feature #9 summary test.
func TestAnonymousAccess_PublicEndpoints200Summary(t *testing.T) {
	t.Parallel()
	srv := buildAnonTestServer(t, func(o *Options) {
		o.Probes = []ReadinessProbe{&passingProbe{probeName: "database"}}
		o.MetricsHandler = metricsStubHandler
	})

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"healthz", http.MethodGet, "/healthz"},
		{"readyz", http.MethodGet, "/readyz"},
		{"metrics", http.MethodGet, "/metrics"},
		{"v1/info", http.MethodGet, "/v1/info"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rr := requestAnon(srv, tc.method, tc.path)
			if rr.Code != http.StatusOK {
				t.Errorf("GET %s (anon): status = %d, want 200", tc.path, rr.Code)
			}
		})
	}
}

// =============================================================================
// Compile-time interface guard
// =============================================================================

var _ ReadinessProbe = (*passingProbe)(nil)

// v1_routes_test.go verifies feature #11:
// "All routes mounted under /v1 prefix"
//
// The feature requires that versioned business endpoints live under /v1/ and
// that paths without the prefix (e.g. /info, /api/info, /echo) return 404 with
// the standard JSON error envelope.
//
// All six feature steps are covered:
//
//  1. GET /v1/info              → 200
//  2. GET /info                 → 404 with JSON error envelope
//  3. GET /api/info             → 404
//  4. POST /v1/echo             → 401 (route is mounted, auth guard fires)
//  5. POST /echo                → 404 (not mounted at root level)
//  6. chi Walk verification     → every business route has /v1 prefix
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httpadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/http"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/go-chi/chi/v5"
)

// buildV1TestServer creates a fully-wired Server with stub auth enabled so that
// all /v1 business routes are mounted. This mirrors the production wiring from
// main.go without requiring a live PostgreSQL connection — the fakePoolDB,
// noopIdemStore, and captureAuditWriter test doubles (defined in
// echo_audit_test.go) satisfy all three PoolDB/idempotency.Store/audit.Writer
// dependencies.
func buildV1TestServer(t *testing.T) *Server {
	t.Helper()

	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  "test-secret-v1-routes",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}

	cfg := &config.Config{
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
	}

	return New(Options{
		Config: cfg,
		Auth:   stub,
		Audit:  &captureAuditWriter{},
		Idem:   &noopIdemStore{},
		Pool:   &fakePoolDB{tx: &fakeTx{}},
	})
}

// operationalPrefixes lists the paths that are intentionally mounted outside
// /v1 as infrastructure/operational endpoints.
var operationalPrefixes = []string{"/healthz", "/readyz", "/metrics"}

// isOperational reports whether a route pattern belongs to the operational
// (non-versioned) layer.
func isOperational(pattern string) bool {
	for _, p := range operationalPrefixes {
		if strings.HasPrefix(pattern, p) {
			return true
		}
	}
	return false
}

// =============================================================================
// Step 1 — GET /v1/info returns 200
// =============================================================================

// TestV1Routes_InfoReturns200 verifies step 1: GET /v1/info is mounted and
// returns HTTP 200 with a JSON body. No database connection is required because
// the fakePoolDB QueryRow returns an empty string that causes handleInfo to log
// a warning and fall through to the static metadata path.
func TestV1Routes_InfoReturns200(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/v1/info")
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/info: want 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("GET /v1/info Content-Type: want application/json, got %q", ct)
	}
}

// =============================================================================
// Step 2 — GET /info (no prefix) returns 404 with JSON error envelope
// =============================================================================

// TestV1Routes_InfoNoPrefixReturns404 verifies step 2: a request to /info
// without the /v1 version segment falls through to the custom NotFound handler
// and returns the standard JSON error envelope with code "http.not_found".
func TestV1Routes_InfoNoPrefixReturns404(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/info")
	if err != nil {
		t.Fatalf("GET /info: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /info: want 404, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("GET /info Content-Type: want application/json, got %q", ct)
	}

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("GET /info body not valid JSON: %v", err)
	}
	if body.Error.Code != "http.not_found" {
		t.Errorf("GET /info error.code: want %q, got %q", "http.not_found", body.Error.Code)
	}
	if body.Error.Message == "" {
		t.Error("GET /info error.message must be non-empty")
	}
}

// TestV1Routes_InfoNoPrefixHasRequestID verifies that the 404 for /info
// still carries a non-empty X-Request-Id header — the middleware chain must
// run before the NotFound handler.
func TestV1Routes_InfoNoPrefixHasRequestID(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/info")
	if err != nil {
		t.Fatalf("GET /info: %v", err)
	}
	defer resp.Body.Close()

	if resp.Header.Get(httpadapter.HeaderRequestID) == "" {
		t.Error("GET /info X-Request-Id header must be non-empty on 404 response")
	}
}

// =============================================================================
// Step 3 — GET /api/info returns 404
// =============================================================================

// TestV1Routes_ApiInfoReturns404 verifies step 3: a request to /api/info
// (a common alternative prefix) also falls through to the NotFound handler.
func TestV1Routes_ApiInfoReturns404(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/api/info")
	if err != nil {
		t.Fatalf("GET /api/info: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /api/info: want 404, got %d", resp.StatusCode)
	}
}

// =============================================================================
// Step 4 — POST /v1/echo is mounted; returns 401 without a token
// =============================================================================

// TestV1Routes_EchoMountedReturns401 verifies step 4: POST /v1/echo is mounted
// under /v1 and the auth middleware fires, returning 401 Unauthorized when no
// Authorization header is provided. A 404 here would mean the route is absent;
// a 401 confirms the route exists and the auth guard is active.
func TestV1Routes_EchoMountedReturns401(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"hi"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Deliberately no Authorization header — auth middleware must reject this.

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		t.Fatal("POST /v1/echo returned 404 — the route is not mounted")
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /v1/echo without token: want 401, got %d", resp.StatusCode)
	}
}

// TestV1Routes_EchoMountedContentTypeIsJSON verifies that the 401 returned by
// POST /v1/echo also uses the standard JSON error envelope (not a plain-text
// response), confirming the middleware chain and error handler are consistent.
func TestV1Routes_EchoMountedContentTypeIsJSON(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"hi"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("POST /v1/echo 401 Content-Type: want application/json, got %q", ct)
	}
}

// =============================================================================
// Step 5 — POST /echo (no prefix) returns 404
// =============================================================================

// TestV1Routes_EchoNoPrefixReturns404 verifies step 5: POST /echo without the
// /v1 prefix is not mounted and returns the custom 404 JSON error envelope.
func TestV1Routes_EchoNoPrefixReturns404(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/echo",
		strings.NewReader(`{"message":"hi"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /echo: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST /echo: want 404, got %d", resp.StatusCode)
	}
}

// =============================================================================
// Step 6 — chi Walk: every business route is /v1/...
// =============================================================================

// TestV1Routes_AllBusinessRoutesUnderV1Prefix verifies step 6: walking the
// entire chi route tree confirms that every route whose pattern is NOT an
// operational endpoint (/healthz, /readyz, /metrics) carries the /v1 prefix.
//
// This is the canonical "inspect the router mount tree" check. It guards
// against a future accidental mount of a business route outside /v1 (e.g.
// r.Get("/echo", ...) at the root level).
func TestV1Routes_AllBusinessRoutesUnderV1Prefix(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)

	// chi.Walk traverses the full route tree, calling walkFn for every leaf
	// route (method + pattern + handler). The pattern includes the full
	// mounted path, e.g. "/v1/echo" for routes inside Route("/v1", ...).
	var violations []string
	err := chi.Walk(s.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if isOperational(route) {
			// /healthz, /readyz, /metrics are intentionally outside /v1.
			return nil
		}
		if !strings.HasPrefix(route, "/v1") {
			violations = append(violations, method+" "+route)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk failed: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("found %d business route(s) mounted outside /v1:\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}

// TestV1Routes_V1InfoAppearsInWalk verifies that the chi Walk visit set
// contains GET /v1/info — confirming the route was actually registered and
// that the Walk helper visits it.
func TestV1Routes_V1InfoAppearsInWalk(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)

	found := false
	err := chi.Walk(s.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == http.MethodGet && route == "/v1/info" {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk failed: %v", err)
	}
	if !found {
		t.Error("GET /v1/info not found in chi route walk — route may not be mounted")
	}
}

// TestV1Routes_V1EchoAppearsInWalk verifies that POST /v1/echo appears in the
// walk output when the stub auth, pool, audit, and idempotency dependencies are
// all supplied (triggering the conditional mount in mountV1Routes).
func TestV1Routes_V1EchoAppearsInWalk(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)

	found := false
	err := chi.Walk(s.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == http.MethodPost && route == "/v1/echo" {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk failed: %v", err)
	}
	if !found {
		t.Error("POST /v1/echo not found in chi route walk — route may not be mounted")
	}
}

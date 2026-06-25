// scaffold_cleanup_171_test.go covers feature #171:
// "Remove scaffold_echo + dev routes from production builds"
//
// Tests verify:
//
//   - Step 1: Migration 0031_remove_scaffold_echo.sql exists and drops scaffold_echo table
//   - Step 2: POST /v1/scaffold/echo returns 404 (route removed from server)
//   - Step 3: /v1/dev/token and /v1/dev/auth/token return 404 when stub is nil
//     (production-mode: ENABLE_DEV_AUTH=false → stub=nil → routes not registered)
//   - Step 4: /v1/debug/panic and /v1/debug/slow return 404 when DebugRoutesEnabled=false
//     (production default: DEBUG_ROUTES_ENABLED=false)
//   - Step 5: chi.Walk on a production-like server finds no dev/debug/scaffold routes
package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// buildProdLikeServer creates a Server that mimics production configuration:
//   - Auth=nil (stub=nil — ENABLE_DEV_AUTH=false — no dev auth token endpoints)
//   - DebugRoutesEnabled=false (DEBUG_ROUTES_ENABLED=false — no debug endpoints)
//
// This is the "production binary" configuration described in feature #171.
func buildProdLikeServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		EnableStubAuth: false,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
	}
	// No Auth (stub=nil) → dev endpoints not registered.
	// DebugRoutesEnabled defaults to false → debug endpoints not registered.
	return New(Options{
		Config:             cfg,
		DebugRoutesEnabled: false, // explicit: production default
	})
}

// =============================================================================
// Step 1 — Migration 0031 exists and has the correct DROP TABLE statement
// =============================================================================

// TestScaffoldCleanup171_Migration0031Exists verifies that the cleanup migration
// file 0031_remove_scaffold_echo.sql exists in the migrations directory.
func TestScaffoldCleanup171_Migration0031Exists(t *testing.T) {
	t.Parallel()

	data := findFileByName(t, "0031_remove_scaffold_echo.sql")
	if len(data) == 0 {
		t.Fatal("0031_remove_scaffold_echo.sql is empty or not found")
	}
}

// TestScaffoldCleanup171_Migration0031HasDropTable verifies that the Up section
// of the migration drops the scaffold_echo table.
func TestScaffoldCleanup171_Migration0031HasDropTable(t *testing.T) {
	t.Parallel()

	data := findFileByName(t, "0031_remove_scaffold_echo.sql")
	upperData := strings.ToUpper(data)
	if !strings.Contains(upperData, "DROP TABLE") {
		t.Error("0031_remove_scaffold_echo.sql: missing DROP TABLE statement in Up section")
	}
	if !strings.Contains(data, "scaffold_echo") {
		t.Error("0031_remove_scaffold_echo.sql: missing scaffold_echo table name")
	}
}

// TestScaffoldCleanup171_Migration0031HasGooseUpDown verifies the migration has
// both +goose Up and +goose Down markers (clean uninstall is possible).
func TestScaffoldCleanup171_Migration0031HasGooseUpDown(t *testing.T) {
	t.Parallel()

	data := findFileByName(t, "0031_remove_scaffold_echo.sql")
	if !strings.Contains(data, "-- +goose Up") {
		t.Error("0031_remove_scaffold_echo.sql: missing -- +goose Up marker")
	}
	if !strings.Contains(data, "-- +goose Down") {
		t.Error("0031_remove_scaffold_echo.sql: missing -- +goose Down marker (needed for clean uninstall)")
	}
}

// TestScaffoldCleanup171_Migration0031DownRecreatesTable verifies the Down section
// recreates the scaffold_echo table (enables rollback).
func TestScaffoldCleanup171_Migration0031DownRecreatesTable(t *testing.T) {
	t.Parallel()

	data := findFileByName(t, "0031_remove_scaffold_echo.sql")
	upperData := strings.ToUpper(data)
	if !strings.Contains(upperData, "CREATE TABLE") {
		t.Error("0031_remove_scaffold_echo.sql: Down section missing CREATE TABLE (rollback not possible)")
	}
}

// =============================================================================
// Step 2 — POST /v1/scaffold/echo returns 404 (route removed from production build)
// =============================================================================

// TestScaffoldCleanup171_ScaffoldEchoRoute404 verifies that the scaffold echo
// route has been removed from the server. A production binary must return 404
// for POST /v1/scaffold/echo (the scaffolding example endpoint was removed in
// feature #171 when ticket issuance Wave 8 shipped).
func TestScaffoldCleanup171_ScaffoldEchoRoute404(t *testing.T) {
	t.Parallel()

	// Even with a fully wired dev server (stub enabled), the route must not exist.
	// The route was removed from mountV1Routes in server.go.
	s := buildProdLikeServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/scaffold/echo",
		strings.NewReader(`{"message":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("POST /v1/scaffold/echo: want 404 (route removed), got %d; body=%s",
			rr.Code, rr.Body.String())
	}
}

// TestScaffoldCleanup171_ScaffoldEchoRoute404WithStub verifies the route is absent
// even when stub auth is enabled (the route removal is unconditional, not runtime-gated).
func TestScaffoldCleanup171_ScaffoldEchoRoute404WithStub(t *testing.T) {
	t.Parallel()

	// Build a fully wired test server (stub enabled, pool wired, audit/idem present).
	// The scaffold/echo route must still be absent because it was physically removed.
	s := buildV1TestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/scaffold/echo",
		strings.NewReader(`{"message":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("POST /v1/scaffold/echo returned 401 — route is still mounted (auth guard fired); it should return 404 (not mounted)")
	}
	if rr.Code != http.StatusNotFound {
		t.Errorf("POST /v1/scaffold/echo with stub: want 404 (route removed), got %d; body=%s",
			rr.Code, rr.Body.String())
	}
}

// =============================================================================
// Step 3 — /v1/dev/token and /v1/dev/auth/token return 404 in production mode
// =============================================================================

// TestScaffoldCleanup171_DevTokenRoute404WhenStubNil verifies that
// POST /v1/dev/token returns 404 when the server is built without stub auth
// (i.e. ENABLE_DEV_AUTH=false, which is the production default).
// This is the "integration test against prod binary verifies 404 on /v1/dev/token"
// requirement from feature #171.
func TestScaffoldCleanup171_DevTokenRoute404WhenStubNil(t *testing.T) {
	t.Parallel()

	s := buildProdLikeServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token", nil)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("POST /v1/dev/token without stub: want 404, got %d; body=%s",
			rr.Code, rr.Body.String())
	}
}

// TestScaffoldCleanup171_DevAuthTokenRoute404WhenStubNil verifies that
// POST /v1/dev/auth/token returns 404 in production mode (stub=nil).
func TestScaffoldCleanup171_DevAuthTokenRoute404WhenStubNil(t *testing.T) {
	t.Parallel()

	s := buildProdLikeServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/auth/token", nil)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("POST /v1/dev/auth/token without stub: want 404, got %d; body=%s",
			rr.Code, rr.Body.String())
	}
}

// =============================================================================
// Step 4 — /v1/debug/* routes return 404 when DebugRoutesEnabled=false
// =============================================================================

// TestScaffoldCleanup171_DebugPanicRoute404WhenDisabled verifies that
// GET /v1/debug/panic returns 404 when DEBUG_ROUTES_ENABLED=false (production default).
func TestScaffoldCleanup171_DebugPanicRoute404WhenDisabled(t *testing.T) {
	t.Parallel()

	s := buildProdLikeServer(t) // debugRoutesEnabled=false
	req := httptest.NewRequest(http.MethodGet, "/v1/debug/panic", nil)
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code == http.StatusInternalServerError {
		t.Fatalf("GET /v1/debug/panic returned 500 — debug endpoint is mounted and panicked; it should return 404")
	}
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET /v1/debug/panic without debug routes: want 404, got %d; body=%s",
			rr.Code, rr.Body.String())
	}
}

// TestScaffoldCleanup171_DebugSlowRoute404WhenDisabled verifies that
// GET /v1/debug/slow returns 404 when DEBUG_ROUTES_ENABLED=false (production default).
func TestScaffoldCleanup171_DebugSlowRoute404WhenDisabled(t *testing.T) {
	t.Parallel()

	s := buildProdLikeServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/debug/slow", nil)
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("GET /v1/debug/slow without debug routes: want 404, got %d; body=%s",
			rr.Code, rr.Body.String())
	}
}

// =============================================================================
// Step 5 — chi.Walk on production-like server finds no dev/debug/scaffold routes
// =============================================================================

// TestScaffoldCleanup171_ProdModeNoDevRoutes verifies via chi.Walk that a
// production-like server (stub=nil, debugRoutesEnabled=false) has no routes
// under /v1/dev/*, /v1/debug/*, or /v1/scaffold/*.
func TestScaffoldCleanup171_ProdModeNoDevRoutes(t *testing.T) {
	t.Parallel()

	s := buildProdLikeServer(t)

	devPrefixes := []string{"/v1/dev/", "/v1/debug/", "/v1/scaffold/"}
	var violations []string

	err := chi.Walk(s.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		for _, prefix := range devPrefixes {
			if strings.HasPrefix(route, prefix) {
				violations = append(violations, method+" "+route)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk failed: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("production-mode server has %d dev/debug/scaffold route(s) that must not be registered:\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}

// TestScaffoldCleanup171_OperationalRoutesStillPresent verifies that removing
// dev routes did not accidentally remove the operational/production endpoints.
func TestScaffoldCleanup171_OperationalRoutesStillPresent(t *testing.T) {
	t.Parallel()

	s := buildProdLikeServer(t)

	// /healthz and /readyz must always be present (not gated by stub).
	for _, path := range []string{"/healthz", "/readyz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		s.router.ServeHTTP(rr, req)
		if rr.Code == http.StatusNotFound {
			t.Errorf("GET %s should be present in production mode, got 404", path)
		}
	}
}

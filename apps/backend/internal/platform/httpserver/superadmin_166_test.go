// superadmin_166_test.go — unit tests for the platform superadmin console (feature #166).
//
// Test naming convention: TestSuperadmin166_*
// All tests verify structural and behavioural contracts without a live database.
//
// Test categories:
//   - Source file and migration file structure
//   - SQL query file structure
//   - Generated Go file structure
//   - Querier interface compile-time check
//   - Route auth-gating (401 without JWT)
//   - X-Admin-Reason header enforcement (400 without header)
//   - Nil superadminQueries guard (503)
//   - Server wiring (superadminQueries field + route registration)
//   - Query parameter validation (invalid org_id, invalid limit)
//   - Response Content-Type
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// ─────────────────────────────────────────────────────────────────────────────
// Server factory and JWT helper for superadmin route tests
// ─────────────────────────────────────────────────────────────────────────────

const superadminTestActorID = "00000000-0000-0000-0000-000000000166"

// buildSuperadminServer166 builds a Server with stub auth and superadmin routes mounted.
// A gen.New(nil) Queries instance is used so routes are wired without a real DB.
func buildSuperadminServer166(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
	}
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("buildSuperadminServer166: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:            cfg,
		Auth:              stub,
		Pool:              &dbDownPool{},
		SuperadminQueries: gen.New(nil),
	})
}

// buildSuperadminServerNoQueries166 builds a Server WITHOUT superadminQueries
// to test the nil-guard (503 response).
func buildSuperadminServerNoQueries166(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
	}
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("buildSuperadminServerNoQueries166: NewStubProvider: %v", err)
	}
	// No SuperadminQueries set — routes not mounted.
	return New(Options{
		Config: cfg,
		Auth:   stub,
		Pool:   &dbDownPool{},
	})
}

// mintSuperadminToken mints a dev JWT for superadmin route tests with admin role.
func mintSuperadminToken(t *testing.T, s *Server) string {
	t.Helper()
	body := `{"actor_id":"` + superadminTestActorID + `","roles":["admin"]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintSuperadminToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintSuperadminToken: decode: %v", err)
	}
	tok, ok := resp["token"]
	if !ok || tok == "" {
		t.Fatalf("mintSuperadminToken: no token in response")
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Source file structure tests
// ─────────────────────────────────────────────────────────────────────────────

func TestSuperadmin166_SourceFileExists(t *testing.T) {
	content := findFileByName(t, "superadmin.go")
	checks := []string{
		"handleSuperadminListOrganizations",
		"handleSuperadminListOrders",
		"handleSuperadminListTickets",
		"handleSuperadminListRefunds",
		"X-Admin-Reason",
		"superadminAdminReasonHeader",
		"requireAdminReason",
		"logSuperadminAudit",
		"superadmin.missing_reason",
		"parseSuperadminPagination",
		"parseSuperadminOrgID",
		"superadminDefaultLimit",
		"superadminMaxLimit",
	}
	for _, want := range checks {
		if !strings.Contains(content, want) {
			t.Errorf("superadmin.go: missing expected string %q", want)
		}
	}
}

func TestSuperadmin166_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0034_superadmin.sql")
	checks := []string{
		"platform_superadmin",
		"superadmin.read",
		"role_permissions",
		"+goose Up",
		"+goose Down",
	}
	for _, want := range checks {
		if !strings.Contains(content, want) {
			t.Errorf("0034_superadmin.sql: missing expected string %q", want)
		}
	}
}

func TestSuperadmin166_SQLQueryFileExists(t *testing.T) {
	content := findFileByName(t, "superadmin.sql")
	checks := []string{
		"ListAllCheckoutSessions",
		"ListAllTickets",
		"ListAllRefunds",
		"checkout_sessions",
		"tickets",
		"refunds",
		"$1::uuid IS NULL OR org_id = $1",
		"$2::text  IS NULL OR state  = $2",
		"LIMIT  $3 OFFSET $4",
	}
	for _, want := range checks {
		if !strings.Contains(content, want) {
			t.Errorf("superadmin.sql: missing expected string %q", want)
		}
	}
}

func TestSuperadmin166_GenFileExists(t *testing.T) {
	content := findFileByName(t, "superadmin.sql.go")
	checks := []string{
		"ListAllCheckoutSessions",
		"ListAllTickets",
		"ListAllRefunds",
		"func (q *Queries) ListAllCheckoutSessions",
		"func (q *Queries) ListAllTickets",
		"func (q *Queries) ListAllRefunds",
		"CheckoutSessionRow",
		"TicketRow",
		"RefundRow",
	}
	for _, want := range checks {
		if !strings.Contains(content, want) {
			t.Errorf("superadmin.sql.go: missing expected string %q", want)
		}
	}
}

func TestSuperadmin166_QuerierInterfaceHasMethods(t *testing.T) {
	content := findFileByName(t, "querier.go")
	checks := []string{
		"ListAllCheckoutSessions",
		"ListAllTickets",
		"ListAllRefunds",
		"superadmin",
	}
	for _, want := range checks {
		if !strings.Contains(content, want) {
			t.Errorf("querier.go: missing expected string %q", want)
		}
	}
}

// Compile-time check: *gen.Queries implements Querier (superadmin methods included).
var _ gen.Querier = (*gen.Queries)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// Route auth-gating tests (401 without JWT)
// ─────────────────────────────────────────────────────────────────────────────

var superadminEndpoints = []struct {
	method string
	path   string
}{
	{http.MethodGet, "/v1/admin/organizations"},
	{http.MethodGet, "/v1/admin/orders"},
	{http.MethodGet, "/v1/admin/tickets"},
	{http.MethodGet, "/v1/admin/refunds"},
}

func TestSuperadmin166_RouteAuth_NoJWT(t *testing.T) {
	s := buildSuperadminServer166(t)
	for _, ep := range superadminEndpoints {
		t.Run(ep.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(ep.method, ep.path, nil)
			s.router.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: expected 401, got %d; body: %s",
					ep.method, ep.path, w.Code, w.Body.String())
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// X-Admin-Reason header enforcement
// ─────────────────────────────────────────────────────────────────────────────

func TestSuperadmin166_MissingAdminReason_Returns400(t *testing.T) {
	s := buildSuperadminServer166(t)
	tok := mintSuperadminToken(t, s)

	for _, ep := range superadminEndpoints {
		t.Run(ep.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(ep.method, ep.path, nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			// Intentionally NOT setting X-Admin-Reason
			s.router.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s %s: expected 400, got %d; body: %s",
					ep.method, ep.path, w.Code, w.Body.String())
			}
			body := w.Body.String()
			if !strings.Contains(body, "superadmin.missing_reason") {
				t.Errorf("%s %s: expected error code 'superadmin.missing_reason' in body, got: %s",
					ep.method, ep.path, body)
			}
		})
	}
}

func TestSuperadmin166_EmptyAdminReason_Returns400(t *testing.T) {
	s := buildSuperadminServer166(t)
	tok := mintSuperadminToken(t, s)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/organizations", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Admin-Reason", "   ") // whitespace only
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for whitespace-only reason, got %d", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Nil superadminQueries guard tests
// ─────────────────────────────────────────────────────────────────────────────

func TestSuperadmin166_NilQueries_RoutesNotMounted(t *testing.T) {
	// Server built without SuperadminQueries → routes not mounted → 404
	s := buildSuperadminServerNoQueries166(t)
	tok := mintSuperadminToken(t, s)

	for _, ep := range superadminEndpoints {
		t.Run(ep.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(ep.method, ep.path, nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			req.Header.Set("X-Admin-Reason", "test audit check")
			s.router.ServeHTTP(w, req)
			// Routes are not mounted when superadminQueries is nil, so chi returns 404.
			if w.Code != http.StatusNotFound {
				t.Errorf("%s %s (no queries): expected 404, got %d; body: %s",
					ep.method, ep.path, w.Code, w.Body.String())
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Query parameter validation tests
// ─────────────────────────────────────────────────────────────────────────────

func TestSuperadmin166_InvalidOrgID_Returns400(t *testing.T) {
	s := buildSuperadminServer166(t)
	tok := mintSuperadminToken(t, s)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/orders?org_id=not-a-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Admin-Reason", "investigating fraud report #123")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid org_id: expected 400, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "superadmin.invalid_org_id") {
		t.Errorf("expected 'superadmin.invalid_org_id' in error body, got: %s", w.Body.String())
	}
}

func TestSuperadmin166_InvalidLimit_Returns400(t *testing.T) {
	s := buildSuperadminServer166(t)
	tok := mintSuperadminToken(t, s)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/tickets?limit=-5", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Admin-Reason", "monitoring report")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid limit: expected 400, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "superadmin.invalid_limit") {
		t.Errorf("expected 'superadmin.invalid_limit' in error body, got: %s", w.Body.String())
	}
}

func TestSuperadmin166_InvalidOffset_Returns400(t *testing.T) {
	s := buildSuperadminServer166(t)
	tok := mintSuperadminToken(t, s)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/refunds?offset=-1", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Admin-Reason", "compliance audit")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid offset: expected 400, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "superadmin.invalid_offset") {
		t.Errorf("expected 'superadmin.invalid_offset' in error body, got: %s", w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Response Content-Type test
// ─────────────────────────────────────────────────────────────────────────────

func TestSuperadmin166_ResponseContentType(t *testing.T) {
	s := buildSuperadminServer166(t)
	tok := mintSuperadminToken(t, s)

	// The 400 error response from missing reason still has Content-Type: application/json.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/organizations", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	// No X-Admin-Reason → 400 but still JSON
	s.router.ServeHTTP(w, req)
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Server wiring tests
// ─────────────────────────────────────────────────────────────────────────────

func TestSuperadmin166_ServerHasSuperadminQueriesField(t *testing.T) {
	content := findFileByName(t, "server.go")
	checks := []string{
		"superadminQueries",
		"SuperadminQueries",
		"superadmin.read",
		"handleSuperadminListOrganizations",
		"handleSuperadminListOrders",
		"handleSuperadminListTickets",
		"handleSuperadminListRefunds",
		"/admin/organizations",
		"/admin/orders",
		"/admin/tickets",
		"/admin/refunds",
	}
	for _, want := range checks {
		if !strings.Contains(content, want) {
			t.Errorf("server.go: missing expected string %q", want)
		}
	}
}

func TestSuperadmin166_PermissionIsRequired(t *testing.T) {
	// The server.go route group must use RequirePermission with "superadmin.read".
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, `"superadmin.read"`) {
		t.Error(`server.go: missing RequirePermission("superadmin.read", ...)`)
	}
}

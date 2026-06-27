// networks_208_test.go — unit tests for feature #208 (operator-network CRUD).
//
// Covers:
//   - Route mounting and auth-gating for the five endpoints
//     (POST/GET list/GET id/PATCH/POST archive).
//   - Handler input validation: empty body, invalid JSON, missing fields,
//     malformed slug, nil-queries / nil-pool 503 short-circuit.
//   - Mount permission strings (network.read/create/update/archive) are wired
//     into mount_networks.go so the binding pattern in 0044_network_permissions.sql
//     remains the effective gate.
package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

func buildNetworkServer208(t *testing.T) *Server {
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
		t.Fatalf("NewStubProvider: %v", err)
	}
	return New(Options{
		Config:         cfg,
		Auth:           stub,
		Pool:           &dbDownPool{},
		NetworkQueries: gen.New(nil),
		Audit:          &captureAuditWriter{},
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Route mounting & auth gating
// ─────────────────────────────────────────────────────────────────────────────

func TestOperatorNetwork208_RoutesRequireAuth(t *testing.T) {
	cases := []struct {
		method, path string
		body         string
	}{
		{http.MethodGet, "/v1/operator-networks", ""},
		{http.MethodGet, "/v1/operator-networks/" + uuid.New().String(), ""},
		{http.MethodPost, "/v1/operator-networks", `{"name":"N","slug":"n"}`},
		{http.MethodPatch, "/v1/operator-networks/" + uuid.New().String(), `{"name":"N"}`},
		{http.MethodPost, "/v1/operator-networks/" + uuid.New().String() + "/archive", ""},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			s := buildNetworkServer208(t)
			var body *strings.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			} else {
				body = strings.NewReader("")
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, req)

			if rec.Code == http.StatusNotFound {
				t.Fatalf("%s %s returned 404 — route not mounted", tc.method, tc.path)
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: expected 401 without JWT, got %d (body=%s)",
					tc.method, tc.path, rec.Code, rec.Body.String())
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler validation (no auth — call handler directly)
// ─────────────────────────────────────────────────────────────────────────────

func TestOperatorNetwork208_Create_NilQueriesReturns503(t *testing.T) {
	s := &Server{cfg: &config.Config{DefaultLocale: "en"}}
	req := httptest.NewRequest(http.MethodPost, "/v1/operator-networks",
		strings.NewReader(`{"name":"N","slug":"n"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleCreateOperatorNetwork(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestOperatorNetwork208_Create_EmptyBodyReturns400(t *testing.T) {
	s := &Server{
		cfg:            &config.Config{DefaultLocale: "en"},
		networkQueries: gen.New(nil),
		pool:           &dbDownPool{},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/operator-networks", http.NoBody)
	rec := httptest.NewRecorder()
	s.handleCreateOperatorNetwork(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestOperatorNetwork208_Create_InvalidJSONReturns400(t *testing.T) {
	s := &Server{
		cfg:            &config.Config{DefaultLocale: "en"},
		networkQueries: gen.New(nil),
		pool:           &dbDownPool{},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/operator-networks",
		strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "saui-09 test")
	rec := httptest.NewRecorder()
	s.handleCreateOperatorNetwork(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
	if code := errorCode(t, orgRespJSON(t, rec)); code != "operator_network.invalid_json" {
		t.Errorf("error.code = %q want operator_network.invalid_json", code)
	}
}

func TestOperatorNetwork208_Create_MissingNameReturns400(t *testing.T) {
	s := &Server{
		cfg:            &config.Config{DefaultLocale: "en"},
		networkQueries: gen.New(nil),
		pool:           &dbDownPool{},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/operator-networks",
		strings.NewReader(`{"name":"","slug":"valid-slug"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "saui-09 test")
	rec := httptest.NewRecorder()
	s.handleCreateOperatorNetwork(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
	if code := errorCode(t, orgRespJSON(t, rec)); code != "operator_network.invalid_name" {
		t.Errorf("error.code = %q want operator_network.invalid_name", code)
	}
}

func TestOperatorNetwork208_Create_MissingSlugReturns400(t *testing.T) {
	s := &Server{
		cfg:            &config.Config{DefaultLocale: "en"},
		networkQueries: gen.New(nil),
		pool:           &dbDownPool{},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/operator-networks",
		strings.NewReader(`{"name":"Net","slug":""}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "saui-09 test")
	rec := httptest.NewRecorder()
	s.handleCreateOperatorNetwork(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
	if code := errorCode(t, orgRespJSON(t, rec)); code != "operator_network.invalid_slug" {
		t.Errorf("error.code = %q want operator_network.invalid_slug", code)
	}
}

func TestOperatorNetwork208_Create_BadSlugReturns400(t *testing.T) {
	s := &Server{
		cfg:            &config.Config{DefaultLocale: "en"},
		networkQueries: gen.New(nil),
		pool:           &dbDownPool{},
	}
	// Capital + underscore both violate the slug regex.
	req := httptest.NewRequest(http.MethodPost, "/v1/operator-networks",
		strings.NewReader(`{"name":"Net","slug":"Bad_Slug"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "saui-09 test")
	rec := httptest.NewRecorder()
	s.handleCreateOperatorNetwork(rec, req)
	// Lowercased to "bad_slug", underscore still invalid.
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid slug shape, got %d", rec.Code)
	}
	if code := errorCode(t, orgRespJSON(t, rec)); code != "operator_network.invalid_slug" {
		t.Errorf("error.code = %q want operator_network.invalid_slug", code)
	}
}

func TestOperatorNetwork208_List_NilQueriesReturns503(t *testing.T) {
	s := &Server{cfg: &config.Config{DefaultLocale: "en"}}
	req := httptest.NewRequest(http.MethodGet, "/v1/operator-networks", nil)
	rec := httptest.NewRecorder()
	s.handleListOperatorNetworks(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestOperatorNetwork208_Get_InvalidUUIDReturns400(t *testing.T) {
	s := &Server{
		cfg:            &config.Config{DefaultLocale: "en"},
		networkQueries: gen.New(nil),
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/operator-networks/not-a-uuid", nil)
	rec := httptest.NewRecorder()
	s.handleGetOperatorNetwork(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid UUID, got %d", rec.Code)
	}
}

func TestOperatorNetwork208_Update_EmptyBodyReturns400(t *testing.T) {
	s := &Server{
		cfg:            &config.Config{DefaultLocale: "en"},
		networkQueries: gen.New(nil),
		pool:           &dbDownPool{},
	}
	req := httptest.NewRequest(http.MethodPatch,
		"/v1/operator-networks/"+uuid.New().String(), http.NoBody)
	rec := httptest.NewRecorder()
	s.handleUpdateOperatorNetwork(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestOperatorNetwork208_Update_NoChangesReturns400(t *testing.T) {
	s := &Server{
		cfg:            &config.Config{DefaultLocale: "en"},
		networkQueries: gen.New(nil),
		pool:           &dbDownPool{},
	}
	// Inject a chi route context so uuidPathParam("id") resolves.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", uuid.New().String())

	req := httptest.NewRequest(http.MethodPatch,
		"/v1/operator-networks/x", strings.NewReader(`{"name":"","slug":""}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "saui-09 test")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	s.handleUpdateOperatorNetwork(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty patch body, got %d (body=%s)",
			rec.Code, rec.Body.String())
	}
	if code := errorCode(t, orgRespJSON(t, rec)); code != "operator_network.no_changes" {
		t.Errorf("error.code = %q want operator_network.no_changes", code)
	}
}

func TestOperatorNetwork208_Archive_NilQueriesReturns503(t *testing.T) {
	s := &Server{cfg: &config.Config{DefaultLocale: "en"}}
	req := httptest.NewRequest(http.MethodPost,
		"/v1/operator-networks/"+uuid.New().String()+"/archive", nil)
	rec := httptest.NewRecorder()
	s.handleArchiveOperatorNetwork(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Mount-file content checks — guard against permission strings drifting away
// from the bindings asserted by 0044_network_permissions.sql (feature #206).
// ─────────────────────────────────────────────────────────────────────────────

func TestOperatorNetwork208_MountFileUsesCorrectPermissions(t *testing.T) {
	body := readSourceFile(t, "mount_networks.go")
	for _, want := range []string{
		`"network.read"`,
		`"network.create"`,
		`"network.update"`,
		`"network.archive"`,
		`"/operator-networks"`,
		`"/operator-networks/{id}"`,
		`"/operator-networks/{id}/archive"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mount_networks.go missing %q", want)
		}
	}
}

// readSourceFile resolves a path relative to this test file's directory.
func readSourceFile(t *testing.T, name string) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(here)
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// network_users_209_test.go — unit tests for feature #209
// (network user assignment endpoints under /v1/admin/networks/{id}/users).
//
// Covers:
//   - Route mounting and auth gating for the three endpoints
//     (POST assign, GET list, DELETE remove).
//   - Handler input validation: nil-queries / nil-pool 503 short-circuit,
//     empty body / invalid JSON / missing user_id / malformed user_id 400s,
//     invalid path-UUID 400s.
//   - Mount-file content check: the new admin/networks/{id}/users patterns
//     and the `network.manage_users` permission string are present so the
//     binding pattern in 0044_network_permissions.sql remains the effective
//     gate.
package httpserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// chiPathRequest builds an httptest.NewRequest with a chi.RouteContext
// pre-populated so handlers calling uuidPathParam(...) resolve the URL
// parameters without going through the router.
func chiPathRequest(method, target string, body io.Reader, params map[string]string) *http.Request {
	req := httptest.NewRequest(method, target, body)
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func buildNetworkUserServer209(t *testing.T) *Server {
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

func TestNetworkUsers209_RoutesRequireAuth(t *testing.T) {
	netID := uuid.New().String()
	userID := uuid.New().String()
	cases := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/v1/admin/networks/" + netID + "/users", ""},
		{http.MethodPost, "/v1/admin/networks/" + netID + "/users",
			`{"user_id":"` + userID + `"}`},
		{http.MethodDelete, "/v1/admin/networks/" + netID + "/users/" + userID, ""},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			s := buildNetworkUserServer209(t)
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
// Handler validation — call handlers directly to bypass middleware
// ─────────────────────────────────────────────────────────────────────────────

func TestNetworkUsers209_Assign_NilQueriesReturns503(t *testing.T) {
	s := &Server{cfg: &config.Config{DefaultLocale: "en"}}
	req := httptest.NewRequest(http.MethodPost,
		"/v1/admin/networks/"+uuid.New().String()+"/users",
		strings.NewReader(`{"user_id":"`+uuid.New().String()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleAssignNetworkUser(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestNetworkUsers209_Assign_EmptyBodyReturns400(t *testing.T) {
	s := &Server{
		cfg:            &config.Config{DefaultLocale: "en"},
		networkQueries: gen.New(nil),
		pool:           &dbDownPool{},
	}
	// Inject a chi route context so uuidPathParam("id") resolves.
	req := chiPathRequest(http.MethodPost,
		"/v1/admin/networks/x/users", http.NoBody,
		map[string]string{"id": uuid.New().String()})
	req.Header.Set("X-Admin-Reason", "saui-09 test")
	rec := httptest.NewRecorder()
	s.handleAssignNetworkUser(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, orgRespJSON(t, rec)); code != "network_user.empty_body" {
		t.Errorf("error.code = %q want network_user.empty_body", code)
	}
}

func TestNetworkUsers209_Assign_InvalidJSONReturns400(t *testing.T) {
	s := &Server{
		cfg:            &config.Config{DefaultLocale: "en"},
		networkQueries: gen.New(nil),
		pool:           &dbDownPool{},
	}
	req := chiPathRequest(http.MethodPost,
		"/v1/admin/networks/x/users",
		strings.NewReader("not-json"),
		map[string]string{"id": uuid.New().String()})
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "saui-09 test")
	rec := httptest.NewRecorder()
	s.handleAssignNetworkUser(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
	if code := errorCode(t, orgRespJSON(t, rec)); code != "network_user.invalid_json" {
		t.Errorf("error.code = %q want network_user.invalid_json", code)
	}
}

func TestNetworkUsers209_Assign_MissingUserIDReturns400(t *testing.T) {
	s := &Server{
		cfg:            &config.Config{DefaultLocale: "en"},
		networkQueries: gen.New(nil),
		pool:           &dbDownPool{},
	}
	req := chiPathRequest(http.MethodPost,
		"/v1/admin/networks/x/users",
		strings.NewReader(`{"user_id":""}`),
		map[string]string{"id": uuid.New().String()})
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "saui-09 test")
	rec := httptest.NewRecorder()
	s.handleAssignNetworkUser(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
	if code := errorCode(t, orgRespJSON(t, rec)); code != "network_user.invalid_user_id" {
		t.Errorf("error.code = %q want network_user.invalid_user_id", code)
	}
}

func TestNetworkUsers209_Assign_MalformedUserIDReturns400(t *testing.T) {
	s := &Server{
		cfg:            &config.Config{DefaultLocale: "en"},
		networkQueries: gen.New(nil),
		pool:           &dbDownPool{},
	}
	req := chiPathRequest(http.MethodPost,
		"/v1/admin/networks/x/users",
		strings.NewReader(`{"user_id":"not-a-uuid"}`),
		map[string]string{"id": uuid.New().String()})
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "saui-09 test")
	rec := httptest.NewRecorder()
	s.handleAssignNetworkUser(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
	if code := errorCode(t, orgRespJSON(t, rec)); code != "network_user.invalid_user_id" {
		t.Errorf("error.code = %q want network_user.invalid_user_id", code)
	}
}

func TestNetworkUsers209_Assign_InvalidNetworkIDReturns400(t *testing.T) {
	s := &Server{
		cfg:            &config.Config{DefaultLocale: "en"},
		networkQueries: gen.New(nil),
		pool:           &dbDownPool{},
	}
	req := chiPathRequest(http.MethodPost,
		"/v1/admin/networks/x/users",
		strings.NewReader(`{"user_id":"`+uuid.New().String()+`"}`),
		map[string]string{"id": "not-a-uuid"})
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleAssignNetworkUser(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid path UUID, got %d", rec.Code)
	}
}

func TestNetworkUsers209_Remove_NilQueriesReturns503(t *testing.T) {
	s := &Server{cfg: &config.Config{DefaultLocale: "en"}}
	req := httptest.NewRequest(http.MethodDelete,
		"/v1/admin/networks/"+uuid.New().String()+"/users/"+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	s.handleRemoveNetworkUser(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestNetworkUsers209_Remove_InvalidUserIDReturns400(t *testing.T) {
	s := &Server{
		cfg:            &config.Config{DefaultLocale: "en"},
		networkQueries: gen.New(nil),
		pool:           &dbDownPool{},
	}
	req := chiPathRequest(http.MethodDelete,
		"/v1/admin/networks/x/users/y", nil,
		map[string]string{"id": uuid.New().String(), "userId": "not-a-uuid"})
	rec := httptest.NewRecorder()
	s.handleRemoveNetworkUser(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid userId, got %d", rec.Code)
	}
}

func TestNetworkUsers209_List_NilQueriesReturns503(t *testing.T) {
	s := &Server{cfg: &config.Config{DefaultLocale: "en"}}
	req := httptest.NewRequest(http.MethodGet,
		"/v1/admin/networks/"+uuid.New().String()+"/users", nil)
	rec := httptest.NewRecorder()
	s.handleListNetworkUsers(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestNetworkUsers209_List_InvalidNetworkIDReturns400(t *testing.T) {
	s := &Server{
		cfg:            &config.Config{DefaultLocale: "en"},
		networkQueries: gen.New(nil),
	}
	req := chiPathRequest(http.MethodGet,
		"/v1/admin/networks/x/users", nil,
		map[string]string{"id": "not-a-uuid"})
	rec := httptest.NewRecorder()
	s.handleListNetworkUsers(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid path UUID, got %d", rec.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Mount-file content check — guards permissions and route patterns against
// silent drift from the bindings in 0044_network_permissions.sql.
// ─────────────────────────────────────────────────────────────────────────────

func TestNetworkUsers209_MountFileUsesCorrectPermissions(t *testing.T) {
	body := readSourceFile(t, "mount_networks.go")
	for _, want := range []string{
		`"network.manage_users"`,
		`"/admin/networks/{id}/users"`,
		`"/admin/networks/{id}/users/{userId}"`,
		`handleAssignNetworkUser`,
		`handleListNetworkUsers`,
		`handleRemoveNetworkUser`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mount_networks.go missing %q", want)
		}
	}
}

package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestAdminUsers_CreateUserRouteMounted(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/users", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "test")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatalf("POST /v1/admin/users returned 404 - route not mounted")
	}
	if rec.Code == http.StatusMethodNotAllowed {
		t.Fatalf("POST /v1/admin/users returned 405 - method not registered")
	}
}

func TestAdminUsers_CreateUserRequiresAuth(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/users",
		strings.NewReader(`{"email":"new@example.com","role":"platform_operator"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "test")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without JWT, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestAdminUsers_CreateUserRequiresAdminReason(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	token := mintMembershipToken(t, s, "admin")
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/users",
		strings.NewReader(`{"email":"new@example.com","role":"platform_operator"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without X-Admin-Reason, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "superadmin.missing_reason") {
		t.Fatalf("expected superadmin.missing_reason, body=%s", rec.Body.String())
	}
}

func TestAdminUsers_CreateUserValidation(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	token := mintMembershipToken(t, s, "admin")

	cases := []struct {
		name string
		body string
		code string
	}{
		{
			name: "invalid email",
			body: `{"email":"not-an-email","role":"platform_operator"}`,
			code: "admin_user.invalid_email",
		},
		{
			name: "invalid role",
			body: `{"email":"new@example.com","role":"admin"}`,
			code: "admin_user.invalid_role",
		},
		{
			name: "org scoped role requires org",
			body: `{"email":"new@example.com","role":"organizer"}`,
			code: "admin_user.missing_org_id",
		},
		{
			name: "global role rejects org",
			body: `{"email":"new@example.com","role":"platform_operator","org_id":"` + uuid.New().String() + `"}`,
			code: "admin_user.org_not_allowed",
		},
		{
			name: "bad org uuid",
			body: `{"email":"new@example.com","role":"agent","org_id":"not-a-uuid"}`,
			code: "admin_user.invalid_org_id",
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/v1/admin/users", strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("X-Admin-Reason", "test")
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (body=%s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), c.code) {
				t.Fatalf("expected %s, body=%s", c.code, rec.Body.String())
			}
		})
	}
}

func TestAdminUsers_CreateUserValidGlobalRoleReachesDB(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	token := mintMembershipToken(t, s, "admin")
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/users",
		strings.NewReader(`{"email":"new@example.com","role":"platform_operator"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Admin-Reason", "test")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected dbDownPool 503 after validation passed, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestAdminUsers_RouteRequiresSuperadminRead(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, `s.applyAuth(pr, "superadmin.read", "users")`) {
		t.Fatalf("POST /v1/admin/users must be gated by superadmin.read")
	}
	if strings.Contains(content, `s.applyAuth(pr, "membership.grant", "users")`) {
		t.Fatalf("POST /v1/admin/users must not be gated only by membership.grant")
	}
}

func TestAdminUsers_RoleResolverIncludesGlobalUserRoles(t *testing.T) {
	t.Parallel()
	sql := findFileByName(t, "memberships.sql")
	for _, want := range []string{
		"FROM   user_roles ur",
		"JOIN   roles r ON r.id = ur.role_id",
		"ur.org_id IS NULL",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("memberships.sql missing %q", want)
		}
	}

	gen := findFileByName(t, "memberships.sql.go")
	if !strings.Contains(gen, "FROM   user_roles ur") {
		t.Fatalf("generated memberships.sql.go must mirror user_roles role resolver")
	}
}

func TestAdminUsers_MigrationGrantsProvisioningToPlatformSuperadmin(t *testing.T) {
	t.Parallel()
	migration := findFileByName(t, "0047_admin_user_provisioning.sql")
	for _, want := range []string{
		"platform_superadmin",
		"membership.grant",
		"INSERT INTO role_permissions",
	} {
		if !strings.Contains(migration, want) {
			t.Fatalf("0047 migration missing %q", want)
		}
	}
}

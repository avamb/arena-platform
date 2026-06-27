// admin_memberships_test.go — unit tests for feature #234 (Admin namespace
// organization-memberships endpoints).
//
// Coverage:
//   - SQL file presence and shape (queries + sqlc gen file)
//   - Route mounting (no 404) for all four routes
//   - Auth gating (401 without JWT)
//   - X-Admin-Reason header gating (400 without header, even with JWT)
//   - Nil-dependency guards return 503
//   - Body / role / user_id validation at the handler layer
//
// Live PostgreSQL is not required — dbDownPool is used to satisfy the pool
// guard so the routes mount, and validation errors are checked before the
// DB layer is touched.
package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// mintMembershipToken issues a stub-provider JWT carrying the supplied roles
// so admin_memberships routes pass the JWT + RBAC gate. The membership server
// always uses the test-secret-which-is-long-enough-for-hs256 secret.
func mintMembershipToken(t *testing.T, _ *Server, roles ...string) string {
	t.Helper()
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  "test-secret-which-is-long-enough-for-hs256",
		Issuer:  "arena-test",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("mintMembershipToken: NewStubProvider: %v", err)
	}
	tok, _, err := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID: uuid.New().String(),
		Roles:   roles,
	})
	if err != nil {
		t.Fatalf("mintMembershipToken: IssueToken: %v", err)
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// SQL surface
// ─────────────────────────────────────────────────────────────────────────────

func TestAdminMembership234_QueriesSQLHasNewQueries(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "memberships.sql")
	for _, want := range []string{
		"-- name: GetMembershipByID :one",
		"-- name: ChangeMembershipRole :one",
		"-- name: DeactivateMembership :one",
		"SET    status = 'revoked'",
		"SET    role = $3",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("memberships.sql: expected fragment %q but not found", want)
		}
	}
}

func TestAdminMembership234_GenFileHasNewFunctions(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "memberships.sql.go")
	for _, want := range []string{
		"func (q *Queries) GetMembershipByID",
		"func (q *Queries) ChangeMembershipRole",
		"func (q *Queries) DeactivateMembership",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("memberships.sql.go: expected fragment %q but not found", want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Route mounting
// ─────────────────────────────────────────────────────────────────────────────

func TestAdminMembership234_RoutesMounted(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	orgID := uuid.New().String()
	memberID := uuid.New().String()
	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/admin/organizations/" + orgID + "/members"},
		{http.MethodPost, "/v1/admin/organizations/" + orgID + "/members"},
		{http.MethodPatch, "/v1/admin/organizations/" + orgID + "/members/" + memberID},
		{http.MethodDelete, "/v1/admin/organizations/" + orgID + "/members/" + memberID},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Admin-Reason", "test")
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Errorf("%s %s returned 404 — route not mounted", c.method, c.path)
		}
		if rec.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s %s returned 405 — method not registered", c.method, c.path)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Auth gating (401 without JWT)
// ─────────────────────────────────────────────────────────────────────────────

func TestAdminMembership234_RoutesRequireAuth(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	orgID := uuid.New().String()
	memberID := uuid.New().String()
	cases := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/v1/admin/organizations/" + orgID + "/members", ""},
		{http.MethodPost, "/v1/admin/organizations/" + orgID + "/members",
			`{"user_id":"` + uuid.New().String() + `","role":"organizer"}`},
		{http.MethodPatch, "/v1/admin/organizations/" + orgID + "/members/" + memberID,
			`{"role":"agent"}`},
		{http.MethodDelete, "/v1/admin/organizations/" + orgID + "/members/" + memberID, ""},
	}
	for _, c := range cases {
		var body *strings.Reader
		if c.body != "" {
			body = strings.NewReader(c.body)
		} else {
			body = strings.NewReader("")
		}
		req := httptest.NewRequest(c.method, c.path, body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Admin-Reason", "test")
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: expected 401 without JWT, got %d (body=%s)",
				c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Nil-dep guards return 503
// ─────────────────────────────────────────────────────────────────────────────

func TestAdminMembership234_NilQueriesReturns503(t *testing.T) {
	t.Parallel()
	s := &Server{}

	t.Run("list", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet,
			"/v1/admin/organizations/"+uuid.New().String()+"/members", nil)
		req.Header.Set("X-Admin-Reason", "test")
		rec := httptest.NewRecorder()
		s.handleAdminListMembers(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", rec.Code)
		}
	})

	t.Run("add", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost,
			"/v1/admin/organizations/"+uuid.New().String()+"/members",
			strings.NewReader(`{"user_id":"x","role":"organizer"}`))
		req.Header.Set("X-Admin-Reason", "test")
		rec := httptest.NewRecorder()
		s.handleAdminAddMember(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", rec.Code)
		}
	})

	t.Run("change_role", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPatch,
			"/v1/admin/organizations/"+uuid.New().String()+"/members/"+uuid.New().String(),
			strings.NewReader(`{"role":"agent"}`))
		req.Header.Set("X-Admin-Reason", "test")
		rec := httptest.NewRecorder()
		s.handleAdminChangeMemberRole(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", rec.Code)
		}
	})

	t.Run("deactivate", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete,
			"/v1/admin/organizations/"+uuid.New().String()+"/members/"+uuid.New().String(), nil)
		req.Header.Set("X-Admin-Reason", "test")
		rec := httptest.NewRecorder()
		s.handleAdminDeactivateMember(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", rec.Code)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// X-Admin-Reason gating (after authenticating to bypass JWT)
// ─────────────────────────────────────────────────────────────────────────────

func TestAdminMembership234_RoutesRequireAdminReason(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	token := mintMembershipToken(t, s, "admin")
	orgID := uuid.New().String()
	memberID := uuid.New().String()
	cases := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/v1/admin/organizations/" + orgID + "/members", ""},
		{http.MethodPost, "/v1/admin/organizations/" + orgID + "/members",
			`{"user_id":"` + uuid.New().String() + `","role":"organizer"}`},
		{http.MethodPatch, "/v1/admin/organizations/" + orgID + "/members/" + memberID,
			`{"role":"agent"}`},
		{http.MethodDelete, "/v1/admin/organizations/" + orgID + "/members/" + memberID, ""},
	}
	for _, c := range cases {
		var body *strings.Reader
		if c.body != "" {
			body = strings.NewReader(c.body)
		} else {
			body = strings.NewReader("")
		}
		req := httptest.NewRequest(c.method, c.path, body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		// Note: NO X-Admin-Reason header.
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s: expected 400 without X-Admin-Reason, got %d (body=%s)",
				c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Body / role / user_id validation
// ─────────────────────────────────────────────────────────────────────────────

func TestAdminMembership234_AddMember_EmptyBodyIs400(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	token := mintMembershipToken(t, s, "admin")
	req := httptest.NewRequest(http.MethodPost,
		"/v1/admin/organizations/"+uuid.New().String()+"/members",
		strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "test")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 with empty body, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestAdminMembership234_AddMember_MissingUserAndEmailIs400(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	token := mintMembershipToken(t, s, "admin")
	req := httptest.NewRequest(http.MethodPost,
		"/v1/admin/organizations/"+uuid.New().String()+"/members",
		strings.NewReader(`{"role":"organizer"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "test")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 with neither user_id nor email, got %d (body=%s)",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "admin_membership.missing_user") {
		t.Errorf("expected admin_membership.missing_user error, body=%s", rec.Body.String())
	}
}

func TestAdminMembership234_AddMember_BothUserAndEmailIs400(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	token := mintMembershipToken(t, s, "admin")
	req := httptest.NewRequest(http.MethodPost,
		"/v1/admin/organizations/"+uuid.New().String()+"/members",
		strings.NewReader(`{"user_id":"`+uuid.New().String()+`","email":"x@example.com","role":"organizer"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "test")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 with both user_id and email, got %d (body=%s)",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "admin_membership.ambiguous_user") {
		t.Errorf("expected admin_membership.ambiguous_user error, body=%s", rec.Body.String())
	}
}

func TestAdminMembership234_AddMember_InvalidRoleIs400(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	token := mintMembershipToken(t, s, "admin")
	req := httptest.NewRequest(http.MethodPost,
		"/v1/admin/organizations/"+uuid.New().String()+"/members",
		strings.NewReader(`{"user_id":"`+uuid.New().String()+`","role":"nope"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "test")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 with invalid role, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "admin_membership.invalid_role") {
		t.Errorf("expected admin_membership.invalid_role error, body=%s", rec.Body.String())
	}
}

func TestAdminMembership234_AddMember_InvalidUserUUIDIs400(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	token := mintMembershipToken(t, s, "admin")
	req := httptest.NewRequest(http.MethodPost,
		"/v1/admin/organizations/"+uuid.New().String()+"/members",
		strings.NewReader(`{"user_id":"not-a-uuid","role":"organizer"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "test")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 with bad user_id, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "admin_membership.invalid_user_id") {
		t.Errorf("expected admin_membership.invalid_user_id error, body=%s", rec.Body.String())
	}
}

func TestAdminMembership234_ChangeRole_EmptyBodyIs400(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	token := mintMembershipToken(t, s, "admin")
	req := httptest.NewRequest(http.MethodPatch,
		"/v1/admin/organizations/"+uuid.New().String()+"/members/"+uuid.New().String(),
		strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "test")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 with empty body, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestAdminMembership234_ChangeRole_InvalidRoleIs400(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	token := mintMembershipToken(t, s, "admin")
	req := httptest.NewRequest(http.MethodPatch,
		"/v1/admin/organizations/"+uuid.New().String()+"/members/"+uuid.New().String(),
		strings.NewReader(`{"role":"badrole"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "test")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 with invalid role, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "admin_membership.invalid_role") {
		t.Errorf("expected admin_membership.invalid_role, body=%s", rec.Body.String())
	}
}

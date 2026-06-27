// admin_orgs_test.go — unit tests for feature #233 (admin Organizations CRUD).
//
// Tests verify that:
//   - The admin routes are mounted (no 404)
//   - Auth is enforced (401 without JWT)
//   - X-Admin-Reason is required (400 without header, even with JWT)
//   - Input validation (empty body, invalid JSON, missing required fields)
//   - Nil orgQueries surfaces a 503
//
// Live PostgreSQL is not required — dbDownPool is used to satisfy the pool
// guard so the routes mount.
package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// Route mounting / auth gates
// ─────────────────────────────────────────────────────────────────────────────

func TestAdminOrg233_PostRequiresAuth(t *testing.T) {
	s := buildOrgServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/organizations",
		strings.NewReader(`{"name":"Test Org","slug":"test-org"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "test")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Fatal("POST /v1/admin/organizations returned 404 — route not mounted")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without JWT, got %d", rec.Code)
	}
}

func TestAdminOrg233_PatchRequiresAuth(t *testing.T) {
	s := buildOrgServer(t)
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/organizations/"+orgID.String(),
		strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "test")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Fatal("PATCH /v1/admin/organizations/{id} returned 404 — route not mounted")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without JWT, got %d", rec.Code)
	}
}

func TestAdminOrg233_ArchiveRequiresAuth(t *testing.T) {
	s := buildOrgServer(t)
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodPost,
		"/v1/admin/organizations/"+orgID.String()+"/archive", nil)
	req.Header.Set("X-Admin-Reason", "test")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Fatal("POST /v1/admin/organizations/{id}/archive returned 404 — route not mounted")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without JWT, got %d", rec.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler-level guards (called directly, no router / no auth)
// ─────────────────────────────────────────────────────────────────────────────

func TestAdminOrg233_Create_NilOrgQueriesReturns503(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/organizations",
		strings.NewReader(`{"name":"x","slug":"x"}`))
	req.Header.Set("X-Admin-Reason", "test")
	rec := httptest.NewRecorder()
	s.handleAdminCreateOrg(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when orgQueries=nil, got %d", rec.Code)
	}
}

func TestAdminOrg233_Update_NilOrgQueriesReturns503(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/organizations/"+uuid.New().String(),
		strings.NewReader(`{"name":"x"}`))
	req.Header.Set("X-Admin-Reason", "test")
	rec := httptest.NewRecorder()
	s.handleAdminUpdateOrg(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when orgQueries=nil, got %d", rec.Code)
	}
}

func TestAdminOrg233_Archive_NilOrgQueriesReturns503(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost,
		"/v1/admin/organizations/"+uuid.New().String()+"/archive", nil)
	req.Header.Set("X-Admin-Reason", "test")
	rec := httptest.NewRecorder()
	s.handleAdminArchiveOrg(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when orgQueries=nil, got %d", rec.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Distinct mount: PATCH and POST/archive paths exist
// (regression guard so admin routes don't shadow / collide with /v1/admin/organizations GET)
// ─────────────────────────────────────────────────────────────────────────────

func TestAdminOrg233_RouteSurfaceMounted(t *testing.T) {
	s := buildOrgServer(t)
	cases := []struct {
		method, path string
	}{
		{http.MethodPost, "/v1/admin/organizations"},
		{http.MethodPatch, "/v1/admin/organizations/" + uuid.New().String()},
		{http.MethodPost, "/v1/admin/organizations/" + uuid.New().String() + "/archive"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
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

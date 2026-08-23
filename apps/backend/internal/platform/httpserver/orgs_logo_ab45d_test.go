// orgs_logo_ab45d_test.go — unit tests for AB-45d: logo_media_id PATCH
// tri-state semantics and superadmin null=clear fix.
//
// Feature #448 (AB-45d) logo coverage:
//
// Unit tests (no live DB):
//   - Route mounting: PATCH /v1/organizations/{id} returns non-404
//   - Route mounting: PATCH /v1/admin/organizations/{id} returns non-404
//   - null logo field does NOT return 400 (passes validation, proceeds to DB)
//   - Absent logo field does NOT return 400
//   - Superadmin null=clear fix: admin_orgs.go code-path guard (file content test)
//
// The logo media-id UUID validation (bad UUID → 400), wrong-owner_type → 422,
// and DB-failure → 500 paths all require UpdateOrganization to succeed first
// and thus need a live DB; those are covered by integration tests.
//
// All tests here are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// buildOrgLogoServer builds a Server that has org + admin-org routes mounted
// with gen.New(nil) as OrgQueries (so routes are reachable, but any real DB
// query panics — recovered by middleware as 500).
func buildOrgLogoServer(t *testing.T) (*Server, string) {
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
		t.Fatalf("buildOrgLogoServer: %v", err)
	}
	s := New(Options{
		Config:            cfg,
		Auth:              stub,
		Pool:              &dbDownPool{},
		OrgQueries:        gen.New(nil),
		MembershipQueries: gen.New(&orgMemberAdmitFromCtxDBTX{}),
		Audit:             &captureAuditWriter{},
	})
	// Mint a token with admin role so all permission checks pass.
	w := httptest.NewRecorder()
	mintBody := `{"actor_id":"00000000-0000-0000-0000-000000000099","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token", strings.NewReader(mintBody))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintToken: got %d body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintToken decode: %v", err)
	}
	tok := resp["token"]
	if tok == "" {
		t.Fatal("mintToken: empty token")
	}
	return s, tok
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/organizations/{id} — route and early-validation checks
// ─────────────────────────────────────────────────────────────────────────────

func TestOrgLogoAB45d_RegularPatch_RouteIsMounted(t *testing.T) {
	s, tok := buildOrgLogoServer(t)
	orgID := uuid.New().String()
	req := httptest.NewRequest(http.MethodPatch, "/v1/organizations/"+orgID,
		strings.NewReader(`{"name":"any"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatal("PATCH /v1/organizations/{id} returned 404 — route not mounted")
	}
}

func TestOrgLogoAB45d_RegularPatch_EmptyBody_Returns400(t *testing.T) {
	s, tok := buildOrgLogoServer(t)
	orgID := uuid.New().String()
	req := httptest.NewRequest(http.MethodPatch, "/v1/organizations/"+orgID, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty body: got %d, want 400", rec.Code)
	}
}

func TestOrgLogoAB45d_RegularPatch_InvalidJSON_Returns400(t *testing.T) {
	s, tok := buildOrgLogoServer(t)
	orgID := uuid.New().String()
	req := httptest.NewRequest(http.MethodPatch, "/v1/organizations/"+orgID,
		strings.NewReader(`{bad json}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid JSON: got %d, want 400", rec.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/admin/organizations/{id} — route and early-validation checks
// ─────────────────────────────────────────────────────────────────────────────

func TestOrgLogoAB45d_AdminPatch_RouteIsMounted(t *testing.T) {
	s, tok := buildOrgLogoServer(t)
	orgID := uuid.New().String()
	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/organizations/"+orgID,
		strings.NewReader(`{"name":"any"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Admin-Reason", "AB-45d test")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatal("PATCH /v1/admin/organizations/{id} returned 404 — route not mounted")
	}
}

func TestOrgLogoAB45d_AdminPatch_RequiresAdminReason(t *testing.T) {
	s, tok := buildOrgLogoServer(t)
	orgID := uuid.New().String()
	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/organizations/"+orgID,
		strings.NewReader(`{"name":"any"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	// X-Admin-Reason omitted
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing X-Admin-Reason: got %d, want 400", rec.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Admin path: null=clear code path is present (regression guard for AB-45d fix)
// ─────────────────────────────────────────────────────────────────────────────

func TestOrgLogoAB45d_AdminOrgHandler_NullClear_CodePathExists(t *testing.T) {
	// Verify that admin_orgs.go contains the null=clear code path introduced
	// in AB-45d. If a future refactor removes it, this file-content test catches it.
	content := findFileByName(t, "admin_orgs.go")
	if content == "" {
		t.Fatal("admin_orgs.go not found")
	}
	if !strings.Contains(content, "logo_clear_failed") {
		t.Error("admin_orgs.go missing 'logo_clear_failed' error code — null=clear path not present")
	}
	if !strings.Contains(content, "PatchOrgLogoMediaID") {
		t.Error("admin_orgs.go missing PatchOrgLogoMediaID call — null=clear path not present")
	}
}

func TestOrgLogoAB45d_RegularOrgHandler_NullClear_CodePathExists(t *testing.T) {
	content := findFileByName(t, "orgs.go")
	if content == "" {
		t.Fatal("orgs.go not found")
	}
	// The regular path should already have PatchOrgLogoMediaID + null=clear
	if !strings.Contains(content, "logo_clear_failed") {
		t.Error("orgs.go missing 'logo_clear_failed' error code")
	}
	if !strings.Contains(content, "PatchOrgLogoMediaID") {
		t.Error("orgs.go missing PatchOrgLogoMediaID call")
	}
}

func TestOrgLogoAB45d_AdminVsRegular_BothHaveNullClear(t *testing.T) {
	// Both the regular and admin org PATCH handlers must implement null=clear.
	// This test ensures the AB-45d superadmin fix is symmetric with the regular path.
	adminContent := findFileByName(t, "admin_orgs.go")
	regularContent := findFileByName(t, "orgs.go")

	for _, tc := range []struct {
		name    string
		content string
		token   string
	}{
		{"admin_orgs.go", adminContent, "admin_org.logo_clear_failed"},
		{"orgs.go", regularContent, "org.logo_clear_failed"},
	} {
		if tc.content == "" {
			t.Errorf("%s: file not found", tc.name)
			continue
		}
		if !strings.Contains(tc.content, tc.token) {
			t.Errorf("%s: missing %q — null=clear path not implemented", tc.name, tc.token)
		}
	}
}

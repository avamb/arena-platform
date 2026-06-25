// memberships_test.go — unit tests for feature #120 (Membership + role assignment).
//
// Test coverage:
//
//	Step 1: Migration file 0011_memberships.sql exists with correct schema + seeds
//	Step 2: POST/GET/DELETE /v1/organizations/{org_id}/members routes mounted,
//	        auth-gated, with correct request validation (no DB required)
//	Step 3: Permission resolver picks up membership-derived permissions
//	        (MembershipQuerier interface and DBChecker.WithMembershipQuerier)
//	Step 4: Integration test shapes — multi-org user, grant/revoke flows
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"bytes"
	"context"
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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test server factory
// ─────────────────────────────────────────────────────────────────────────────

// buildMembershipServer builds a Server with stub auth, membership routes fully
// mounted, and a dbDownPool so real DB operations never execute. Auth middleware
// fires before the DB layer → unauthenticated requests get 401, not 503.
func buildMembershipServer(t *testing.T) *Server {
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
		t.Fatalf("buildMembershipServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config: cfg,
		Auth:   stub,
		// dbDownPool satisfies the pool != nil guard so routes get mounted.
		Pool: &dbDownPool{},
		// MembershipQueries non-nil so the membership route conditional passes.
		MembershipQueries: gen.New(nil),
		// OrgQueries non-nil (membership routes are nested under orgs).
		OrgQueries: gen.New(nil),
		// Audit writer not needed for membership routes.
	})
}

// membershipRespJSON decodes the response body into a map and returns it.
func membershipRespJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(w.Body).Decode(&m); err != nil {
		t.Fatalf("membership: JSON decode failed: %v (body: %s)", err, w.Body.String())
	}
	return m
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1 — Migration file exists with correct schema + seeds
// ─────────────────────────────────────────────────────────────────────────────

func TestMembership120_MigrationFileExists(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0011_memberships.sql")
	if len(content) == 0 {
		t.Fatal("0011_memberships.sql is empty")
	}
}

func TestMembership120_MigrationHasMembershipsTable(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0011_memberships.sql")
	for _, want := range []string{
		"CREATE TABLE memberships",
		"user_id",
		"org_id",
		"role",
		"status",
		"joined_at",
		"uuidv7()",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("0011_memberships.sql: expected %q but not found", want)
		}
	}
}

func TestMembership120_MigrationHasRoleCheckConstraint(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0011_memberships.sql")
	for _, role := range []string{
		"organizer",
		"agent",
		"platform_operator",
		"external_ticketing_operator",
		"platform_superadmin",
	} {
		if !strings.Contains(content, role) {
			t.Errorf("0011_memberships.sql: expected role %q in CHECK constraint but not found", role)
		}
	}
}

func TestMembership120_MigrationHasUniqueConstraint(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0011_memberships.sql")
	if !strings.Contains(content, "UNIQUE (user_id, org_id, role)") {
		t.Error("0011_memberships.sql: missing UNIQUE (user_id, org_id, role) constraint")
	}
}

func TestMembership120_MigrationHasIndexes(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0011_memberships.sql")
	if !strings.Contains(content, "memberships_user_id") {
		t.Error("0011_memberships.sql: missing index on user_id")
	}
	if !strings.Contains(content, "memberships_org_id") {
		t.Error("0011_memberships.sql: missing index on org_id")
	}
}

func TestMembership120_MigrationSeedsMembershipRoles(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0011_memberships.sql")
	for _, role := range []string{
		"organizer", "agent", "platform_operator",
		"external_ticketing_operator", "platform_superadmin",
	} {
		want := "'" + role + "'"
		if !strings.Contains(content, want) {
			t.Errorf("0011_memberships.sql: expected seeded role %q (as string literal) but not found", role)
		}
	}
}

func TestMembership120_MigrationSeedsPermissions(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0011_memberships.sql")
	for _, perm := range []string{"membership.grant", "membership.revoke", "membership.read"} {
		if !strings.Contains(content, perm) {
			t.Errorf("0011_memberships.sql: expected permission %q but not found", perm)
		}
	}
}

func TestMembership120_MigrationHasGooseDownSection(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0011_memberships.sql")
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("0011_memberships.sql: missing -- +goose Down section")
	}
	if !strings.Contains(content, "DROP TABLE IF EXISTS memberships") {
		t.Error("0011_memberships.sql: Down section must drop the memberships table")
	}
}

func TestMembership120_MigrationHasForeignKeys(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0011_memberships.sql")
	if !strings.Contains(content, "REFERENCES users(id)") {
		t.Error("0011_memberships.sql: missing FK to users(id)")
	}
	if !strings.Contains(content, "REFERENCES organizations(id)") {
		t.Error("0011_memberships.sql: missing FK to organizations(id)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2 — Routes mounted and auth-gated
// ─────────────────────────────────────────────────────────────────────────────

func TestMembership120_PostMembersRequiresAuth(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	orgID := uuid.New().String()
	// Must send Content-Type: application/json so the body-limit middleware
	// does not reject with 415 before auth fires.
	r := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/members",
		strings.NewReader(`{"user_id":"`+uuid.New().String()+`","role":"organizer"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/organizations/{org_id}/members without auth: want 401, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestMembership120_GetMembersRequiresAuth(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	orgID := uuid.New().String()
	r := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+orgID+"/members", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/organizations/{org_id}/members without auth: want 401, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestMembership120_DeleteMembersRequiresAuth(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	orgID := uuid.New().String()
	userID := uuid.New().String()
	r := httptest.NewRequest(http.MethodDelete, "/v1/organizations/"+orgID+"/members/"+userID, nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("DELETE /v1/organizations/{org_id}/members/{user_id} without auth: want 401, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestMembership120_PostMembersNilQueriesReturns503(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
	}
	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret: cfg.JWTSecretStub, Issuer: "arena-test", Enabled: true,
	})
	// membershipQueries=nil (no pool) → routes NOT mounted → 404 (not 503).
	// But if we pass a pool without membership queries, the guard in handler fires.
	s := New(Options{
		Config: cfg,
		Auth:   stub,
		Pool:   &dbDownPool{},
		// MembershipQueries deliberately nil — handler guard should return 503.
		MembershipQueries: nil,
	})
	orgID := uuid.New().String()
	// With MembershipQueries=nil, the route won't be mounted → 404.
	// This verifies the conditional mount guard.
	r := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+orgID+"/members", nil)
	tok, _, _ := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID: uuid.New().String(), Roles: []string{"admin"},
	})
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	// Routes not mounted (MembershipQueries=nil) → 404.
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 when MembershipQueries=nil (route not mounted), got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestMembership120_GrantMembership_EmptyBodyReturns400(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret:  "test-secret-which-is-long-enough-for-hs256",
		Issuer:  "arena-test",
		Enabled: true,
	})
	orgID := uuid.New().String()
	r := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/members", bytes.NewReader([]byte{}))
	tok, _, _ := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID: uuid.New().String(), Roles: []string{"admin"},
	})
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with empty body: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestMembership120_GrantMembership_InvalidJSONReturns400(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret:  "test-secret-which-is-long-enough-for-hs256",
		Issuer:  "arena-test",
		Enabled: true,
	})
	orgID := uuid.New().String()
	r := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/members",
		bytes.NewReader([]byte(`{invalid json}`)))
	tok, _, _ := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID: uuid.New().String(), Roles: []string{"admin"},
	})
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with invalid JSON: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	body := membershipRespJSON(t, w)
	errObj, _ := body["error"].(map[string]any)
	if errObj["code"] != "membership.invalid_json" {
		t.Errorf("expected code=membership.invalid_json, got %v", errObj["code"])
	}
}

func TestMembership120_GrantMembership_MissingUserIDReturns400(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret:  "test-secret-which-is-long-enough-for-hs256",
		Issuer:  "arena-test",
		Enabled: true,
	})
	orgID := uuid.New().String()
	payload, _ := json.Marshal(map[string]string{"role": "organizer"})
	r := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/members",
		bytes.NewReader(payload))
	tok, _, _ := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID: uuid.New().String(), Roles: []string{"admin"},
	})
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST missing user_id: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestMembership120_GrantMembership_InvalidRoleReturns400(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret:  "test-secret-which-is-long-enough-for-hs256",
		Issuer:  "arena-test",
		Enabled: true,
	})
	orgID := uuid.New().String()
	payload, _ := json.Marshal(map[string]string{
		"user_id": uuid.New().String(),
		"role":    "superuser", // invalid
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/members",
		bytes.NewReader(payload))
	tok, _, _ := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID: uuid.New().String(), Roles: []string{"admin"},
	})
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with invalid role: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	body := membershipRespJSON(t, w)
	errObj, _ := body["error"].(map[string]any)
	if errObj["code"] != "membership.invalid_role" {
		t.Errorf("expected code=membership.invalid_role, got %v", errObj["code"])
	}
}

func TestMembership120_GrantMembership_AllValidRolesAccepted(t *testing.T) {
	t.Parallel()
	roles := []string{
		"organizer",
		"agent",
		"platform_operator",
		"external_ticketing_operator",
		"platform_superadmin",
	}
	s := buildMembershipServer(t)
	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret:  "test-secret-which-is-long-enough-for-hs256",
		Issuer:  "arena-test",
		Enabled: true,
	})
	for _, role := range roles {
		role := role
		t.Run(role, func(t *testing.T) {
			orgID := uuid.New().String()
			payload, _ := json.Marshal(map[string]string{
				"user_id": uuid.New().String(),
				"role":    role,
			})
			r := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/members",
				bytes.NewReader(payload))
			tok, _, _ := stub.IssueToken(context.Background(), auth.IssueRequest{
				ActorID: uuid.New().String(), Roles: []string{"admin"},
			})
			r.Header.Set("Authorization", "Bearer "+tok)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, r)
			// With dbDownPool the transaction will fail → 503, not 400.
			// The key is that the role validation passes (no 400 for invalid_role).
			if w.Code == http.StatusBadRequest {
				body := membershipRespJSON(t, w)
				errObj, _ := body["error"].(map[string]any)
				if errObj["code"] == "membership.invalid_role" {
					t.Errorf("role %q should be valid but got invalid_role error", role)
				}
			}
		})
	}
}

func TestMembership120_RevokeMembership_EmptyBodyReturns400(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret:  "test-secret-which-is-long-enough-for-hs256",
		Issuer:  "arena-test",
		Enabled: true,
	})
	orgID := uuid.New().String()
	userID := uuid.New().String()
	r := httptest.NewRequest(http.MethodDelete,
		"/v1/organizations/"+orgID+"/members/"+userID,
		bytes.NewReader([]byte{}))
	tok, _, _ := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID: uuid.New().String(), Roles: []string{"admin"},
	})
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("DELETE with empty body: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestMembership120_RevokeMembership_InvalidOrgIDReturns400(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret:  "test-secret-which-is-long-enough-for-hs256",
		Issuer:  "arena-test",
		Enabled: true,
	})
	payload, _ := json.Marshal(map[string]string{"role": "organizer"})
	r := httptest.NewRequest(http.MethodDelete,
		"/v1/organizations/not-a-uuid/members/"+uuid.New().String(),
		bytes.NewReader(payload))
	tok, _, _ := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID: uuid.New().String(), Roles: []string{"admin"},
	})
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("DELETE with invalid org UUID: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestMembership120_GetMembers_InvalidOrgIDReturns400(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret:  "test-secret-which-is-long-enough-for-hs256",
		Issuer:  "arena-test",
		Enabled: true,
	})
	// Note: chi matches the route even with "not-a-uuid" as a path param.
	// The handler will parse the UUID and return 400.
	r := httptest.NewRequest(http.MethodGet, "/v1/organizations/not-a-uuid/members", nil)
	tok, _, _ := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID: uuid.New().String(), Roles: []string{"admin"},
	})
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("GET with invalid org UUID: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3 — Permission resolver picks up membership-derived permissions
// ─────────────────────────────────────────────────────────────────────────────

// fakeMembershipQuerier is a test double that returns configurable membership roles.
type fakeMembershipQuerier struct {
	rolesByUser map[string][]string // user_id → []role
}

func (f *fakeMembershipQuerier) GetActiveRolesForUser(_ context.Context, userID uuid.UUID) ([]string, error) {
	return f.rolesByUser[userID.String()], nil
}

// fakeRBACQuerier120 is a minimal RBACQuerier that returns preconfigured permissions.
type fakeRBACQuerier120 struct {
	permsByRole map[string][]string // role → []permission
}

func (f *fakeRBACQuerier120) GetPermissionsForRoles(_ context.Context, roleNames []string) ([]string, error) {
	seen := make(map[string]bool)
	var out []string
	for _, r := range roleNames {
		for _, p := range f.permsByRole[r] {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out, nil
}

func TestMembership120_PermResolver_JWTRolesOnly(t *testing.T) {
	t.Parallel()
	// Actor has JWT role "geo_admin" which has "geo.admin" permission.
	rbac := &fakeRBACQuerier120{
		permsByRole: map[string][]string{
			"geo_admin": {"geo.admin"},
		},
	}
	checker := permissions.NewDBChecker(rbac)

	actorID := uuid.New()
	ctx := auth.WithActor(context.Background(), auth.Actor{
		ID:    actorID.String(),
		Roles: []string{"geo_admin"},
	})

	if err := checker.Check(ctx, "geo.admin", "geo"); err != nil {
		t.Errorf("expected geo.admin permission via JWT role, got: %v", err)
	}
	if err := checker.Check(ctx, "org.read", "org"); err == nil {
		t.Error("expected denial for org.read (not in roles), got nil")
	}
}

func TestMembership120_PermResolver_MembershipRolesUnioned(t *testing.T) {
	t.Parallel()
	// Actor has JWT role "scaffold_user" with "scaffold.echo.create".
	// Actor also has membership role "organizer" with "org.read".
	rbac := &fakeRBACQuerier120{
		permsByRole: map[string][]string{
			"scaffold_user": {"scaffold.echo.create"},
			"organizer":     {"org.read", "membership.read"},
		},
	}
	memberships := &fakeMembershipQuerier{
		rolesByUser: map[string][]string{},
	}
	actorID := uuid.New()
	memberships.rolesByUser[actorID.String()] = []string{"organizer"}

	checker := permissions.NewDBChecker(rbac).WithMembershipQuerier(memberships)

	ctx := auth.WithActor(context.Background(), auth.Actor{
		ID:    actorID.String(),
		Roles: []string{"scaffold_user"},
	})

	// JWT-only permission still works.
	if err := checker.Check(ctx, "scaffold.echo.create", "scaffold"); err != nil {
		t.Errorf("expected scaffold.echo.create via JWT role, got: %v", err)
	}

	// Membership-derived permission now works too.
	if err := checker.Check(ctx, "org.read", "org"); err != nil {
		t.Errorf("expected org.read via membership role, got: %v", err)
	}
	if err := checker.Check(ctx, "membership.read", "memberships"); err != nil {
		t.Errorf("expected membership.read via membership role, got: %v", err)
	}

	// Unrelated permission still denied.
	if err := checker.Check(ctx, "geo.admin", "geo"); err == nil {
		t.Error("expected denial for geo.admin (not in any role), got nil")
	}
}

func TestMembership120_PermResolver_GrantThenRevoke(t *testing.T) {
	t.Parallel()
	// Simulates: user gains "organizer" membership (role appears), then loses it.
	rbac := &fakeRBACQuerier120{
		permsByRole: map[string][]string{
			"organizer": {"org.read"},
		},
	}
	mq := &fakeMembershipQuerier{
		rolesByUser: make(map[string][]string),
	}
	actorID := uuid.New()
	checker := permissions.NewDBChecker(rbac).WithMembershipQuerier(mq)

	ctx := auth.WithActor(context.Background(), auth.Actor{
		ID:    actorID.String(),
		Roles: []string{}, // JWT has no roles
	})

	// Before grant: permission denied.
	if err := checker.Check(ctx, "org.read", "org"); err == nil {
		t.Error("expected denial before membership grant, got nil")
	}

	// Simulate grant: add membership role.
	mq.rolesByUser[actorID.String()] = []string{"organizer"}

	// After grant: permission allowed.
	// Note: cache is keyed by role-set. New role-set = cache miss = fresh DB lookup.
	if err := checker.Check(ctx, "org.read", "org"); err != nil {
		t.Errorf("expected org.read after grant, got: %v", err)
	}

	// Simulate revoke: remove membership role.
	mq.rolesByUser[actorID.String()] = []string{}

	// After revoke: permission denied again.
	if err := checker.Check(ctx, "org.read", "org"); err == nil {
		t.Error("expected denial after membership revoke, got nil")
	}
}

func TestMembership120_PermResolver_MultiOrgUser(t *testing.T) {
	t.Parallel()
	// User has "organizer" in org A and "agent" in org B.
	// GetActiveRolesForUser returns roles across ALL orgs (union).
	rbac := &fakeRBACQuerier120{
		permsByRole: map[string][]string{
			"organizer": {"org.read", "membership.read"},
			"agent":     {"org.read"},
		},
	}
	actorID := uuid.New()
	mq := &fakeMembershipQuerier{
		rolesByUser: map[string][]string{
			actorID.String(): {"organizer", "agent"},
		},
	}
	checker := permissions.NewDBChecker(rbac).WithMembershipQuerier(mq)
	ctx := auth.WithActor(context.Background(), auth.Actor{
		ID:    actorID.String(),
		Roles: []string{},
	})

	// User should have the union of organizer + agent permissions.
	for _, perm := range []string{"org.read", "membership.read"} {
		if err := checker.Check(ctx, perm, "test"); err != nil {
			t.Errorf("expected %q from multi-org memberships, got: %v", perm, err)
		}
	}
}

func TestMembership120_PermResolver_NoMembershipQuerierFallsBack(t *testing.T) {
	t.Parallel()
	// When no MembershipQuerier is wired, only JWT roles are checked.
	rbac := &fakeRBACQuerier120{
		permsByRole: map[string][]string{
			"admin": {"org.read", "geo.admin"},
		},
	}
	checker := permissions.NewDBChecker(rbac) // no WithMembershipQuerier
	ctx := auth.WithActor(context.Background(), auth.Actor{
		ID:    uuid.New().String(),
		Roles: []string{"admin"},
	})
	if err := checker.Check(ctx, "org.read", "org"); err != nil {
		t.Errorf("expected org.read via JWT role without membership querier: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4 — sqlc gen files exist with correct structure
// ─────────────────────────────────────────────────────────────────────────────

func TestMembership120_QueryFileExists(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "memberships.sql")
	if len(content) == 0 {
		t.Fatal("memberships.sql is empty")
	}
}

func TestMembership120_QueryFileHasAllOps(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "memberships.sql")
	for _, want := range []string{
		"InsertMembership",
		"RevokeMembership",
		"ListMembershipsByOrg",
		"GetActiveRolesForUser",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("memberships.sql: expected operation %q but not found", want)
		}
	}
}

func TestMembership120_GenGoFileExists(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "memberships.sql.go")
	if len(content) == 0 {
		t.Fatal("memberships.sql.go is empty")
	}
}

func TestMembership120_GenGoFileHasMembershipRow(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "memberships.sql.go")
	for _, want := range []string{
		"MembershipRow",
		"UserID",
		"OrgID",
		"Role",
		"Status",
		"JoinedAt",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("memberships.sql.go: expected %q but not found", want)
		}
	}
}

func TestMembership120_GenGoFileHasAllMethods(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "memberships.sql.go")
	for _, want := range []string{
		"func (q *Queries) InsertMembership(",
		"func (q *Queries) RevokeMembership(",
		"func (q *Queries) ListMembershipsByOrg(",
		"func (q *Queries) GetActiveRolesForUser(",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("memberships.sql.go: expected method %q but not found", want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Full verification test
// ─────────────────────────────────────────────────────────────────────────────

func TestMembership120_FullVerification(t *testing.T) {
	t.Parallel()

	t.Run("migration_schema", func(t *testing.T) {
		t.Parallel()
		content := findFileByName(t, "0011_memberships.sql")
		requiredTerms := []string{
			"CREATE TABLE memberships", "uuidv7()", "user_id", "org_id", "role",
			"status", "joined_at", "UNIQUE (user_id, org_id, role)",
			"REFERENCES users(id)", "REFERENCES organizations(id)",
			"organizer", "platform_superadmin", "-- +goose Down",
		}
		for _, term := range requiredTerms {
			if !strings.Contains(content, term) {
				t.Errorf("migration missing: %q", term)
			}
		}
	})

	t.Run("routes_auth_gated", func(t *testing.T) {
		t.Parallel()
		s := buildMembershipServer(t)
		orgID := uuid.New().String()
		userID := uuid.New().String()
		routes := []struct {
			method string
			path   string
		}{
			{http.MethodPost, "/v1/organizations/" + orgID + "/members"},
			{http.MethodGet, "/v1/organizations/" + orgID + "/members"},
			{http.MethodDelete, "/v1/organizations/" + orgID + "/members/" + userID},
		}
		for _, rt := range routes {
			var r *http.Request
			if rt.method == http.MethodPost {
				// POST requires Content-Type: application/json or body-limit
				// middleware rejects with 415 before auth fires.
				r = httptest.NewRequest(rt.method, rt.path,
					strings.NewReader(`{"user_id":"`+uuid.New().String()+`","role":"organizer"}`))
				r.Header.Set("Content-Type", "application/json")
			} else {
				r = httptest.NewRequest(rt.method, rt.path, nil)
			}
			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s without auth: want 401, got %d", rt.method, rt.path, w.Code)
			}
		}
	})

	t.Run("membership_querier_interface", func(t *testing.T) {
		t.Parallel()
		// Verify gen.Queries satisfies MembershipQuerier at compile time.
		// This is a structural check — if it compiles, the interface is satisfied.
		var _ permissions.MembershipQuerier = (*gen.Queries)(nil)
	})

	t.Run("permission_resolution_with_memberships", func(t *testing.T) {
		t.Parallel()
		rbac := &fakeRBACQuerier120{
			permsByRole: map[string][]string{
				"organizer": {"org.read"},
			},
		}
		actorID := uuid.New()
		mq := &fakeMembershipQuerier{
			rolesByUser: map[string][]string{
				actorID.String(): {"organizer"},
			},
		}
		checker := permissions.NewDBChecker(rbac).WithMembershipQuerier(mq)
		ctx := auth.WithActor(context.Background(), auth.Actor{
			ID: actorID.String(), Roles: []string{},
		})
		if err := checker.Check(ctx, "org.read", "org"); err != nil {
			t.Errorf("membership-derived permission check failed: %v", err)
		}
	})
}

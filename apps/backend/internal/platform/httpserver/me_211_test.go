// me_211_test.go — unit tests for feature #211
// "Extend current-user context endpoint for network scopes".
//
// Coverage:
//
//   - GET /v1/me is mounted on the chi router and protected by auth.Middleware.
//   - Unauthenticated requests receive 401.
//   - For a superadmin actor: roles, permissions, available_scopes contain
//     "global"; assigned_networks/organization_memberships are honoured from
//     the fake store.
//   - For a network_operator actor with two assigned networks: scopes contain
//     deterministically-ordered "network:<uuid>" entries (NO "global").
//   - For an organizer actor with one membership: scopes contain the
//     corresponding "organization:<uuid>" entry.
//   - For a platform_operator actor with no network/org assignment: scopes
//     contain "platform" (and NOT "global", per the bypass policy).
//
// All tests run without a live PostgreSQL connection. The handler depends on
// the narrow meQuerier interface; tests inject a fakeMeQuerier so the
// response-shaping logic is exercised end-to-end through the chi router.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// ─────────────────────────────────────────────────────────────────────────────
// fakeMeQuerier — in-memory meQuerier for tests
// ─────────────────────────────────────────────────────────────────────────────

type fakeMeQuerier struct {
	rolesByUser       map[uuid.UUID][]string
	permsByRoles      map[string][]string // key = sorted-comma-joined role list
	membershipsByUser map[uuid.UUID][]gen.MembershipRow
	networksByUser    map[uuid.UUID][]gen.OperatorNetworkRow
}

func (f *fakeMeQuerier) GetActiveRolesForUser(_ context.Context, userID uuid.UUID) ([]string, error) {
	return append([]string(nil), f.rolesByUser[userID]...), nil
}

func (f *fakeMeQuerier) GetPermissionsForRoles(_ context.Context, roleNames []string) ([]string, error) {
	out := map[string]bool{}
	for _, r := range roleNames {
		for _, p := range f.permsByRoles[r] {
			out[p] = true
		}
	}
	perms := make([]string, 0, len(out))
	for p := range out {
		perms = append(perms, p)
	}
	sort.Strings(perms)
	return perms, nil
}

func (f *fakeMeQuerier) ListMembershipsByUser(_ context.Context, userID uuid.UUID) ([]gen.MembershipRow, error) {
	return append([]gen.MembershipRow(nil), f.membershipsByUser[userID]...), nil
}

func (f *fakeMeQuerier) ListNetworksByUser(_ context.Context, userID uuid.UUID) ([]gen.OperatorNetworkRow, error) {
	return append([]gen.OperatorNetworkRow(nil), f.networksByUser[userID]...), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Test server factory
// ─────────────────────────────────────────────────────────────────────────────

func buildMeServer(t *testing.T, store *fakeMeQuerier) (*Server, *auth.StubProvider) {
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
		t.Fatalf("buildMeServer: NewStubProvider: %v", err)
	}
	s := New(Options{
		Config:    cfg,
		Auth:      stub,
		MeQueries: store,
	})
	return s, stub
}

func issueMeToken(t *testing.T, stub *auth.StubProvider, userID uuid.UUID, roles []string) string {
	t.Helper()
	tok, _, err := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID:   userID.String(),
		ActorType: auth.ActorTypeStubUser,
		Roles:     roles,
	})
	if err != nil {
		t.Fatalf("issueMeToken: %v", err)
	}
	return tok
}

func decodeMeResponse(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(w.Body).Decode(&m); err != nil {
		t.Fatalf("decodeMeResponse: %v (body: %s)", err, w.Body.String())
	}
	return m
}

func mustStringSlice(t *testing.T, v any, field string) []string {
	t.Helper()
	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("%s: not a JSON array, got %T (%v)", field, v, v)
	}
	out := make([]string, 0, len(raw))
	for i, x := range raw {
		s, ok := x.(string)
		if !ok {
			t.Fatalf("%s[%d]: not a string, got %T", field, i, x)
		}
		out = append(out, s)
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 1 — unauthenticated request returns 401
// ─────────────────────────────────────────────────────────────────────────────

func TestMe211_Unauthenticated_Returns401(t *testing.T) {
	t.Parallel()
	store := &fakeMeQuerier{}
	s, _ := buildMeServer(t, store)

	r := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/me without auth: want 401, got %d (body: %s)", w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 2 — superadmin actor: bypass role yields "global" scope
// ─────────────────────────────────────────────────────────────────────────────

func TestMe211_Superadmin_HasGlobalScope(t *testing.T) {
	t.Parallel()
	userID := uuid.New()
	store := &fakeMeQuerier{
		rolesByUser: map[uuid.UUID][]string{
			userID: {"platform_superadmin"},
		},
		permsByRoles: map[string][]string{
			"platform_superadmin": {"org.read", "network.read", "network.manage_users"},
		},
		// Superadmins typically have no per-org membership row — global scope
		// is what authorises them. Leave both maps empty for this user.
	}
	s, stub := buildMeServer(t, store)
	tok := issueMeToken(t, stub, userID, []string{"platform_superadmin"})

	r := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("superadmin GET /v1/me: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	body := decodeMeResponse(t, w)

	scopes := mustStringSlice(t, body["available_scopes"], "available_scopes")
	if len(scopes) == 0 || scopes[0] != "global" {
		t.Errorf("superadmin available_scopes: expected 'global' first, got %v", scopes)
	}
	roles := mustStringSlice(t, body["roles"], "roles")
	if !containsString(roles, "platform_superadmin") {
		t.Errorf("superadmin roles missing platform_superadmin: %v", roles)
	}
	perms := mustStringSlice(t, body["permissions"], "permissions")
	if !containsString(perms, "network.manage_users") {
		t.Errorf("superadmin permissions missing network.manage_users: %v", perms)
	}
	if memberships, ok := body["organization_memberships"].([]any); !ok || len(memberships) != 0 {
		t.Errorf("superadmin organization_memberships: expected [], got %v", body["organization_memberships"])
	}
	if networks, ok := body["assigned_networks"].([]any); !ok || len(networks) != 0 {
		t.Errorf("superadmin assigned_networks: expected [], got %v", body["assigned_networks"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 3 — network_operator actor: two assigned networks, no bypass scope
// ─────────────────────────────────────────────────────────────────────────────

func TestMe211_NetworkOperator_HasNetworkScopes(t *testing.T) {
	t.Parallel()
	userID := uuid.New()
	netA := gen.OperatorNetworkRow{
		ID: uuid.MustParse("0193f01a-0000-7000-8000-00000000000a"), Name: "Alpha Net", Slug: "alpha", Status: "active",
	}
	netB := gen.OperatorNetworkRow{
		ID: uuid.MustParse("0193f01a-0000-7000-8000-00000000000b"), Name: "Bravo Net", Slug: "bravo", Status: "active",
	}
	store := &fakeMeQuerier{
		rolesByUser: map[uuid.UUID][]string{
			userID: {"network_operator"},
		},
		permsByRoles: map[string][]string{
			"network_operator": {"network.read", "network.view_sales", "network.support_orders"},
		},
		networksByUser: map[uuid.UUID][]gen.OperatorNetworkRow{
			userID: {netB, netA}, // deliberately unsorted in the store
		},
	}
	s, stub := buildMeServer(t, store)
	tok := issueMeToken(t, stub, userID, []string{"network_operator"})

	r := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("network_operator GET /v1/me: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	body := decodeMeResponse(t, w)

	scopes := mustStringSlice(t, body["available_scopes"], "available_scopes")
	if containsString(scopes, "global") {
		t.Errorf("network_operator must NOT have global scope, got %v", scopes)
	}
	if containsString(scopes, "platform") {
		t.Errorf("network_operator must NOT have platform scope, got %v", scopes)
	}
	wantA := "network:" + netA.ID.String()
	wantB := "network:" + netB.ID.String()
	if !containsString(scopes, wantA) || !containsString(scopes, wantB) {
		t.Errorf("network_operator scopes missing one of %s / %s: %v", wantA, wantB, scopes)
	}
	// Deterministic ordering: alphabetical among network: entries.
	idxA := indexOf(scopes, wantA)
	idxB := indexOf(scopes, wantB)
	if idxA > idxB {
		t.Errorf("network scopes not sorted: %v (expected %s before %s)", scopes, wantA, wantB)
	}

	nets, ok := body["assigned_networks"].([]any)
	if !ok || len(nets) != 2 {
		t.Fatalf("assigned_networks: expected 2 entries, got %v", body["assigned_networks"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 4 — organizer actor: one membership -> organization scope
// ─────────────────────────────────────────────────────────────────────────────

func TestMe211_Organizer_HasOrganizationScope(t *testing.T) {
	t.Parallel()
	userID := uuid.New()
	orgID := uuid.MustParse("0193f01a-0000-7000-8000-00000000ff01")
	store := &fakeMeQuerier{
		rolesByUser: map[uuid.UUID][]string{
			userID: {"organizer"},
		},
		permsByRoles: map[string][]string{
			"organizer": {"org.read", "event.create", "event.update"},
		},
		membershipsByUser: map[uuid.UUID][]gen.MembershipRow{
			userID: {{
				ID:       uuid.New(),
				UserID:   userID,
				OrgID:    orgID,
				Role:     "organizer",
				Status:   "active",
				JoinedAt: time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC),
			}},
		},
	}
	s, stub := buildMeServer(t, store)
	tok := issueMeToken(t, stub, userID, []string{"organizer"})

	r := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("organizer GET /v1/me: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	body := decodeMeResponse(t, w)

	scopes := mustStringSlice(t, body["available_scopes"], "available_scopes")
	want := "organization:" + orgID.String()
	if !containsString(scopes, want) {
		t.Errorf("organizer available_scopes missing %s: %v", want, scopes)
	}
	if containsString(scopes, "global") {
		t.Errorf("organizer must NOT have global scope, got %v", scopes)
	}

	memberships, ok := body["organization_memberships"].([]any)
	if !ok || len(memberships) != 1 {
		t.Fatalf("organization_memberships: expected 1 entry, got %v", body["organization_memberships"])
	}
	first, _ := memberships[0].(map[string]any)
	if got, _ := first["org_id"].(string); got != orgID.String() {
		t.Errorf("organization_memberships[0].org_id = %q, want %q", got, orgID.String())
	}
	if got, _ := first["role"].(string); got != "organizer" {
		t.Errorf("organization_memberships[0].role = %q, want organizer", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 5 — platform_operator: 'platform' scope present, no 'global'
// ─────────────────────────────────────────────────────────────────────────────

func TestMe211_PlatformOperator_HasPlatformScopeNotGlobal(t *testing.T) {
	t.Parallel()
	userID := uuid.New()
	store := &fakeMeQuerier{
		rolesByUser: map[uuid.UUID][]string{
			userID: {"platform_operator"},
		},
		permsByRoles: map[string][]string{
			"platform_operator": {"org.read", "superadmin.read"},
		},
		// No org memberships, no network assignments — platform_operator
		// is an internal Arena role evaluated by middleware, not membership.
	}
	s, stub := buildMeServer(t, store)
	tok := issueMeToken(t, stub, userID, []string{"platform_operator"})

	r := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("platform_operator GET /v1/me: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	body := decodeMeResponse(t, w)

	scopes := mustStringSlice(t, body["available_scopes"], "available_scopes")
	if !containsString(scopes, "platform") {
		t.Errorf("platform_operator scopes missing 'platform': %v", scopes)
	}
	if containsString(scopes, "global") {
		t.Errorf("platform_operator must NOT have 'global' scope: %v", scopes)
	}

	roles := mustStringSlice(t, body["roles"], "roles")
	if !containsString(roles, "platform_operator") {
		t.Errorf("platform_operator roles missing platform_operator: %v", roles)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 6 — backwards-compat: response always includes the new fields, even
//          when both are empty. Clients can rely on non-nil arrays.
// ─────────────────────────────────────────────────────────────────────────────

func TestMe211_EmptyAssignments_StillEmitsNewFields(t *testing.T) {
	t.Parallel()
	userID := uuid.New()
	store := &fakeMeQuerier{
		rolesByUser:  map[uuid.UUID][]string{userID: {"organizer"}},
		permsByRoles: map[string][]string{"organizer": {"org.read"}},
		// no memberships, no networks
	}
	s, stub := buildMeServer(t, store)
	tok := issueMeToken(t, stub, userID, []string{"organizer"})

	r := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("organizer (empty) GET /v1/me: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	for _, field := range []string{"assigned_networks", "available_scopes", "organization_memberships", "roles", "permissions"} {
		if !strings.Contains(w.Body.String(), `"`+field+`"`) {
			t.Errorf("response missing required field %q: %s", field, w.Body.String())
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// computeAvailableScopes — pure-function regression tests
// ─────────────────────────────────────────────────────────────────────────────

func TestMe211_ComputeAvailableScopes_OrderingAndDedup(t *testing.T) {
	t.Parallel()
	netA := uuid.MustParse("0193f01a-0000-7000-8000-00000000aaaa")
	netB := uuid.MustParse("0193f01a-0000-7000-8000-00000000bbbb")
	orgA := uuid.MustParse("0193f01a-0000-7000-8000-00000000ccc1")
	orgB := uuid.MustParse("0193f01a-0000-7000-8000-00000000ccc2")

	roles := []string{"network_operator", "organizer"}
	memberships := []gen.MembershipRow{
		{OrgID: orgB},
		{OrgID: orgA},
		{OrgID: orgA}, // duplicate -> should be deduped
	}
	networks := []gen.OperatorNetworkRow{
		{ID: netB},
		{ID: netA},
		{ID: netA}, // duplicate -> should be deduped
	}
	scopes := computeAvailableScopes(roles, memberships, networks)

	want := []string{
		"network:" + netA.String(),
		"network:" + netB.String(),
		"organization:" + orgA.String(),
		"organization:" + orgB.String(),
	}
	if len(scopes) != len(want) {
		t.Fatalf("computeAvailableScopes length: got %d (%v), want %d (%v)", len(scopes), scopes, len(want), want)
	}
	for i, w := range want {
		if scopes[i] != w {
			t.Errorf("scopes[%d] = %q, want %q (full=%v)", i, scopes[i], w, scopes)
		}
	}
}

func TestMe211_ComputeAvailableScopes_BypassFirst(t *testing.T) {
	t.Parallel()
	scopes := computeAvailableScopes([]string{"platform_superadmin", "platform_operator"}, nil, nil)
	if len(scopes) < 2 {
		t.Fatalf("expected at least 2 scopes, got %v", scopes)
	}
	if scopes[0] != "global" {
		t.Errorf("expected scopes[0]=\"global\", got %q (full=%v)", scopes[0], scopes)
	}
	if scopes[1] != "platform" {
		t.Errorf("expected scopes[1]=\"platform\", got %q (full=%v)", scopes[1], scopes)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func indexOf(haystack []string, needle string) int {
	for i, s := range haystack {
		if s == needle {
			return i
		}
	}
	return -1
}

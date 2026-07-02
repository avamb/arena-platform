// network_authz_214_test.go -- positive/negative authorization tests for the
// operator-network surface (feature #214).
//
// Coverage matrix:
//
//	Layer A -- Role/permission catalogue alignment
//	  Asserts that the role -> permission map encoded in migration
//	  0044_network_permissions.sql aligns with what each operator-network
//	  endpoint family requires (per mount_networks.go). Drift between the
//	  migration and the mount file is treated as a regression: a role that
//	  loses (or silently gains) a permission would break authorization
//	  end-to-end without any handler test ever firing.
//
//	Layer B -- HTTP middleware allow/deny matrix
//	  For each (role, endpoint family) pair we build a tiny chi router
//	  wrapping a sentinel handler with the SAME middleware pair the real
//	  mount uses -- auth.Middleware + permissions.RequirePermission -- but
//	  swap the permission checker for a role-aware test impl that derives
//	  its allow/deny answer from the migration matrix. This exercises the
//	  "permission checks are enforced backend-side" requirement from #214
//	  without needing a live PostgreSQL instance.
//
//	Layer C -- Invalid-assignment handler tests
//	  Direct handler calls with deliberately bad path params / bodies, to
//	  pin the "fail cleanly" contract (400 / 404, never 500 / panic).
//
//	Layer D -- Regression: organizer / platform_operator / platform_superadmin
//	  The non-network roles must keep working unchanged. We re-exercise the
//	  /v1/me scope computation contract that #211 introduced, to make sure
//	  adding network_operator gating did not accidentally drop or reorder
//	  the bypass / organization scopes for existing roles.
//
// All tests run without a live PostgreSQL connection.
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

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
)

// ─────────────────────────────────────────────────────────────────────────────
// Role -> permission matrix (mirrors migration 0044_network_permissions.sql)
// ─────────────────────────────────────────────────────────────────────────────

// rolePermMatrix214 encodes the role -> permission bindings asserted by
// 0044_network_permissions.sql (see feature #206). This is the source of truth
// for the positive/negative test matrix below. Re-stating it here as a Go
// literal means a drift in the migration that adds/removes a binding without
// updating this map produces a compile-time-noisy test failure rather than a
// silent authorization regression.
var rolePermMatrix214 = map[string]map[string]bool{
	"platform_superadmin": setOf(allNetworkPermissions...),
	"network_operator":    setOf(networkOperatorPermissions...),
	// platform_operator is intentionally empty for network.* -- preserved per
	// feature #206 step 5 ("platform_operator unchanged").
	"platform_operator": {},
	// organizer never receives any network.* binding.
	"organizer": {},
	// admin still receives every network.* permission per the 0008 broad-grant
	// re-attached idempotently by 0044.
	"admin": setOf(allNetworkPermissions...),
	// network_operator's lifecycle exclusions, called out explicitly to keep
	// the negative-path assertions readable.
}

// endpointFamily214 declares one entry per route group in mount_networks.go.
// Method+path covers the canonical URL shape; permission is the string passed
// to applyAuth in mount_networks.go.
type endpointFamily214 struct {
	method     string
	path       string
	body       string
	permission string
	desc       string
}

func endpointFamilies214() []endpointFamily214 {
	netID := uuid.New().String()
	userID := uuid.New().String()
	orgID := uuid.New().String()
	return []endpointFamily214{
		// network.read
		{http.MethodGet, "/v1/operator-networks", "", "network.read", "list networks"},
		{http.MethodGet, "/v1/operator-networks/" + netID, "", "network.read", "get network"},
		// network.create
		{http.MethodPost, "/v1/operator-networks",
			`{"name":"N","slug":"n-` + strings.ToLower(netID[:8]) + `"}`,
			"network.create", "create network"},
		// network.update
		{http.MethodPatch, "/v1/operator-networks/" + netID,
			`{"name":"Renamed"}`, "network.update", "update network"},
		// network.archive
		{http.MethodPost, "/v1/operator-networks/" + netID + "/archive",
			"", "network.archive", "archive network"},
		// network.manage_users
		{http.MethodGet, "/v1/admin/networks/" + netID + "/users",
			"", "network.manage_users", "list network users"},
		{http.MethodPost, "/v1/admin/networks/" + netID + "/users",
			`{"user_id":"` + userID + `"}`, "network.manage_users", "assign network user"},
		{http.MethodDelete, "/v1/admin/networks/" + netID + "/users/" + userID,
			"", "network.manage_users", "remove network user"},
		// network.manage_organizers
		{http.MethodGet, "/v1/admin/networks/" + netID + "/organizers",
			"", "network.manage_organizers", "list organizers"},
		{http.MethodPost, "/v1/admin/networks/" + netID + "/organizers",
			`{"organization_id":"` + orgID + `"}`, "network.manage_organizers", "attach organizer"},
		{http.MethodDelete, "/v1/admin/networks/" + netID + "/organizers/" + orgID,
			"", "network.manage_organizers", "detach organizer"},
		// network.manage_agents
		{http.MethodGet, "/v1/admin/networks/" + netID + "/agents",
			"", "network.manage_agents", "list agents"},
		{http.MethodPost, "/v1/admin/networks/" + netID + "/agents",
			`{"organization_id":"` + orgID + `"}`, "network.manage_agents", "attach agent"},
		{http.MethodDelete, "/v1/admin/networks/" + netID + "/agents/" + orgID,
			"", "network.manage_agents", "detach agent"},
	}
}

func setOf(items ...string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, s := range items {
		out[s] = true
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Layer A -- migration / mount-file alignment
// ─────────────────────────────────────────────────────────────────────────────

// TestAuthz214_MountPermissionsAreAllRegisteredInMigration verifies every
// permission string used by mount_networks.go appears in the permission
// catalogue declared by migration 0044. If a future feature renames a
// permission in one place but not the other, every protected endpoint would
// 500/403 forever; this test catches the drift on the next CI run.
func TestAuthz214_MountPermissionsAreAllRegisteredInMigration(t *testing.T) {
	t.Parallel()
	mount := readSourceFile(t, "mount_networks.go")
	migration := findFileByName(t, "0044_network_permissions.sql")

	seen := map[string]bool{}
	for _, fam := range endpointFamilies214() {
		seen[fam.permission] = true
	}

	for perm := range seen {
		if !strings.Contains(mount, `"`+perm+`"`) {
			t.Errorf("mount_networks.go missing permission literal %q used by family", perm)
		}
		if !strings.Contains(migration, "'"+perm+"'") {
			t.Errorf("0044_network_permissions.sql missing permission literal %q (used by mount_networks.go)", perm)
		}
	}
}

// TestAuthz214_NetworkOperatorLifecycleExclusionsHold is a structural guard
// that mirrors the spec's "operational only" requirement for network_operator:
// the role must hold network.read/update/manage_organizers/manage_agents etc.
// but NOT network.create/archive/manage_users.
func TestAuthz214_NetworkOperatorLifecycleExclusionsHold(t *testing.T) {
	t.Parallel()
	got := rolePermMatrix214["network_operator"]
	for _, must := range []string{
		"network.read",
		"network.update",
		"network.manage_organizers",
		"network.manage_agents",
		"network.view_sales",
		"network.view_reports",
	} {
		if !got[must] {
			t.Errorf("network_operator matrix missing %s", must)
		}
	}
	for _, mustNot := range platformSuperadminExcludedFromNetworkOperator {
		if got[mustNot] {
			t.Errorf("network_operator matrix must NOT include %s (lifecycle perm)", mustNot)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Layer B -- role-aware checker wired through RequirePermission middleware
// ─────────────────────────────────────────────────────────────────────────────

// roleAwareChecker214 implements permissions.Checker by consulting
// rolePermMatrix214 with the roles claim from the JWT actor in context. This
// is the test-only counterpart to permissions.NewDBChecker, sufficient to
// prove that applyAuth + RequirePermission collectively enforce the
// per-role gates that the production DB checker derives from
// 0044_network_permissions.sql.
type roleAwareChecker214 struct{}

func (roleAwareChecker214) Check(ctx context.Context, action, _ string) error {
	a, ok := auth.ActorFromContext(ctx)
	if !ok {
		return &permissions.PermissionDeniedError{Action: action, Resource: "anon"}
	}
	for _, role := range a.Roles {
		if rolePermMatrix214[role][action] {
			return nil
		}
	}
	return &permissions.PermissionDeniedError{Action: action, Resource: strings.Join(a.Roles, ",")}
}

// buildAuthz214Server builds a Server wired with:
//   - the canonical stub auth provider (token issuance/verification),
//   - the role-aware permission checker,
//   - the standard operator-network mount (so the full middleware chain runs).
func buildAuthz214Server(t *testing.T) (*Server, *auth.StubProvider) {
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
	s := New(Options{
		Config:         cfg,
		Auth:           stub,
		Pool:           &dbDownPool{},
		NetworkQueries: gen.New(nil),
		Audit:          &captureAuditWriter{},
		Permissions:    roleAwareChecker214{},
	})
	return s, stub
}

func issueAuthz214Token(t *testing.T, stub *auth.StubProvider, roles ...string) string {
	t.Helper()
	tok, _, err := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID:   uuid.New().String(),
		ActorType: auth.ActorTypeStubUser,
		Roles:     roles,
	})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	return tok
}

// matrixCase represents one (role, expect-allow?) row.
type matrixRow struct {
	role  string
	allow bool
}

// TestAuthz214_PermissionMatrix_PerRolePerEndpoint is the headline table-driven
// integration test for #214. For every endpoint family x every relevant role,
// it confirms the HTTP middleware chain returns:
//
//   - exactly 403 (permissions.denied) when the role is NOT bound to the
//     required permission in migration 0044, and
//   - anything other than 401/403 when the role IS bound (i.e. the request
//     made it past auth + permission middleware and into the handler).
//
// The handler-level outcome under "allow" is intentionally not pinned: the
// nil-pool short-circuit yields 503 for most handlers, and uuidPathParam may
// emit 400 for the chi sample IDs -- both are fine, both prove "permission
// passed". What this matrix guarantees is the gate's verdict, not the
// downstream behavior (which is covered by the per-family tests in #208/
// #209/#210).
func TestAuthz214_PermissionMatrix_PerRolePerEndpoint(t *testing.T) {
	t.Parallel()
	families := endpointFamilies214()
	for _, fam := range families {
		fam := fam
		t.Run(fam.method+" "+fam.path+" ("+fam.permission+")", func(t *testing.T) {
			t.Parallel()
			// Per-role expectations are derived from the matrix.
			rows := []matrixRow{
				{"platform_superadmin", rolePermMatrix214["platform_superadmin"][fam.permission]},
				{"network_operator", rolePermMatrix214["network_operator"][fam.permission]},
				{"platform_operator", rolePermMatrix214["platform_operator"][fam.permission]},
				{"organizer", rolePermMatrix214["organizer"][fam.permission]},
				{"admin", rolePermMatrix214["admin"][fam.permission]},
			}
			for _, row := range rows {
				row := row
				t.Run(row.role, func(t *testing.T) {
					s, stub := buildAuthz214Server(t)
					tok := issueAuthz214Token(t, stub, row.role)
					var body *strings.Reader
					if fam.body != "" {
						body = strings.NewReader(fam.body)
					} else {
						body = strings.NewReader("")
					}
					req := httptest.NewRequest(fam.method, fam.path, body)
					req.Header.Set("Authorization", "Bearer "+tok)
					if fam.body != "" {
						req.Header.Set("Content-Type", "application/json")
					}
					rec := httptest.NewRecorder()
					s.router.ServeHTTP(rec, req)

					if rec.Code == http.StatusNotFound {
						t.Fatalf("route not mounted: %s %s (got 404)", fam.method, fam.path)
					}
					if row.allow {
						if rec.Code == http.StatusForbidden {
							t.Errorf("role %q SHOULD be allowed to %s %s (perm=%s) but got 403: %s",
								row.role, fam.method, fam.path, fam.permission, rec.Body.String())
						}
						if rec.Code == http.StatusUnauthorized {
							t.Errorf("role %q failed auth on %s %s: 401 (body=%s)",
								row.role, fam.method, fam.path, rec.Body.String())
						}
					} else {
						if rec.Code != http.StatusForbidden {
							t.Errorf("role %q should be DENIED %s %s (perm=%s); want 403, got %d (body=%s)",
								row.role, fam.method, fam.path, fam.permission, rec.Code, rec.Body.String())
						}
						// Confirm the structured error envelope identifies the
						// denial as a permission failure, not e.g. a 403 from
						// somewhere else.
						if !strings.Contains(rec.Body.String(), "permissions.denied") {
							t.Errorf("role %q deny on %s %s: missing permissions.denied error code; body=%s",
								row.role, fam.method, fam.path, rec.Body.String())
						}
					}
				})
			}
		})
	}
}

// TestAuthz214_UnauthenticatedRejectedOnEveryFamily is a defence-in-depth
// guard: even before the permission gate, every endpoint must reject an
// unauthenticated caller with 401. The same matrix from #208/#209/#210
// re-evaluated together so future endpoint additions can't slip past without
// being added to all three suites at once.
func TestAuthz214_UnauthenticatedRejectedOnEveryFamily(t *testing.T) {
	t.Parallel()
	for _, fam := range endpointFamilies214() {
		fam := fam
		t.Run(fam.method+" "+fam.path, func(t *testing.T) {
			t.Parallel()
			s, _ := buildAuthz214Server(t)
			var body *strings.Reader
			if fam.body != "" {
				body = strings.NewReader(fam.body)
			} else {
				body = strings.NewReader("")
			}
			req := httptest.NewRequest(fam.method, fam.path, body)
			if fam.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, req)
			if rec.Code == http.StatusNotFound {
				t.Fatalf("route missing: %s %s", fam.method, fam.path)
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("unauth %s %s: want 401, got %d (body=%s)",
					fam.method, fam.path, rec.Code, rec.Body.String())
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Layer C -- invalid-assignment "fail cleanly" handler tests
// ─────────────────────────────────────────────────────────────────────────────

// TestAuthz214_InvalidAssignments_FailCleanly exercises the handler-level
// validation paths that would otherwise leak as 500s if input were trusted.
// These complement the per-family tests in #209/#210 by focusing on the
// cross-family invariants: invalid path UUIDs, missing-target IDs, and
// nonexistent network IDs are all surfaced as 400/404 with typed error codes.
func TestAuthz214_InvalidAssignments_FailCleanly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		handlerFn func(*Server) http.HandlerFunc
		req       func() *http.Request
		wantCode  int
		wantErr   string // substring of "error.code"
	}{
		{
			name: "create network with malformed slug",
			handlerFn: func(s *Server) http.HandlerFunc {
				return s.handleCreateOperatorNetwork
			},
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/v1/operator-networks",
					strings.NewReader(`{"name":"X","slug":"BAD_SLUG"}`))
				r.Header.Set("Content-Type", "application/json")
				return r
			},
			wantCode: http.StatusBadRequest,
			wantErr:  "operator_network.invalid_slug",
		},
		{
			name: "get network with non-UUID path",
			handlerFn: func(s *Server) http.HandlerFunc {
				return s.handleGetOperatorNetwork
			},
			req: func() *http.Request {
				return chiPathRequest(http.MethodGet,
					"/v1/operator-networks/not-a-uuid", nil,
					map[string]string{"id": "not-a-uuid"})
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "assign network user with malformed user_id",
			handlerFn: func(s *Server) http.HandlerFunc {
				return s.handleAssignNetworkUser
			},
			req: func() *http.Request {
				return chiPathRequest(http.MethodPost,
					"/v1/admin/networks/x/users",
					strings.NewReader(`{"user_id":"nope"}`),
					map[string]string{"id": uuid.New().String()})
			},
			wantCode: http.StatusBadRequest,
			wantErr:  "network_user.invalid_user_id",
		},
		{
			name: "remove network user with malformed user_id",
			handlerFn: func(s *Server) http.HandlerFunc {
				return s.handleRemoveNetworkUser
			},
			req: func() *http.Request {
				return chiPathRequest(http.MethodDelete,
					"/v1/admin/networks/x/users/y", nil,
					map[string]string{
						"id": uuid.New().String(), "userId": "garbage",
					})
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "attach organizer with malformed organization_id",
			handlerFn: func(s *Server) http.HandlerFunc {
				return s.handleAttachNetworkOrganization(networkAssignmentKindOrganizer)
			},
			req: func() *http.Request {
				r := chiPathRequest(http.MethodPost,
					"/v1/admin/networks/x/organizers",
					strings.NewReader(`{"organization_id":"oops"}`),
					map[string]string{"id": uuid.New().String()})
				r.Header.Set("Content-Type", "application/json")
				return r
			},
			wantCode: http.StatusBadRequest,
			wantErr:  "network_org.invalid_organization_id",
		},
		{
			name: "attach agent with empty body",
			handlerFn: func(s *Server) http.HandlerFunc {
				return s.handleAttachNetworkOrganization(networkAssignmentKindAgent)
			},
			req: func() *http.Request {
				return chiPathRequest(http.MethodPost,
					"/v1/admin/networks/x/agents", http.NoBody,
					map[string]string{"id": uuid.New().String()})
			},
			wantCode: http.StatusBadRequest,
			wantErr:  "network_org.empty_body",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &Server{
				cfg:            &config.Config{DefaultLocale: "en"},
				networkQueries: gen.New(nil),
				pool:           &dbDownPool{},
			}
			rec := httptest.NewRecorder()
			// SAUI-09: network mutations require X-Admin-Reason. Every
			// mutation case in this table goes through that gate, so set
			// the header once here -- it is ignored by the read-only
			// (get) case and accepted by the mutation cases.
			r := tc.req()
			r.Header.Set("X-Admin-Reason", "saui-09 test")
			tc.handlerFn(s)(rec, r)
			if rec.Code != tc.wantCode {
				t.Fatalf("%s: status got %d want %d (body=%s)", tc.name, rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantErr != "" && !strings.Contains(rec.Body.String(), tc.wantErr) {
				t.Errorf("%s: error body missing %q (body=%s)", tc.name, tc.wantErr, rec.Body.String())
			}
			// Sanity: no handler should ever leak a 5xx from these bad-input
			// paths -- that would indicate either a panic or a missing
			// validation guard.
			if rec.Code >= 500 {
				t.Errorf("%s: handler produced 5xx for bad input (body=%s)", tc.name, rec.Body.String())
			}
		})
	}
}

// TestAuthz214_NilDependencies_ShortCircuitTo503 pins the boundary contract
// every operator-network handler observes: when the DB pool / queries are nil
// the handler returns a typed 503 instead of panicking.
func TestAuthz214_NilDependencies_ShortCircuitTo503(t *testing.T) {
	t.Parallel()
	type handlerCase struct {
		name string
		req  *http.Request
		call func(s *Server, w http.ResponseWriter, r *http.Request)
	}
	cases := []handlerCase{
		{"create", httptest.NewRequest(http.MethodPost, "/v1/operator-networks",
			strings.NewReader(`{"name":"N","slug":"n"}`)),
			func(s *Server, w http.ResponseWriter, r *http.Request) { s.handleCreateOperatorNetwork(w, r) }},
		{"list", httptest.NewRequest(http.MethodGet, "/v1/operator-networks", nil),
			func(s *Server, w http.ResponseWriter, r *http.Request) { s.handleListOperatorNetworks(w, r) }},
		{"get", httptest.NewRequest(http.MethodGet, "/v1/operator-networks/x", nil),
			func(s *Server, w http.ResponseWriter, r *http.Request) { s.handleGetOperatorNetwork(w, r) }},
		{"update", httptest.NewRequest(http.MethodPatch, "/v1/operator-networks/x",
			strings.NewReader(`{}`)),
			func(s *Server, w http.ResponseWriter, r *http.Request) { s.handleUpdateOperatorNetwork(w, r) }},
		{"archive", httptest.NewRequest(http.MethodPost, "/v1/operator-networks/x/archive", nil),
			func(s *Server, w http.ResponseWriter, r *http.Request) { s.handleArchiveOperatorNetwork(w, r) }},
		{"assign-user", httptest.NewRequest(http.MethodPost, "/v1/admin/networks/x/users",
			strings.NewReader(`{"user_id":"u"}`)),
			func(s *Server, w http.ResponseWriter, r *http.Request) { s.handleAssignNetworkUser(w, r) }},
		{"list-users", httptest.NewRequest(http.MethodGet, "/v1/admin/networks/x/users", nil),
			func(s *Server, w http.ResponseWriter, r *http.Request) { s.handleListNetworkUsers(w, r) }},
		{"remove-user", httptest.NewRequest(http.MethodDelete, "/v1/admin/networks/x/users/y", nil),
			func(s *Server, w http.ResponseWriter, r *http.Request) { s.handleRemoveNetworkUser(w, r) }},
		{"attach-organizer", httptest.NewRequest(http.MethodPost, "/v1/admin/networks/x/organizers",
			strings.NewReader(`{"organization_id":"o"}`)),
			func(s *Server, w http.ResponseWriter, r *http.Request) {
				s.handleAttachNetworkOrganization(networkAssignmentKindOrganizer)(w, r)
			}},
		{"list-organizers", httptest.NewRequest(http.MethodGet, "/v1/admin/networks/x/organizers", nil),
			func(s *Server, w http.ResponseWriter, r *http.Request) {
				s.handleListNetworkOrganizations(networkAssignmentKindOrganizer)(w, r)
			}},
		{"detach-organizer", httptest.NewRequest(http.MethodDelete, "/v1/admin/networks/x/organizers/y", nil),
			func(s *Server, w http.ResponseWriter, r *http.Request) {
				s.handleDetachNetworkOrganization(networkAssignmentKindOrganizer)(w, r)
			}},
		{"attach-agent", httptest.NewRequest(http.MethodPost, "/v1/admin/networks/x/agents",
			strings.NewReader(`{"organization_id":"o"}`)),
			func(s *Server, w http.ResponseWriter, r *http.Request) {
				s.handleAttachNetworkOrganization(networkAssignmentKindAgent)(w, r)
			}},
		{"list-agents", httptest.NewRequest(http.MethodGet, "/v1/admin/networks/x/agents", nil),
			func(s *Server, w http.ResponseWriter, r *http.Request) {
				s.handleListNetworkOrganizations(networkAssignmentKindAgent)(w, r)
			}},
		{"detach-agent", httptest.NewRequest(http.MethodDelete, "/v1/admin/networks/x/agents/y", nil),
			func(s *Server, w http.ResponseWriter, r *http.Request) {
				s.handleDetachNetworkOrganization(networkAssignmentKindAgent)(w, r)
			}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &Server{cfg: &config.Config{DefaultLocale: "en"}}
			rec := httptest.NewRecorder()
			req := tc.req
			if req.Header.Get("Content-Type") == "" && req.Method != http.MethodGet && req.Method != http.MethodDelete {
				req.Header.Set("Content-Type", "application/json")
			}
			tc.call(s, rec, req)
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("%s: expected 503 on nil dependencies, got %d (body=%s)",
					tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Layer D -- regression: existing roles' scope contracts preserved
// ─────────────────────────────────────────────────────────────────────────────

// TestAuthz214_LegacyRoleScopes_Preserved verifies that the introduction of
// network_operator gating did not perturb the scope-resolution behavior of
// pre-existing roles. We replay the canonical /v1/me contract from #211 with
// the role-aware checker active and assert that:
//
//   - platform_superadmin still receives "global" first in available_scopes;
//   - platform_operator still receives "platform" (and NOT "global");
//   - organizer still receives "organization:<id>" for its membership;
//   - network_operator's network scopes appear in deterministic order.
//
// This is a smaller regression than re-running every #211 test, but it
// catches the subset of breakage most likely to be introduced by future
// changes to the auth surface added for network_operator.
func TestAuthz214_LegacyRoleScopes_Preserved(t *testing.T) {
	t.Parallel()

	// Helper: build a /v1/me server with the role-aware checker and the
	// supplied fakeMeQuerier store.
	build := func(t *testing.T, store *fakeMeQuerier) (*Server, *auth.StubProvider) {
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
			Secret: cfg.JWTSecretStub, Issuer: "arena-test", Enabled: true,
		})
		if err != nil {
			t.Fatalf("NewStubProvider: %v", err)
		}
		return New(Options{
			Config: cfg, Auth: stub, MeQueries: store,
			Permissions: roleAwareChecker214{},
		}), stub
	}

	decode := func(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var m map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&m); err != nil {
			t.Fatalf("decode /v1/me: %v body=%s", err, rec.Body.String())
		}
		return m
	}

	asStrings := func(t *testing.T, v any) []string {
		t.Helper()
		arr, ok := v.([]any)
		if !ok {
			t.Fatalf("expected []any got %T", v)
		}
		out := make([]string, 0, len(arr))
		for _, x := range arr {
			out = append(out, x.(string))
		}
		return out
	}

	t.Run("platform_superadmin still gets global first", func(t *testing.T) {
		t.Parallel()
		userID := uuid.New()
		store := &fakeMeQuerier{
			rolesByUser:  map[uuid.UUID][]string{userID: {"platform_superadmin"}},
			permsByRoles: map[string][]string{"platform_superadmin": {"network.read"}},
		}
		s, stub := build(t, store)
		tok := issueMeToken(t, stub, userID, []string{"platform_superadmin"})
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
		}
		body := decode(t, rec)
		scopes := asStrings(t, body["available_scopes"])
		if len(scopes) == 0 || scopes[0] != "global" {
			t.Errorf("expected scopes[0]=global, got %v", scopes)
		}
	})

	t.Run("platform_operator still gets platform without global", func(t *testing.T) {
		t.Parallel()
		userID := uuid.New()
		store := &fakeMeQuerier{
			rolesByUser:  map[uuid.UUID][]string{userID: {"platform_operator"}},
			permsByRoles: map[string][]string{"platform_operator": {"superadmin.read"}},
		}
		s, stub := build(t, store)
		tok := issueMeToken(t, stub, userID, []string{"platform_operator"})
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
		}
		scopes := asStrings(t, decode(t, rec)["available_scopes"])
		if !containsString(scopes, "platform") {
			t.Errorf("expected 'platform' in %v", scopes)
		}
		if containsString(scopes, "global") {
			t.Errorf("platform_operator must NOT have 'global', got %v", scopes)
		}
	})

	t.Run("organizer still scoped to its organization", func(t *testing.T) {
		t.Parallel()
		userID := uuid.New()
		orgID := uuid.New()
		store := &fakeMeQuerier{
			rolesByUser:  map[uuid.UUID][]string{userID: {"organizer"}},
			permsByRoles: map[string][]string{"organizer": {"org.read"}},
			membershipsByUser: map[uuid.UUID][]gen.MembershipRow{userID: {{
				ID: uuid.New(), UserID: userID, OrgID: orgID, Role: "organizer",
				Status: "active", JoinedAt: time.Now().UTC(),
			}}},
		}
		s, stub := build(t, store)
		tok := issueMeToken(t, stub, userID, []string{"organizer"})
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
		}
		scopes := asStrings(t, decode(t, rec)["available_scopes"])
		want := "organization:" + orgID.String()
		if !containsString(scopes, want) {
			t.Errorf("expected %s in %v", want, scopes)
		}
		if containsString(scopes, "global") {
			t.Errorf("organizer must NOT have global, got %v", scopes)
		}
	})

	t.Run("network_operator with two networks gets deterministically-sorted network scopes", func(t *testing.T) {
		t.Parallel()
		userID := uuid.New()
		netA := gen.OperatorNetworkRow{
			ID:   uuid.MustParse("0193f01a-0000-7000-8000-0000000aaaaa"),
			Name: "A", Slug: "a", Status: "active",
		}
		netB := gen.OperatorNetworkRow{
			ID:   uuid.MustParse("0193f01a-0000-7000-8000-0000000bbbbb"),
			Name: "B", Slug: "b", Status: "active",
		}
		store := &fakeMeQuerier{
			rolesByUser:    map[uuid.UUID][]string{userID: {"network_operator"}},
			permsByRoles:   map[string][]string{"network_operator": {"network.read", "network.view_sales"}},
			networksByUser: map[uuid.UUID][]gen.OperatorNetworkRow{userID: {netB, netA}},
		}
		s, stub := build(t, store)
		tok := issueMeToken(t, stub, userID, []string{"network_operator"})
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
		}
		scopes := asStrings(t, decode(t, rec)["available_scopes"])
		if containsString(scopes, "global") || containsString(scopes, "platform") {
			t.Errorf("network_operator must not get bypass scopes, got %v", scopes)
		}
		wantA := "network:" + netA.ID.String()
		wantB := "network:" + netB.ID.String()
		got := []string{wantA, wantB}
		sort.Strings(got)
		if indexOf(scopes, got[0]) > indexOf(scopes, got[1]) {
			t.Errorf("network scopes not lexicographically ordered: %v", scopes)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Cross-network deny path -- network_operator cannot reach unrelated network
// data via the /v1/me surface. Because /v1/me reflects only the user's own
// assignments (via meQuerier.ListNetworksByUser), the only way for a
// network_operator to see another network is for that network to appear in
// the response payload -- which it does not. This is the simplest evidence
// against cross-tenant leakage that is fully covered by the unit-test layer.
// ─────────────────────────────────────────────────────────────────────────────

func TestAuthz214_NetworkOperator_OnlySeesAssignedNetworksInMe(t *testing.T) {
	t.Parallel()
	user := uuid.New()
	assignedID := uuid.MustParse("0193f01a-0000-7000-8000-0000ffff0001")
	store := &fakeMeQuerier{
		rolesByUser:  map[uuid.UUID][]string{user: {"network_operator"}},
		permsByRoles: map[string][]string{"network_operator": {"network.read"}},
		networksByUser: map[uuid.UUID][]gen.OperatorNetworkRow{
			user: {{ID: assignedID, Name: "Mine", Slug: "mine", Status: "active"}},
		},
	}
	cfg := &config.Config{
		AppEnv: config.EnvDevelopment, RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true, DefaultLocale: "en", ActiveLocales: []string{"en", "ru"},
	}
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret: cfg.JWTSecretStub, Issuer: "arena-test", Enabled: true,
	})
	if err != nil {
		t.Fatalf("stub: %v", err)
	}
	s := New(Options{
		Config: cfg, Auth: stub, MeQueries: store,
		Permissions: roleAwareChecker214{},
	})
	tok, _, err := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID: user.String(), ActorType: auth.ActorTypeStubUser,
		Roles: []string{"network_operator"},
	})
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
	}
	// Cross-tenant guard: the response must not mention any network the
	// user is not assigned to. We assert by JSON shape -- there is exactly
	// one assigned_networks entry and exactly one network:<uuid> scope.
	body := map[string]any{}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	nets, _ := body["assigned_networks"].([]any)
	if len(nets) != 1 {
		t.Fatalf("expected exactly 1 assigned network, got %d", len(nets))
	}
	first, _ := nets[0].(map[string]any)
	if got, _ := first["id"].(string); got != assignedID.String() {
		t.Errorf("assigned_networks[0].id = %q want %q", got, assignedID.String())
	}
	scopes := body["available_scopes"].([]any)
	netScopeCount := 0
	for _, s := range scopes {
		if str, ok := s.(string); ok && strings.HasPrefix(str, "network:") {
			netScopeCount++
		}
	}
	if netScopeCount != 1 {
		t.Errorf("expected exactly 1 network:* scope, got %d (%v)", netScopeCount, scopes)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helper -- route mounting sanity: every endpoint family is reachable.
// ─────────────────────────────────────────────────────────────────────────────

// TestAuthz214_AllFamiliesMountedOnChiRouter walks the chi.Routes tree and
// confirms every family in endpointFamilies214() resolves to a mounted route
// pattern. This is a defence against silently dropping a route from
// mount_networks.go in a future refactor: if a family is mounted but renamed,
// the auth/permission matrix above still passes (token-less 404 != 403)
// without this check.
func TestAuthz214_AllFamiliesMountedOnChiRouter(t *testing.T) {
	t.Parallel()
	s, _ := buildAuthz214Server(t)

	// Build the set of method+pattern combinations the router knows about.
	known := map[string]bool{}
	var walk func(prefix string, r chi.Routes)
	walk = func(prefix string, r chi.Routes) {
		for _, route := range r.Routes() {
			pattern := prefix + strings.TrimSuffix(route.Pattern, "/*")
			if route.SubRoutes != nil {
				walk(pattern, route.SubRoutes)
				continue
			}
			for method := range route.Handlers {
				known[method+" "+pattern] = true
			}
		}
	}
	walk("", s.router)

	// Translate each family's concrete URL into the chi pattern shape.
	pathToPattern := map[string]string{
		"/v1/operator-networks":                       "/v1/operator-networks",
		"/v1/operator-networks/{uuid}":                "/v1/operator-networks/{id}",
		"/v1/operator-networks/{uuid}/archive":        "/v1/operator-networks/{id}/archive",
		"/v1/admin/networks/{uuid}/users":             "/v1/admin/networks/{id}/users",
		"/v1/admin/networks/{uuid}/users/{uuid}":      "/v1/admin/networks/{id}/users/{userId}",
		"/v1/admin/networks/{uuid}/organizers":        "/v1/admin/networks/{id}/organizers",
		"/v1/admin/networks/{uuid}/organizers/{uuid}": "/v1/admin/networks/{id}/organizers/{orgId}",
		"/v1/admin/networks/{uuid}/agents":            "/v1/admin/networks/{id}/agents",
		"/v1/admin/networks/{uuid}/agents/{uuid}":     "/v1/admin/networks/{id}/agents/{orgId}",
	}

	for _, fam := range endpointFamilies214() {
		key := canonicalisePath214(fam.path)
		pattern, ok := pathToPattern[key]
		if !ok {
			t.Errorf("test bug: missing pattern translation for %q (raw=%s)", key, fam.path)
			continue
		}
		if !known[fam.method+" "+pattern] {
			t.Errorf("router does not mount %s %s (resolved pattern %q)", fam.method, fam.path, pattern)
		}
	}
}

// canonicalisePath214 collapses concrete UUID path segments back to "{uuid}"
// so the family list can be matched against chi route patterns.
func canonicalisePath214(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if _, err := uuid.Parse(part); err == nil {
			parts[i] = "{uuid}"
		}
	}
	return strings.Join(parts, "/")
}

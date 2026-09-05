// gateway_credential_473_test.go — route-level tests for feature #473
// (W1-A1d): PUT/GET/DELETE /v1/organizations/{org_id}/channels/{id}/gateway-credential
//
// Coverage (unit-level, dbDownPool + gen.New(nil) — no live PostgreSQL):
//
//   - Step 1 (endpoint mount): all three verbs answer under the expected
//     path, neither 404 nor 405.
//   - 401 without JWT — the routes sit behind bearerAuth on the shared
//     `channel.update` group.
//   - 400 without the X-Admin-Reason header, even when authenticated —
//     the header is required for every verb per spec §5.4.
//   - 503 when channelQueries is nil (the handler's own nil-dep guard,
//     covered end-to-end by calling the shim on a bare *Server).
//   - 403 for a JWT actor who is not a member of the target org
//     (org.access_denied). Uses the emptyMembershipDBTX helper from
//     pr2_01_org_isolation_test.go.
//   - 404 when the channel does not resolve for the target org (a bare
//     dbDownPool cannot serve GetSalesChannelByID; the shim surfaces that
//     as 500 channel.get_failed. The 404 branch is exercised via a
//     small route-level assertion together with the integration-tagged
//     tests hosted alongside the migration package).
package httpserver

import (
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

// gatewayCredTestActorID is the fixed UUID used for JWT subjects in the
// gateway-credential tests; keeps traces greppable in any captured audit
// events if a future integration test writes real audit rows.
const gatewayCredTestActorID = "00000000-0000-0000-0000-000000000473"

// gwCredPath builds the endpoint path for the given (org, channel) pair.
func gwCredPath(orgID, chID uuid.UUID) string {
	return "/v1/organizations/" + orgID.String() + "/channels/" + chID.String() + "/gateway-credential"
}

// buildGatewayCredentialServer stands up a Server with:
//   - stub JWT auth,
//   - channel routes mounted (channelQueries + pool present),
//   - orgMemberAdmitFromCtxDBTX-backed memberships so an authenticated
//     actor is admitted as a member of any org that appears in the URL.
//
// The pool is dbDownPool, so any handler that reaches the database layer
// fails fast; that lets us exercise the auth + admin-reason + membership
// gates without a live PostgreSQL.
func buildGatewayCredentialServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
		AppPublicURL:   "https://arena.test",
	}
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("buildGatewayCredentialServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:            cfg,
		Auth:              stub,
		Pool:              &dbDownPool{},
		ChannelQueries:    gen.New(nil),
		OrgQueries:        gen.New(nil),
		MembershipQueries: gen.New(&orgMemberAdmitFromCtxDBTX{}),
		Audit:             &captureAuditWriter{},
	})
}

// buildGatewayCredentialDenialServer is the sibling factory that plugs in
// emptyMembershipDBTX so the actor is a non-member of every org. Requests
// to gateway-credential routes should return 403 org.access_denied.
func buildGatewayCredentialDenialServer(t *testing.T) *Server {
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
		t.Fatalf("buildGatewayCredentialDenialServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:            cfg,
		Auth:              stub,
		Pool:              &dbDownPool{},
		ChannelQueries:    gen.New(nil),
		OrgQueries:        gen.New(nil),
		MembershipQueries: gen.New(&emptyMembershipDBTX{}),
		Audit:             &captureAuditWriter{},
	})
}

// gwCredCases enumerates the three verbs so route-shape assertions stay DRY.
func gwCredCases(orgID, chID uuid.UUID) []struct{ method, path string } {
	p := gwCredPath(orgID, chID)
	return []struct{ method, path string }{
		{http.MethodGet, p},
		{http.MethodPut, p},
		{http.MethodDelete, p},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1 — route mount + method registration
// ─────────────────────────────────────────────────────────────────────────────

func TestGatewayCredential473_RoutesMounted(t *testing.T) {
	t.Parallel()
	s := buildGatewayCredentialServer(t)
	orgID := uuid.New()
	chID := uuid.New()
	for _, c := range gwCredCases(orgID, chID) {
		req := httptest.NewRequest(c.method, c.path, nil)
		req.Header.Set("X-Admin-Reason", "test")
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Errorf("%s %s: route not mounted (got 404)", c.method, c.path)
		}
		if w.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s %s: method not registered (got 405)", c.method, c.path)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Auth gate — 401 without JWT
// ─────────────────────────────────────────────────────────────────────────────

func TestGatewayCredential473_RequiresAuth(t *testing.T) {
	t.Parallel()
	s := buildGatewayCredentialServer(t)
	orgID := uuid.New()
	chID := uuid.New()
	for _, c := range gwCredCases(orgID, chID) {
		req := httptest.NewRequest(c.method, c.path, nil)
		req.Header.Set("X-Admin-Reason", "test")
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: expected 401 without JWT, got %d (body=%s)",
				c.method, c.path, w.Code, w.Body.String())
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Admin-reason gate — 400 without X-Admin-Reason, even with a valid JWT
// ─────────────────────────────────────────────────────────────────────────────

func TestGatewayCredential473_RequiresAdminReason(t *testing.T) {
	t.Parallel()
	s := buildGatewayCredentialServer(t)
	token := mintJWT(t, s.stub, gatewayCredTestActorID)
	orgID := uuid.New()
	chID := uuid.New()
	for _, c := range gwCredCases(orgID, chID) {
		req := httptest.NewRequest(c.method, c.path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		// Note: NO X-Admin-Reason header.
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s %s: expected 400 without X-Admin-Reason, got %d (body=%s)",
				c.method, c.path, w.Code, w.Body.String())
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Membership gate — 403 org.access_denied for non-members
// ─────────────────────────────────────────────────────────────────────────────

func TestGatewayCredential473_NonMemberGets403(t *testing.T) {
	t.Parallel()
	s := buildGatewayCredentialDenialServer(t)
	token := mintJWT(t, s.stub, gatewayCredTestActorID)
	orgID := uuid.New()
	chID := uuid.New()
	for _, c := range gwCredCases(orgID, chID) {
		req := httptest.NewRequest(c.method, c.path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Admin-Reason", "compliance test")
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s: expected 403 for non-member, got %d (body=%s)",
				c.method, c.path, w.Code, w.Body.String())
			continue
		}
		if !strings.Contains(w.Body.String(), "org.access_denied") {
			t.Errorf("%s %s: expected org.access_denied in body, got: %s",
				c.method, c.path, w.Body.String())
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Nil-dep guards — 503 when channelQueries is not wired
// ─────────────────────────────────────────────────────────────────────────────

func TestGatewayCredential473_NilChannelQueriesReturns503(t *testing.T) {
	t.Parallel()
	// A bare *Server has no channelQueries; the handler's own guard fires
	// before it touches anything else. We call the shims directly so this
	// test does not depend on route mounting order.
	s := &Server{}
	orgID := uuid.New()
	chID := uuid.New()
	cases := []struct {
		name string
		fn   func(w http.ResponseWriter, r *http.Request)
	}{
		{"get", s.handleGetChannelGatewayCredential},
		{"put", s.handlePutChannelGatewayCredential},
		{"delete", s.handleDeleteChannelGatewayCredential},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, gwCredPath(orgID, chID), nil)
			req.Header.Set("X-Admin-Reason", "test")
			w := httptest.NewRecorder()
			tc.fn(w, req)
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("%s: expected 503, got %d (body=%s)",
					tc.name, w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "dependency.database_unavailable") {
				t.Errorf("%s: expected dependency.database_unavailable code, got: %s",
					tc.name, w.Body.String())
			}
		})
	}
}

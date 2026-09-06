// wp_webhook_507_test.go — route-level tests for feature #507 (W1-B7d):
// PUT/GET/DELETE /v1/organizations/{org_id}/channels/{id}/wp-webhook
//
// Coverage (unit-level, dbDownPool + gen.New(nil) — no live PostgreSQL),
// mirroring gateway_credential_473_test.go:
//
//   - route mount: all three verbs answer under the expected path, neither
//     404 nor 405.
//   - 401 without JWT.
//   - 400 without the X-Admin-Reason header, even when authenticated.
//   - 403 for a JWT actor who is not a member of the target org.
//   - 503 when channelQueries is nil (the handler's own nil-dep guard).
//
// The synchronous test-delivery flow (PUT against a live callback URL,
// GET/DELETE lifecycle against a real subscriber row) needs a live
// PostgreSQL and is covered separately by the integration-tagged test in
// wp_webhook_507_integration_test.go.
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

// wpWebhookTestActorID is the fixed UUID used for JWT subjects in these
// tests; keeps traces greppable in any captured audit events.
const wpWebhookTestActorID = "00000000-0000-0000-0000-000000000507"

// wpWebhookPath builds the endpoint path for the given (org, channel) pair.
func wpWebhookPath(orgID, chID uuid.UUID) string {
	return "/v1/organizations/" + orgID.String() + "/channels/" + chID.String() + "/wp-webhook"
}

// buildWPWebhookServer stands up a Server with stub JWT auth, channel routes
// mounted, and orgMemberAdmitFromCtxDBTX-backed memberships so an
// authenticated actor is admitted as a member of any org in the URL. The
// pool is dbDownPool, so any handler that reaches the database layer fails
// fast; that lets us exercise the auth + admin-reason + membership gates
// without a live PostgreSQL.
func buildWPWebhookServer(t *testing.T) *Server {
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
		t.Fatalf("buildWPWebhookServer: NewStubProvider: %v", err)
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

// buildWPWebhookDenialServer is the sibling factory that plugs in
// emptyMembershipDBTX so the actor is a non-member of every org. Requests to
// wp-webhook routes should return 403 org.access_denied.
func buildWPWebhookDenialServer(t *testing.T) *Server {
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
		t.Fatalf("buildWPWebhookDenialServer: NewStubProvider: %v", err)
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

// wpWebhookCases enumerates the three verbs so route-shape assertions stay DRY.
func wpWebhookCases(orgID, chID uuid.UUID) []struct{ method, path string } {
	p := wpWebhookPath(orgID, chID)
	return []struct{ method, path string }{
		{http.MethodGet, p},
		{http.MethodPut, p},
		{http.MethodDelete, p},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Route mount + method registration
// ─────────────────────────────────────────────────────────────────────────────

func TestWPWebhook507_RoutesMounted(t *testing.T) {
	t.Parallel()
	s := buildWPWebhookServer(t)
	orgID := uuid.New()
	chID := uuid.New()
	for _, c := range wpWebhookCases(orgID, chID) {
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

func TestWPWebhook507_RequiresAuth(t *testing.T) {
	t.Parallel()
	s := buildWPWebhookServer(t)
	orgID := uuid.New()
	chID := uuid.New()
	for _, c := range wpWebhookCases(orgID, chID) {
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

func TestWPWebhook507_RequiresAdminReason(t *testing.T) {
	t.Parallel()
	s := buildWPWebhookServer(t)
	token := mintJWT(t, s.stub, wpWebhookTestActorID)
	orgID := uuid.New()
	chID := uuid.New()
	for _, c := range wpWebhookCases(orgID, chID) {
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

func TestWPWebhook507_NonMemberGets403(t *testing.T) {
	t.Parallel()
	s := buildWPWebhookDenialServer(t)
	token := mintJWT(t, s.stub, wpWebhookTestActorID)
	orgID := uuid.New()
	chID := uuid.New()
	for _, c := range wpWebhookCases(orgID, chID) {
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

func TestWPWebhook507_NilChannelQueriesReturns503(t *testing.T) {
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
		{"get", s.handleGetChannelWPWebhook},
		{"put", s.handlePutChannelWPWebhook},
		{"delete", s.handleDeleteChannelWPWebhook},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, wpWebhookPath(orgID, chID), nil)
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

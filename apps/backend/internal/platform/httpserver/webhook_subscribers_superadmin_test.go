// webhook_subscribers_superadmin_test.go — route-level tests for the
// superadmin-only webhook subscriber registry:
//
//	GET    /v1/webhooks/subscribers
//	POST   /v1/webhooks/subscribers
//	GET    /v1/webhooks/subscribers/{id}
//	PATCH  /v1/webhooks/subscribers/{id}
//	DELETE /v1/webhooks/subscribers/{id}
//	GET    /v1/webhooks/subscribers/{id}/recent-deliveries
//
// The registry is platform-wide (a subscriber row names no organization) and
// POST hands out a signing secret, so holding webhook.subscriber.manage is
// not enough: migration 0040 grants that permission to org_admin, and an
// organization owner must still be refused with 403 superadmin.required.
//
// Coverage (unit-level, dbDownPool + gen.New(nil) — no live PostgreSQL),
// mirroring wp_webhook_507_test.go:
//
//   - 401 without JWT.
//   - 403 superadmin.required for an org_admin JWT on every route.
//   - the platform superadmin passes the gate (neither 401 nor 403); the
//     handler then fails on the database, which is not what is under test.
//   - the source still installs the gate on both route groups.
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

// webhookSubSuperadminActorID is the fixed UUID used for JWT subjects in these
// tests; keeps traces greppable in any captured audit events.
const webhookSubSuperadminActorID = "00000000-0000-0000-0000-000000000294"

// buildWebhookSubSuperadminServer stands up a Server with stub JWT auth and
// the webhook subscriber routes mounted. The pool is dbDownPool, so a request
// that clears the auth gates fails fast in the handler instead of needing a
// live PostgreSQL.
func buildWebhookSubSuperadminServer(t *testing.T) *Server {
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
		t.Fatalf("buildWebhookSubSuperadminServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:            cfg,
		Auth:              stub,
		Pool:              &dbDownPool{},
		WebhookSubQueries: gen.New(nil),
		Audit:             &captureAuditWriter{},
	})
}

// mintWebhookSubToken mints a dev JWT carrying the given roles claim.
func mintWebhookSubToken(t *testing.T, s *Server, role string) string {
	t.Helper()
	body := `{"actor_id":"` + webhookSubSuperadminActorID + `","roles":["` + role + `"]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintWebhookSubToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintWebhookSubToken: decode: %v", err)
	}
	tok := resp["token"]
	if tok == "" {
		t.Fatalf("mintWebhookSubToken: no token in response")
	}
	return tok
}

// webhookSubCases enumerates every registry route so the gate assertions stay
// DRY. Bodies are valid JSON so a request that clears the gate reaches the
// handler's database call rather than a 400.
func webhookSubCases() []struct{ method, path, body string } {
	id := uuid.New().String()
	return []struct{ method, path, body string }{
		{http.MethodGet, "/v1/webhooks/subscribers", ""},
		{http.MethodPost, "/v1/webhooks/subscribers", `{"callback_url":"https://wp.example.com/wp-json/arena-events/v1/webhook"}`},
		{http.MethodGet, "/v1/webhooks/subscribers/" + id, ""},
		{http.MethodPatch, "/v1/webhooks/subscribers/" + id, `{"active":false}`},
		{http.MethodDelete, "/v1/webhooks/subscribers/" + id, ""},
		{http.MethodGet, "/v1/webhooks/subscribers/" + id + "/recent-deliveries", ""},
	}
}

func serveWebhookSub(s *Server, method, path, body, token string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// ─────────────────────────────────────────────────────────────────────────────
// Auth gate — 401 without JWT
// ─────────────────────────────────────────────────────────────────────────────

func TestWebhookSubscribersSuperadmin_RequiresAuth(t *testing.T) {
	t.Parallel()
	s := buildWebhookSubSuperadminServer(t)
	for _, c := range webhookSubCases() {
		w := serveWebhookSub(s, c.method, c.path, c.body, "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: expected 401 without JWT, got %d (body=%s)",
				c.method, c.path, w.Code, w.Body.String())
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Superadmin gate — an organization owner is refused on every route
// ─────────────────────────────────────────────────────────────────────────────

func TestWebhookSubscribersSuperadmin_OrgOwnerGets403(t *testing.T) {
	t.Parallel()
	s := buildWebhookSubSuperadminServer(t)
	token := mintWebhookSubToken(t, s, "org_admin")
	for _, c := range webhookSubCases() {
		w := serveWebhookSub(s, c.method, c.path, c.body, token)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s: expected 403 for an org owner, got %d (body=%s)",
				c.method, c.path, w.Code, w.Body.String())
			continue
		}
		if !strings.Contains(w.Body.String(), "superadmin.required") {
			t.Errorf("%s %s: expected superadmin.required in body, got: %s",
				c.method, c.path, w.Body.String())
		}
	}
}

func TestWebhookSubscribersSuperadmin_SuperadminPassesGate(t *testing.T) {
	t.Parallel()
	s := buildWebhookSubSuperadminServer(t)
	token := mintWebhookSubToken(t, s, "platform_superadmin")
	for _, c := range webhookSubCases() {
		w := serveWebhookSub(s, c.method, c.path, c.body, token)
		switch w.Code {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed:
			t.Errorf("%s %s: superadmin should pass the gate, got %d (body=%s)",
				c.method, c.path, w.Code, w.Body.String())
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Source structure — both route groups install the gate after applyAuth
// ─────────────────────────────────────────────────────────────────────────────

func TestWebhookSubscribersSuperadmin_MountInstallsGate(t *testing.T) {
	content := findFileByName(t, "mount_admin.go")
	start := strings.Index(content, "func (s *Server) mountWebhookSubscriberRoutes(")
	if start < 0 {
		t.Fatal("mount_admin.go: mountWebhookSubscriberRoutes not found")
	}
	body := content[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	if got := strings.Count(body, "pr.Use(requirePlatformSuperadminMW)"); got != 2 {
		t.Errorf("mountWebhookSubscriberRoutes: expected the superadmin gate on both route groups, found %d", got)
	}
	if strings.Index(body, "pr.Use(requirePlatformSuperadminMW)") < strings.Index(body, "s.applyAuth(") {
		t.Error("mountWebhookSubscriberRoutes: the superadmin gate must come after applyAuth, which sets the superadmin marker")
	}
}

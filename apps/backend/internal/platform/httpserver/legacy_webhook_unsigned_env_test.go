// legacy_webhook_unsigned_env_test.go — the legacy un-suffixed webhook routes
// POST /v1/payment-intents/webhook and POST /v1/refunds/webhook answer 401
// under APP_ENV staging and production when no secret at all can verify a
// delivery; under APP_ENV development an unsigned body is still processed
// (the local mock-provider mode).
//
// Pure unit tests: the server runs on a dbDownPool and the bodies stay on
// paths that never reach the database.
package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

func buildLegacyWebhookEnvServer(t *testing.T, env config.AppEnv) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:         env,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en"},
	}
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: false,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}
	return New(Options{
		Config:               cfg,
		Auth:                 stub,
		Pool:                 &dbDownPool{},
		RefundQueries:        gen.New(nil),
		PaymentIntentQueries: gen.New(nil),
		TicketQueries:        gen.New(nil),
	})
}

func postLegacyWebhook(s *Server, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	return w
}

// An unknown event type on the payment route and a body without
// provider_refund_id on the refund route are answered without the database:
// 200 and 400 once the signature check lets them through.
const (
	legacyUnsignedPaymentBody = `{"id":"evt_env_1","type":"payment_intent.created","data":{"object":{"id":"pi_env_1","status":"requires_payment_method"}}}`
	legacyUnsignedRefundBody  = `{"event_type":"mock.refund.succeeded","refund_id":"00000000-0000-0000-0000-000000000001"}`
)

func TestLegacyWebhook_UnsignedRefusedOutsideDevelopment(t *testing.T) {
	for _, env := range []config.AppEnv{config.EnvStaging, config.EnvProduction} {
		t.Run(string(env), func(t *testing.T) {
			s := buildLegacyWebhookEnvServer(t, env)
			for _, c := range []struct{ path, body string }{
				{"/v1/payment-intents/webhook", legacyUnsignedPaymentBody},
				{"/v1/refunds/webhook", legacyUnsignedRefundBody},
			} {
				w := postLegacyWebhook(s, c.path, c.body)
				if w.Code != http.StatusUnauthorized {
					t.Fatalf("%s unsigned under %s = %d, want 401; body %s", c.path, env, w.Code, w.Body.String())
				}
				if !strings.Contains(w.Body.String(), "webhook.invalid_signature") {
					t.Fatalf("%s: body %s, want webhook.invalid_signature", c.path, w.Body.String())
				}
			}
		})
	}
}

func TestLegacyWebhook_UnsignedAcceptedInDevelopment(t *testing.T) {
	s := buildLegacyWebhookEnvServer(t, config.EnvDevelopment)
	if w := postLegacyWebhook(s, "/v1/payment-intents/webhook", legacyUnsignedPaymentBody); w.Code != http.StatusOK {
		t.Fatalf("payment route unsigned under development = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if w := postLegacyWebhook(s, "/v1/refunds/webhook", legacyUnsignedRefundBody); w.Code != http.StatusBadRequest {
		t.Fatalf("refund route unsigned under development = %d, want 400; body %s", w.Code, w.Body.String())
	}
}

// webhook_auth_362_test.go — unit tests for feature #362 (PR2-06 BLOCKER):
// Authenticate payment and refund webhooks via provider HMAC signature.
//
// Test coverage (4 steps):
//
//	Step 1: Verify provider signature (Stripe/AllPay HMAC) before processing body.
//	Step 2: Reject unsigned/invalid-signature requests with 401 and no state change.
//	Step 3: Keep non-production mock path gated by empty secrets (both empty = dev mode).
//	Step 4: Test forged body rejection and valid signed event happy path.
//
// All tests are pure unit tests — no live PostgreSQL required.
// The Handler under test is constructed with a nil *gen.Queries (database operations
// never execute because the guard checks are hit before DB lookups).
package hcheckout

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

// buildHandlerWithSecrets constructs a minimal Handler with only the webhook
// secrets wired. All *gen.Queries fields are nil so DB calls never execute.
func buildHandlerWithSecrets(stripeSecret, allPaySecret string) *Handler {
	return &Handler{
		webhookStripeSecret: stripeSecret,
		webhookAllPaySecret: allPaySecret,
	}
}

// stripeSignatureHeader computes a valid Stripe-Signature header value for body
// and secret, using the current Unix time as the timestamp.
func stripeSignatureHeader(secret string, body []byte) string {
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("t=%d,v1=%s", ts, sig)
}

// allPaySignatureHeader computes a valid X-AllPay-Signature header value for
// body and secret (lowercase hex HMAC-SHA256 of the raw body).
func allPaySignatureHeader(secret string, body []byte) string {
	return payments.ComputeHMACSHA256(secret, body)
}

// webhookPIBody builds a minimal payment-intent webhook JSON body that passes
// field-validation inside HandlePaymentIntentWebhook. We don't need a real DB
// because the handler reaches signature verification BEFORE the DB lookups.
// Note: verifyWebhookSignature runs before JSON unmarshal when the secrets are
// wired, so even a minimal body is sufficient for the rejection tests; for the
// accept tests we also need the body to pass JSON parsing and field validation.
func webhookPIBody(providerPaymentID, eventType string) []byte {
	b, _ := json.Marshal(map[string]string{
		"provider_payment_id": providerPaymentID,
		"event_type":          eventType,
	})
	return b
}

// webhookRefundBody builds a minimal refund webhook JSON body.
func webhookRefundBody(providerRefundID, refundID, eventType string) []byte {
	b, _ := json.Marshal(map[string]string{
		"provider_refund_id": providerRefundID,
		"refund_id":          refundID,
		"event_type":         eventType,
	})
	return b
}

// postWebhookRequest constructs an httptest.Request for the given webhook URL
// with optional headers set.
func postWebhookRequest(url string, body []byte, headers map[string]string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1 — verifyWebhookSignature: Stripe HMAC verification
// ─────────────────────────────────────────────────────────────────────────────

// TestWH362_Step1_StripeValidSignatureAccepted verifies that a correctly signed
// Stripe-Signature header passes verification.
func TestWH362_Step1_StripeValidSignatureAccepted(t *testing.T) {
	t.Parallel()
	secret := "whsec_test_secret_for_362"
	body := []byte(`{"event_type":"payment_intent.succeeded"}`)
	h := buildHandlerWithSecrets(secret, "")

	req := postWebhookRequest("/v1/payment-intents/webhook", body,
		map[string]string{"Stripe-Signature": stripeSignatureHeader(secret, body)},
	)

	if err := h.verifyWebhookSignature(req, body); err != nil {
		t.Errorf("valid Stripe signature rejected: %v", err)
	}
}

// TestWH362_Step1_StripeWrongSecretRejected verifies that a Stripe-Signature
// signed with a DIFFERENT secret is rejected.
func TestWH362_Step1_StripeWrongSecretRejected(t *testing.T) {
	t.Parallel()
	realSecret := "whsec_real_secret"
	forgerySecret := "whsec_wrong_secret"
	body := []byte(`{"event_type":"payment_intent.succeeded"}`)
	h := buildHandlerWithSecrets(realSecret, "")

	req := postWebhookRequest("/v1/payment-intents/webhook", body,
		map[string]string{"Stripe-Signature": stripeSignatureHeader(forgerySecret, body)},
	)

	if err := h.verifyWebhookSignature(req, body); err == nil {
		t.Error("wanted rejection of wrong-secret Stripe signature, got nil error")
	}
}

// TestWH362_Step1_AllPayValidSignatureAccepted verifies that a correctly
// signed X-AllPay-Signature header passes verification.
func TestWH362_Step1_AllPayValidSignatureAccepted(t *testing.T) {
	t.Parallel()
	secret := "allpay-shared-secret-362"
	body := []byte(`{"event_type":"refund.succeeded"}`)
	h := buildHandlerWithSecrets("", secret)

	req := postWebhookRequest("/v1/refunds/webhook", body,
		map[string]string{"X-AllPay-Signature": allPaySignatureHeader(secret, body)},
	)

	if err := h.verifyWebhookSignature(req, body); err != nil {
		t.Errorf("valid AllPay signature rejected: %v", err)
	}
}

// TestWH362_Step1_AllPayWrongSecretRejected verifies that an AllPay signature
// signed with the wrong secret is rejected.
func TestWH362_Step1_AllPayWrongSecretRejected(t *testing.T) {
	t.Parallel()
	realSecret := "allpay-real-secret"
	body := []byte(`{"event_type":"refund.failed"}`)
	h := buildHandlerWithSecrets("", realSecret)

	req := postWebhookRequest("/v1/refunds/webhook", body,
		map[string]string{"X-AllPay-Signature": allPaySignatureHeader("wrong-secret", body)},
	)

	if err := h.verifyWebhookSignature(req, body); err == nil {
		t.Error("wanted rejection of wrong-secret AllPay signature, got nil error")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2 — Reject unsigned/invalid-signature requests with 401
// ─────────────────────────────────────────────────────────────────────────────

// TestWH362_Step2_NoSignatureHeaderReturns401OnPaymentWebhook verifies that a
// payment-intent webhook with no signature header is rejected with 401 when a
// Stripe secret is configured.
func TestWH362_Step2_NoSignatureHeaderReturns401OnPaymentWebhook(t *testing.T) {
	t.Parallel()
	h := buildHandlerWithSecrets("whsec_configured_secret", "")
	// No Stripe-Signature or X-AllPay-Signature header → must be rejected.
	body := webhookPIBody("pi_test_id", "payment_intent.succeeded")
	req := postWebhookRequest("/v1/payment-intents/webhook", body, nil)

	w := httptest.NewRecorder()
	h.HandlePaymentIntentWebhook(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %d; body: %s", w.Code, w.Body.String())
	}
}

// TestWH362_Step2_NoSignatureHeaderReturns401OnRefundWebhook verifies that a
// refund webhook with no signature header is rejected with 401 when a secret
// is configured.
func TestWH362_Step2_NoSignatureHeaderReturns401OnRefundWebhook(t *testing.T) {
	t.Parallel()
	h := buildHandlerWithSecrets("", "allpay-configured-secret")
	body := webhookRefundBody("re_test", "00000000-0000-0000-0000-000000000001", "refund.succeeded")
	req := postWebhookRequest("/v1/refunds/webhook", body, nil)

	w := httptest.NewRecorder()
	h.HandleRefundWebhook(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %d; body: %s", w.Code, w.Body.String())
	}
}

// TestWH362_Step2_ForgedBodyRejectedWithStripeSignature verifies that a body
// that has been tampered with after signing is rejected (body ≠ signed content).
func TestWH362_Step2_ForgedBodyRejectedWithStripeSignature(t *testing.T) {
	t.Parallel()
	secret := "whsec_test_real"
	originalBody := []byte(`{"event_type":"payment_intent.succeeded","provider_payment_id":"pi_real"}`)
	forgedBody := []byte(`{"event_type":"payment_intent.succeeded","provider_payment_id":"pi_forged"}`)

	// Sign original, then send forged body.
	h := buildHandlerWithSecrets(secret, "")
	req := postWebhookRequest("/v1/payment-intents/webhook", forgedBody,
		map[string]string{"Stripe-Signature": stripeSignatureHeader(secret, originalBody)},
	)

	w := httptest.NewRecorder()
	h.HandlePaymentIntentWebhook(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for tampered body, got %d; body: %s", w.Code, w.Body.String())
	}
}

// TestWH362_Step2_ForgedBodyRejectedWithAllPaySignature verifies that a forged
// body is rejected by the AllPay HMAC path.
func TestWH362_Step2_ForgedBodyRejectedWithAllPaySignature(t *testing.T) {
	t.Parallel()
	secret := "allpay-real-secret-362"
	originalBody := []byte(`{"provider_refund_id":"re_real","event_type":"refund.succeeded"}`)
	forgedBody := []byte(`{"provider_refund_id":"re_FORGED","event_type":"refund.succeeded"}`)

	h := buildHandlerWithSecrets("", secret)
	req := postWebhookRequest("/v1/refunds/webhook", forgedBody,
		map[string]string{"X-AllPay-Signature": allPaySignatureHeader(secret, originalBody)},
	)

	w := httptest.NewRecorder()
	h.HandleRefundWebhook(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for forged AllPay body, got %d; body: %s", w.Code, w.Body.String())
	}
}

// TestWH362_Step2_InvalidSignatureFormatRejected verifies that a malformed
// Stripe-Signature header value is rejected.
func TestWH362_Step2_InvalidSignatureFormatRejected(t *testing.T) {
	t.Parallel()
	h := buildHandlerWithSecrets("whsec_secret", "")
	body := webhookPIBody("pi_test", "payment_intent.succeeded")
	req := postWebhookRequest("/v1/payment-intents/webhook", body,
		map[string]string{"Stripe-Signature": "not-a-valid-stripe-sig"},
	)

	if err := h.verifyWebhookSignature(req, body); err == nil {
		t.Error("expected error for malformed Stripe-Signature header, got nil")
	}
}

// TestWH362_Step2_RejectedWith401NotOtherCodes verifies the rejection response
// is 401 Unauthorized (not 400, 403, or 500) so clients understand it's an
// authentication failure, not a bad request.
func TestWH362_Step2_RejectedWith401NotOtherCodes(t *testing.T) {
	t.Parallel()
	h := buildHandlerWithSecrets("whsec_check_status_code", "")
	body := webhookPIBody("pi_test_401", "payment_intent.succeeded")
	// Send with completely wrong signature value.
	req := postWebhookRequest("/v1/payment-intents/webhook", body,
		map[string]string{"Stripe-Signature": "t=1,v1=deadbeef"},
	)

	w := httptest.NewRecorder()
	h.HandlePaymentIntentWebhook(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401 Unauthorized, got %d", w.Code)
	}
	// Response body must contain an error field (standard envelope).
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if _, hasErr := resp["error"]; !hasErr {
		t.Errorf("response JSON missing 'error' field: %v", resp)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3 — Non-production mock path: gated by both secrets empty
// ─────────────────────────────────────────────────────────────────────────────

// TestWH362_Step3_NoSecretsConfiguredAllowsMockRequests verifies that when
// BOTH webhook secrets are empty, verifyWebhookSignature returns nil (dev/mock
// mode). This path is blocked in production by config.validateProduction.
func TestWH362_Step3_NoSecretsConfiguredAllowsMockRequests(t *testing.T) {
	t.Parallel()
	h := buildHandlerWithSecrets("", "") // dev/mock mode — no secrets
	body := webhookPIBody("pi_mock", "mock.succeeded")
	req := postWebhookRequest("/v1/payment-intents/webhook", body, nil) // no sig header

	if err := h.verifyWebhookSignature(req, body); err != nil {
		t.Errorf("dev/mock mode should allow unsigned requests, got: %v", err)
	}
}

// TestWH362_Step3_OnlyStripeSecretSetRequiresStripeHeader verifies that when
// only the Stripe secret is configured, requests without Stripe-Signature
// are rejected even if they have a different provider header.
func TestWH362_Step3_OnlyStripeSecretSetRequiresStripeHeader(t *testing.T) {
	t.Parallel()
	stripeSecret := "whsec_only_stripe_configured"
	h := buildHandlerWithSecrets(stripeSecret, "") // only Stripe secret set
	body := webhookPIBody("pi_test", "payment_intent.succeeded")

	// Provide an X-AllPay-Signature but no Stripe-Signature → should reject.
	req := postWebhookRequest("/v1/payment-intents/webhook", body,
		map[string]string{"X-AllPay-Signature": "some-allpay-sig"},
	)

	if err := h.verifyWebhookSignature(req, body); err == nil {
		t.Error("wanted rejection when AllPay header present but only Stripe secret configured")
	}
}

// TestWH362_Step3_OnlyAllPaySecretSetRequiresAllPayHeader verifies the
// symmetric case: AllPay-only config rejects Stripe-Signature headers.
func TestWH362_Step3_OnlyAllPaySecretSetRequiresAllPayHeader(t *testing.T) {
	t.Parallel()
	allPaySecret := "allpay-only-configured"
	h := buildHandlerWithSecrets("", allPaySecret)
	body := webhookRefundBody("re_test", "00000000-0000-0000-0000-000000000002", "refund.succeeded")

	// Provide a Stripe-Signature but no X-AllPay-Signature → should reject.
	req := postWebhookRequest("/v1/refunds/webhook", body,
		map[string]string{"Stripe-Signature": "t=1,v1=deadbeef"},
	)

	if err := h.verifyWebhookSignature(req, body); err == nil {
		t.Error("wanted rejection when Stripe header present but only AllPay secret configured")
	}
}

// TestWH362_Step3_MockModeDoesNotRequireAnyHeader verifies that in mock mode
// (both secrets empty) any request passes regardless of headers.
func TestWH362_Step3_MockModeDoesNotRequireAnyHeader(t *testing.T) {
	t.Parallel()
	h := buildHandlerWithSecrets("", "")
	body := []byte(`{"anything":"goes"}`)

	// No headers at all — should pass in dev/mock mode.
	req := postWebhookRequest("/v1/payment-intents/webhook", body, nil)
	if err := h.verifyWebhookSignature(req, body); err != nil {
		t.Errorf("mock mode: unexpected error: %v", err)
	}

	// Even a garbage header shouldn't cause a failure in dev mode.
	req2 := postWebhookRequest("/v1/payment-intents/webhook", body,
		map[string]string{"Stripe-Signature": "garbage"},
	)
	if err := h.verifyWebhookSignature(req2, body); err != nil {
		t.Errorf("mock mode with garbage header: unexpected error: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4 — Forged body rejection + valid signed event happy path
// ─────────────────────────────────────────────────────────────────────────────

// TestWH362_Step4_ValidStripeWebhookReachesDBLayer verifies that a correctly
// signed payment-intent webhook passes the auth gate and reaches the DB layer
// (where it will fail because DB is nil — expected 503, not 401).
func TestWH362_Step4_ValidStripeWebhookReachesDBLayer(t *testing.T) {
	t.Parallel()
	secret := "whsec_step4_real_secret"
	body := webhookPIBody("pi_step4", "payment_intent.succeeded")
	h := buildHandlerWithSecrets(secret, "")

	req := postWebhookRequest("/v1/payment-intents/webhook", body,
		map[string]string{"Stripe-Signature": stripeSignatureHeader(secret, body)},
	)

	w := httptest.NewRecorder()
	h.HandlePaymentIntentWebhook(w, req)

	// Must NOT be 401 (auth rejected).
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("valid Stripe signature incorrectly rejected (401): %s", w.Body.String())
	}
	// With nil DB the handler returns 503 Service Unavailable — that means
	// the request got past the signature gate as expected.
	if w.Code != http.StatusServiceUnavailable {
		t.Logf("note: got %d (not 503) — DB gate may have changed; body: %s", w.Code, w.Body.String())
	}
}

// TestWH362_Step4_ValidAllPayRefundWebhookReachesDBLayer verifies that a
// correctly signed AllPay refund webhook passes the auth gate.
func TestWH362_Step4_ValidAllPayRefundWebhookReachesDBLayer(t *testing.T) {
	t.Parallel()
	secret := "allpay-step4-secret"
	body := webhookRefundBody("re_step4", "00000000-0000-0000-0000-000000000004", "refund.succeeded")
	h := buildHandlerWithSecrets("", secret)

	req := postWebhookRequest("/v1/refunds/webhook", body,
		map[string]string{"X-AllPay-Signature": allPaySignatureHeader(secret, body)},
	)

	w := httptest.NewRecorder()
	h.HandleRefundWebhook(w, req)

	// Must NOT be 401 (auth rejected).
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("valid AllPay signature incorrectly rejected (401): %s", w.Body.String())
	}
	// With nil DB the handler returns 503 — meaning the request passed the gate.
	if w.Code != http.StatusServiceUnavailable {
		t.Logf("note: got %d (not 503) — DB gate may have changed; body: %s", w.Code, w.Body.String())
	}
}

// TestWH362_Step4_ForgedProviderIDRejected demonstrates end-to-end that an
// attacker who knows a provider_payment_id cannot mint tickets by sending an
// unsigned payment.succeeded webhook. The request is rejected at the HMAC gate
// (401) before the body is parsed or any DB state is mutated.
func TestWH362_Step4_ForgedProviderIDRejected(t *testing.T) {
	t.Parallel()
	// Simulate production: Stripe secret configured.
	h := buildHandlerWithSecrets("whsec_production_secret", "")

	// Attacker knows the provider_payment_id and sends a payment.succeeded event.
	attackBody := webhookPIBody("pi_victim_payment_id", "payment_intent.succeeded")
	req := postWebhookRequest("/v1/payment-intents/webhook", attackBody, nil) // no signature

	w := httptest.NewRecorder()
	h.HandlePaymentIntentWebhook(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("attack should be rejected with 401, got %d; body: %s", w.Code, w.Body.String())
	}
}

// TestWH362_Step4_BothSecretsConfiguredStripePathWorks verifies that when both
// Stripe and AllPay secrets are configured, a valid Stripe signature still works.
func TestWH362_Step4_BothSecretsConfiguredStripePathWorks(t *testing.T) {
	t.Parallel()
	stripeSecret := "whsec_both_stripe"
	allPaySecret := "allpay-both-secret"
	body := webhookPIBody("pi_both", "payment_intent.succeeded")
	h := buildHandlerWithSecrets(stripeSecret, allPaySecret)

	req := postWebhookRequest("/v1/payment-intents/webhook", body,
		map[string]string{"Stripe-Signature": stripeSignatureHeader(stripeSecret, body)},
	)

	if err := h.verifyWebhookSignature(req, body); err != nil {
		t.Errorf("valid Stripe sig rejected when both secrets configured: %v", err)
	}
}

// TestWH362_Step4_BothSecretsConfiguredAllPayPathWorks verifies that when both
// secrets are configured, a valid AllPay signature still works.
func TestWH362_Step4_BothSecretsConfiguredAllPayPathWorks(t *testing.T) {
	t.Parallel()
	stripeSecret := "whsec_both_stripe_allpay"
	allPaySecret := "allpay-both-secret-allpay"
	body := []byte(`{"event_type":"refund.succeeded"}`)
	h := buildHandlerWithSecrets(stripeSecret, allPaySecret)

	req := postWebhookRequest("/v1/refunds/webhook", body,
		map[string]string{"X-AllPay-Signature": allPaySignatureHeader(allPaySecret, body)},
	)

	if err := h.verifyWebhookSignature(req, body); err != nil {
		t.Errorf("valid AllPay sig rejected when both secrets configured: %v", err)
	}
}

// TestWH362_Step4_ErrInvalidWebhookSignatureWrapped verifies that verification
// failures wrap payments.ErrInvalidWebhookSignature so callers can use
// errors.Is() for typed error handling.
func TestWH362_Step4_ErrInvalidWebhookSignatureWrapped(t *testing.T) {
	t.Parallel()
	h := buildHandlerWithSecrets("whsec_err_wrap", "")
	body := []byte(`{"test":"body"}`)
	req := postWebhookRequest("/v1/payment-intents/webhook", body, nil) // no sig header

	err := h.verifyWebhookSignature(req, body)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !isInvalidWebhookSignatureError(err) {
		t.Errorf("expected error to wrap payments.ErrInvalidWebhookSignature, got: %v", err)
	}
}

// isInvalidWebhookSignatureError checks whether err wraps
// payments.ErrInvalidWebhookSignature. Using a helper avoids importing errors
// twice and keeps the test concise.
func isInvalidWebhookSignatureError(err error) bool {
	type unwrapper interface{ Unwrap() error }
	for err != nil {
		if err == payments.ErrInvalidWebhookSignature {
			return true
		}
		u, ok := err.(unwrapper)
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	return false
}

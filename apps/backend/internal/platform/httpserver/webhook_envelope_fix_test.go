// webhook_envelope_fix_test.go — HTTP-level unit tests proving that
// POST /v1/payment-intents/webhook parses a genuine Stripe event envelope
// ({"id","type","data":{"object":{...}}}) through the full router, alongside
// the pre-existing flat body shape (see payment_intents_137_test.go).
//
// These tests deliberately stay on the "unknown event type" / validation-4xx
// paths so they never touch the nil DB wired by buildPaymentIntentServer
// (same technique as TestPI137_WebhookHandler_UnknownEventTypeReturnsAcknowledgedFalse) —
// they exist to prove request PARSING works for the envelope shape, not to
// exercise the DB-backed completion logic (that needs a live DB; see the
// integration test in webhook_widget_completion_488_integration_test.go).
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWebhookFix_StripeEnvelope_UnknownTypeAcknowledged verifies that a
// genuine Stripe event envelope is detected and parsed by the full HTTP
// handler: an envelope carrying an event type this repo doesn't map is
// acknowledged with processed:false and echoes back the envelope's own
// `type` as event_type. A parsing failure would instead 400 with
// webhook.invalid_json; a missed provider_payment_id would 400 with
// webhook.missing_provider_payment_id — neither happens here.
func TestWebhookFix_StripeEnvelope_UnknownTypeAcknowledged(t *testing.T) {
	s := buildPaymentIntentServer(t)
	payload := map[string]any{
		"id":   "evt_unknown_type_1",
		"type": "payment_intent.created", // deliberately not in webhookEventTypeToState
		"data": map[string]any{
			"object": map[string]any{
				"id":     "pi_envelope_unknown",
				"status": "requires_payment_method",
			},
		},
	}
	bodyBytes, _ := json.Marshal(payload)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/payment-intents/webhook", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("envelope with unknown type: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if processed, ok := resp["processed"].(bool); !ok || processed {
		t.Errorf("expected processed=false, got: %v", resp["processed"])
	}
	if et, _ := resp["event_type"].(string); et != "payment_intent.created" {
		t.Errorf("event_type echoed = %q, want payment_intent.created (proves the envelope's type field was extracted)", et)
	}
}

// TestWebhookFix_FlatBody_MissingProviderPaymentIDStill400 verifies the
// pre-existing flat-body validation path is untouched by the new envelope
// detection logic.
func TestWebhookFix_FlatBody_MissingProviderPaymentIDStill400(t *testing.T) {
	s := buildPaymentIntentServer(t)
	body := `{"event_type":"mock.succeeded"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/payment-intents/webhook", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("flat body without provider_payment_id: got %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestWebhookFix_EnvelopeMissingObjectID400 verifies that a Stripe envelope
// whose data.object.id is empty still 400s with the same
// webhook.missing_provider_payment_id error as the flat shape would.
func TestWebhookFix_EnvelopeMissingObjectID400(t *testing.T) {
	s := buildPaymentIntentServer(t)
	payload := map[string]any{
		"id":   "evt_no_object_id",
		"type": "payment_intent.succeeded",
		"data": map[string]any{"object": map[string]any{"status": "succeeded"}},
	}
	bodyBytes, _ := json.Marshal(payload)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/payment-intents/webhook", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("envelope without object id: got %d, want 400; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	errObj, _ := resp["error"].(map[string]any)
	if code, _ := errObj["code"].(string); code != "webhook.missing_provider_payment_id" {
		t.Errorf("error code = %q, want webhook.missing_provider_payment_id; full body: %s", code, w.Body.String())
	}
}

// TestWebhookFix_MockShorthand_StillAcknowledgedUnknownWithoutEnvelopeFields
// verifies the existing mock.* shorthand flat body (no top-level type/data)
// is unaffected by envelope detection.
func TestWebhookFix_MockShorthand_StillWorks(t *testing.T) {
	s := buildPaymentIntentServer(t)
	payload := map[string]any{
		"provider_payment_id": "pi_mock_shorthand",
		"event_type":          "mock.not_a_real_mapped_type",
	}
	bodyBytes, _ := json.Marshal(payload)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/payment-intents/webhook", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("flat mock body: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if processed, ok := resp["processed"].(bool); !ok || processed {
		t.Errorf("expected processed=false for unmapped event type, got: %v", resp["processed"])
	}
}

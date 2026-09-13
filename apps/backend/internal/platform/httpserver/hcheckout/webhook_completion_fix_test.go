// webhook_completion_fix_test.go — unit tests for the HIGH-severity widget
// payment-webhook defect fixed 2026-09-13:
//
//  1. HandlePaymentIntentWebhook never completed the linked checkout session
//     on payment success, so GET /v1/public/checkout/{token} answered
//     "pending" forever for a paid widget purchase (see the checkout
//     completion / manual_review logic added to HandlePaymentIntentWebhook
//     in payment_intents.go — integration coverage lives in
//     apps/backend/internal/platform/httpserver, the top package, because it
//     needs a live DB and the full router).
//  2. validPaymentIntentTransitions was too strict for real provider webhook
//     delivery (Stripe commonly sends payment_intent.succeeded directly from
//     'created', skipping 'processing') — fixed by the webhook-only
//     validWebhookTransitions table / validWebhookTransition helper, WITHOUT
//     loosening the authenticated POST /v1/payment-intents/{id}/transition
//     endpoint, which still uses validPaymentIntentTransitions.
//  3. The webhook body decoder only accepted the flat, normalised shape and
//     rejected a genuine Stripe event envelope
//     ({"id","type","data":{"object":{...}}}) with
//     webhook.missing_provider_payment_id — fixed by
//     parseWebhookPaymentIntentRequest, which accepts both shapes.
//
// All tests in this file are pure unit tests — no live PostgreSQL required.
package hcheckout

import (
	"encoding/json"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Webhook-specific transition table: more permissive than the authenticated
// /transition endpoint, but only for the webhook path.
// ─────────────────────────────────────────────────────────────────────────────

func TestWebhookFix_ValidWebhookTransition_AcceptsDirectSuccessFromCreated(t *testing.T) {
	cases := []struct{ from, to string }{
		{"created", "succeeded"},
		{"created", "failed"},
		{"created", "authorized"},
		{"requires_action", "succeeded"},
		{"requires_action", "failed"},
		{"requires_action", "authorized"},
	}
	for _, tc := range cases {
		if !validWebhookTransition(tc.from, tc.to) {
			t.Errorf("validWebhookTransition(%q, %q) = false, want true (a real provider may deliver this directly)", tc.from, tc.to)
		}
	}
}

func TestWebhookFix_ValidWebhookTransition_StillRejectsInvalidChains(t *testing.T) {
	cases := []struct{ from, to string }{
		{"succeeded", "failed"},  // terminal
		{"failed", "succeeded"},  // terminal
		{"succeeded", "created"}, // terminal, cannot go back
		{"created", "created"},   // no self-loop
		{"manual_review", "created"},
		{"authorized", "requires_action"},
	}
	for _, tc := range cases {
		if validWebhookTransition(tc.from, tc.to) {
			t.Errorf("validWebhookTransition(%q, %q) = true, want false", tc.from, tc.to)
		}
	}
}

func TestWebhookFix_AuthenticatedTransitionEndpoint_StaysStrict(t *testing.T) {
	// The authenticated POST /v1/payment-intents/{id}/transition endpoint uses
	// validPaymentIntentTransitions directly (see HandleTransitionPaymentIntent)
	// and MUST NOT gain the webhook's relaxed created/requires_action targets.
	strictlyRejected := []struct{ from, to string }{
		{"created", "succeeded"},
		{"created", "failed"},
		{"created", "authorized"},
		{"requires_action", "succeeded"},
		{"requires_action", "authorized"},
	}
	for _, tc := range strictlyRejected {
		if validPaymentIntentTransitions[tc.from][tc.to] {
			t.Errorf("validPaymentIntentTransitions(%q, %q) = true, want false — the authenticated /transition endpoint must stay strict", tc.from, tc.to)
		}
		// And the webhook table must accept exactly what the strict table forbids.
		if !validWebhookTransition(tc.from, tc.to) {
			t.Errorf("validWebhookTransition(%q, %q) = false, want true", tc.from, tc.to)
		}
	}
}

func TestWebhookFix_TerminalStatesStayTerminalInWebhookTable(t *testing.T) {
	for _, s := range []string{"succeeded", "failed"} {
		if targets := validWebhookTransitions[s]; len(targets) != 0 {
			t.Errorf("validWebhookTransitions[%q] = %v, want empty (terminal state)", s, targets)
		}
	}
}

func TestWebhookFix_AllStatesPresentInWebhookTable(t *testing.T) {
	for s := range validPaymentIntentTransitions {
		if _, ok := validWebhookTransitions[s]; !ok {
			t.Errorf("state %q from validPaymentIntentTransitions missing from validWebhookTransitions", s)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// parseWebhookPaymentIntentRequest: flat shape (backward compat) + Stripe
// event envelope.
// ─────────────────────────────────────────────────────────────────────────────

func TestWebhookFix_ParseFlatBody_StillWorks(t *testing.T) {
	body := []byte(`{"provider_payment_id":"pi_flat_1","event_type":"mock.succeeded"}`)
	req, err := parseWebhookPaymentIntentRequest(body)
	if err != nil {
		t.Fatalf("parse flat body: %v", err)
	}
	if req.ProviderPaymentID != "pi_flat_1" {
		t.Errorf("ProviderPaymentID = %q, want pi_flat_1", req.ProviderPaymentID)
	}
	if req.EventType != "mock.succeeded" {
		t.Errorf("EventType = %q, want mock.succeeded", req.EventType)
	}
}

func TestWebhookFix_ParseFlatBody_WithFailureFields(t *testing.T) {
	body := []byte(`{
		"provider_payment_id": "pi_flat_2",
		"event_type": "payment_intent.payment_failed",
		"failure_code": "card_declined",
		"failure_message": "declined"
	}`)
	req, err := parseWebhookPaymentIntentRequest(body)
	if err != nil {
		t.Fatalf("parse flat body: %v", err)
	}
	if req.FailureCode == nil || *req.FailureCode != "card_declined" {
		t.Errorf("FailureCode = %v, want card_declined", req.FailureCode)
	}
	if req.FailureMessage == nil || *req.FailureMessage != "declined" {
		t.Errorf("FailureMessage = %v, want declined", req.FailureMessage)
	}
}

func TestWebhookFix_ParseStripeEnvelope_Succeeded(t *testing.T) {
	body := []byte(`{
		"id": "evt_test_1",
		"type": "payment_intent.succeeded",
		"data": {"object": {"id": "pi_envelope_1", "status": "succeeded"}}
	}`)
	req, err := parseWebhookPaymentIntentRequest(body)
	if err != nil {
		t.Fatalf("parse envelope: %v", err)
	}
	if req.ProviderPaymentID != "pi_envelope_1" {
		t.Errorf("ProviderPaymentID = %q, want pi_envelope_1", req.ProviderPaymentID)
	}
	if req.EventType != "payment_intent.succeeded" {
		t.Errorf("EventType = %q, want payment_intent.succeeded", req.EventType)
	}
	if req.FailureCode != nil || req.FailureMessage != nil {
		t.Errorf("FailureCode/FailureMessage should be nil for a succeeded event, got %v / %v", req.FailureCode, req.FailureMessage)
	}
	if req.EventPayload == nil {
		t.Fatal("EventPayload should carry the raw envelope for the audit trail")
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(req.EventPayload, &roundTrip); err != nil {
		t.Errorf("EventPayload is not valid JSON: %v", err)
	}
	if roundTrip["id"] != "evt_test_1" {
		t.Errorf("EventPayload lost the Stripe event id: %v", roundTrip)
	}
}

func TestWebhookFix_ParseStripeEnvelope_PaymentFailedWithLastPaymentError(t *testing.T) {
	body := []byte(`{
		"id": "evt_test_2",
		"type": "payment_intent.payment_failed",
		"data": {"object": {
			"id": "pi_envelope_2",
			"status": "requires_payment_method",
			"last_payment_error": {"code": "card_declined", "message": "Your card was declined."}
		}}
	}`)
	req, err := parseWebhookPaymentIntentRequest(body)
	if err != nil {
		t.Fatalf("parse envelope: %v", err)
	}
	if req.ProviderPaymentID != "pi_envelope_2" {
		t.Errorf("ProviderPaymentID = %q, want pi_envelope_2", req.ProviderPaymentID)
	}
	if req.EventType != "payment_intent.payment_failed" {
		t.Errorf("EventType = %q, want payment_intent.payment_failed", req.EventType)
	}
	if req.FailureCode == nil || *req.FailureCode != "card_declined" {
		t.Errorf("FailureCode = %v, want card_declined", req.FailureCode)
	}
	if req.FailureMessage == nil || *req.FailureMessage != "Your card was declined." {
		t.Errorf("FailureMessage = %v, want %q", req.FailureMessage, "Your card was declined.")
	}
}

func TestWebhookFix_ParseStripeEnvelope_NoLastPaymentErrorLeavesFailureFieldsNil(t *testing.T) {
	body := []byte(`{
		"id": "evt_test_3",
		"type": "payment_intent.succeeded",
		"data": {"object": {"id": "pi_envelope_3", "status": "succeeded"}}
	}`)
	req, err := parseWebhookPaymentIntentRequest(body)
	if err != nil {
		t.Fatalf("parse envelope: %v", err)
	}
	if req.FailureCode != nil {
		t.Errorf("FailureCode = %v, want nil", req.FailureCode)
	}
	if req.FailureMessage != nil {
		t.Errorf("FailureMessage = %v, want nil", req.FailureMessage)
	}
}

func TestWebhookFix_ParseInvalidJSON_ReturnsError(t *testing.T) {
	if _, err := parseWebhookPaymentIntentRequest([]byte(`not json`)); err == nil {
		t.Error("expected an error for invalid JSON")
	}
}

func TestWebhookFix_ParseFlatBody_WithUnrelatedTopLevelKeysStillFlat(t *testing.T) {
	// A "type"-less, "data"-less flat body must never be mistaken for an
	// envelope, regardless of what other keys it carries.
	body := []byte(`{"provider_payment_id":"pi_flat_3","event_type":"mock.succeeded","target_state":"succeeded"}`)
	req, err := parseWebhookPaymentIntentRequest(body)
	if err != nil {
		t.Fatalf("parse flat body: %v", err)
	}
	if req.TargetState != "succeeded" {
		t.Errorf("TargetState = %q, want succeeded (flat-shape field must survive envelope detection)", req.TargetState)
	}
}

func TestWebhookFix_WebhookEventTypeMapping_StripeAmountCapturableUpdated(t *testing.T) {
	// Stripe's real event type is "payment_intent.amount_capturable_updated"
	// (not the abbreviated "payment_intent.amount_capturable" already in the
	// map) — both must map to "authorized".
	got, ok := webhookEventTypeToState["payment_intent.amount_capturable_updated"]
	if !ok || got != "authorized" {
		t.Errorf("webhookEventTypeToState[%q] = (%q, %v), want (authorized, true)",
			"payment_intent.amount_capturable_updated", got, ok)
	}
}

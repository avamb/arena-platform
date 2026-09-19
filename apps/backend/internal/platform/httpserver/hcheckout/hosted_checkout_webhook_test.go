package hcheckout

import (
	"encoding/json"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// The widget's hosted-payment flow keys its payment_intents row by the
// Stripe Checkout Session id (cs_…), so every checkout.session.* event must
// parse into that id, and only a genuinely PAID session may advance the
// intent to succeeded. Both rules are money-critical: get the first wrong and
// the webhook 404s a real payment, get the second wrong and arena ships
// tickets for money that never settled.

const hostedCompletedPaidEvent = `{
  "id": "evt_1",
  "type": "checkout.session.completed",
  "data": {
    "object": {
      "id": "cs_test_abc",
      "object": "checkout.session",
      "payment_status": "paid",
      "status": "complete",
      "payment_intent": "pi_test_xyz"
    }
  }
}`

const hostedCompletedUnpaidEvent = `{
  "id": "evt_2",
  "type": "checkout.session.completed",
  "data": {
    "object": {
      "id": "cs_test_abc",
      "payment_status": "unpaid",
      "status": "complete",
      "payment_intent": "pi_test_xyz"
    }
  }
}`

const hostedExpiredEvent = `{
  "id": "evt_3",
  "type": "checkout.session.expired",
  "data": {
    "object": {
      "id": "cs_test_abc",
      "payment_status": "unpaid",
      "status": "expired"
    }
  }
}`

func TestParseWebhook_CheckoutSessionCompleted_CarriesIDStatusAndPaymentIntent(t *testing.T) {
	req, err := parseWebhookPaymentIntentRequest([]byte(hostedCompletedPaidEvent))
	if err != nil {
		t.Fatalf("parse returned an error: %v", err)
	}
	// The cs_ id — NOT the pi_ — is what arena stored as
	// provider_payment_id when it created the hosted page.
	if req.ProviderPaymentID != "cs_test_abc" {
		t.Errorf("ProviderPaymentID = %q; want the cs_ session id", req.ProviderPaymentID)
	}
	if req.EventType != "checkout.session.completed" {
		t.Errorf("EventType = %q; want checkout.session.completed", req.EventType)
	}
	if req.PaymentStatus != "paid" {
		t.Errorf("PaymentStatus = %q; want paid", req.PaymentStatus)
	}
	// Refunds run through the pi_, never the cs_ — it must survive parsing.
	if req.HostedPaymentID != "pi_test_xyz" {
		t.Errorf("HostedPaymentID = %q; want pi_test_xyz", req.HostedPaymentID)
	}
	if ref := providerChargeRef(req); ref == nil || *ref != "pi_test_xyz" {
		t.Errorf("providerChargeRef = %v; want a pointer to pi_test_xyz", ref)
	}
	if len(req.EventPayload) == 0 {
		t.Error("EventPayload is empty; the raw body must be kept for the audit trail")
	}
}

func TestParseWebhook_CheckoutSessionExpired_HasNoChargeRef(t *testing.T) {
	req, err := parseWebhookPaymentIntentRequest([]byte(hostedExpiredEvent))
	if err != nil {
		t.Fatalf("parse returned an error: %v", err)
	}
	if req.ProviderPaymentID != "cs_test_abc" {
		t.Errorf("ProviderPaymentID = %q; want cs_test_abc", req.ProviderPaymentID)
	}
	if req.EventType != "checkout.session.expired" {
		t.Errorf("EventType = %q; want checkout.session.expired", req.EventType)
	}
	// A session that expired unpaid never had a PaymentIntent behind it.
	if ref := providerChargeRef(req); ref != nil {
		t.Errorf("providerChargeRef = %q; want nil for an unpaid expired session", *ref)
	}
}

func TestWebhookEventTypeToState_CoversEveryHostedCheckoutEvent(t *testing.T) {
	want := map[string]string{
		"checkout.session.completed":               "succeeded",
		"checkout.session.async_payment_succeeded": "succeeded",
		"checkout.session.async_payment_failed":    "failed",
		"checkout.session.expired":                 "failed",
	}
	for event, state := range want {
		got, ok := webhookEventTypeToState[event]
		if !ok {
			t.Errorf("event %q is not mapped; Stripe will send it and the payment would be dropped", event)
			continue
		}
		if got != state {
			t.Errorf("event %q maps to %q; want %q", event, got, state)
		}
	}
}

// TestWebhookTransitions_AllowCreatedToSucceeded covers the only path a
// hosted checkout ever takes: the intent is born `created` when the page is
// made and hears nothing again until the buyer has paid. The STRICT table
// (used by the authenticated transition endpoint) must NOT be widened for it.
func TestWebhookTransitions_AllowCreatedToSucceededButStrictDoesNot(t *testing.T) {
	if !validWebhookTransition("created", "succeeded") {
		t.Error("the webhook table must allow created → succeeded for a hosted checkout")
	}
	if !validWebhookTransition("created", "failed") {
		t.Error("the webhook table must allow created → failed for an expired hosted session")
	}
	if validPaymentIntentTransitions["created"]["succeeded"] {
		t.Error("the STRICT table was widened; only validWebhookTransitions may accept created → succeeded")
	}
}

// TestParseWebhook_FlatLegacyShapeStillWorks guards the mock provider, AllPay
// and every pre-existing fixture: they POST the flat body and must keep
// working unchanged.
func TestParseWebhook_FlatLegacyShapeStillWorks(t *testing.T) {
	body := `{"provider_payment_id":"pi_flat","event_type":"payment_intent.succeeded"}`
	req, err := parseWebhookPaymentIntentRequest([]byte(body))
	if err != nil {
		t.Fatalf("parse returned an error: %v", err)
	}
	if req.ProviderPaymentID != "pi_flat" || req.EventType != "payment_intent.succeeded" {
		t.Errorf("flat body parsed as %+v; want provider_payment_id pi_flat / payment_intent.succeeded", req)
	}
	if req.PaymentStatus != "" {
		t.Errorf("PaymentStatus = %q; a non-session event carries none", req.PaymentStatus)
	}
	if ref := providerChargeRef(req); ref != nil {
		t.Errorf("providerChargeRef = %q; want nil for a flat body", *ref)
	}
}

// TestParseWebhook_PaymentIntentEnvelopeStillWorks guards the pre-existing
// Stripe payment_intent.* envelope path.
func TestParseWebhook_PaymentIntentEnvelopeStillWorks(t *testing.T) {
	body := `{"id":"evt_9","type":"payment_intent.payment_failed","data":{"object":{"id":"pi_env","status":"requires_payment_method","last_payment_error":{"code":"card_declined","message":"Your card was declined."}}}}`
	req, err := parseWebhookPaymentIntentRequest([]byte(body))
	if err != nil {
		t.Fatalf("parse returned an error: %v", err)
	}
	if req.ProviderPaymentID != "pi_env" {
		t.Errorf("ProviderPaymentID = %q; want pi_env", req.ProviderPaymentID)
	}
	if req.FailureCode == nil || *req.FailureCode != "card_declined" {
		t.Errorf("FailureCode = %v; want card_declined", req.FailureCode)
	}
	if req.FailureMessage == nil || *req.FailureMessage != "Your card was declined." {
		t.Errorf("FailureMessage = %v; want Stripe's message", req.FailureMessage)
	}
}

// TestHostedCheckoutPaymentStatusGate_ConstantsMatchStripe pins the literal
// strings the handler branches on. They come straight from Stripe's API and a
// typo here is silent: a paid session would look unpaid and never issue
// tickets.
func TestHostedCheckoutPaymentStatusGate_ConstantsMatchStripe(t *testing.T) {
	if eventCheckoutSessionCompleted != "checkout.session.completed" {
		t.Errorf("eventCheckoutSessionCompleted = %q", eventCheckoutSessionCompleted)
	}
	if eventCheckoutSessionExpired != "checkout.session.expired" {
		t.Errorf("eventCheckoutSessionExpired = %q", eventCheckoutSessionExpired)
	}
	if checkoutSessionPaid != "paid" {
		t.Errorf("checkoutSessionPaid = %q", checkoutSessionPaid)
	}
	if failureCodeSessionExpired != "session_expired" {
		t.Errorf("failureCodeSessionExpired = %q", failureCodeSessionExpired)
	}

	// The unpaid variant must be distinguishable from the paid one purely by
	// the parsed PaymentStatus — that is the whole gate.
	paid, _ := parseWebhookPaymentIntentRequest([]byte(hostedCompletedPaidEvent))
	unpaid, _ := parseWebhookPaymentIntentRequest([]byte(hostedCompletedUnpaidEvent))
	if paid.PaymentStatus == unpaid.PaymentStatus {
		t.Fatal("paid and unpaid checkout.session.completed events parse identically")
	}
	if unpaid.PaymentStatus == checkoutSessionPaid {
		t.Error("an unpaid session must not report payment_status paid")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Per-org webhook secret resolution
// ─────────────────────────────────────────────────────────────────────────────

// TestProviderPaymentIDFromEnvelope_FindsTheOrgKey is the fix for the reason
// two organizers on two separate Stripe accounts could not both work: a real
// Stripe event carries none of arena's own ids, so the per-org secret lookup
// had nothing to key on and fell back to the single process-wide env secret —
// which at most one of them can own.
func TestProviderPaymentIDFromEnvelope_FindsTheOrgKey(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"hosted checkout session", hostedCompletedPaidEvent, "cs_test_abc"},
		{"hosted session expired", hostedExpiredEvent, "cs_test_abc"},
		{
			"payment intent envelope",
			`{"id":"evt_9","type":"payment_intent.succeeded","data":{"object":{"id":"pi_env"}}}`,
			"pi_env",
		},
		{
			"flat legacy body is not an envelope",
			`{"provider_payment_id":"pi_flat","event_type":"payment_intent.succeeded"}`,
			"",
		},
		{"no data object", `{"type":"payment_intent.succeeded"}`, ""},
		{"no type", `{"data":{"object":{"id":"pi_x"}}}`, ""},
		{"not json", `nonsense`, ""},
		{"empty object", `{}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := providerPaymentIDFromEnvelope([]byte(tc.body)); got != tc.want {
				t.Errorf("providerPaymentIDFromEnvelope = %q; want %q", got, tc.want)
			}
		})
	}
}

// TestSecretFieldFromConfig_ReadsTheOrgsOwnKey proves credentials are taken
// from the org's config row. There is no Stripe Connect in this wave: each
// organizer's own secret key is the ONLY way to charge on their behalf.
func TestSecretFieldFromConfig_ReadsTheOrgsOwnKey(t *testing.T) {
	cfg := paymentConfigWithSecrets(t, map[string]string{
		"api_key":        "sk_test_org_one",
		"webhook_secret": "whsec_org_one",
	})
	if got := SecretFieldFromConfig(cfg, "api_key"); got != "sk_test_org_one" {
		t.Errorf("api_key = %q; want sk_test_org_one", got)
	}
	if got := WebhookSecretFromConfig(cfg); got != "whsec_org_one" {
		t.Errorf("webhook secret = %q; want whsec_org_one", got)
	}
	if got := SecretFieldFromConfig(cfg, "nope"); got != "" {
		t.Errorf("missing field = %q; want empty", got)
	}
	if got := SecretFieldFromConfig(paymentConfigWithSecrets(t, nil), "api_key"); got != "" {
		t.Errorf("empty secrets blob yielded %q; want empty", got)
	}
}

func TestSecretFieldFromConfig_TrimsWhitespace(t *testing.T) {
	// A key pasted into the admin UI often carries a trailing newline; sent
	// verbatim in an Authorization header it is a 401 from Stripe with a
	// baffling message.
	cfg := paymentConfigWithSecrets(t, map[string]string{"api_key": "  sk_test_padded\n"})
	if got := SecretFieldFromConfig(cfg, "api_key"); got != "sk_test_padded" {
		t.Errorf("api_key = %q; want it trimmed", got)
	}
}

func paymentConfigWithSecrets(t *testing.T, secrets map[string]string) gen.PaymentProviderConfigRow {
	t.Helper()
	row := gen.PaymentProviderConfigRow{Provider: "stripe"}
	if secrets == nil {
		return row
	}
	raw, err := json.Marshal(secrets)
	if err != nil {
		t.Fatalf("marshal secrets: %v", err)
	}
	row.Secrets = raw
	return row
}

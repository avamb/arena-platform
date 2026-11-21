// legacy_webhook_signer_test.go — the legacy webhook signature check reports
// WHO verified a delivery: an organization's own secret (the caller then binds
// the processed row to that organization and provider), the platform's
// process-env secret, or nobody (dev/mock mode, no secrets).
package hcheckout

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

func legacySignerEnvelope() []byte {
	return []byte(`{"id":"evt_1","type":"checkout.session.completed","data":{"object":{"id":"cs_test_1","payment_status":"paid"}}}`)
}

func TestLegacyWebhookSigner_OrgSecretNamesTheOrganization(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	body := legacySignerEnvelope()
	req := postWebhookRequest("/v1/payment-intents/webhook", body,
		map[string]string{"Stripe-Signature": stripeSignatureHeader("whsec_org", body)})

	signer, err := verifyLegacyWebhook(req.Context(), req, body,
		legacyOrgSecrets{OrgID: &orgID, Secrets: map[string]string{"stripe": "whsec_org"}},
		map[string]string{"stripe": "whsec_platform"})
	if err != nil {
		t.Fatalf("org-signed delivery rejected: %v", err)
	}
	if signer.OrgID == nil || *signer.OrgID != orgID {
		t.Fatalf("signer.OrgID = %v; want %s", signer.OrgID, orgID)
	}
	if signer.Provider != "stripe" || signer.Platform {
		t.Errorf("signer = %+v; want provider stripe, not platform", signer)
	}
}

func TestLegacyWebhookSigner_PlatformSecretIsNotAnOrganization(t *testing.T) {
	t.Parallel()
	body := legacySignerEnvelope()
	req := postWebhookRequest("/v1/payment-intents/webhook", body,
		map[string]string{"Stripe-Signature": stripeSignatureHeader("whsec_platform", body)})

	signer, err := verifyLegacyWebhook(req.Context(), req, body,
		legacyOrgSecrets{}, map[string]string{"stripe": "whsec_platform"})
	if err != nil {
		t.Fatalf("platform-signed delivery rejected: %v", err)
	}
	if signer.OrgID != nil || !signer.Platform || signer.Provider != "stripe" {
		t.Errorf("signer = %+v; want platform stripe with no organization", signer)
	}
}

// An organization resolved from the body but holding no secret of its own
// falls back to the platform secret, and the result names no organization.
func TestLegacyWebhookSigner_OrgWithoutSecretFallsBackToPlatform(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	body := legacySignerEnvelope()
	req := postWebhookRequest("/v1/payment-intents/webhook", body,
		map[string]string{"Stripe-Signature": stripeSignatureHeader("whsec_platform", body)})

	signer, err := verifyLegacyWebhook(req.Context(), req, body,
		legacyOrgSecrets{OrgID: &orgID, Secrets: map[string]string{}},
		map[string]string{"stripe": "whsec_platform"})
	if err != nil {
		t.Fatalf("platform-signed delivery rejected: %v", err)
	}
	if signer.OrgID != nil || !signer.Platform {
		t.Errorf("signer = %+v; want platform with no organization", signer)
	}
}

func TestLegacyWebhookSigner_NoSecretsIsDevMode(t *testing.T) {
	t.Parallel()
	body := legacySignerEnvelope()
	req := httptest.NewRequest(http.MethodPost, "/v1/payment-intents/webhook", nil)

	signer, err := verifyLegacyWebhook(req.Context(), req, body, legacyOrgSecrets{}, map[string]string{})
	if err != nil {
		t.Fatalf("dev mode rejected: %v", err)
	}
	if signer != (legacyWebhookSigner{}) {
		t.Errorf("signer = %+v; want the zero value", signer)
	}
}

func TestLegacyWebhookSigner_WrongSecretIsRefusedWithoutSigner(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	body := legacySignerEnvelope()
	req := postWebhookRequest("/v1/payment-intents/webhook", body,
		map[string]string{"Stripe-Signature": stripeSignatureHeader("whsec_other", body)})

	signer, err := verifyLegacyWebhook(req.Context(), req, body,
		legacyOrgSecrets{OrgID: &orgID, Secrets: map[string]string{"stripe": "whsec_org"}},
		map[string]string{})
	if err == nil {
		t.Fatal("a delivery signed with another secret was accepted")
	}
	if !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Errorf("err = %v; want ErrInvalidWebhookSignature", err)
	}
	if signer != (legacyWebhookSigner{}) {
		t.Errorf("signer = %+v; want the zero value on failure", signer)
	}
}

func TestLegacyOrgSignedBodyShapeOK(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"envelope", string(legacySignerEnvelope()), true},
		{"flat shape", `{"provider_payment_id":"cs_1","event_type":"payment_intent.succeeded"}`, false},
		{"flat with target_state", `{"provider_payment_id":"cs_1","event_type":"x","target_state":"succeeded"}`, false},
		{"envelope with target_state", `{"type":"checkout.session.completed","data":{"object":{"id":"cs_1"}},"target_state":"succeeded"}`, false},
		{"not json", `not json`, false},
	}
	for _, tc := range cases {
		if got := legacyOrgSignedBodyShapeOK([]byte(tc.body)); got != tc.want {
			t.Errorf("%s: legacyOrgSignedBodyShapeOK = %v; want %v", tc.name, got, tc.want)
		}
	}
}

// With no organization queries the payment route resolves no organization,
// whatever arena ids the body carries.
func TestLegacyWebhookSecrets_NoOrgQueriesResolveNothing(t *testing.T) {
	t.Parallel()
	h := &Handler{}
	body := []byte(`{"refund_id":"` + uuid.NewString() + `","payment_intent_id":"` + uuid.NewString() + `"}`)
	for _, kind := range []legacyWebhookKind{legacyWebhookPayment, legacyWebhookRefund} {
		got := h.webhookSecretsFromOrgConfig(t.Context(), body, kind)
		if got.OrgID != nil || len(got.Secrets) != 0 {
			t.Errorf("kind %d: resolved %+v; want nothing", kind, got)
		}
	}
}

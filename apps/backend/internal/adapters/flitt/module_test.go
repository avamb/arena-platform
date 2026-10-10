// module_test.go — Flitt against the payment-module contract (PAY-01,
// domain/payments/contracttest), plus the rules the per-config webhook route
// relies on: merchant_id is compared before the hash, and a declined attempt
// does not fail the payment.
package flitt

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments/contracttest"
)

func flittModule(t *testing.T, merchantID, key string) payments.Module {
	t.Helper()
	m, err := Entry().Build(map[string]string{SecretMerchantID: merchantID, SecretPaymentKey: key}, payments.Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return m
}

func TestFlittModule_Contract(t *testing.T) {
	// Refund through Flitt is PAY-13: the descriptor declares no Refund
	// capability, so the contract skips the refund sub-tests (and says why).
	contracttest.Run(t, contracttest.Case{
		Module:              flittModule(t, "1549901", "secret"),
		GoodWebhook:         payments.WebhookInput{Body: signedCallback(t, "secret"), Header: http.Header{}},
		BadSignatureWebhook: payments.WebhookInput{Body: signedCallback(t, "not-the-key"), Header: http.Header{}},
		WantEvent: payments.NormalizedEvent{
			Kind:              payments.EventPaymentSucceeded,
			Type:              "flitt.order.approved",
			ProviderPaymentID: "6f1c0f6e-0000-4000-8000-000000000001",
			ProviderChargeRef: "805243692",
			AmountMinor:       200,
			Currency:          "EUR",
		},
	})
}

func TestFlittModule_AnotherMerchantIsRefusedBeforeTheHash(t *testing.T) {
	parser := flittModule(t, "424242", "secret").(payments.WebhookParser)
	_, err := parser.VerifyAndParse(t.Context(), payments.WebhookInput{Body: signedCallback(t, "secret")})
	if !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Fatalf("callback for another merchant = %v; want ErrInvalidWebhookSignature", err)
	}
}

func TestFlittModule_DeclinedIsNotAFailure(t *testing.T) {
	m, err := decodeFlat([]byte(strings.Replace(callbackBody, `"order_status":"approved"`, `"order_status":"declined"`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	m["response_code"] = "1004"
	m["signature"] = Sign("secret", m)
	body, _ := json.Marshal(m)
	ev, err := flittModule(t, "1549901", "secret").(payments.WebhookParser).VerifyAndParse(t.Context(), payments.WebhookInput{Body: body})
	if err != nil {
		t.Fatalf("VerifyAndParse: %v", err)
	}
	if ev.Kind != payments.EventPaymentDeclined || ev.Kind == payments.EventPaymentFailed {
		t.Fatalf("Kind = %q; a declined card must leave the Flitt order open (EventPaymentDeclined)", ev.Kind)
	}
	if ev.FailureCode != "1004" {
		t.Fatalf("FailureCode = %q; want 1004", ev.FailureCode)
	}
}

func TestFlittModule_RecognizesOnlyItsBodyShape(t *testing.T) {
	parser := flittModule(t, "", "").(payments.WebhookParser)
	if !parser.RecognizesWebhook(payments.WebhookInput{Body: []byte(callbackBody)}) {
		t.Fatal("a Flitt callback body was not recognized")
	}
	stripeEnvelope := []byte(`{"id":"evt_1","type":"payment_intent.succeeded","data":{"object":{"id":"pi_1"}}}`)
	if parser.RecognizesWebhook(payments.WebhookInput{Body: stripeEnvelope}) {
		t.Fatal("a non-Flitt body was recognized as a Flitt callback")
	}
}

// module_test.go — PayPlus against the payment-module contract (PAY-01,
// domain/payments/contracttest), plus the registry wiring.
package payplus

import (
	"net/http"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments/contracttest"
)

func signedHeader(key string) http.Header {
	h := http.Header{}
	h.Set(HeaderHash, Sign(key, []byte(callbackBody)))
	h.Set("User-Agent", "PayPlus")
	return h
}

func TestPayPlusModule_Contract(t *testing.T) {
	stub := newStubPayPlus(t, testAPIKey, testSecret)
	stub.addCharge("charge-contract", "page-contract", 10_000, "ILS")
	stub.addCharge("charge-partial", "page-partial", 10_000, "ILS")
	m, err := Entry().Build(map[string]string{SecretAPIKey: testAPIKey, SecretKey: testSecret, SecretPaymentPageUID: testPageUID},
		payments.Options{PayPlusAPIBaseURL: stub.baseURL(), Mode: "test"})
	if err != nil {
		t.Fatal(err)
	}
	contracttest.Run(t, contracttest.Case{
		Module:              m,
		GoodWebhook:         payments.WebhookInput{Body: []byte(callbackBody), Header: signedHeader(testSecret)},
		BadSignatureWebhook: payments.WebhookInput{Body: []byte(callbackBody), Header: signedHeader("not-the-secret")},
		WantEvent: payments.NormalizedEvent{
			Kind:              payments.EventPaymentSucceeded,
			Type:              EventTypeApproved,
			ProviderPaymentID: "ef76432c-769a-43a6-ba7a-6f70272539d8",
			ProviderChargeRef: "dcb11c1e7-a1231-37cf-6311-f5111eeb69c7",
			AmountMinor:       1250,
			Currency:          "ILS",
			Status:            StatusCodeApproved,
		},
		Refund: &contracttest.RefundCase{
			ChargeRef:          "charge-contract",
			PaymentAmountMinor: 10_000,
			Currency:           "ILS",
			StubRequests:       func() int { return stub.count("RefundByTransactionUID") },
			StubCreated:        stub.refundCount,
			StubAmountFor:      func(key string) int64 { return stub.refundAmountFor(RefundMarker(key)) },
			PartialChargeRef:   "charge-partial",
		},
	})
}

func TestPayPlusModule_MissingHashIsRefused(t *testing.T) {
	m := New(Config{SecretKey: testSecret})
	if _, err := m.VerifyAndParse(t.Context(), payments.WebhookInput{Body: []byte(callbackBody), Header: http.Header{}}); err == nil {
		t.Fatal("a callback without the hash header verified")
	}
}

func TestPayPlusModule_ModeSelectsHost(t *testing.T) {
	for mode, want := range map[string]string{"test": StagingBaseURL, "live": ProductionBaseURL} {
		m, err := Entry().Build(map[string]string{SecretAPIKey: "a", SecretKey: "b", SecretPaymentPageUID: testPageUID}, payments.Options{Mode: mode})
		if err != nil {
			t.Fatal(err)
		}
		if got := m.(*Adapter).BaseURL(); got != want {
			t.Errorf("mode %s → %s; want %s", mode, got, want)
		}
	}
	d := ProviderDescriptor()
	for _, key := range []string{SecretAPIKey, SecretKey} {
		if f, _ := d.Secret(key); !f.Hidden {
			t.Errorf("%s is not Hidden; a config read would echo it", key)
		}
	}
	if d.WebhookSecretKey() != SecretKey || !d.Capabilities.WebhookConfigRouteOnly {
		t.Errorf("descriptor = %+v; the secret key must verify callbacks on the config route only", d)
	}
}

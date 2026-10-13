// module_test.go — AllPay against the payment-module contract (PAY-01,
// domain/payments/contracttest), plus the rules the per-config webhook route
// relies on: a failed attempt (status 0) does not fail the payment, and a
// webhook that took no money moves nothing.
package allpay

import (
	"encoding/json"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments/contracttest"
)

const testOrderID = "6f1c0f6e-0000-4000-8000-000000000001"

func allpayModule(t *testing.T, key string, opts payments.Options) payments.Module {
	t.Helper()
	m, err := Entry().Build(map[string]string{SecretMerchantID: "pp100", SecretKey: key}, opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return m
}

func TestAllPayModule_Contract(t *testing.T) {
	// Refund through AllPay is PAY-14b: the descriptor declares no Refund
	// capability, so the contract skips the refund sub-tests (and says why).
	contracttest.Run(t, contracttest.Case{
		Module:              allpayModule(t, "secret", payments.Options{}),
		GoodWebhook:         payments.WebhookInput{Body: signedWebhook(t, "secret", paidWebhookFields(testOrderID))},
		BadSignatureWebhook: payments.WebhookInput{Body: signedWebhook(t, "not-the-key", paidWebhookFields(testOrderID))},
		WantEvent: payments.NormalizedEvent{
			Kind:              payments.EventPaymentSucceeded,
			Type:              EventTypePaid,
			ProviderPaymentID: testOrderID,
			ProviderChargeRef: testOrderID,
			AmountMinor:       12350,
			Currency:          "ILS",
			Status:            "1",
		},
	})
}

func normalized(t *testing.T, fields map[string]any) payments.NormalizedEvent {
	t.Helper()
	ev, err := allpayModule(t, "secret", payments.Options{}).(payments.WebhookParser).VerifyAndParse(t.Context(),
		payments.WebhookInput{Body: signedWebhook(t, "secret", fields)})
	if err != nil {
		t.Fatalf("VerifyAndParse: %v", err)
	}
	return ev
}

func TestAllPayModule_FailedAttemptIsNotAFailure(t *testing.T) {
	f := paidWebhookFields(testOrderID)
	f["status"] = json.Number("0")
	f["error"] = "Declined by the card issuer"
	f["decline_code"] = json.Number("20004")
	f["decline_reason"] = "Declined by the card issuer"
	delete(f, "items")
	ev := normalized(t, f)
	if ev.Kind != payments.EventPaymentDeclined {
		t.Fatalf("Kind = %q; a failed attempt must leave the AllPay page open (EventPaymentDeclined)", ev.Kind)
	}
	if ev.Type != EventTypeDeclined || ev.FailureCode != "20004" || ev.FailureMessage != "Declined by the card issuer" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestAllPayModule_NoMoneyTakenMovesNothing(t *testing.T) {
	saved := paidWebhookFields(testOrderID)
	saved["subscription_create"] = json.Number("0")
	reserved := paidWebhookFields(testOrderID)
	reserved["preauth"] = "authorized"
	for name, f := range map[string]map[string]any{"saved card": saved, "J5 reservation": reserved} {
		if ev := normalized(t, f); ev.Kind != payments.EventUnknown || ev.Type != EventTypeNotCharged {
			t.Errorf("%s: event = %q/%q; want unknown/%s", name, ev.Kind, ev.Type, EventTypeNotCharged)
		}
	}
}

func TestAllPayModule_Chargebacks(t *testing.T) {
	cb := paidWebhookFields(testOrderID)
	cb["type"] = "chargeback"
	cb["status"] = json.Number("3")
	if ev := normalized(t, cb); ev.Kind != payments.EventDisputeOpened || ev.Type != EventTypeChargeback {
		t.Errorf("chargeback = %q/%q", ev.Kind, ev.Type)
	}
	cb["type"] = "chargeback_revert"
	if ev := normalized(t, cb); ev.Kind != payments.EventUnknown || ev.Type != EventTypeChargebackRevert {
		t.Errorf("chargeback_revert = %q/%q", ev.Kind, ev.Type)
	}
}

func TestAllPayModule_ConfigModeDrivesTestMode(t *testing.T) {
	if !allpayModule(t, "k", payments.Options{Mode: "test"}).(*Adapter).cfg.TestMode {
		t.Error("a test config must force test_mode")
	}
	if allpayModule(t, "k", payments.Options{Mode: "live"}).(*Adapter).cfg.TestMode {
		t.Error("a live config must leave test mode to the integration")
	}
}

func TestAllPayModule_OptionalSecrets(t *testing.T) {
	m, err := Entry().Build(map[string]string{
		SecretMerchantID: "pp100", SecretKey: "k", SecretVAT: "1", SecretMaxInstallments: "12",
	}, payments.Options{AllPayAPIBaseURL: "http://stub.test/app/"})
	if err != nil {
		t.Fatal(err)
	}
	a := m.(*Adapter)
	if a.cfg.VAT != "1" || a.cfg.MaxInstallments != 12 || a.cfg.BaseURL != "http://stub.test/app" {
		t.Fatalf("cfg = %+v", a.cfg)
	}
	d := ProviderDescriptor()
	if got := d.RequiredSecretKeys(); len(got) != 2 {
		t.Errorf("required secrets = %v; the VAT code and installments must stay optional", got)
	}
}

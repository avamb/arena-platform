// module_test.go — AllPay against the payment-module contract (PAY-01,
// domain/payments/contracttest), plus the rules the per-config webhook route
// relies on: a failed attempt (status 0) does not fail the payment, and a
// webhook that took no money moves nothing.
package allpay

import (
	"encoding/json"
	"errors"
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
	// The module talks to a stub that keeps a ledger of orders: the paid
	// order of the webhook, one order for the full-refund sub-tests and one
	// for the partial refund (AllPay tells refunds apart only by an order's
	// refunded total).
	stub := newStubAllPay(t, "pp100", "secret")
	stub.addOrder(testOrderID, stubOrder{Status: StatusPaid, Amount: 12350})
	stub.addOrder("contract-full", stubOrder{Status: StatusPaid, Amount: 12350})
	stub.addOrder("contract-partial", stubOrder{Status: StatusPaid, Amount: 12350})
	contracttest.Run(t, contracttest.Case{
		Module: allpayModule(t, "secret", payments.Options{AllPayAPIBaseURL: stub.server.URL}),
		Refund: &contracttest.RefundCase{
			ChargeRef:          "contract-full",
			PartialChargeRef:   "contract-partial",
			PaymentAmountMinor: 12350,
			Currency:           "ILS",
			StubRequests:       func() int { return stub.count(methodRefund) },
			StubCreated:        func() int { return len(stub.refunds("contract-full")) + len(stub.refunds("contract-partial")) },
			StubAmountFor: func(string) int64 {
				if r := stub.refunds("contract-partial"); len(r) > 0 {
					return r[len(r)-1]
				}
				return -1
			},
		},
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
	live := allpayModule(t, "k", payments.Options{Mode: "live"}).(*Adapter).cfg
	if live.TestMode || !live.LiveMode {
		t.Error("a live config must force test_mode=0, whatever the dashboard says")
	}
	if c := allpayModule(t, "k", payments.Options{}).(*Adapter).cfg; c.TestMode || c.LiveMode {
		t.Error("an unknown mode must leave test_mode to the integration")
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

func TestAllPayModule_InstallmentsThresholdSecret(t *testing.T) {
	m, err := Entry().Build(map[string]string{SecretMerchantID: "pp100", SecretKey: "k",
		SecretMaxInstallments: "6", SecretInstallmentsMinAmount: "300.50"}, payments.Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := m.(*Adapter).cfg.InstallmentsMinMinor; got != 30050 {
		t.Errorf("InstallmentsMinMinor = %d; want 30050", got)
	}
	if _, err := Entry().Build(map[string]string{SecretMerchantID: "pp100", SecretKey: "k",
		SecretInstallmentsMinAmount: "a lot"}, payments.Options{}); err == nil {
		t.Error("an unreadable threshold was accepted")
	}
}

func TestAllPayModule_PaidWebhookIsConfirmedWithAllPay(t *testing.T) {
	stub := newStubAllPay(t, "pp100", "secret")
	m := allpayModule(t, "secret", payments.Options{AllPayAPIBaseURL: stub.server.URL})
	parse := func(order string) (payments.NormalizedEvent, error) {
		return m.(payments.WebhookParser).VerifyAndParse(t.Context(),
			payments.WebhookInput{Body: signedWebhook(t, "secret", paidWebhookFields(order))})
	}

	stub.addOrder("o-paid", stubOrder{Status: StatusPaid, Amount: 12350, Discount: 350})
	ev, err := parse("o-paid")
	if err != nil || ev.Kind != payments.EventPaymentSucceeded || ev.AmountMinor != 12000 || ev.Currency != "ILS" {
		t.Errorf("paid order: %+v, %v; want succeeded for the 120.00 AllPay charged", ev, err)
	}

	stub.addOrder("o-unpaid", stubOrder{Status: 0, Amount: 12350})
	if _, err := parse("o-unpaid"); !errors.Is(err, payments.ErrProviderUnreachable) {
		t.Errorf("unpaid order: %v; want ErrProviderUnreachable so AllPay delivers it again", err)
	}

	stub.addOrder("o-refunded", stubOrder{Status: StatusPaid, Amount: 12350, Refunded: 2000})
	if ev, err := parse("o-refunded"); err != nil || ev.Kind != payments.EventPaymentManualReview {
		t.Errorf("refunded order: %+v, %v; want manual review", ev, err)
	}

	if ev, err := parse("o-missing"); err != nil || ev.Kind != payments.EventPaymentManualReview {
		t.Errorf("order AllPay does not hold: %+v, %v; want manual review", ev, err)
	}

	// AllPay charged in another currency than arena's (the docs: a currency
	// that is not enabled falls back to ILS with no error): no tickets.
	stub.addOrder("o-fallback", stubOrder{Status: StatusPaid, Amount: 12350, Currency: "EUR", ChargeCurrency: "ILS"})
	if ev, err := parse("o-fallback"); err != nil || ev.Kind != payments.EventPaymentManualReview || ev.FailureCode != "allpay_charge_currency_differs" {
		t.Errorf("charged in another currency: %+v, %v; want manual review", ev, err)
	}

	// Review M1: status 1 without an amount or a currency confirms nothing.
	stub.answer[methodPaymentStatus] = `{"order_id":"o-paid","status":1}`
	if _, err := parse("o-paid"); !errors.Is(err, payments.ErrProviderUnreachable) {
		t.Errorf("paid without amount: %v; want ErrProviderUnreachable", err)
	}
	stub.answer[methodPaymentStatus] = `{"order_id":"o-paid","status":1,"amount":123.5}`
	if _, err := parse("o-paid"); !errors.Is(err, payments.ErrProviderUnreachable) {
		t.Errorf("paid without currency: %v; want ErrProviderUnreachable", err)
	}

	stub.answer[methodPaymentStatus] = `oops`
	if _, err := parse("o-paid"); !errors.Is(err, payments.ErrProviderUnreachable) {
		t.Errorf("unreadable status: %v; want ErrProviderUnreachable", err)
	}
}

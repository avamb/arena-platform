// Package contracttest is the test suite every payment module runs against
// itself (spec 36 §5 "Контрактные тесты", PAY-01). A module's own
// module_test.go builds a Case — the module pointed at a local stub, one
// correctly signed delivery with the event it must normalize to, the same
// delivery with a wrong signature, and (when the module can refund) the
// stub's counters — and calls Run. The suite then proves:
//
//   - the descriptor is valid and consistent with the module value (the
//     name matches, every advertised capability is backed by its interface);
//   - a wrong signature is ErrInvalidWebhookSignature and the known-good
//     delivery normalizes as declared;
//   - a refund of zero, or of more than the payment, is refused BEFORE any
//     network call;
//   - two Refund calls with the same IdempotencyKey create ONE refund at the
//     stub and answer the same result.
//
// It is an importable package (not _test.go) so that each adapter package
// can run it; it must stay free of provider names.
package contracttest

import (
	"errors"
	"reflect"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// Case is one module under test.
type Case struct {
	// Module is built against the test stub (its secrets and Options point
	// at local servers; nothing here may reach the internet).
	Module payments.Module
	// GoodWebhook is a correctly signed delivery of this provider.
	GoodWebhook payments.WebhookInput
	// WantEvent is the normalization GoodWebhook must produce. Kind, Type,
	// ProviderPaymentID and ProviderChargeRef are always compared; the other
	// fields only when set here.
	WantEvent payments.NormalizedEvent
	// BadSignatureWebhook is a delivery of this provider whose signature
	// does not verify (wrong key, tampered body, or no signature at all).
	BadSignatureWebhook payments.WebhookInput
	// Refund drives the refund sub-tests. Required when the descriptor
	// declares Capabilities.Refund; ignored otherwise.
	Refund *RefundCase
}

// RefundCase is what the refund sub-tests need from the stub.
type RefundCase struct {
	// ChargeRef is a payment the stub accepts refunds for.
	ChargeRef string
	// PaymentAmountMinor is that payment's amount, in arena minor units.
	PaymentAmountMinor int64
	// Currency of the payment.
	Currency string
	// StubRequests reports how many refund requests the stub RECEIVED in
	// total; the pre-flight refusals must leave it unchanged.
	StubRequests func() int
	// StubCreated reports how many distinct refunds the stub CREATED (one
	// per idempotency key).
	StubCreated func() int
}

// Run executes the contract against c.
func Run(t *testing.T, c Case) {
	t.Helper()
	if c.Module == nil {
		t.Fatal("contracttest: Case.Module is nil")
	}
	d := c.Module.Descriptor()

	t.Run("descriptor", func(t *testing.T) {
		if err := d.Validate(); err != nil {
			t.Fatalf("descriptor does not validate: %v", err)
		}
		if len(d.Secrets) == 0 {
			t.Error("descriptor lists no secret fields; a provider with no credential cannot exist")
		}
		if len(d.RequiredSecretKeys()) == 0 {
			t.Error("descriptor marks no secret as Required; the config status would be 'configured' with nothing stored")
		}
		if d.Capabilities.HostedCheckout {
			if _, ok := c.Module.(payments.HostedCheckoutProvider); !ok {
				t.Error("Capabilities.HostedCheckout is true but the module is not a HostedCheckoutProvider")
			}
		}
		if d.Capabilities.Refund {
			if _, ok := c.Module.(payments.Refunder); !ok {
				t.Error("Capabilities.Refund is true but the module is not a Refunder")
			}
		}
		if d.Capabilities.RefundLookup {
			if _, ok := c.Module.(payments.RefundLookup); !ok {
				t.Error("Capabilities.RefundLookup is true but the module is not a RefundLookup")
			}
		}
		if (d.Capabilities.PartialRefund || d.Capabilities.AsyncRefund) && !d.Capabilities.Refund {
			t.Error("PartialRefund/AsyncRefund without Refund makes no sense")
		}
		if d.Capabilities.WebhookConfigRouteOnly && d.WebhookSecretKey() == "" {
			t.Error("WebhookConfigRouteOnly needs a WebhookSecret field to verify the callback with")
		}
		if named, ok := c.Module.(interface{ ProviderName() string }); ok && named.ProviderName() != d.Name {
			t.Errorf("ProviderName() = %q but Descriptor().Name = %q", named.ProviderName(), d.Name)
		}
	})

	t.Run("webhook", func(t *testing.T) {
		parser, ok := c.Module.(payments.WebhookParser)
		if !ok {
			t.Skip("module is not a WebhookParser")
		}
		if len(c.GoodWebhook.Body) == 0 {
			t.Fatal("Case.GoodWebhook is empty")
		}
		if !parser.RecognizesWebhook(c.GoodWebhook) {
			t.Error("RecognizesWebhook(GoodWebhook) = false")
		}
		if _, err := parser.VerifyAndParse(t.Context(), c.BadSignatureWebhook); !errors.Is(err, payments.ErrInvalidWebhookSignature) {
			t.Errorf("VerifyAndParse(BadSignatureWebhook) = %v; want ErrInvalidWebhookSignature", err)
		}
		got, err := parser.VerifyAndParse(t.Context(), c.GoodWebhook)
		if err != nil {
			t.Fatalf("VerifyAndParse(GoodWebhook): %v", err)
		}
		if len(got.Raw) == 0 {
			t.Error("NormalizedEvent.Raw is empty; the body must be kept for the audit trail")
		}
		want := c.WantEvent
		if got.Kind != want.Kind || got.Type != want.Type || got.ProviderPaymentID != want.ProviderPaymentID || got.ProviderChargeRef != want.ProviderChargeRef {
			t.Errorf("normalized (kind=%q type=%q payment=%q charge=%q); want (kind=%q type=%q payment=%q charge=%q)",
				got.Kind, got.Type, got.ProviderPaymentID, got.ProviderChargeRef,
				want.Kind, want.Type, want.ProviderPaymentID, want.ProviderChargeRef)
		}
		if want.ProviderRefundID != "" && got.ProviderRefundID != want.ProviderRefundID {
			t.Errorf("ProviderRefundID = %q; want %q", got.ProviderRefundID, want.ProviderRefundID)
		}
		if want.AmountMinor != 0 && got.AmountMinor != want.AmountMinor {
			t.Errorf("AmountMinor = %d; want %d", got.AmountMinor, want.AmountMinor)
		}
		if want.Currency != "" && got.Currency != want.Currency {
			t.Errorf("Currency = %q; want %q", got.Currency, want.Currency)
		}
		if want.Status != "" && got.Status != want.Status {
			t.Errorf("Status = %q; want %q", got.Status, want.Status)
		}
		if want.FailureCode != "" && got.FailureCode != want.FailureCode {
			t.Errorf("FailureCode = %q; want %q", got.FailureCode, want.FailureCode)
		}
	})

	t.Run("refund", func(t *testing.T) {
		if !d.Capabilities.Refund {
			t.Skipf("%s declares no refund capability (arena cannot refund through it yet)", d.Name)
		}
		refunder := c.Module.(payments.Refunder)
		if c.Refund == nil {
			t.Fatal("Case.Refund is required for a module with Capabilities.Refund")
		}
		rc := c.Refund
		base := payments.RefundRequest{
			ProviderChargeRef:  rc.ChargeRef,
			PaymentAmountMinor: rc.PaymentAmountMinor,
			Currency:           rc.Currency,
			Reason:             "contract test",
			Metadata:           payments.RefundMetadata{RefundID: "refund-contract", OrderID: "order-contract"},
		}

		t.Run("zero amount is refused before any call", func(t *testing.T) {
			before := rc.StubRequests()
			req := base
			req.AmountMinor = 0
			req.IdempotencyKey = "contract-zero"
			if _, err := refunder.Refund(t.Context(), req); !errors.Is(err, payments.ErrRefundAmountInvalid) {
				t.Errorf("Refund(0) = %v; want ErrRefundAmountInvalid", err)
			}
			if got := rc.StubRequests(); got != before {
				t.Errorf("the stub received %d request(s) for a zero refund", got-before)
			}
		})

		t.Run("more than the payment is refused before any call", func(t *testing.T) {
			before := rc.StubRequests()
			req := base
			req.AmountMinor = rc.PaymentAmountMinor + 1
			req.IdempotencyKey = "contract-over"
			if _, err := refunder.Refund(t.Context(), req); !errors.Is(err, payments.ErrRefundExceedsPayment) {
				t.Errorf("Refund(payment+1) = %v; want ErrRefundExceedsPayment", err)
			}
			if got := rc.StubRequests(); got != before {
				t.Errorf("the stub received %d request(s) for an over-refund", got-before)
			}
		})

		t.Run("same idempotency key creates one refund", func(t *testing.T) {
			created := rc.StubCreated()
			req := base
			req.AmountMinor = rc.PaymentAmountMinor
			req.IdempotencyKey = "contract-idem"
			first, err := refunder.Refund(t.Context(), req)
			if err != nil {
				t.Fatalf("first Refund: %v", err)
			}
			second, err := refunder.Refund(t.Context(), req)
			if err != nil {
				t.Fatalf("second Refund: %v", err)
			}
			if first.ProviderRefundID == "" {
				t.Error("first Refund answered no ProviderRefundID")
			}
			if !reflect.DeepEqual(first, second) {
				t.Errorf("a retry answered differently: first %+v, second %+v", first, second)
			}
			if got := rc.StubCreated() - created; got != 1 {
				t.Errorf("the stub created %d refund(s) for one idempotency key; want 1", got)
			}
			if first.Status != payments.RefundPending && first.Status != payments.RefundSucceeded && first.Status != payments.RefundFailed {
				t.Errorf("Status = %q is not one of the RefundStatus values", first.Status)
			}
		})

		if d.Capabilities.PartialRefund {
			t.Run("partial amount is sent", func(t *testing.T) {
				req := base
				req.AmountMinor = rc.PaymentAmountMinor / 2
				if req.AmountMinor == 0 {
					t.Skip("payment too small to halve")
				}
				req.IdempotencyKey = "contract-partial"
				if _, err := refunder.Refund(t.Context(), req); err != nil {
					t.Errorf("partial Refund: %v", err)
				}
			})
		}
	})
}

// provider_134_test.go — unit tests for Feature #134: Payment provider adapter
// interface + routing policy.
//
// Test groups:
//
//  1. Compile-time interface assertions (MockProvider, ErrorProvider implement PaymentProvider)
//  2. PaymentProvider interface contract (MockProvider default stub behaviour)
//  3. PaymentRoutingPolicy — correct registration + resolution
//  4. PaymentRoutingPolicy — wrong/unknown provider → ErrUnknownProvider
//  5. PaymentRoutingPolicy — invalid/empty payment_mode → ErrUnknownProvider
//  6. PaymentRoutingPolicy — Register panics on nil/empty-name adapter
//  7. Webhook helpers — VerifyStripeSignature
//  8. Webhook helpers — VerifyAllPaySignature
//  9. Webhook helpers — ComputeHMACSHA256
//
// 10. ErrorProvider always returns the configured error
package payments_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/payments"
)

// ─────────────────────────────────────────────────────────────────────────────
// 1. Compile-time interface assertions
// ─────────────────────────────────────────────────────────────────────────────

// These lines fail to compile if MockProvider or ErrorProvider do not fully
// implement the PaymentProvider interface — the earliest possible feedback.
var _ payments.PaymentProvider = (*payments.MockProvider)(nil)
var _ payments.PaymentProvider = (*payments.ErrorProvider)(nil)
var _ payments.PaymentProvider = payments.NewErrorProvider("test", nil)

// ─────────────────────────────────────────────────────────────────────────────
// 2. PaymentProvider interface contract — MockProvider default stubs
// ─────────────────────────────────────────────────────────────────────────────

func TestPayment134_MockProvider_ProviderName(t *testing.T) {
	m := payments.NewMockProvider("stripe")
	if m.ProviderName() != "stripe" {
		t.Fatalf("expected provider name %q, got %q", "stripe", m.ProviderName())
	}
}

func TestPayment134_MockProvider_CreateIntent_DefaultStub(t *testing.T) {
	m := payments.NewMockProvider("stripe")
	resp, err := m.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:         10000,
		Currency:       "RUB",
		IdempotencyKey: "idem-001",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.ProviderIntentID == "" {
		t.Error("expected non-empty ProviderIntentID")
	}
	if resp.Status == "" {
		t.Error("expected non-empty Status")
	}
}

func TestPayment134_MockProvider_CapturePayment_DefaultStub(t *testing.T) {
	m := payments.NewMockProvider("stripe")
	resp, err := m.CapturePayment(context.Background(), payments.CapturePaymentRequest{
		ProviderIntentID: "pi_test_123",
		Amount:           5000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ProviderIntentID != "pi_test_123" {
		t.Errorf("expected ProviderIntentID %q, got %q", "pi_test_123", resp.ProviderIntentID)
	}
	if resp.Status == "" {
		t.Error("expected non-empty Status")
	}
}

func TestPayment134_MockProvider_RefundPayment_DefaultStub(t *testing.T) {
	m := payments.NewMockProvider("allpay")
	resp, err := m.RefundPayment(context.Background(), payments.RefundPaymentRequest{
		ProviderIntentID: "pi_test_456",
		Amount:           1000,
		Reason:           "customer_request",
		IdempotencyKey:   "idem-ref-001",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.RefundID == "" {
		t.Error("expected non-empty RefundID")
	}
	if resp.Status == "" {
		t.Error("expected non-empty Status")
	}
}

func TestPayment134_MockProvider_HandleWebhook_DefaultStub(t *testing.T) {
	m := payments.NewMockProvider("stripe")
	resp, err := m.HandleWebhook(context.Background(), payments.WebhookRequest{
		Provider:        "stripe",
		SignatureHeader: "t=1234567890,v1=abc",
		Body:            []byte(`{"type":"payment_intent.succeeded"}`),
		Secret:          "whsec_test",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.EventType == "" {
		t.Error("expected non-empty EventType")
	}
}

func TestPayment134_MockProvider_RecordedCalls(t *testing.T) {
	m := payments.NewMockProvider("stripe")
	ctx := context.Background()

	_, _ = m.CreateIntent(ctx, payments.CreateIntentRequest{IdempotencyKey: "k1"})
	_, _ = m.CapturePayment(ctx, payments.CapturePaymentRequest{ProviderIntentID: "pi_1"})
	_, _ = m.RefundPayment(ctx, payments.RefundPaymentRequest{ProviderIntentID: "pi_1"})
	_, _ = m.HandleWebhook(ctx, payments.WebhookRequest{Provider: "stripe"})

	if len(m.RecordedCalls) != 4 {
		t.Fatalf("expected 4 recorded calls, got %d: %v", len(m.RecordedCalls), m.RecordedCalls)
	}
}

func TestPayment134_MockProvider_CustomStub(t *testing.T) {
	m := payments.NewMockProvider("stripe")
	wantErr := errors.New("provider unavailable")
	m.CreateIntentFn = func(_ context.Context, _ payments.CreateIntentRequest) (*payments.CreateIntentResponse, error) {
		return nil, wantErr
	}

	_, err := m.CreateIntent(context.Background(), payments.CreateIntentRequest{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected custom error %v, got %v", wantErr, err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. PaymentRoutingPolicy — registration + successful resolution
// ─────────────────────────────────────────────────────────────────────────────

func TestPayment134_RoutingPolicy_ResolvesStripe(t *testing.T) {
	policy := payments.NewPaymentRoutingPolicy()
	stripe := payments.NewMockProvider("stripe")
	policy.Register(stripe)

	got, err := policy.ResolveProvider(payments.ChannelConfig{
		Provider:    "stripe",
		PaymentMode: "direct_merchant",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ProviderName() != "stripe" {
		t.Errorf("expected provider %q, got %q", "stripe", got.ProviderName())
	}
}

func TestPayment134_RoutingPolicy_ResolvesAllPay(t *testing.T) {
	policy := payments.NewPaymentRoutingPolicy()
	allpay := payments.NewMockProvider("allpay")
	policy.Register(allpay)

	got, err := policy.ResolveProvider(payments.ChannelConfig{
		Provider:    "allpay",
		PaymentMode: "merchant_of_record",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ProviderName() != "allpay" {
		t.Errorf("expected provider %q, got %q", "allpay", got.ProviderName())
	}
}

func TestPayment134_RoutingPolicy_RegistersMultipleAdapters(t *testing.T) {
	policy := payments.NewPaymentRoutingPolicy()
	policy.Register(payments.NewMockProvider("stripe"))
	policy.Register(payments.NewMockProvider("allpay"))

	if policy.Len() != 2 {
		t.Fatalf("expected 2 registered adapters, got %d", policy.Len())
	}
}

func TestPayment134_RoutingPolicy_RegisterOverwrites(t *testing.T) {
	policy := payments.NewPaymentRoutingPolicy()
	policy.Register(payments.NewMockProvider("stripe"))
	policy.Register(payments.NewMockProvider("stripe")) // second registration should overwrite

	if policy.Len() != 1 {
		t.Fatalf("expected 1 adapter after overwrite, got %d", policy.Len())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. PaymentRoutingPolicy — unknown provider → ErrUnknownProvider (→ 400)
// ─────────────────────────────────────────────────────────────────────────────

func TestPayment134_RoutingPolicy_UnknownProvider_ReturnsErrUnknownProvider(t *testing.T) {
	policy := payments.NewPaymentRoutingPolicy()
	policy.Register(payments.NewMockProvider("stripe"))

	_, err := policy.ResolveProvider(payments.ChannelConfig{
		Provider:    "nonexistent_provider",
		PaymentMode: "direct_merchant",
	})
	if !errors.Is(err, payments.ErrUnknownProvider) {
		t.Fatalf("expected ErrUnknownProvider, got %v", err)
	}
}

func TestPayment134_RoutingPolicy_EmptyPolicy_ReturnsErrUnknownProvider(t *testing.T) {
	policy := payments.NewPaymentRoutingPolicy()

	_, err := policy.ResolveProvider(payments.ChannelConfig{
		Provider:    "stripe",
		PaymentMode: "direct_merchant",
	})
	if !errors.Is(err, payments.ErrUnknownProvider) {
		t.Fatalf("expected ErrUnknownProvider for empty policy, got %v", err)
	}
}

func TestPayment134_RoutingPolicy_EmptyProvider_ReturnsErrUnknownProvider(t *testing.T) {
	policy := payments.NewPaymentRoutingPolicy()
	policy.Register(payments.NewMockProvider("stripe"))

	_, err := policy.ResolveProvider(payments.ChannelConfig{
		Provider:    "",
		PaymentMode: "direct_merchant",
	})
	if !errors.Is(err, payments.ErrUnknownProvider) {
		t.Fatalf("expected ErrUnknownProvider for empty provider, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 5. PaymentRoutingPolicy — invalid/empty payment_mode → ErrUnknownProvider
// ─────────────────────────────────────────────────────────────────────────────

func TestPayment134_RoutingPolicy_InvalidPaymentMode_ReturnsErrUnknownProvider(t *testing.T) {
	policy := payments.NewPaymentRoutingPolicy()
	policy.Register(payments.NewMockProvider("stripe"))

	cases := []struct {
		name        string
		paymentMode string
	}{
		{"empty payment_mode", ""},
		{"unknown payment_mode", "cash"},
		{"typo in payment_mode", "direct merchant"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := policy.ResolveProvider(payments.ChannelConfig{
				Provider:    "stripe",
				PaymentMode: tc.paymentMode,
			})
			if !errors.Is(err, payments.ErrUnknownProvider) {
				t.Fatalf("expected ErrUnknownProvider for payment_mode=%q, got %v", tc.paymentMode, err)
			}
		})
	}
}

// Table-driven test: all valid (provider, payment_mode) combos resolve correctly.
func TestPayment134_RoutingPolicy_ValidCombinations(t *testing.T) {
	policy := payments.NewPaymentRoutingPolicy()
	policy.Register(payments.NewMockProvider("stripe"))
	policy.Register(payments.NewMockProvider("allpay"))

	cases := []struct {
		provider    string
		paymentMode string
	}{
		{"stripe", "direct_merchant"},
		{"stripe", "merchant_of_record"},
		{"allpay", "direct_merchant"},
		{"allpay", "merchant_of_record"},
	}

	for _, tc := range cases {
		name := fmt.Sprintf("%s/%s", tc.provider, tc.paymentMode)
		t.Run(name, func(t *testing.T) {
			got, err := policy.ResolveProvider(payments.ChannelConfig{
				Provider:    tc.provider,
				PaymentMode: tc.paymentMode,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.ProviderName() != tc.provider {
				t.Errorf("expected provider %q, got %q", tc.provider, got.ProviderName())
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 6. PaymentRoutingPolicy — Register panics on nil/empty-name adapter
// ─────────────────────────────────────────────────────────────────────────────

func TestPayment134_RoutingPolicy_RegisterNilAdapterPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when registering nil adapter")
		}
	}()
	policy := payments.NewPaymentRoutingPolicy()
	policy.Register(nil)
}

func TestPayment134_RoutingPolicy_RegisterEmptyNamePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when registering adapter with empty ProviderName")
		}
	}()
	policy := payments.NewPaymentRoutingPolicy()
	policy.Register(payments.NewMockProvider("")) // empty name
}

// ─────────────────────────────────────────────────────────────────────────────
// 7. VerifyStripeSignature
// ─────────────────────────────────────────────────────────────────────────────

// buildStripeHeader constructs a valid Stripe-Signature header value.
func buildStripeHeader(ts int64, body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("t=%d,v1=%s", ts, sig)
}

func TestPayment134_StripeSignature_ValidSignature(t *testing.T) {
	secret := "whsec_test_secret"
	body := []byte(`{"type":"payment_intent.succeeded","id":"evt_1"}`)
	ts := time.Now().Unix()
	header := buildStripeHeader(ts, body, secret)

	if err := payments.VerifyStripeSignature(header, body, secret, payments.DefaultWebhookTolerance); err != nil {
		t.Fatalf("expected valid signature, got error: %v", err)
	}
}

func TestPayment134_StripeSignature_WrongSecret(t *testing.T) {
	body := []byte(`{"type":"payment_intent.succeeded"}`)
	ts := time.Now().Unix()
	header := buildStripeHeader(ts, body, "correct_secret")

	err := payments.VerifyStripeSignature(header, body, "wrong_secret", payments.DefaultWebhookTolerance)
	if !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Fatalf("expected ErrInvalidWebhookSignature, got %v", err)
	}
}

func TestPayment134_StripeSignature_EmptyHeader(t *testing.T) {
	err := payments.VerifyStripeSignature("", []byte("body"), "secret", 0)
	if !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Fatalf("expected ErrInvalidWebhookSignature for empty header, got %v", err)
	}
}

func TestPayment134_StripeSignature_MissingTimestamp(t *testing.T) {
	// Construct header without t= component
	body := []byte("body")
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	header := "v1=" + sig // missing t= part

	err := payments.VerifyStripeSignature(header, body, "secret", 0)
	if !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Fatalf("expected ErrInvalidWebhookSignature for missing timestamp, got %v", err)
	}
}

func TestPayment134_StripeSignature_MissingV1Signature(t *testing.T) {
	header := "t=1234567890" // no v1= part
	err := payments.VerifyStripeSignature(header, []byte("body"), "secret", 0)
	if !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Fatalf("expected ErrInvalidWebhookSignature for missing v1, got %v", err)
	}
}

func TestPayment134_StripeSignature_TamperedBody(t *testing.T) {
	secret := "whsec_test"
	originalBody := []byte(`{"amount":10000}`)
	ts := time.Now().Unix()
	header := buildStripeHeader(ts, originalBody, secret)

	// Attacker sends different body
	tamperedBody := []byte(`{"amount":1}`)
	err := payments.VerifyStripeSignature(header, tamperedBody, secret, 0)
	if !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Fatalf("expected ErrInvalidWebhookSignature for tampered body, got %v", err)
	}
}

func TestPayment134_StripeSignature_ExpiredTimestamp(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{"type":"payment_intent.succeeded"}`)
	// Timestamp 10 minutes in the past
	ts := time.Now().Add(-10 * time.Minute).Unix()
	header := buildStripeHeader(ts, body, secret)

	err := payments.VerifyStripeSignature(header, body, secret, payments.DefaultWebhookTolerance)
	if !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Fatalf("expected ErrInvalidWebhookSignature for expired timestamp, got %v", err)
	}
}

func TestPayment134_StripeSignature_ZeroToleranceSkipsTimestampCheck(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{"type":"payment_intent.succeeded"}`)
	// Very old timestamp
	ts := int64(1000000000) // 2001
	header := buildStripeHeader(ts, body, secret)

	// With tolerance=0 the timestamp check is skipped
	if err := payments.VerifyStripeSignature(header, body, secret, 0); err != nil {
		t.Fatalf("expected valid signature with zero tolerance, got error: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 8. VerifyAllPaySignature
// ─────────────────────────────────────────────────────────────────────────────

func TestPayment134_AllPaySignature_ValidSignature(t *testing.T) {
	secret := "allpay_webhook_secret"
	body := []byte(`{"event":"payment.completed","id":"ap_evt_1"}`)
	sigHeader := payments.ComputeHMACSHA256(secret, body)

	if err := payments.VerifyAllPaySignature(sigHeader, body, secret); err != nil {
		t.Fatalf("expected valid signature, got error: %v", err)
	}
}

func TestPayment134_AllPaySignature_WrongSecret(t *testing.T) {
	body := []byte(`{"event":"payment.completed"}`)
	sigHeader := payments.ComputeHMACSHA256("correct_secret", body)

	err := payments.VerifyAllPaySignature(sigHeader, body, "wrong_secret")
	if !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Fatalf("expected ErrInvalidWebhookSignature, got %v", err)
	}
}

func TestPayment134_AllPaySignature_EmptyHeader(t *testing.T) {
	err := payments.VerifyAllPaySignature("", []byte("body"), "secret")
	if !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Fatalf("expected ErrInvalidWebhookSignature for empty header, got %v", err)
	}
}

func TestPayment134_AllPaySignature_TamperedBody(t *testing.T) {
	secret := "allpay_secret"
	originalBody := []byte(`{"amount":10000}`)
	sigHeader := payments.ComputeHMACSHA256(secret, originalBody)

	tamperedBody := []byte(`{"amount":1}`)
	err := payments.VerifyAllPaySignature(sigHeader, tamperedBody, secret)
	if !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Fatalf("expected ErrInvalidWebhookSignature for tampered body, got %v", err)
	}
}

func TestPayment134_AllPaySignature_UppercaseHeaderAccepted(t *testing.T) {
	secret := "allpay_secret"
	body := []byte(`{"event":"payment.completed"}`)
	// Compute lowercase hex, then uppercase it
	lowerhex := payments.ComputeHMACSHA256(secret, body)
	upperhex := ""
	for _, c := range lowerhex {
		if c >= 'a' && c <= 'f' {
			upperhex += string(rune(c - 32))
		} else {
			upperhex += string(c)
		}
	}

	// Should accept uppercase hex (normalised internally)
	if err := payments.VerifyAllPaySignature(upperhex, body, secret); err != nil {
		t.Fatalf("expected uppercase header to be accepted, got error: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 9. ComputeHMACSHA256
// ─────────────────────────────────────────────────────────────────────────────

func TestPayment134_ComputeHMACSHA256_IsLowercaseHex(t *testing.T) {
	result := payments.ComputeHMACSHA256("secret", []byte("payload"))
	if result == "" {
		t.Fatal("expected non-empty HMAC")
	}
	for _, c := range result {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("expected lowercase hex, got char %q in %q", c, result)
		}
	}
}

func TestPayment134_ComputeHMACSHA256_DeterministicForSameInput(t *testing.T) {
	a := payments.ComputeHMACSHA256("secret", []byte("payload"))
	b := payments.ComputeHMACSHA256("secret", []byte("payload"))
	if a != b {
		t.Fatalf("expected deterministic HMAC: %q != %q", a, b)
	}
}

func TestPayment134_ComputeHMACSHA256_DiffersForDifferentPayload(t *testing.T) {
	a := payments.ComputeHMACSHA256("secret", []byte("payload1"))
	b := payments.ComputeHMACSHA256("secret", []byte("payload2"))
	if a == b {
		t.Fatal("expected different HMACs for different payloads")
	}
}

func TestPayment134_ComputeHMACSHA256_DiffersForDifferentSecret(t *testing.T) {
	a := payments.ComputeHMACSHA256("secret1", []byte("payload"))
	b := payments.ComputeHMACSHA256("secret2", []byte("payload"))
	if a == b {
		t.Fatal("expected different HMACs for different secrets")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 10. ErrorProvider
// ─────────────────────────────────────────────────────────────────────────────

func TestPayment134_ErrorProvider_AllMethodsReturnConfiguredError(t *testing.T) {
	sentinel := errors.New("payment backend unavailable")
	ep := payments.NewErrorProvider("stripe", sentinel)
	ctx := context.Background()

	t.Run("ProviderName", func(t *testing.T) {
		if ep.ProviderName() != "stripe" {
			t.Errorf("expected name %q, got %q", "stripe", ep.ProviderName())
		}
	})

	t.Run("CreateIntent", func(t *testing.T) {
		_, err := ep.CreateIntent(ctx, payments.CreateIntentRequest{})
		if !errors.Is(err, sentinel) {
			t.Fatalf("expected sentinel error, got %v", err)
		}
	})

	t.Run("CapturePayment", func(t *testing.T) {
		_, err := ep.CapturePayment(ctx, payments.CapturePaymentRequest{})
		if !errors.Is(err, sentinel) {
			t.Fatalf("expected sentinel error, got %v", err)
		}
	})

	t.Run("RefundPayment", func(t *testing.T) {
		_, err := ep.RefundPayment(ctx, payments.RefundPaymentRequest{})
		if !errors.Is(err, sentinel) {
			t.Fatalf("expected sentinel error, got %v", err)
		}
	})

	t.Run("HandleWebhook", func(t *testing.T) {
		_, err := ep.HandleWebhook(ctx, payments.WebhookRequest{})
		if !errors.Is(err, sentinel) {
			t.Fatalf("expected sentinel error, got %v", err)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Full integration scenario — routing + mock + error path
// ─────────────────────────────────────────────────────────────────────────────

func TestPayment134_Integration_FullRoutingFlow(t *testing.T) {
	policy := payments.NewPaymentRoutingPolicy()
	policy.Register(payments.NewMockProvider("stripe"))
	policy.Register(payments.NewMockProvider("allpay"))

	// Scenario 1: direct_merchant channel on Stripe → CreateIntent succeeds
	t.Run("Stripe_DirectMerchant_CreateIntent", func(t *testing.T) {
		provider, err := policy.ResolveProvider(payments.ChannelConfig{
			Provider:    "stripe",
			PaymentMode: "direct_merchant",
		})
		if err != nil {
			t.Fatalf("routing failed: %v", err)
		}
		resp, err := provider.CreateIntent(context.Background(), payments.CreateIntentRequest{
			Amount:         25000,
			Currency:       "RUB",
			IdempotencyKey: "order-abc-001",
		})
		if err != nil {
			t.Fatalf("CreateIntent failed: %v", err)
		}
		if resp.ProviderIntentID == "" {
			t.Error("expected non-empty ProviderIntentID")
		}
	})

	// Scenario 2: merchant_of_record channel on AllPay → RefundPayment succeeds
	t.Run("AllPay_MOR_Refund", func(t *testing.T) {
		provider, err := policy.ResolveProvider(payments.ChannelConfig{
			Provider:    "allpay",
			PaymentMode: "merchant_of_record",
		})
		if err != nil {
			t.Fatalf("routing failed: %v", err)
		}
		resp, err := provider.RefundPayment(context.Background(), payments.RefundPaymentRequest{
			ProviderIntentID: "ap_pi_999",
			Amount:           0, // full refund
			Reason:           "customer_request",
			IdempotencyKey:   "refund-999-001",
		})
		if err != nil {
			t.Fatalf("RefundPayment failed: %v", err)
		}
		if resp.RefundID == "" {
			t.Error("expected non-empty RefundID")
		}
	})

	// Scenario 3: wrong provider for channel → ErrUnknownProvider (→ 400)
	t.Run("WrongProvider_Returns400Error", func(t *testing.T) {
		_, err := policy.ResolveProvider(payments.ChannelConfig{
			Provider:    "paypal", // not registered
			PaymentMode: "direct_merchant",
		})
		if !errors.Is(err, payments.ErrUnknownProvider) {
			t.Fatalf("expected ErrUnknownProvider (→ 400), got %v", err)
		}
	})
}

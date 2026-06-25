// mock.go provides test doubles for the PaymentProvider interface.
//
// MockProvider — a fully configurable stub whose per-method behaviour can be
// overridden via function fields. All invocations are recorded in RecordedCalls.
//
// ErrorProvider — a minimal provider that always returns a preset error.
// Useful for testing error-path handling without needing a fully configured mock.
//
// Both types are exported so that integration tests in other packages can
// construct test scenarios without importing a real payment adapter.
package payments

import (
	"context"
	"fmt"
)

// ─────────────────────────────────────────────────────────────────────────────
// MockProvider
// ─────────────────────────────────────────────────────────────────────────────

// MockProvider is a configurable test double implementing PaymentProvider.
// Each method dispatches to the corresponding Fn field; swap the Fn to inject
// custom behaviour.  All calls are appended to RecordedCalls in the form
// "<MethodName>:<key>" for later assertion.
type MockProvider struct {
	name string

	// Configurable per-method stubs. NewMockProvider sets sensible defaults.
	CreateIntentFn   func(ctx context.Context, req CreateIntentRequest) (*CreateIntentResponse, error)
	CapturePaymentFn func(ctx context.Context, req CapturePaymentRequest) (*CapturePaymentResponse, error)
	RefundPaymentFn  func(ctx context.Context, req RefundPaymentRequest) (*RefundPaymentResponse, error)
	HandleWebhookFn  func(ctx context.Context, req WebhookRequest) (*WebhookResponse, error)

	// RecordedCalls accumulates one entry per method invocation.
	RecordedCalls []string
}

// NewMockProvider creates a MockProvider with the given provider name and
// default stub implementations that return zero-value success responses.
func NewMockProvider(name string) *MockProvider {
	m := &MockProvider{name: name}

	m.CreateIntentFn = func(_ context.Context, req CreateIntentRequest) (*CreateIntentResponse, error) {
		//nolint:gosec // literal mock fixture, not a real credential
		return &CreateIntentResponse{
			ProviderIntentID: "mock_intent_" + req.IdempotencyKey,
			ClientSecret:     "mock_client_secret",
			Status:           "requires_payment_method",
		}, nil
	}

	m.CapturePaymentFn = func(_ context.Context, req CapturePaymentRequest) (*CapturePaymentResponse, error) {
		return &CapturePaymentResponse{
			ProviderIntentID: req.ProviderIntentID,
			Status:           "captured",
		}, nil
	}

	m.RefundPaymentFn = func(_ context.Context, req RefundPaymentRequest) (*RefundPaymentResponse, error) {
		key := req.IdempotencyKey
		if key == "" {
			key = req.ProviderIntentID
		}
		return &RefundPaymentResponse{
			RefundID: "mock_refund_" + key,
			Status:   "pending",
		}, nil
	}

	m.HandleWebhookFn = func(_ context.Context, req WebhookRequest) (*WebhookResponse, error) {
		return &WebhookResponse{
			EventType: "payment_intent.mock_event",
			Status:    "succeeded",
			Metadata:  map[string]string{"provider": req.Provider},
		}, nil
	}

	return m
}

// ProviderName implements PaymentProvider.
func (m *MockProvider) ProviderName() string { return m.name }

// CreateIntent implements PaymentProvider.
func (m *MockProvider) CreateIntent(ctx context.Context, req CreateIntentRequest) (*CreateIntentResponse, error) {
	m.RecordedCalls = append(m.RecordedCalls, fmt.Sprintf("CreateIntent:%s", req.IdempotencyKey))
	return m.CreateIntentFn(ctx, req)
}

// CapturePayment implements PaymentProvider.
func (m *MockProvider) CapturePayment(ctx context.Context, req CapturePaymentRequest) (*CapturePaymentResponse, error) {
	m.RecordedCalls = append(m.RecordedCalls, fmt.Sprintf("CapturePayment:%s", req.ProviderIntentID))
	return m.CapturePaymentFn(ctx, req)
}

// RefundPayment implements PaymentProvider.
func (m *MockProvider) RefundPayment(ctx context.Context, req RefundPaymentRequest) (*RefundPaymentResponse, error) {
	m.RecordedCalls = append(m.RecordedCalls, fmt.Sprintf("RefundPayment:%s", req.ProviderIntentID))
	return m.RefundPaymentFn(ctx, req)
}

// HandleWebhook implements PaymentProvider.
func (m *MockProvider) HandleWebhook(ctx context.Context, req WebhookRequest) (*WebhookResponse, error) {
	m.RecordedCalls = append(m.RecordedCalls, fmt.Sprintf("HandleWebhook:%s", req.Provider))
	return m.HandleWebhookFn(ctx, req)
}

// ─────────────────────────────────────────────────────────────────────────────
// ErrorProvider
// ─────────────────────────────────────────────────────────────────────────────

// ErrorProvider is a minimal PaymentProvider that always returns a preset error
// for every method. Use NewErrorProvider to construct one.
type ErrorProvider struct {
	name string
	err  error
}

// NewErrorProvider returns a PaymentProvider that always returns err.
// Useful for testing error-handling paths without a real adapter.
func NewErrorProvider(name string, err error) PaymentProvider {
	return &ErrorProvider{name: name, err: err}
}

// ProviderName implements PaymentProvider.
func (e *ErrorProvider) ProviderName() string { return e.name }

// CreateIntent implements PaymentProvider — always returns e.err.
func (e *ErrorProvider) CreateIntent(_ context.Context, _ CreateIntentRequest) (*CreateIntentResponse, error) {
	return nil, e.err
}

// CapturePayment implements PaymentProvider — always returns e.err.
func (e *ErrorProvider) CapturePayment(_ context.Context, _ CapturePaymentRequest) (*CapturePaymentResponse, error) {
	return nil, e.err
}

// RefundPayment implements PaymentProvider — always returns e.err.
func (e *ErrorProvider) RefundPayment(_ context.Context, _ RefundPaymentRequest) (*RefundPaymentResponse, error) {
	return nil, e.err
}

// HandleWebhook implements PaymentProvider — always returns e.err.
func (e *ErrorProvider) HandleWebhook(_ context.Context, _ WebhookRequest) (*WebhookResponse, error) {
	return nil, e.err
}

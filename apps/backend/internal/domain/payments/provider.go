// Package payments defines the core payment adapter interfaces and request/response
// types for the arena payment layer (ADR-008: adapter pattern, ADR-011: direct-merchant
// vs merchant-of-record routing).
//
// Payment provider adapters (Stripe, AllPay, …) each implement the PaymentProvider
// interface. Higher-level application code uses PaymentRoutingPolicy to obtain the
// correct adapter for a sales channel without coupling to a concrete provider.
//
// Sentinel errors:
//
//	ErrUnknownProvider        — routing policy cannot find adapter for the requested provider
//	ErrInvalidWebhookSignature — HMAC verification of an inbound webhook failed
//
// Typical usage:
//
//	policy := payments.NewPaymentRoutingPolicy()
//	policy.Register(stripeAdapter)
//	policy.Register(allpayAdapter)
//
//	provider, err := policy.ResolveProvider(payments.ChannelConfig{
//	    Provider:    channel.Provider,
//	    PaymentMode: channel.PaymentMode,
//	})
//	if errors.Is(err, payments.ErrUnknownProvider) {
//	    http.Error(w, "bad provider", http.StatusBadRequest)
//	    return
//	}
//	resp, err := provider.CreateIntent(ctx, req)
package payments

import (
	"context"
	"errors"
)

// ─────────────────────────────────────────────────────────────────────────────
// Sentinel errors
// ─────────────────────────────────────────────────────────────────────────────

// ErrUnknownProvider is returned by PaymentRoutingPolicy.ResolveProvider when no
// adapter is registered for the requested provider name, or when the channel
// configuration specifies an unrecognised payment_mode.
//
// HTTP handlers should map this error to 400 Bad Request.
var ErrUnknownProvider = errors.New("payments: unknown provider")

// ErrInvalidWebhookSignature is returned by webhook verification helpers when
// the HMAC signature in the inbound webhook request does not match the expected
// value computed from the shared secret.
var ErrInvalidWebhookSignature = errors.New("payments: invalid webhook signature")

// ─────────────────────────────────────────────────────────────────────────────
// Request / response types
// ─────────────────────────────────────────────────────────────────────────────

// CreateIntentRequest holds the parameters for creating a new payment intent.
type CreateIntentRequest struct {
	// Amount is the total in the smallest currency unit (e.g. kopeks for RUB, cents for USD).
	Amount int64
	// Currency is the ISO 4217 three-letter currency code, e.g. "RUB".
	Currency string
	// Description is a human-readable label attached to the payment (optional).
	Description string
	// Metadata is an arbitrary set of string key-value pairs forwarded to the provider.
	Metadata map[string]string
	// IdempotencyKey is the caller-supplied key used to de-duplicate retries.
	IdempotencyKey string
}

// CreateIntentResponse is returned by PaymentProvider.CreateIntent.
type CreateIntentResponse struct {
	// ProviderIntentID is the provider-assigned payment intent identifier.
	ProviderIntentID string
	// ClientSecret is the client-side credential that the front-end SDK uses to
	// confirm the payment (Stripe-style). May be empty for providers that do not
	// use client-side confirmation.
	ClientSecret string
	// Status is the initial intent status returned by the provider,
	// e.g. "requires_payment_method", "pending".
	Status string
	// Metadata echoes back provider-enriched metadata.
	Metadata map[string]string
}

// CapturePaymentRequest holds the parameters for capturing an authorised payment.
type CapturePaymentRequest struct {
	// ProviderIntentID is the identifier of the previously authorised intent.
	ProviderIntentID string
	// Amount is the amount to capture (must be ≤ authorised amount).
	// A zero value captures the full authorised amount.
	Amount int64
}

// CapturePaymentResponse is returned by PaymentProvider.CapturePayment.
type CapturePaymentResponse struct {
	// ProviderIntentID echoes back the captured intent ID.
	ProviderIntentID string
	// Status is the post-capture status, e.g. "captured", "succeeded".
	Status string
}

// RefundPaymentRequest holds the parameters for refunding a captured payment.
type RefundPaymentRequest struct {
	// ProviderIntentID is the identifier of the captured payment to refund.
	ProviderIntentID string
	// Amount is the amount to refund in the smallest currency unit.
	// A zero value triggers a full refund.
	Amount int64
	// Reason is a human-readable label for the refund, e.g. "customer_request".
	Reason string
	// IdempotencyKey is the caller-supplied key used to de-duplicate retries.
	IdempotencyKey string
}

// RefundPaymentResponse is returned by PaymentProvider.RefundPayment.
type RefundPaymentResponse struct {
	// RefundID is the provider-assigned refund identifier.
	RefundID string
	// Status is the initial refund status, e.g. "pending", "succeeded".
	Status string
}

// WebhookRequest is the raw inbound webhook payload from a payment provider.
type WebhookRequest struct {
	// Provider is the canonical provider name, e.g. "stripe" or "allpay".
	Provider string
	// SignatureHeader is the provider-specific signature header value.
	// For Stripe this is the raw "Stripe-Signature" header value.
	// For AllPay this is the raw "X-AllPay-Signature" header value.
	SignatureHeader string
	// Body is the raw (un-parsed) request body bytes used for signature verification.
	Body []byte
	// Secret is the webhook endpoint secret configured for this provider channel.
	Secret string
}

// WebhookResponse is returned by PaymentProvider.HandleWebhook after signature
// verification and event parsing succeed.
type WebhookResponse struct {
	// EventType is the provider-specific event type string,
	// e.g. "payment_intent.succeeded".
	EventType string
	// ProviderIntentID is the payment intent the event refers to (may be empty
	// for non-payment events).
	ProviderIntentID string
	// Status is the resource status reported in the event.
	Status string
	// Metadata contains any additional event-level key-value data.
	Metadata map[string]string
}

// ─────────────────────────────────────────────────────────────────────────────
// PaymentProvider interface
// ─────────────────────────────────────────────────────────────────────────────

// PaymentProvider is the adapter interface that all payment backend providers
// must implement (ADR-008). Concrete adapters for Stripe, AllPay, and any future
// provider implement this interface.
//
// All methods accept a context.Context for cancellation and timeout propagation.
// Errors from the underlying provider should be wrapped with additional context
// before being returned.
type PaymentProvider interface {
	// ProviderName returns the canonical, lowercase name for this provider,
	// e.g. "stripe" or "allpay". This value is used as the registry key in
	// PaymentRoutingPolicy.
	ProviderName() string

	// CreateIntent creates a new payment intent with the given parameters.
	// Implementations must honour the IdempotencyKey to allow safe retries.
	CreateIntent(ctx context.Context, req CreateIntentRequest) (*CreateIntentResponse, error)

	// CapturePayment captures (finalises) a previously authorised payment intent.
	CapturePayment(ctx context.Context, req CapturePaymentRequest) (*CapturePaymentResponse, error)

	// RefundPayment initiates a refund for a captured payment. A zero Amount
	// triggers a full refund; otherwise a partial refund is applied.
	RefundPayment(ctx context.Context, req RefundPaymentRequest) (*RefundPaymentResponse, error)

	// HandleWebhook parses and validates an inbound webhook notification from the
	// provider. Implementations MUST verify the webhook signature using
	// req.SignatureHeader and req.Secret before returning any data.
	// Returns ErrInvalidWebhookSignature on verification failure.
	HandleWebhook(ctx context.Context, req WebhookRequest) (*WebhookResponse, error)
}

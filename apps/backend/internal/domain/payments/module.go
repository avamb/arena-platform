// module.go — the payment MODULE contract (spec
// 08_architecture/36_payment_modules_refunds_acquiring_ru.md §5, PAY-01).
//
// A provider is one package under internal/adapters/<provider>. The core
// (hcheckout, hfeed, hpayments) knows only what is declared here: a static
// Descriptor (name, secrets, capabilities — no network) and a handful of
// narrow, OPTIONAL interfaces discovered by type assertion:
//
//	HostedCheckoutProvider — hosted_checkout.go (already existed)
//	CredentialVerifier     — credentials.go (already existed)
//	WebhookParser          — verifies a delivery and normalizes it
//	Refunder               — drives a refund through the provider
//	RefundLookup           — reads a refund's status back
//
// Why optional interfaces rather than one wide one: AllPay has no hosted
// page, Flitt cannot refund yet, a declared-but-unbuilt provider has nothing
// at all. A module advertises what it can do in Capabilities AND implements
// the matching interface; the core checks both, so a capability flag can
// never promise what the type cannot deliver.
//
// Provider NAMES never appear in the core: a static guardrail
// (tests/staticanalysis/payment_provider_literals_test.go) fails on a
// "stripe"/"flitt"/"allpay" literal in the handler packages. The only list
// of connected modules is internal/app/payments/modules.go.
package payments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// Descriptor
// ─────────────────────────────────────────────────────────────────────────────

// SecretField describes one key of a config's secrets blob
// (payment_provider_configs.secrets) as the provider needs it.
type SecretField struct {
	// Key is the jsonb key ("api_key", "merchant_id", …).
	Key string
	// Label is how the field is described to an operator, in the provider's
	// own vocabulary, so they know what to go and fetch.
	Label string
	// Required: the config does not count as "configured" without it.
	Required bool
	// Credential: needed to TALK to the provider's API (hosted checkout,
	// credential verification, refunds). A config missing one cannot start
	// a payment even when its status says configured.
	Credential bool
	// WebhookSecret: verifies INBOUND deliveries. A provider that signs
	// callbacks with the same key that signs requests marks that key with
	// both Credential and WebhookSecret.
	WebhookSecret bool
	// Hidden: never echoed back to a client, even to its owner.
	Hidden bool
	// Pattern is an optional regexp the value must match. Empty means no
	// rule. Prefer Prefixes for an operator-facing message.
	Pattern string
	// Prefixes the value may start with. Empty means "no prefix rule".
	Prefixes []string
	// ModePrefixes maps a config mode ("test"/"live") to the prefixes legal
	// in THAT mode, so a live key cannot be filed under test. A mode absent
	// from the map is checked against Prefixes alone.
	ModePrefixes map[string][]string
}

// Capabilities is what a module can do. Each true flag MUST be backed by the
// matching interface on the module value; the core checks both.
type Capabilities struct {
	// HostedCheckout: the module renders a provider-hosted payment page
	// (HostedCheckoutProvider).
	HostedCheckout bool
	// Refund: arena can drive a refund through the provider (Refunder).
	Refund bool
	// PartialRefund: a refund may be for less than the payment.
	PartialRefund bool
	// AsyncRefund: the final verdict of a refund arrives later by webhook.
	AsyncRefund bool
	// RefundLookup: the module can read a refund's status by its id.
	RefundLookup bool
	// BuyerReturnsByPOST: the provider may bring the buyer back to the
	// return URL with a POST, so the hosted checkout must route the return
	// through arena's own GET|POST payment-return hop rather than straight
	// to the buyer's (static) page.
	BuyerReturnsByPOST bool
	// WebhookConfigRouteOnly: the provider's callback can only be
	// authenticated on the per-config webhook route — it carries no header
	// that would pick a process-wide fallback secret, and is signed with a
	// per-merchant key the config alone holds. The legacy un-suffixed route
	// refuses such a body outright.
	WebhookConfigRouteOnly bool
}

// Descriptor is the static, network-free description of a module.
type Descriptor struct {
	// Name is the canonical lower-case provider key: the value stored in
	// sales_channels.provider, payment_provider_configs.provider and
	// payment_intents.provider.
	Name string
	// Title is the human name ("Stripe").
	Title string
	// Secrets lists the config's secret fields.
	Secrets []SecretField
	// Capabilities is what the module can do.
	Capabilities Capabilities
}

// Module is the one thing every provider package must implement.
type Module interface {
	Descriptor() Descriptor
}

var providerNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Validate checks the descriptor's own consistency: a canonical name, a
// title, unique non-empty secret keys, compilable patterns, at most one
// webhook secret.
func (d Descriptor) Validate() error {
	if !providerNamePattern.MatchString(d.Name) {
		return fmt.Errorf("payments: descriptor name %q must be lower-case [a-z0-9_] and start with a letter", d.Name)
	}
	if strings.TrimSpace(d.Title) == "" {
		return fmt.Errorf("payments: descriptor %q has no title", d.Name)
	}
	seen := make(map[string]bool, len(d.Secrets))
	webhookSecrets := 0
	for _, s := range d.Secrets {
		key := strings.TrimSpace(s.Key)
		if key == "" {
			return fmt.Errorf("payments: descriptor %q has a secret field with an empty key", d.Name)
		}
		if seen[key] {
			return fmt.Errorf("payments: descriptor %q lists secret %q twice", d.Name, key)
		}
		seen[key] = true
		if s.Pattern != "" {
			if _, err := regexp.Compile(s.Pattern); err != nil {
				return fmt.Errorf("payments: descriptor %q secret %q pattern: %w", d.Name, key, err)
			}
		}
		if s.WebhookSecret {
			webhookSecrets++
		}
	}
	if webhookSecrets > 1 {
		return fmt.Errorf("payments: descriptor %q marks %d secrets as the webhook secret; at most one", d.Name, webhookSecrets)
	}
	return nil
}

// Secret returns the field with the given key.
func (d Descriptor) Secret(key string) (SecretField, bool) {
	for _, s := range d.Secrets {
		if s.Key == key {
			return s, true
		}
	}
	return SecretField{}, false
}

// RequiredSecretKeys lists the keys a config must hold to count as
// configured, in declaration order.
func (d Descriptor) RequiredSecretKeys() []string {
	return d.keysWhere(func(s SecretField) bool { return s.Required })
}

// CredentialKeys lists the keys needed to talk to the provider's API.
func (d Descriptor) CredentialKeys() []string {
	return d.keysWhere(func(s SecretField) bool { return s.Credential })
}

// WebhookSecretKey is the key of the secret that verifies inbound
// deliveries, or "" when the module declares none.
func (d Descriptor) WebhookSecretKey() string {
	for _, s := range d.Secrets {
		if s.WebhookSecret {
			return s.Key
		}
	}
	return ""
}

func (d Descriptor) keysWhere(keep func(SecretField) bool) []string {
	out := make([]string, 0, len(d.Secrets))
	for _, s := range d.Secrets {
		if keep(s) {
			out = append(out, s.Key)
		}
	}
	return out
}

// NormalizeProviderName folds a stored provider value to the registry key:
// trimmed and lower-cased.
func NormalizeProviderName(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// SecretsFromJSON decodes a config's secrets blob into the flat string map
// a Factory takes. Values are trimmed; non-string values are skipped; an
// unreadable or empty blob yields an empty map, never an error — a module
// built from it simply holds no credential.
func SecretsFromJSON(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return out
	}
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = strings.TrimSpace(s)
		}
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Webhooks
// ─────────────────────────────────────────────────────────────────────────────

// WebhookInput is one inbound delivery as received: the raw bytes the
// signature was computed over and the request headers. The secrets are NOT
// here — the module was built from the config's secrets and holds them.
type WebhookInput struct {
	Body   []byte
	Header http.Header
}

// EventKind is the provider-neutral meaning of a delivery.
type EventKind string

// Event kinds. The core maps a kind to a payment-intent state; a kind it
// does not map is acknowledged without a transition (the provider is told
// "received", nothing moves).
const (
	// EventUnknown: a delivery the module does not interpret (an event type
	// arena does not handle, or a body that is not an event at all).
	EventUnknown EventKind = "unknown"
	// EventPaymentRequiresAction: the buyer must act (3DS and the like).
	EventPaymentRequiresAction EventKind = "payment_requires_action"
	// EventPaymentProcessing: the provider is working on it.
	EventPaymentProcessing EventKind = "payment_processing"
	// EventPaymentAuthorized: funds are held, not yet captured.
	EventPaymentAuthorized EventKind = "payment_authorized"
	// EventPaymentPending: the buyer finished the hosted page but the money
	// has NOT settled yet (an async method); nothing may ship on it.
	EventPaymentPending EventKind = "payment_pending"
	// EventPaymentSucceeded: the money settled.
	EventPaymentSucceeded EventKind = "payment_succeeded"
	// EventPaymentFailed: the payment failed for good.
	EventPaymentFailed EventKind = "payment_failed"
	// EventPaymentExpired: the hosted session / order died unpaid.
	EventPaymentExpired EventKind = "payment_expired"
	// EventPaymentDeclined: one attempt was declined but the provider's
	// order stays open for another card — the intent must NOT move.
	EventPaymentDeclined EventKind = "payment_declined"
	// EventPaymentManualReview: the provider parked the payment for review.
	EventPaymentManualReview EventKind = "payment_manual_review"
	// EventRefundUpdated: a refund changed state (PAY-05 acts on it).
	EventRefundUpdated EventKind = "refund_updated"
	// EventDisputeOpened: a chargeback / dispute was opened (PAY-16).
	EventDisputeOpened EventKind = "dispute_opened"
)

// NormalizedEvent is a verified delivery in arena's own vocabulary.
type NormalizedEvent struct {
	// Kind is the meaning; Type is the provider's own event name, kept for
	// the (provider_payment_id, event_type) idempotency key, the metrics
	// label and the response body.
	Kind EventKind
	Type string
	// ProviderEventID is the provider's id of the delivery itself (Stripe
	// evt_…), for the audit trail; empty when the provider has none.
	ProviderEventID string
	// ProviderPaymentID is what arena stored as payment_intents.
	// provider_payment_id for this payment (Stripe cs_…/pi_…, Flitt: our
	// own order id). Empty means the body names no payment arena could own.
	ProviderPaymentID string
	// ProviderChargeRef is the id a REFUND is driven through (Stripe pi_…
	// behind a hosted session, Flitt's numeric payment_id). Empty when the
	// delivery carries none.
	ProviderChargeRef string
	// ProviderRefundID is set on refund events.
	ProviderRefundID string
	// Status is the provider's own status string of the object.
	Status string
	// AmountMinor and Currency, when the delivery states them (minor units
	// as the provider sent them; zero when absent).
	AmountMinor int64
	Currency    string
	// FailureCode / FailureMessage, when the delivery explains a failure.
	FailureCode    string
	FailureMessage string
	// Raw is the body as received, for the audit trail.
	Raw json.RawMessage
}

// ErrWebhookNotRecognized is returned by VerifyAndParse when the request is
// not shaped like this provider's delivery at all (no signature header, not
// its body shape) — distinct from ErrInvalidWebhookSignature, which means
// "it is ours and it does not verify".
var ErrWebhookNotRecognized = errors.New("payments: request is not this provider's webhook")

// WebhookParser verifies and normalizes inbound deliveries.
type WebhookParser interface {
	// RecognizesWebhook reports whether the request is shaped like this
	// provider's delivery — its signature header, or its body shape. It
	// needs no secret and verifies nothing; it exists so a route that does
	// not know the provider up front can ask every module.
	RecognizesWebhook(in WebhookInput) bool
	// VerifyAndParse checks the signature against the secret the module was
	// built with and normalizes the body. A wrong or missing signature is
	// ErrInvalidWebhookSignature (possibly wrapped); a request that is not
	// this provider's at all is ErrWebhookNotRecognized. Nothing may be
	// acted on from a delivery that returned an error.
	VerifyAndParse(ctx context.Context, in WebhookInput) (NormalizedEvent, error)
}

// ─────────────────────────────────────────────────────────────────────────────
// Refunds
// ─────────────────────────────────────────────────────────────────────────────

// RefundMetadata is what arena attaches to a refund at the provider so a
// refund seen in the provider's dashboard can be traced back.
type RefundMetadata struct {
	RefundID string
	OrderID  string
}

// RefundRequest asks the provider to return money.
type RefundRequest struct {
	// ProviderChargeRef is the payment to refund, as the provider names it
	// for refunds (payment_intents.provider_charge_ref).
	ProviderChargeRef string
	// AmountMinor is the amount in ARENA minor units (orders.total units).
	// The module converts to the provider's units; the core never does.
	AmountMinor int64
	// PaymentAmountMinor is the original payment, in the same units, so the
	// module can refuse an over-refund before any network call. Zero means
	// unknown — the provider decides.
	PaymentAmountMinor int64
	// Currency is the ISO 4217 code of the payment.
	Currency string
	// Reason is free text for the provider's record.
	Reason string
	// IdempotencyKey makes a retry safe. Arena uses the refund row id.
	IdempotencyKey string
	// Metadata travels to the provider.
	Metadata RefundMetadata
}

// RefundStatus is the provider-neutral state of a refund.
type RefundStatus string

const (
	RefundPending   RefundStatus = "pending"
	RefundSucceeded RefundStatus = "succeeded"
	RefundFailed    RefundStatus = "failed"
)

// RefundResult is the provider's answer.
type RefundResult struct {
	ProviderRefundID string
	Status           RefundStatus
	FailureCode      string
	FailureMessage   string
}

// Refund refusals a module raises BEFORE any network call.
var (
	ErrRefundAmountInvalid    = errors.New("payments: refund amount must be positive")
	ErrRefundExceedsPayment   = errors.New("payments: refund amount exceeds the payment")
	ErrRefundChargeRefMissing = errors.New("payments: the payment has no provider charge reference")
)

// Refunder drives a refund through the provider.
type Refunder interface {
	Refund(ctx context.Context, req RefundRequest) (RefundResult, error)
}

// RefundLookup reads a refund's status back from the provider.
type RefundLookup interface {
	RefundStatus(ctx context.Context, providerRefundID string) (RefundResult, error)
}

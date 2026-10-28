// module.go — PayPlus as a payment MODULE (spec 36 §5, spec 47 §3):
// descriptor, registry factory and the WebhookParser that verifies a callback.
//
// Capabilities: a hosted payment page; a callback only the per-config route
// can verify (signed under the merchant's own secret key, no header that
// would pick a platform-wide secret); the buyer comes back through arena's
// payment-return hop; refunds, full and partial, answered synchronously by
// Transactions/RefundByTransactionUID (refund.go).
package payplus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// Name is the canonical provider key.
const Name = "payplus"

// Secret keys of a PayPlus payment_provider_configs row.
const (
	// SecretAPIKey is the dashboard's API key (header api-key).
	SecretAPIKey = "api_key"
	// SecretKey is the dashboard's secret key (header secret-key). It also
	// signs every callback, so it is the webhook secret too.
	SecretKey = "secret_key"
	// SecretPaymentPageUID names the payment page links are generated from
	// (dashboard → Payment pages).
	SecretPaymentPageUID = "payment_page_uid"
)

// Event types arena records. They are half of the (provider_payment_id,
// event_type) idempotency key of payment_intent_events, so they must not
// change.
const (
	EventTypeApproved = "payplus.charge.approved"
	EventTypeDeclined = "payplus.charge.declined"
	EventTypeRefund   = "payplus.refund"
	eventTypePrefix   = "payplus."
)

// compile-time guards for the module contract
var (
	_ payments.Module        = (*Adapter)(nil)
	_ payments.WebhookParser = (*Adapter)(nil)
)

const uuidPattern = `^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`

// ProviderDescriptor is PayPlus's static description.
func ProviderDescriptor() payments.Descriptor {
	return payments.Descriptor{
		Name:  Name,
		Title: "PayPlus",
		Secrets: []payments.SecretField{
			{
				Key:        SecretAPIKey,
				Label:      "PayPlus API key (dashboard → Settings → API keys)",
				Required:   true,
				Credential: true,
			},
			{
				Key:           SecretKey,
				Label:         "PayPlus secret key of the same API key pair (also verifies callbacks)",
				Required:      true,
				Credential:    true,
				WebhookSecret: true,
				Hidden:        true,
			},
			{
				Key:        SecretPaymentPageUID,
				Label:      "PayPlus payment page UID (dashboard → Payment pages)",
				Required:   true,
				Credential: true,
				Pattern:    uuidPattern,
			},
		},
		Capabilities: payments.Capabilities{
			HostedCheckout: true,
			// PayPlus does not document the method of its return redirect;
			// the return goes through arena's GET|POST hop.
			BuyerReturnsByPOST: true,
			// Signed under the merchant's own secret key: only the config in
			// the URL can vouch for a callback.
			WebhookConfigRouteOnly: true,
			Refund:                 true,
			PartialRefund:          true,
		},
	}
}

// Descriptor implements payments.Module.
func (a *Adapter) Descriptor() payments.Descriptor { return ProviderDescriptor() }

// Entry is PayPlus's registry entry.
func Entry() payments.Entry {
	return payments.Entry{Descriptor: ProviderDescriptor(), New: newModule}
}

// newModule builds the module. The REST host follows the configuration's
// mode (test → restapidev, live → restapi) unless the test seam
// Options.PayPlusAPIBaseURL (PAYPLUS_API_BASE_URL) overrides both.
func newModule(secrets map[string]string, opts payments.Options) (payments.Module, error) {
	return New(Config{
		APIKey:         secrets[SecretAPIKey],
		SecretKey:      secrets[SecretKey],
		PaymentPageUID: secrets[SecretPaymentPageUID],
		BaseURL:        BaseURLFor(opts.Mode, opts.PayPlusAPIBaseURL),
	}), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// WebhookParser
// ─────────────────────────────────────────────────────────────────────────────

// Callback is a parsed PayPlus callback.
type Callback struct {
	// TransactionType is the top-level transaction_type ("Charge", "Refund").
	TransactionType string
	Transaction     Transaction
	// StatusDescription explains a failed attempt, when PayPlus says why.
	StatusDescription string
}

type callbackShape struct {
	TransactionType string                     `json:"transaction_type"`
	Transaction     map[string]json.RawMessage `json:"transaction"`
}

// LooksLikeCallback reports whether body has the shape of a PayPlus
// callback: a top-level `transaction` object carrying uid and status_code.
// Stripe's envelope, Flitt's and AllPay's flat bodies have no such object.
func LooksLikeCallback(body []byte) bool {
	var s callbackShape
	if err := json.Unmarshal(body, &s); err != nil || s.Transaction == nil {
		return false
	}
	_, hasUID := s.Transaction["uid"]
	_, hasStatus := s.Transaction["status_code"]
	return hasUID && hasStatus
}

// ParseCallback decodes a callback body WITHOUT checking its signature.
func ParseCallback(body []byte) (*Callback, error) {
	var top map[string]any
	if err := decodeNumbers(body, &top); err != nil {
		return nil, fmt.Errorf("payplus: decode callback: %w", err)
	}
	inner, ok := top["transaction"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: no transaction object", payments.ErrWebhookNotRecognized)
	}
	cb := &Callback{
		TransactionType: scalar(top["transaction_type"]),
		Transaction:     transactionFromMap(inner, scalar(top["transaction_type"])),
	}
	cb.StatusDescription = scalar(inner["status_description"])
	return cb, nil
}

// RecognizesWebhook: a PayPlus callback is recognized by its body shape (the
// `hash` header alone would also match other senders' headers).
func (a *Adapter) RecognizesWebhook(in payments.WebhookInput) bool {
	return LooksLikeCallback(in.Body)
}

// VerifyAndParse verifies the `hash` header under the secret key and
// normalizes the callback. An empty key or a missing header never verifies.
func (a *Adapter) VerifyAndParse(_ context.Context, in payments.WebhookInput) (payments.NormalizedEvent, error) {
	if !LooksLikeCallback(in.Body) {
		return payments.NormalizedEvent{}, fmt.Errorf("%w: not a PayPlus callback", payments.ErrWebhookNotRecognized)
	}
	hash := ""
	if in.Header != nil {
		hash = in.Header.Get(HeaderHash)
	}
	if _, err := VerifyHash(in.Body, hash, a.cfg.SecretKey); err != nil {
		return payments.NormalizedEvent{}, err
	}
	cb, err := ParseCallback(in.Body)
	if err != nil {
		return payments.NormalizedEvent{}, err
	}
	return NormalizeCallback(cb, in.Body), nil
}

// NormalizeCallback maps a verified callback onto arena's event vocabulary.
//
//   - ProviderPaymentID is transaction.payment_request_uid: the link's
//     page_request_uid arena stored as provider_payment_id. A callback
//     without one (a transaction made outside a payment page) names no
//     payment arena could own: EventUnknown, acknowledged.
//   - ProviderChargeRef is transaction.uid, the id a refund is driven
//     through. ProviderEventID is the same uid: it identifies the delivery
//     (PayPlus does not document retries; a replay repeats the uid and the
//     core's (payment, event type) key makes it a no-op).
//   - A Charge with status_code 000 is the money settled.
//   - Any other status_code is ONE failed attempt (send_failure_callback):
//     EventPaymentDeclined, which moves nothing. The link stays valid until
//     expiry_datetime and the buyer may try another card on it; failing the
//     intent here would make arena ignore the later approval — a paid
//     purchase lost (the Flitt and AllPay rule). An abandoned link is
//     released by arena's own hold expiry.
//   - A Refund callback (PayPlus also reports refunds to the callback URL)
//     must never read as a payment: arena drives refunds synchronously
//     (refund.go), so it is acknowledged without a transition.
//   - Any other transaction type (Approval J5, Check J2, Token) moves
//     nothing: arena asks for J4 charges only.
func NormalizeCallback(cb *Callback, body []byte) payments.NormalizedEvent {
	tx := cb.Transaction
	ev := payments.NormalizedEvent{
		Kind:              payments.EventUnknown,
		ProviderEventID:   tx.UID,
		ProviderPaymentID: tx.PaymentRequestUID,
		ProviderChargeRef: tx.UID,
		Status:            tx.StatusCode,
		AmountMinor:       tx.AmountMinor,
		Currency:          tx.Currency,
		Raw:               json.RawMessage(bytes.Clone(body)),
	}
	kind := strings.ToLower(strings.TrimSpace(cb.TransactionType))
	switch {
	case strings.Contains(kind, "refund"):
		ev.Type = EventTypeRefund
		ev.ProviderChargeRef = ""
	case kind != "" && kind != "charge":
		ev.Type = eventTypePrefix + strings.ReplaceAll(kind, " ", "_")
	case tx.PaymentRequestUID == "":
		ev.Type = eventTypePrefix + "charge.no_payment_page"
	case tx.StatusCode == StatusCodeApproved:
		ev.Kind = payments.EventPaymentSucceeded
		ev.Type = EventTypeApproved
	default:
		ev.Kind = payments.EventPaymentDeclined
		ev.Type = EventTypeDeclined
		ev.FailureCode = tx.StatusCode
		ev.FailureMessage = cb.StatusDescription
	}
	return ev
}

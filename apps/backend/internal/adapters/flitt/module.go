// module.go — Flitt as a payment MODULE (spec 36 §5, PAY-01): descriptor,
// registry factory and the WebhookParser that verifies a callback. Refunds
// through Flitt are PAY-13: Capabilities.Refund stays false until then, so
// the core answers "provider_not_supported" rather than pretending.
//
// Behaviour is exactly what the per-config webhook route did inline before
// PAY-01: merchant_id compared BEFORE the hash (the cheap check, and a
// callback addressed to another merchant is not this config's even when a
// shared payment key would let it verify), then VerifyCallback, then the
// order_status → event mapping hcheckout used to keep in its state table.
package flitt

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// Name is the canonical provider key.
const Name = "flitt"

// Secret keys of a Flitt payment_provider_configs row.
const (
	// SecretMerchantID is the numeric id from the portal.
	SecretMerchantID = "merchant_id"
	// SecretPaymentKey signs every request AND verifies every callback;
	// there is no separate webhook secret. (The portal's "credit key" is for
	// payouts — never used.)
	SecretPaymentKey = "payment_key"
)

// EventTypePrefix is prepended to a callback's order_status to form the
// event type arena records ("flitt.order.approved"). It is part of the
// (provider_payment_id, event_type) idempotency key of
// payment_intent_events, so it must not change.
const EventTypePrefix = "flitt.order."

// compile-time guards for the module contract
var (
	_ payments.Module        = (*Adapter)(nil)
	_ payments.WebhookParser = (*Adapter)(nil)
)

// ProviderDescriptor is Flitt's static description.
func ProviderDescriptor() payments.Descriptor {
	return payments.Descriptor{
		Name:  Name,
		Title: "Flitt",
		Secrets: []payments.SecretField{
			{
				Key:        SecretMerchantID,
				Label:      "Flitt merchant id (portal → technical settings)",
				Required:   true,
				Credential: true,
			},
			{
				Key:           SecretPaymentKey,
				Label:         "Flitt payment key (portal → technical settings → payment key)",
				Required:      true,
				Credential:    true,
				WebhookSecret: true,
				Hidden:        true,
			},
		},
		Capabilities: payments.Capabilities{
			HostedCheckout: true,
			// Flitt returns the buyer with the method set in ITS portal
			// (default POST), and a static tickets page answers a POST with
			// 405 — the return goes through arena's payment-return hop.
			BuyerReturnsByPOST: true,
			// The callback carries its signature in the body under the
			// merchant's own key; only the config named in the URL can vouch
			// for it.
			WebhookConfigRouteOnly: true,
			// PAY-13: Refund (reverse), AsyncRefund, PartialRefund.
		},
	}
}

// Descriptor implements payments.Module.
func (a *Adapter) Descriptor() payments.Descriptor { return ProviderDescriptor() }

// Entry is Flitt's registry entry. BaseURL comes from Options.FlittAPIBaseURL
// (the test seam); empty means pay.flitt.com.
func Entry() payments.Entry {
	return payments.Entry{Descriptor: ProviderDescriptor(), New: newModule}
}

func newModule(secrets map[string]string, opts payments.Options) (payments.Module, error) {
	return New(Config{
		MerchantID: secrets[SecretMerchantID],
		PaymentKey: secrets[SecretPaymentKey],
		BaseURL:    opts.FlittAPIBaseURL,
	}), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// WebhookParser
// ─────────────────────────────────────────────────────────────────────────────

// RecognizesWebhook: a Flitt callback has no header of its own; it is
// recognised by its body shape (order_id, order_status, merchant_id).
func (a *Adapter) RecognizesWebhook(in payments.WebhookInput) bool {
	return LooksLikeCallback(in.Body)
}

// VerifyAndParse checks the callback belongs to THIS merchant, verifies its
// body signature under the payment key and normalizes it.
func (a *Adapter) VerifyAndParse(_ context.Context, in payments.WebhookInput) (payments.NormalizedEvent, error) {
	if !LooksLikeCallback(in.Body) {
		return payments.NormalizedEvent{}, fmt.Errorf("%w: not a Flitt callback", payments.ErrWebhookNotRecognized)
	}
	unverified, err := ParseCallbackUnverified(in.Body)
	if err != nil {
		return payments.NormalizedEvent{}, fmt.Errorf("flitt: unreadable callback: %w", err)
	}
	if a.cfg.MerchantID == "" || unverified.MerchantID != a.cfg.MerchantID {
		return payments.NormalizedEvent{}, fmt.Errorf("%w: callback merchant_id does not match this config", payments.ErrInvalidWebhookSignature)
	}
	cb, err := VerifyCallback(in.Body, a.cfg.PaymentKey)
	if err != nil {
		return payments.NormalizedEvent{}, err
	}
	return NormalizeCallback(cb, in.Body), nil
}

// NormalizeCallback maps a verified callback onto arena's event vocabulary.
//
//   - ProviderPaymentID is order_id: arena's checkout session id, the value
//     stored as payment_intents.provider_payment_id.
//   - ProviderChargeRef is the numeric payment_id Flitt assigns (refunds, audit).
//   - Only two statuses move an intent: approved (the money settled) and
//     expired (the order outlived its lifetime unpaid). "declined" is
//     deliberately NOT a failure: a declined card leaves the Flitt order
//     open for another card, and a terminal intent would swallow the later
//     approval — a paid purchase lost. processing/created are acknowledged
//     without a transition; reversed is a refund (PAY-13).
func NormalizeCallback(cb *Callback, body []byte) payments.NormalizedEvent {
	ev := payments.NormalizedEvent{
		Kind:              payments.EventUnknown,
		Type:              EventTypePrefix + cb.OrderStatus,
		ProviderPaymentID: cb.OrderID,
		ProviderChargeRef: cb.PaymentID,
		Status:            cb.OrderStatus,
		AmountMinor:       cb.Amount,
		Currency:          cb.Currency,
		Raw:               json.RawMessage(body),
	}
	switch cb.OrderStatus {
	case StatusApproved:
		ev.Kind = payments.EventPaymentSucceeded
	case StatusExpired:
		ev.Kind = payments.EventPaymentExpired
	case StatusDeclined:
		ev.Kind = payments.EventPaymentDeclined
	case StatusReversed:
		ev.Kind = payments.EventRefundUpdated
	default:
		// processing / created: Flitt's intermediate states were never
		// mapped to an intent state (an intent is `created` until the money
		// settles or the order dies), so they stay EventUnknown — acknowledged,
		// nothing moves — exactly as before PAY-01.
	}
	if cb.OrderStatus != StatusApproved {
		ev.FailureCode = cb.ResponseCode
		ev.FailureMessage = cb.ResponseDescription
	}
	return ev
}

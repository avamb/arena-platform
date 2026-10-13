// module.go — AllPay as a payment MODULE (spec 36 §5; PAY-14, spec
// 08_architecture/37_allpay_payment_provider_ru.md): descriptor, registry
// factory and the WebhookParser that verifies a webhook.
//
// Capabilities: a hosted payment page, and a webhook only the per-config
// route can verify (signed in the body with the integration's own key, no
// merchant field, no header). The buyer comes back through arena's
// payment-return hop because AllPay does not document the method of its
// redirect. Refunds are NOT a capability yet: AllPay's refund method has no
// idempotency key and returns no refund id, so a retried call after a lost
// answer could return the money twice (spec 37 §8). Until that is designed,
// a refund is made in the AllPay dashboard and the core answers
// refund.provider_not_supported.
package allpay

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// Name is the canonical provider key.
const Name = "allpay"

// Secret keys of an AllPay payment_provider_configs row. The first two keep
// the names the config used before AllPay had a module, so a stored config
// and the admin console's form stay valid.
const (
	// SecretMerchantID holds the integration's API LOGIN (AllPay dashboard →
	// Settings → API Integrations).
	SecretMerchantID = "merchant_id"
	// SecretKey holds the same integration's API KEY: it signs every request
	// AND verifies every webhook, so one key serves both roles.
	SecretKey = "secret_key"
	// SecretVAT is optional: the VAT code of the receipt line (0, 1 or 3).
	SecretVAT = "vat"
	// SecretMaxInstallments is optional: the most installments a buyer may
	// choose, 2..12 (ILS only).
	SecretMaxInstallments = "max_installments"
)

// Event types arena records. They are half of the (provider_payment_id,
// event_type) idempotency key of payment_intent_events, so they must not
// change.
const (
	EventTypePaid             = "allpay.payment.paid"
	EventTypeDeclined         = "allpay.payment.declined"
	EventTypeNotCharged       = "allpay.payment.not_charged"
	EventTypeChargeback       = "allpay.chargeback"
	EventTypeChargebackRevert = "allpay.chargeback_revert"
	eventTypeOtherPrefix      = "allpay."
)

// compile-time guards for the module contract
var (
	_ payments.Module        = (*Adapter)(nil)
	_ payments.WebhookParser = (*Adapter)(nil)
)

// ProviderDescriptor is AllPay's static description.
func ProviderDescriptor() payments.Descriptor {
	return payments.Descriptor{
		Name:  Name,
		Title: "AllPay",
		Secrets: []payments.SecretField{
			{
				Key:        SecretMerchantID,
				Label:      "AllPay API login (dashboard → Settings → API Integrations)",
				Required:   true,
				Credential: true,
			},
			{
				Key:           SecretKey,
				Label:         "AllPay API key of the same integration (signs requests and webhooks)",
				Required:      true,
				Credential:    true,
				WebhookSecret: true,
				Hidden:        true,
			},
			{
				Key:     SecretVAT,
				Label:   "VAT code for AllPay receipts: 0 no VAT (default), 1 VAT included, 3 VAT 0%",
				Pattern: `^[013]$`,
			},
			{
				Key:     SecretMaxInstallments,
				Label:   "most installments a buyer may choose, 2 to 12 (ILS only); empty for one payment",
				Pattern: `^([2-9]|1[0-2])$`,
			},
		},
		Capabilities: payments.Capabilities{
			HostedCheckout: true,
			// AllPay does not document how its page sends the buyer to
			// success_url; the return goes through arena's GET|POST hop.
			BuyerReturnsByPOST: true,
			// The webhook is signed in the body with the integration's own
			// key and names no merchant: only the config in the URL can vouch
			// for it.
			WebhookConfigRouteOnly: true,
			// PAY-14b: Refund, PartialRefund (spec 37 §8).
		},
	}
}

// Descriptor implements payments.Module.
func (a *Adapter) Descriptor() payments.Descriptor { return ProviderDescriptor() }

// Entry is AllPay's registry entry. BaseURL comes from
// Options.AllPayAPIBaseURL (the test seam); empty means allpay.to.
func Entry() payments.Entry {
	return payments.Entry{Descriptor: ProviderDescriptor(), New: newModule}
}

func newModule(secrets map[string]string, opts payments.Options) (payments.Module, error) {
	inst, _ := strconv.Atoi(strings.TrimSpace(secrets[SecretMaxInstallments]))
	return New(Config{
		Login:           secrets[SecretMerchantID],
		APIKey:          secrets[SecretKey],
		VAT:             secrets[SecretVAT],
		MaxInstallments: inst,
		TestMode:        strings.EqualFold(strings.TrimSpace(opts.Mode), "test"),
		BaseURL:         opts.AllPayAPIBaseURL,
	}), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// WebhookParser
// ─────────────────────────────────────────────────────────────────────────────

// RecognizesWebhook: an AllPay webhook has no header of its own; it is
// recognised by its body shape (order_id, status, sign).
func (a *Adapter) RecognizesWebhook(in payments.WebhookInput) bool {
	return LooksLikeCallback(in.Body)
}

// VerifyAndParse verifies the body signature under the API key and
// normalizes the webhook. An empty key never verifies.
func (a *Adapter) VerifyAndParse(_ context.Context, in payments.WebhookInput) (payments.NormalizedEvent, error) {
	if !LooksLikeCallback(in.Body) {
		return payments.NormalizedEvent{}, fmt.Errorf("%w: not an AllPay webhook", payments.ErrWebhookNotRecognized)
	}
	cb, err := VerifyCallback(in.Body, a.cfg.APIKey)
	if err != nil {
		return payments.NormalizedEvent{}, err
	}
	return NormalizeCallback(cb, in.Body), nil
}

// NormalizeCallback maps a verified webhook onto arena's event vocabulary.
//
//   - ProviderPaymentID is order_id: arena's checkout session id, the value
//     stored as payment_intents.provider_payment_id. ProviderChargeRef is the
//     same order_id, because AllPay refunds and reads statuses by it.
//   - Only status 1 of a payment webhook moves an intent (paid). Status 0 is
//     ONE failed attempt: AllPay keeps the page open for another card, and a
//     failed intent would swallow the later payment — a paid purchase lost.
//     It is EventPaymentDeclined, acknowledged without a transition.
//   - A webhook that reports no money taken (a saved card, an authorised
//     reservation) moves nothing; arena asks for neither.
//   - A chargeback is a dispute (PAY-16); its revert moves nothing.
func NormalizeCallback(cb *Callback, body []byte) payments.NormalizedEvent {
	ev := payments.NormalizedEvent{
		Kind:              payments.EventUnknown,
		ProviderPaymentID: cb.OrderID,
		ProviderChargeRef: cb.OrderID,
		Status:            strconv.Itoa(cb.Status),
		AmountMinor:       cb.AmountMinor,
		Currency:          cb.Currency,
		Raw:               json.RawMessage(body),
	}
	switch {
	case cb.Type == TypeChargeback:
		ev.Kind = payments.EventDisputeOpened
		ev.Type = EventTypeChargeback
	case cb.Type == TypeChargebackRevert:
		ev.Type = EventTypeChargebackRevert
	case cb.Type != "":
		ev.Type = eventTypeOtherPrefix + cb.Type
	case cb.NotCharged:
		ev.Type = EventTypeNotCharged
	case cb.Status == StatusPaid:
		ev.Kind = payments.EventPaymentSucceeded
		ev.Type = EventTypePaid
	default:
		ev.Kind = payments.EventPaymentDeclined
		ev.Type = EventTypeDeclined
		ev.FailureCode = cb.DeclineCode
		ev.FailureMessage = cb.Error
	}
	return ev
}

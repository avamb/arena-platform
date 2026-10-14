// module.go — AllPay as a payment MODULE (spec 36 §5; PAY-14, spec
// 08_architecture/39_allpay_payment_provider_ru.md): descriptor, registry
// factory and the WebhookParser that verifies a webhook.
//
// Capabilities: a hosted payment page, and a webhook only the per-config
// route can verify (signed in the body with the integration's own key, no
// merchant field, no header). The buyer comes back through arena's
// payment-return hop because AllPay does not document the method of its
// redirect. Refunds, full and partial (PAY-14b): AllPay's refund method has
// no idempotency key and returns no refund id, so the module reads the
// order back before every call and refuses to send when AllPay's refunded
// total does not match what arena knows (refund.go, spec 39 §8).
package allpay

import (
	"context"
	"encoding/json"
	"errors"
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
	// SecretInstallmentsMinAmount is optional: the smallest order total, in
	// shekels (major units), that offers installments. Empty offers them on
	// every ILS order once max_installments is set.
	SecretInstallmentsMinAmount = "installments_min_amount"
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
			{
				Key:     SecretInstallmentsMinAmount,
				Label:   "smallest order total in ILS that offers installments, e.g. 300; empty for any total",
				Pattern: `^[0-9]{1,7}(\.[0-9]{1,2})?$`,
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
			// PAY-14b: the refund is answered synchronously (refund.go).
			Refund:        true,
			PartialRefund: true,
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
	var instMin int64
	if raw := strings.TrimSpace(secrets[SecretInstallmentsMinAmount]); raw != "" {
		v, ok := MajorToMinor(raw)
		if !ok || v < 0 {
			return nil, fmt.Errorf("allpay: %s %q is not an amount", SecretInstallmentsMinAmount, raw)
		}
		instMin = v
	}
	return New(Config{
		Login:                secrets[SecretMerchantID],
		APIKey:               secrets[SecretKey],
		VAT:                  secrets[SecretVAT],
		MaxInstallments:      inst,
		InstallmentsMinMinor: instMin,
		TestMode:             strings.EqualFold(strings.TrimSpace(opts.Mode), "test"),
		LiveMode:             strings.EqualFold(strings.TrimSpace(opts.Mode), "live"),
		BaseURL:              opts.AllPayAPIBaseURL,
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
// normalizes the webhook. An empty key never verifies. A paid webhook is
// then confirmed by reading the order back (confirmPaid).
func (a *Adapter) VerifyAndParse(ctx context.Context, in payments.WebhookInput) (payments.NormalizedEvent, error) {
	if !LooksLikeCallback(in.Body) {
		return payments.NormalizedEvent{}, fmt.Errorf("%w: not an AllPay webhook", payments.ErrWebhookNotRecognized)
	}
	cb, err := VerifyCallback(in.Body, a.cfg.APIKey)
	if err != nil {
		return payments.NormalizedEvent{}, err
	}
	ev := NormalizeCallback(cb, in.Body)
	if ev.Kind == payments.EventPaymentSucceeded {
		return a.confirmPaid(ctx, ev)
	}
	return ev, nil
}

// confirmPaid reads a paid order back from AllPay (paymentstatus) before
// arena issues tickets for it, and takes the amount and currency from that
// answer, not from the webhook: what the buyer was charged is AllPay's
// amount minus its discount. The core then compares them with the payment
// intent (hcheckout payment_amount_guard.go).
//
//   - Not read (network, 5xx, AllPay error): an error wrapping
//     payments.ErrProviderUnreachable; the route answers 503 and AllPay
//     delivers the webhook again (up to 10 times in 24 hours).
//   - Status 0 (AllPay asks to read a status 2 seconds after an attempt, so
//     the read may run ahead of AllPay's own books), a reservation that is
//     not charged, or status 1 without an amount or a currency: the same,
//     AllPay delivers it again.
//   - Status 3 or 4: the money was paid and some of it is already back (a
//     dashboard refund before arena saw the payment). A human decides:
//     EventPaymentManualReview, no tickets.
//   - AllPay has no such order under this login: the webhook is signed with
//     this integration's key but names an order it does not hold (another
//     organization's order id). EventPaymentManualReview as well; the core's
//     organization check answers "not ours" first when the order is not
//     this organization's, so it can only park an order of its own.
func (a *Adapter) confirmPaid(ctx context.Context, ev payments.NormalizedEvent) (payments.NormalizedEvent, error) {
	st, err := a.PaymentStatus(ctx, ev.ProviderPaymentID)
	if errors.Is(err, ErrOrderNotFound) {
		ev.Kind = payments.EventPaymentManualReview
		ev.AmountMinor, ev.Currency = 0, ""
		ev.FailureCode = "allpay_order_not_found"
		ev.FailureMessage = "AllPay has no such order under this API login"
		return ev, nil
	}
	if err != nil {
		return payments.NormalizedEvent{}, fmt.Errorf("%w: allpay: confirm the paid order %s: %s",
			payments.ErrProviderUnreachable, ev.ProviderPaymentID, err.Error())
	}
	switch {
	case st.Preauth == "authorized":
		return payments.NormalizedEvent{}, fmt.Errorf("%w: allpay: order %s is reserved, not charged",
			payments.ErrProviderUnreachable, ev.ProviderPaymentID)
	case st.Status == StatusPaid && (st.ChargedMinor() <= 0 || st.Currency == ""):
		// A paid answer that names no amount or currency cannot be compared
		// with the payment: it confirms nothing.
		return payments.NormalizedEvent{}, fmt.Errorf("%w: allpay: order %s is reported paid without an amount or currency",
			payments.ErrProviderUnreachable, ev.ProviderPaymentID)
	case st.Status == StatusPaid:
		ev.AmountMinor, ev.Currency = st.ChargedMinor(), st.Currency
		return ev, nil
	case st.Status == StatusRefunded || st.Status == StatusPartiallyRefunded:
		ev.Kind = payments.EventPaymentManualReview
		ev.AmountMinor, ev.Currency = st.ChargedMinor(), st.Currency
		ev.FailureCode = "allpay_refunded_before_confirmed"
		ev.FailureMessage = "AllPay reports the order paid and already refunded in part or in full"
		return ev, nil
	}
	return payments.NormalizedEvent{}, fmt.Errorf("%w: allpay: order %s has status %d, not paid yet",
		payments.ErrProviderUnreachable, ev.ProviderPaymentID, st.Status)
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

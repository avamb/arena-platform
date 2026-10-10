// module.go — AllPay as a payment MODULE (spec 36 §5, PAY-01): descriptor,
// registry factory and the WebhookParser behind the legacy
// X-AllPay-Signature verification. AllPay is NOT wired to the hosted checkout
// and cannot refund through arena yet — PAY-14 picks the canonical copy of
// the adapter (this one or domain/payments/allpay.go) and builds the rest.
// Until then its capabilities say so: nothing is promised the code does not
// deliver.
package allpay

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// Name is the canonical provider key.
const Name = "allpay"

// Secret keys of an AllPay payment_provider_configs row.
const (
	// SecretMerchantID is the merchant identifier assigned at registration.
	SecretMerchantID = "merchant_id"
	// SecretKey is the shared secret: it authenticates API calls AND signs
	// inbound webhooks (X-AllPay-Signature), so one key serves both roles.
	SecretKey = "secret_key"
)

// signatureHeader is the header AllPay signs every delivery with.
const signatureHeader = "X-AllPay-Signature"

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
				Label:      "AllPay merchant id",
				Required:   true,
				Credential: true,
			},
			{
				Key:           SecretKey,
				Label:         "AllPay secret key (API and webhook signing)",
				Required:      true,
				Credential:    true,
				WebhookSecret: true,
				Hidden:        true,
			},
		},
		// No hosted checkout, no refunds: PAY-14.
		Capabilities: payments.Capabilities{},
	}
}

// Descriptor implements payments.Module.
func (a *Adapter) Descriptor() payments.Descriptor { return ProviderDescriptor() }

// Entry is AllPay's registry entry. There is no endpoint override in
// payments.Options for AllPay yet (nothing in arena calls its API).
func Entry() payments.Entry {
	return payments.Entry{Descriptor: ProviderDescriptor(), New: newModule}
}

func newModule(secrets map[string]string, _ payments.Options) (payments.Module, error) {
	return New(Config{
		APIKey:        secrets[SecretKey],
		WebhookSecret: secrets[SecretKey],
		MerchantID:    secrets[SecretMerchantID],
	}), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// WebhookParser
// ─────────────────────────────────────────────────────────────────────────────

// RecognizesWebhook: an AllPay delivery carries X-AllPay-Signature.
func (a *Adapter) RecognizesWebhook(in payments.WebhookInput) bool {
	return strings.TrimSpace(in.Header.Get(signatureHeader)) != ""
}

// VerifyAndParse checks the X-AllPay-Signature HMAC-SHA256 of the raw body
// against the configured secret and normalizes the documented event shape.
// An EMPTY secret never verifies.
func (a *Adapter) VerifyAndParse(_ context.Context, in payments.WebhookInput) (payments.NormalizedEvent, error) {
	header := in.Header.Get(signatureHeader)
	if strings.TrimSpace(header) == "" {
		return payments.NormalizedEvent{}, fmt.Errorf("%w: no %s header", payments.ErrWebhookNotRecognized, signatureHeader)
	}
	if a.cfg.WebhookSecret == "" {
		return payments.NormalizedEvent{}, fmt.Errorf("%w: no webhook secret configured", payments.ErrInvalidWebhookSignature)
	}
	if err := payments.VerifyAllPaySignature(header, in.Body, a.cfg.WebhookSecret); err != nil {
		return payments.NormalizedEvent{}, err
	}
	return normalizeEvent(in.Body), nil
}

// normalizeEvent maps AllPay's documented event body (event_type, payment_id,
// status) onto arena's vocabulary. A body of another shape yields
// EventUnknown with no payment id.
func normalizeEvent(body []byte) payments.NormalizedEvent {
	ev := payments.NormalizedEvent{Kind: payments.EventUnknown, Raw: json.RawMessage(body)}
	var event allpayWebhookEvent
	if err := json.Unmarshal(body, &event); err != nil || event.EventType == "" || event.PaymentID == "" {
		return ev
	}
	ev.Type = event.EventType
	ev.ProviderPaymentID = event.PaymentID
	ev.Status = event.Status
	switch event.EventType {
	case "payment.authorized":
		ev.Kind = payments.EventPaymentAuthorized
	case "payment.captured":
		ev.Kind = payments.EventPaymentSucceeded
	case "payment.failed":
		ev.Kind = payments.EventPaymentFailed
	case "payment.expired":
		ev.Kind = payments.EventPaymentExpired
	case "payment.refunded":
		ev.Kind = payments.EventRefundUpdated
	}
	return ev
}

// module.go — Stripe as a payment MODULE (spec 36 §5, PAY-01): the static
// descriptor the core reads, the factory the registry calls per request, and
// the optional capabilities Stripe implements — WebhookParser, Refunder,
// RefundLookup (HostedCheckoutProvider and CredentialVerifier live in
// adapter.go).
//
// Nothing here changes what Stripe did before PAY-01: the signature scheme is
// the same payments.VerifyStripeSignature, the event vocabulary is the one
// hcheckout's envelope parser mapped, the refund call is the same
// POST /v1/refunds. What moved is WHERE the knowledge lives.
package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// Name is the canonical provider key — the value of sales_channels.provider,
// payment_provider_configs.provider and payment_intents.provider.
const Name = "stripe"

// Secret keys of a Stripe payment_provider_configs row.
const (
	// SecretAPIKey is the organizer's secret key (sk_live_…/sk_test_…, or a
	// restricted rk_ key).
	SecretAPIKey = "api_key"
	// SecretWebhookSecret is the endpoint signing secret (whsec_…).
	SecretWebhookSecret = "webhook_secret"
)

// signatureHeader is the header Stripe signs every delivery with.
const signatureHeader = "Stripe-Signature"

// Failure code stamped on a hosted session that died unpaid, when the event
// itself explains nothing. The order-status surface tells "the buyer walked
// away" from "the card was declined" by it.
const (
	failureCodeSessionExpired    = "session_expired"
	failureMessageSessionExpired = "the hosted checkout session expired before it was paid"
)

// compile-time guards for the module contract
var (
	_ payments.Module        = (*Adapter)(nil)
	_ payments.WebhookParser = (*Adapter)(nil)
	_ payments.Refunder      = (*Adapter)(nil)
	_ payments.RefundLookup  = (*Adapter)(nil)
)

// ProviderDescriptor is Stripe's static description. A package-level
// function (not only the method) so the registry entry and tests can read
// it without building an adapter.
func ProviderDescriptor() payments.Descriptor {
	return payments.Descriptor{
		Name:  Name,
		Title: "Stripe",
		Secrets: []payments.SecretField{
			{
				Key:        SecretAPIKey,
				Label:      "Stripe secret key (Developers → API keys → Secret key)",
				Required:   true,
				Credential: true,
				Hidden:     true,
				// Restricted keys (rk_) are accepted: they are valid API
				// credentials and an organization is entitled to scope one
				// down.
				Prefixes: []string{"sk_test_", "sk_live_", "rk_test_", "rk_live_"},
				ModePrefixes: map[string][]string{
					"test": {"sk_test_", "rk_test_"},
					"live": {"sk_live_", "rk_live_"},
				},
			},
			{
				Key:           SecretWebhookSecret,
				Label:         "Stripe webhook signing secret (Developers → Webhooks → your endpoint → Signing secret)",
				Required:      true,
				WebhookSecret: true,
				Hidden:        true,
				Prefixes:      []string{"whsec_"},
			},
		},
		Capabilities: payments.Capabilities{
			HostedCheckout: true,
			Refund:         true,
			PartialRefund:  true,
			// A refund is usually settled at once, but Stripe may answer
			// `pending` and finish it through refund.updated.
			AsyncRefund:  true,
			RefundLookup: true,
		},
	}
}

// Descriptor implements payments.Module.
func (a *Adapter) Descriptor() payments.Descriptor { return ProviderDescriptor() }

// Entry is Stripe's registry entry: the descriptor plus the factory that
// builds an Adapter from one organization's secrets. BaseURL comes from
// Options.StripeAPIBaseURL (the test seam); empty means api.stripe.com.
func Entry() payments.Entry {
	return payments.Entry{Descriptor: ProviderDescriptor(), New: newModule}
}

func newModule(secrets map[string]string, opts payments.Options) (payments.Module, error) {
	return New(Config{
		SecretKey:     secrets[SecretAPIKey],
		WebhookSecret: secrets[SecretWebhookSecret],
		BaseURL:       opts.StripeAPIBaseURL,
	}), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// WebhookParser
// ─────────────────────────────────────────────────────────────────────────────

// RecognizesWebhook: a Stripe delivery always carries Stripe-Signature.
func (a *Adapter) RecognizesWebhook(in payments.WebhookInput) bool {
	return in.Header.Get(signatureHeader) != ""
}

// VerifyAndParse checks the Stripe-Signature header against the configured
// signing secret (the t=,v1= HMAC-SHA256 scheme with the replay tolerance)
// and normalizes the event envelope.
//
// An EMPTY secret never verifies: an HMAC over an empty key would let a
// forged header pass on a config whose owner never pasted the secret.
func (a *Adapter) VerifyAndParse(_ context.Context, in payments.WebhookInput) (payments.NormalizedEvent, error) {
	header := in.Header.Get(signatureHeader)
	if header == "" {
		return payments.NormalizedEvent{}, fmt.Errorf("%w: no %s header", payments.ErrWebhookNotRecognized, signatureHeader)
	}
	if a.cfg.WebhookSecret == "" {
		return payments.NormalizedEvent{}, fmt.Errorf("%w: no webhook signing secret configured", payments.ErrInvalidWebhookSignature)
	}
	if err := payments.VerifyStripeSignature(header, in.Body, a.cfg.WebhookSecret, a.cfg.WebhookTolerance); err != nil {
		return payments.NormalizedEvent{}, err
	}
	return normalizeEvent(in.Body), nil
}

// eventEnvelope is the subset of Stripe's event envelope arena reads.
type eventEnvelope struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data *struct {
		Object struct {
			ID               string `json:"id"`
			Object           string `json:"object"`
			Status           string `json:"status"`
			Amount           int64  `json:"amount"`
			AmountTotal      int64  `json:"amount_total"`
			Currency         string `json:"currency"`
			LastPaymentError *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"last_payment_error"`
			// checkout.session.* only: whether the money actually settled,
			// and the pi_… Stripe created behind the hosted session.
			PaymentStatus string `json:"payment_status"`
			PaymentIntent string `json:"payment_intent"`
			// refund.* only.
			FailureReason string `json:"failure_reason"`
		} `json:"object"`
	} `json:"data"`
}

// checkoutSessionPaid is Stripe's payment_status for settled funds; "unpaid"
// and "no_payment_required" are NOT a payment.
const checkoutSessionPaid = "paid"

// normalizeEvent maps a Stripe envelope onto arena's event vocabulary. A body
// that is not an envelope (no type, no data) yields EventUnknown with no
// payment id — the caller then knows the body names nothing of Stripe's.
func normalizeEvent(body []byte) payments.NormalizedEvent {
	ev := payments.NormalizedEvent{Kind: payments.EventUnknown, Raw: json.RawMessage(body)}
	var env eventEnvelope
	if err := json.Unmarshal(body, &env); err != nil || env.Type == "" || env.Data == nil {
		return ev
	}
	obj := env.Data.Object
	ev.Type = env.Type
	ev.ProviderEventID = env.ID
	ev.ProviderPaymentID = strings.TrimSpace(obj.ID)
	ev.ProviderChargeRef = strings.TrimSpace(obj.PaymentIntent)
	ev.Status = obj.Status
	ev.Currency = strings.ToUpper(obj.Currency)
	ev.AmountMinor = obj.Amount
	if ev.AmountMinor == 0 {
		ev.AmountMinor = obj.AmountTotal
	}
	if obj.LastPaymentError != nil {
		ev.FailureCode = obj.LastPaymentError.Code
		ev.FailureMessage = obj.LastPaymentError.Message
	}

	switch env.Type {
	case "payment_intent.requires_action":
		ev.Kind = payments.EventPaymentRequiresAction
	case "payment_intent.processing":
		ev.Kind = payments.EventPaymentProcessing
	case "payment_intent.amount_capturable", "payment_intent.amount_capturable_updated":
		ev.Kind = payments.EventPaymentAuthorized
	case "payment_intent.succeeded":
		ev.Kind = payments.EventPaymentSucceeded
	case "payment_intent.payment_failed":
		ev.Kind = payments.EventPaymentFailed
	case "payment_intent.manual_review":
		ev.Kind = payments.EventPaymentManualReview
	case "checkout.session.completed":
		// Stripe fires `completed` the moment the buyer finishes the hosted
		// page — for an async method long BEFORE the money settles, and for
		// a zero-amount session no money moves at all. Only payment_status
		// "paid" is a payment; anything else is pending until the matching
		// async_payment_succeeded / _failed event.
		if obj.PaymentStatus == checkoutSessionPaid {
			ev.Kind = payments.EventPaymentSucceeded
		} else {
			ev.Kind = payments.EventPaymentPending
		}
	case "checkout.session.async_payment_succeeded":
		ev.Kind = payments.EventPaymentSucceeded
	case "checkout.session.async_payment_failed":
		ev.Kind = payments.EventPaymentFailed
	case "checkout.session.expired":
		// The hosted session hit its own expires_at without being paid.
		ev.Kind = payments.EventPaymentExpired
		if ev.FailureCode == "" {
			ev.FailureCode = failureCodeSessionExpired
			ev.FailureMessage = failureMessageSessionExpired
		}
	case "refund.created", "refund.updated", "refund.failed", "charge.refund.updated":
		ev.Kind = payments.EventRefundUpdated
		ev.ProviderRefundID = ev.ProviderPaymentID
		ev.ProviderPaymentID = ""
		ev.FailureCode = obj.FailureReason
	case "charge.dispute.created":
		ev.Kind = payments.EventDisputeOpened
		ev.ProviderPaymentID = ""
	}
	return ev
}

// ─────────────────────────────────────────────────────────────────────────────
// Refunder / RefundLookup
// ─────────────────────────────────────────────────────────────────────────────

// stripeRefundReasons are the only values Stripe's `reason` parameter
// accepts; anything else would make the call fail, so a free-text reason
// travels in metadata instead.
var stripeRefundReasons = map[string]bool{
	"duplicate": true, "fraudulent": true, "requested_by_customer": true,
}

// zeroDecimalCurrencies are the currencies Stripe charges in whole units.
// Arena's minor unit for them is ALSO the whole unit (ISO 4217 exponent 0),
// and the hosted checkout already sent orders.total verbatim as unit_amount,
// so no arithmetic applies — the table exists to make that decision
// explicit and testable.
var zeroDecimalCurrencies = map[string]bool{
	"BIF": true, "CLP": true, "DJF": true, "GNF": true, "JPY": true, "KMF": true,
	"KRW": true, "MGA": true, "PYG": true, "RWF": true, "UGX": true, "VND": true,
	"VUV": true, "XAF": true, "XOF": true, "XPF": true,
}

// threeDecimalCurrencies are charged by Stripe in minor units that must be
// multiples of 10 (the last digit is always 0).
var threeDecimalCurrencies = map[string]bool{
	"BHD": true, "JOD": true, "KWD": true, "OMR": true, "TND": true,
}

// providerAmount converts an arena minor-unit amount to what Stripe's API
// takes for the currency. The charge was created with the same arena minor
// units (CreateCheckoutSession passes Amount straight through), so the
// refund must use them too — the only transformation is the validity rule
// of three-decimal currencies.
func providerAmount(currency string, minor int64) (int64, error) {
	cur := strings.ToUpper(strings.TrimSpace(currency))
	if threeDecimalCurrencies[cur] && minor%10 != 0 {
		return 0, fmt.Errorf("%w: %s amounts must be a multiple of 10 minor units for Stripe", payments.ErrRefundAmountInvalid, cur)
	}
	_ = zeroDecimalCurrencies[cur] // identity: arena's minor unit is already the whole unit
	return minor, nil
}

// refundObject is the subset of Stripe's Refund object arena reads.
type refundObject struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	FailureReason string `json:"failure_reason"`
}

func refundResult(r refundObject) payments.RefundResult {
	out := payments.RefundResult{ProviderRefundID: r.ID, FailureCode: r.FailureReason}
	switch r.Status {
	case "succeeded":
		out.Status = payments.RefundSucceeded
	case "failed", "canceled":
		out.Status = payments.RefundFailed
		if out.FailureCode == "" {
			out.FailureCode = r.Status
		}
	default:
		// "pending", "requires_action" and anything Stripe adds later: the
		// money has not moved yet, refund.updated or RefundStatus settles it.
		out.Status = payments.RefundPending
	}
	return out
}

// Refund creates a Stripe Refund via POST /v1/refunds against the pi_…
// behind the payment, with the arena refund id as the Idempotency-Key so a
// retry can never refund twice. The amount is ALWAYS sent explicitly (a
// zero/omitted amount would mean "everything", which arena never asks for
// implicitly) and is refused here, before any network call, when it is not
// positive or exceeds the payment.
//
// Implements payments.Refunder.
func (a *Adapter) Refund(ctx context.Context, req payments.RefundRequest) (payments.RefundResult, error) {
	chargeRef := strings.TrimSpace(req.ProviderChargeRef)
	if chargeRef == "" {
		return payments.RefundResult{}, payments.ErrRefundChargeRefMissing
	}
	if req.AmountMinor <= 0 {
		return payments.RefundResult{}, payments.ErrRefundAmountInvalid
	}
	if req.PaymentAmountMinor > 0 && req.AmountMinor > req.PaymentAmountMinor {
		return payments.RefundResult{}, fmt.Errorf("%w: %d > %d", payments.ErrRefundExceedsPayment, req.AmountMinor, req.PaymentAmountMinor)
	}
	amount, err := providerAmount(req.Currency, req.AmountMinor)
	if err != nil {
		return payments.RefundResult{}, err
	}

	form := url.Values{}
	form.Set("payment_intent", chargeRef)
	form.Set("amount", strconv.FormatInt(amount, 10))
	reason := strings.TrimSpace(req.Reason)
	if stripeRefundReasons[reason] {
		form.Set("reason", reason)
	} else if reason != "" {
		form.Set("metadata[arena_reason]", truncateRunes(reason, 500))
	}
	if req.Metadata.RefundID != "" {
		form.Set("metadata[arena_refund_id]", req.Metadata.RefundID)
	}
	if req.Metadata.OrderID != "" {
		form.Set("metadata[arena_order_id]", req.Metadata.OrderID)
	}

	rawBody, _, err := a.doRequest(ctx, http.MethodPost, a.cfg.BaseURL+"/refunds", form, req.IdempotencyKey)
	if err != nil {
		return payments.RefundResult{}, fmt.Errorf("stripe: Refund: %w", err)
	}
	var refund refundObject
	if err := json.Unmarshal(rawBody, &refund); err != nil {
		return payments.RefundResult{}, fmt.Errorf("stripe: Refund: unmarshal response: %w", err)
	}
	if refund.ID == "" {
		return payments.RefundResult{}, fmt.Errorf("stripe: Refund: response has no refund id")
	}
	return refundResult(refund), nil
}

// RefundStatus reads a refund back via GET /v1/refunds/{id}.
//
// Implements payments.RefundLookup.
func (a *Adapter) RefundStatus(ctx context.Context, providerRefundID string) (payments.RefundResult, error) {
	id := strings.TrimSpace(providerRefundID)
	if id == "" {
		return payments.RefundResult{}, fmt.Errorf("stripe: RefundStatus: refund id is required")
	}
	rawBody, _, err := a.doRequest(ctx, http.MethodGet, a.cfg.BaseURL+"/refunds/"+url.PathEscape(id), nil, "")
	if err != nil {
		return payments.RefundResult{}, fmt.Errorf("stripe: RefundStatus: %w", err)
	}
	var refund refundObject
	if err := json.Unmarshal(rawBody, &refund); err != nil {
		return payments.RefundResult{}, fmt.Errorf("stripe: RefundStatus: unmarshal response: %w", err)
	}
	if refund.ID == "" {
		return payments.RefundResult{}, fmt.Errorf("stripe: RefundStatus: response has no refund id")
	}
	return refundResult(refund), nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

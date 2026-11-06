// hosted_checkout.go — provider-hosted checkout pages (redirect flow).
//
// A hosted checkout is a page the PROVIDER renders and the buyer is
// redirected to; arena never sees card data and ships no card UI. Stripe
// Checkout Sessions are the first (and currently only) implementation.
//
// This is deliberately a SEPARATE, narrow interface rather than three more
// methods on PaymentProvider: not every provider has such a surface, and widening
// PaymentProvider would force every adapter to grow dead methods. Callers
// type-assert:
//
//	hosted, ok := provider.(payments.HostedCheckoutProvider)
//	if !ok { /* this provider cannot host a checkout page */ }
package payments

import (
	"context"
	"strings"
)

// HostedCheckoutLine is one receipt line of a hosted checkout: a label and
// its amount in minor units. See CreateHostedCheckoutRequest.Lines.
type HostedCheckoutLine struct {
	Name   string
	Amount int64
}

// LinesMatch reports whether lines can replace a single line of amount:
// at least one line, every line positive and named, and the sum exactly amount.
func LinesMatch(lines []HostedCheckoutLine, amount int64) bool {
	if len(lines) == 0 {
		return false
	}
	var sum int64
	for _, l := range lines {
		if l.Amount <= 0 || strings.TrimSpace(l.Name) == "" {
			return false
		}
		sum += l.Amount
	}
	return sum == amount
}

// CreateHostedCheckoutRequest holds the parameters for creating a
// provider-hosted checkout page.
type CreateHostedCheckoutRequest struct {
	// Amount is the total to charge in the smallest currency unit
	// (minor units — cents, haléře, kopeks). It is charged as ONE line item;
	// arena has already done all per-ticket arithmetic.
	Amount int64
	// Currency is the ISO 4217 three-letter currency code, e.g. "czk".
	Currency string
	// ProductName is the buyer-facing label of the single line item, e.g.
	// the event title. Providers usually require a non-empty value.
	ProductName string
	// Lines, when set, splits Amount into the lines a provider that prints
	// line items on its receipt should show (AllPay: one items[] entry each,
	// so a shop order's paid delivery is its own line on the tax document).
	// The lines must be positive and sum to exactly Amount; otherwise, and for
	// every provider that does not print lines, Amount is charged as ONE line
	// named ProductName, as before.
	Lines []HostedCheckoutLine
	// CustomerEmail pre-fills the buyer's email on the hosted page. Optional.
	CustomerEmail string
	// ReceiptEmail, when set, asks the provider to e-mail its own receipt of
	// the payment to this address (spec 51 section 2, decision R-6: the
	// organization's setting, or the platform operator's "always send").
	// Empty sends no such request; the provider's own account settings may
	// still send one, which arena cannot prevent.
	ReceiptEmail string
	// CustomerName and CustomerPhone pre-fill the buyer's name and phone when
	// the checkout collected them. Optional; a provider that has no such
	// fields ignores them (AllPay also decides from the phone whether to ask
	// a foreign buyer for an Israeli ID number).
	CustomerName  string
	CustomerPhone string
	// ClientReferenceID is arena's own identifier for this checkout (the
	// checkout_sessions row id). Providers echo it back on their webhook.
	ClientReferenceID string
	// Metadata is forwarded to the provider and attached BOTH to the hosted
	// session and to the underlying payment object, so either webhook shape
	// can be traced back to arena's rows.
	Metadata map[string]string
	// SuccessURL is where the provider sends the buyer after a completed
	// payment. Required.
	SuccessURL string
	// CancelURL is where the provider sends the buyer if they abandon the
	// hosted page. Required.
	CancelURL string
	// ExpiresAtUnix is the Unix timestamp at which the hosted session must
	// stop accepting payment. Zero leaves the provider's own default.
	// Stripe requires this to be at least 30 minutes in the future.
	ExpiresAtUnix int64
	// IdempotencyKey de-duplicates retries of this creation call.
	IdempotencyKey string
	// Locale is the buyer's language as a bare two-letter tag ("ru", "cs"),
	// used to render the provider's hosted page. Empty leaves the provider
	// to guess from the browser, which is what it did before this existed —
	// a Russian-speaking buyer of a Russian-language event was shown an
	// English payment page (first production clients, 2026-09-20).
	//
	// A provider that does not recognise the tag must be sent nothing rather
	// than a value it would reject: a cosmetic mismatch must never cost a
	// sale.
	Locale string
	// CallbackURL is the absolute URL the provider must post the payment
	// outcome to, for providers that take it per order (Flitt's
	// server_callback_url). Stripe configures its endpoint once in the
	// dashboard and ignores this. Empty means "not supplied".
	CallbackURL string
}

// CreateHostedCheckoutResponse is returned by
// HostedCheckoutProvider.CreateCheckoutSession.
type CreateHostedCheckoutResponse struct {
	// SessionID is the provider-assigned hosted-session identifier
	// (Stripe cs_…). This — not the payment id — is what the provider's
	// checkout.session.* webhook events are keyed by, so it is what arena
	// stores as payment_intents.provider_payment_id.
	SessionID string
	// URL is the buyer-facing hosted payment page to redirect to.
	URL string
	// PaymentID is the provider's underlying payment object id (Stripe pi_…).
	// Usually EMPTY at creation time — the provider only mints it once the
	// buyer starts paying — and learned later from the webhook.
	PaymentID string
	// ExpiresAtUnix is the expiry the provider actually applied.
	ExpiresAtUnix int64
}

// HostedCheckoutProvider is implemented by payment adapters that can render a
// hosted payment page and hand back a redirect URL.
type HostedCheckoutProvider interface {
	// ProviderName returns the canonical provider key ("stripe").
	ProviderName() string
	// CreateCheckoutSession creates the hosted page and returns the redirect
	// URL. An error means nothing was created that the buyer could pay on —
	// callers must not hand out a redirect URL.
	CreateCheckoutSession(ctx context.Context, req CreateHostedCheckoutRequest) (*CreateHostedCheckoutResponse, error)
}

// HostedCheckoutExpirer is implemented by providers that can close a hosted
// page before its own expiry, so a buyer who still has it open can no longer
// pay (Stripe: POST /v1/checkout/sessions/{id}/expire). Callers type-assert
// it like HostedCheckoutProvider; a provider without it is left to expire on
// its own, and a payment that still arrives is handled as a late payment.
type HostedCheckoutExpirer interface {
	// ExpireCheckoutSession closes the hosted session sessionID. An error
	// means the provider did not confirm it (the session may already be
	// paid, expired, or the call failed); callers treat it as best effort.
	ExpireCheckoutSession(ctx context.Context, sessionID string) error
}

// HostedPaymentState is what a provider says about one hosted payment arena
// asks it about by reference (HostedPaymentLookup).
type HostedPaymentState int

const (
	// HostedPaymentUnknown: the provider holds no payment under that
	// reference — the page was never created.
	HostedPaymentUnknown HostedPaymentState = iota
	// HostedPaymentUnpaid: the payment exists and nothing was charged; the
	// page may still be paid.
	HostedPaymentUnpaid
	// HostedPaymentSettled: the provider reports an outcome arena must act
	// on. HostedPaymentLookupResult.Event carries it, normalized exactly as
	// the provider's verified webhook would have been (a paid order, or one
	// a human has to review).
	HostedPaymentSettled
	// HostedPaymentPending: anything else (an authorization, a status the
	// provider is still settling). Left alone; asked again later.
	HostedPaymentPending
)

// HostedPaymentLookupResult is one answer of HostedPaymentLookup.
type HostedPaymentLookupResult struct {
	State HostedPaymentState
	// Event is set for HostedPaymentSettled only.
	Event NormalizedEvent
}

// HostedPaymentLookup is implemented by a provider with
// Capabilities.ReferenceIsPaymentID: it reads one hosted payment back by the
// reference arena filed it under. The checkout.payment_recover job uses it
// when arena lost the provider's answer to CreateCheckoutSession.
type HostedPaymentLookup interface {
	LookupHostedPayment(ctx context.Context, reference string) (HostedPaymentLookupResult, error)
}

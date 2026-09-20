// hosted_checkout.go — provider-hosted checkout pages (redirect flow).
//
// A hosted checkout is a page the PROVIDER renders and the buyer is
// redirected to; arena never sees card data and ships no card UI. Stripe
// Checkout Sessions are the first (and currently only) implementation.
//
// This is deliberately a SEPARATE, narrow interface rather than three more
// methods on PaymentProvider: AllPay has no equivalent surface, and widening
// PaymentProvider would force every adapter to grow dead methods. Callers
// type-assert:
//
//	hosted, ok := provider.(payments.HostedCheckoutProvider)
//	if !ok { /* this provider cannot host a checkout page */ }
package payments

import "context"

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
	// CustomerEmail pre-fills the buyer's email on the hosted page. Optional.
	CustomerEmail string
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

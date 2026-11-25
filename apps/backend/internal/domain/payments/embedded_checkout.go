// embedded_checkout.go — a checkout session the buyer pays INSIDE arena's own
// page (spec 65 §3, Stripe Checkout Session with ui_mode=elements).
//
// The hosted flow (hosted_checkout.go) sends the buyer to the provider's page.
// The embedded flow creates the same kind of provider session, but the
// provider hands back a client secret instead of a URL: the provider's
// browser SDK (Stripe.js) renders the card fields in its own iframe on
// arena's page, with the publishable browser key of the organizer's account.
// Card data never touches arena.
//
// What does NOT change from the hosted flow: the session id (cs_…) is what
// arena files the payment under (payment_intents.provider_payment_id), the
// provider's "session completed and paid" webhook is the only proof of
// payment, the session's expiry is the channel's payment window, and the
// provider is called only after the hold transaction has committed.
//
// A separate, narrow interface again: only a provider with such an SDK has
// it, and the hosted CreateCheckoutSession stays exactly what it was.
package payments

import (
	"context"
	"errors"
)

// CheckoutUIModeEmbedded is the public name of the embedded mode in API
// answers ("payment.mode"). Stripe calls the same thing ui_mode=elements.
const CheckoutUIModeEmbedded = "elements"

// Embedded checkout session statuses, as EmbeddedCheckoutSession.Status
// reports them (Stripe's own Checkout Session `status` values).
const (
	EmbeddedSessionOpen     = "open"
	EmbeddedSessionComplete = "complete"
	EmbeddedSessionExpired  = "expired"
)

// CreateEmbeddedCheckoutResponse is returned by
// EmbeddedCheckoutProvider.CreateEmbeddedCheckoutSession.
type CreateEmbeddedCheckoutResponse struct {
	// SessionID is the provider's session id (Stripe cs_…), stored as
	// payment_intents.provider_payment_id exactly as for a hosted session.
	SessionID string
	// ClientSecret is what the provider's browser SDK is initialised with.
	// It is handed to the buyer's browser and NEVER stored or logged: a
	// later read asks the provider again (RetrieveEmbeddedCheckoutSession).
	ClientSecret string
	// PaymentID is the underlying payment object (Stripe pi_…), usually
	// empty at creation and learned from the webhook.
	PaymentID string
	// ExpiresAtUnix is the expiry the provider applied.
	ExpiresAtUnix int64
}

// EmbeddedCheckoutSession is what the provider says about one embedded
// session when arena reads it back.
type EmbeddedCheckoutSession struct {
	ID string
	// Status is EmbeddedSessionOpen, EmbeddedSessionComplete or
	// EmbeddedSessionExpired.
	Status string
	// PaymentStatus is the provider's payment state ("paid", "unpaid").
	PaymentStatus string
	// ClientSecret is set only while the session can still be paid; never
	// stored, never logged.
	ClientSecret string
}

// ErrCheckoutSessionNotExpirable is returned (wrapped) by
// EmbeddedCheckoutProvider.ExpireEmbeddedCheckoutSession when the provider
// ANSWERED and refused to expire the session: it is complete, already
// expired, or a payment on it is being processed. The caller reads the
// session back to tell those apart. Any other error is an unknown outcome.
var ErrCheckoutSessionNotExpirable = errors.New("payments: the provider refused to expire the checkout session")

// EmbeddedCheckoutProvider is implemented by a payment module whose
// checkout session can be paid inside arena's page
// (Capabilities.EmbeddedCheckout).
type EmbeddedCheckoutProvider interface {
	// CreateEmbeddedCheckoutSession creates the session. req.ReturnURL is
	// where the provider sends the buyer when the bank needs its own page
	// (3-D Secure); SuccessURL and CancelURL are ignored. An error means
	// nothing was created the buyer could pay on; a
	// HostedCheckoutRefusedError means the provider refused for good.
	CreateEmbeddedCheckoutSession(ctx context.Context, req CreateHostedCheckoutRequest) (*CreateEmbeddedCheckoutResponse, error)
	// RetrieveEmbeddedCheckoutSession reads the session back, with its
	// client secret while it is open.
	RetrieveEmbeddedCheckoutSession(ctx context.Context, sessionID string) (EmbeddedCheckoutSession, error)
	// ExpireEmbeddedCheckoutSession closes the session so it can no longer
	// be paid. ErrCheckoutSessionNotExpirable (wrapped) is the provider's
	// definitive refusal.
	ExpireEmbeddedCheckoutSession(ctx context.Context, sessionID string) error
}

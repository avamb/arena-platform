// checkout_session_payment.go — reading a provider checkout session together
// with the state of its payment (spec 08_architecture/67 section 5.2).
//
// Before arena closes the payment page of an expired seat hold it asks the
// provider whether a payment on that page is being confirmed right now (the
// buyer on a 3-D Secure screen, a payment being processed). Closing the page
// then would fail a payment the buyer is making, so the hold waits.
package payments

import "context"

// Payment states of the payment behind a checkout session, as
// CheckoutSessionPayment.PaymentStatus reports them (Stripe's PaymentIntent
// `status` values that matter here).
const (
	SessionPaymentProcessing     = "processing"
	SessionPaymentRequiresAction = "requires_action"
	SessionPaymentSucceeded      = "succeeded"
)

// CheckoutSessionPayment is what the provider says about one checkout session
// (embedded or hosted) and the payment behind it.
type CheckoutSessionPayment struct {
	// ID is the provider's session id (Stripe cs_…).
	ID string
	// Status is EmbeddedSessionOpen, EmbeddedSessionComplete or
	// EmbeddedSessionExpired (Stripe's Checkout Session `status`; the values
	// are the same for the hosted page).
	Status string
	// PaymentID is the payment object behind the session (Stripe pi_…),
	// empty while the buyer has not confirmed.
	PaymentID string
	// PaymentStatus is that payment's state (Stripe's PaymentIntent status,
	// for example SessionPaymentProcessing or SessionPaymentRequiresAction),
	// empty when there is no payment yet.
	PaymentStatus string
}

// PaymentInProgress reports whether a payment on the session is being
// confirmed: the session must not be closed now.
func (p CheckoutSessionPayment) PaymentInProgress() bool {
	return p.PaymentStatus == SessionPaymentProcessing || p.PaymentStatus == SessionPaymentRequiresAction
}

// CheckoutSessionPaymentReader is implemented by a payment module that can
// read a checkout session with the state of its payment.
type CheckoutSessionPaymentReader interface {
	// ReadCheckoutSessionPayment reads the session sessionID and its payment.
	// An error is an unknown outcome (the provider did not answer).
	ReadCheckoutSessionPayment(ctx context.Context, sessionID string) (CheckoutSessionPayment, error)
}

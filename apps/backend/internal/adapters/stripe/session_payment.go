// session_payment.go — a Checkout Session read together with its
// PaymentIntent (spec 08_architecture/67 section 5.2): before arena closes the
// page of an expired seat hold it checks that no payment on it is being
// confirmed (3-D Secure, processing).
package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// stripeSessionWithPayment is the subset of a Checkout Session retrieved with
// expand[]=payment_intent. payment_intent is null until the buyer confirms;
// expanded it is an object with its own status.
type stripeSessionWithPayment struct {
	ID               string          `json:"id"`
	Status           string          `json:"status"`
	RawPaymentIntent json.RawMessage `json:"payment_intent"`
}

// ReadCheckoutSessionPayment reads a Checkout Session (embedded or hosted,
// GET /v1/checkout/sessions/{id}?expand[]=payment_intent) with the
// organizer's secret key and returns the session status and its
// PaymentIntent's status. The client secret is never read out.
//
// Implements payments.CheckoutSessionPaymentReader.
func (a *Adapter) ReadCheckoutSessionPayment(ctx context.Context, sessionID string) (payments.CheckoutSessionPayment, error) {
	id := strings.TrimSpace(sessionID)
	if !strings.HasPrefix(id, "cs_") {
		return payments.CheckoutSessionPayment{}, fmt.Errorf("stripe: ReadCheckoutSessionPayment: %q is not a checkout session id", id)
	}
	endpoint := a.cfg.BaseURL + "/checkout/sessions/" + url.PathEscape(id) + "?" + url.Values{"expand[]": {"payment_intent"}}.Encode()
	rawBody, _, err := a.doRequestVersion(ctx, http.MethodGet, endpoint, nil, "", stripeElementsAPIVersion)
	if err != nil {
		return payments.CheckoutSessionPayment{}, fmt.Errorf("stripe: ReadCheckoutSessionPayment: %w", err)
	}
	var cs stripeSessionWithPayment
	if err := json.Unmarshal(rawBody, &cs); err != nil {
		return payments.CheckoutSessionPayment{}, fmt.Errorf("stripe: ReadCheckoutSessionPayment: unmarshal response: %w", err)
	}
	out := payments.CheckoutSessionPayment{ID: cs.ID, Status: cs.Status}
	pi := strings.TrimSpace(string(cs.RawPaymentIntent))
	switch {
	case pi == "" || pi == "null":
	case strings.HasPrefix(pi, "{"):
		var obj struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(cs.RawPaymentIntent, &obj); err != nil {
			return payments.CheckoutSessionPayment{}, fmt.Errorf("stripe: ReadCheckoutSessionPayment: unmarshal payment_intent: %w", err)
		}
		out.PaymentID, out.PaymentStatus = obj.ID, obj.Status
	default:
		// Not expanded (an id only): its status is unknown, so the caller
		// cannot tell a payment in progress from none. Treat it as an
		// unknown outcome rather than guess.
		return payments.CheckoutSessionPayment{}, fmt.Errorf("stripe: ReadCheckoutSessionPayment: payment_intent was not expanded")
	}
	return out, nil
}

var _ payments.CheckoutSessionPaymentReader = (*Adapter)(nil)

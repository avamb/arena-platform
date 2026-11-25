// embedded.go — the Checkout Session a buyer pays inside arena's own page
// (spec 65 §3: ui_mode=elements, Stripe.js initCheckoutElementsSdk).
//
// The hosted CreateCheckoutSession in adapter.go is untouched: this is a
// separate request with its own Stripe-Version, so nothing the hosted path
// sends changes by a byte.
package stripe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// stripeElementsAPIVersion is the Stripe API version every embedded-checkout
// call (create, retrieve, expire) is pinned to.
//
// The parameter names of this mode changed during 2025 (ui_mode `custom`
// became `elements`, and Stripe.js `initCheckout` became
// `initCheckoutElementsSdk`), so the version is pinned here, apart from the
// adapter-wide stripeAPIVersion the hosted flow, refunds and disputes keep
// using. The parameters below follow the Checkout Session API reference of
// THIS version. Spec 65 §9 requires a check on a Stripe test account before
// the embedded checkout goes live on any channel. A live test-mode probe
// (2026-11-22) showed that 2025-09-30.clover REFUSES ui_mode=elements, while
// 2026-03-25.dahlia accepts ui_mode=elements together with
// allowed_payment_method_types[card] (card only; Link hidden by owner decision
// 2026-11-25, Apple Pay and Google Pay ride on card) and returns client_secret
// on both create and retrieve. If Stripe rejects the version or a parameter, every
// creation is refused and checkout/start falls back to the hosted page
// (hfeed), so a wrong pin costs the embedded form, never a sale.
const stripeElementsAPIVersion = "2026-03-25.dahlia"

// stripeUIModeElements is Stripe's ui_mode value for a session paid with
// Stripe.js Elements on the merchant's own page.
const stripeUIModeElements = "elements"

// stripeEmbeddedSession is the subset of the Checkout Session object the
// embedded flow reads. client_secret is never logged or stored.
//
// Under dahlia payment_intent is null until the buyer confirms (the
// PaymentIntent is created lazily), and it may arrive expanded as an object;
// stripeExpandableID accepts a string, an object with an id, or null.
type stripeEmbeddedSession struct {
	ID            string             `json:"id"`
	ClientSecret  string             `json:"client_secret"`
	UIMode        string             `json:"ui_mode"`
	PaymentIntent stripeExpandableID `json:"payment_intent"`
	ExpiresAt     int64              `json:"expires_at"`
	PaymentStatus string             `json:"payment_status"`
	Status        string             `json:"status"`
}

// stripeExpandableID is a Stripe expandable field read as its id only.
type stripeExpandableID string

// UnmarshalJSON accepts "id", {"id": "..."} and null.
func (e *stripeExpandableID) UnmarshalJSON(b []byte) error {
	t := strings.TrimSpace(string(b))
	if t == "" || t == "null" {
		*e = ""
		return nil
	}
	if strings.HasPrefix(t, "{") {
		var obj struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(b, &obj); err != nil {
			return err
		}
		*e = stripeExpandableID(obj.ID)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*e = stripeExpandableID(s)
	return nil
}

// CreateEmbeddedCheckoutSession creates a Checkout Session with
// ui_mode=elements (POST /v1/checkout/sessions).
//
// Differences from the hosted CreateCheckoutSession, all required by this mode:
//
//   - ui_mode=elements, and NO success_url / cancel_url: Stripe refuses them
//     for this mode.
//   - return_url: where Stripe sends the buyer if the bank opens its own
//     3-D Secure page. The caller routes it through arena's
//     /v1/public/payment-return hop with the checkout_token (spec 65 §3.2).
//   - allowed_payment_method_types[0]=card, NOT payment_method_types
//     (not a create parameter of this mode). Card only, for the same reason
//     as the hosted flow: an asynchronous method could "complete" after the
//     hold is gone. Link is hidden (wallet_options[link][display]=never) by
//     owner decision 2026-11-25: ticket buyers rarely repeat and the Link
//     opt-in block covered the Pay button. Apple Pay and Google Pay ride on
//     card per Stripe docs and need no type of their own.
//   - the same single line item, customer_email, receipt e-mail, expires_at,
//     locale, metadata on the session AND its PaymentIntent, and
//     client_reference_id.
//
// The returned ClientSecret is the only thing the buyer's browser needs. It
// is never stored: the status endpoint reads it back from Stripe.
//
// Implements payments.EmbeddedCheckoutProvider.
func (a *Adapter) CreateEmbeddedCheckoutSession(ctx context.Context, req payments.CreateHostedCheckoutRequest) (*payments.CreateEmbeddedCheckoutResponse, error) {
	if strings.TrimSpace(req.ReturnURL) == "" {
		return nil, fmt.Errorf("stripe: CreateEmbeddedCheckoutSession: return_url is required")
	}
	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("ui_mode", stripeUIModeElements)
	form.Set("allowed_payment_method_types[0]", "card")
	form.Set("wallet_options[link][display]", "never")
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", strings.ToLower(req.Currency))
	form.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(req.Amount, 10))
	form.Set("line_items[0][price_data][product_data][name]", req.ProductName)
	form.Set("return_url", req.ReturnURL)
	if req.ClientReferenceID != "" {
		form.Set("client_reference_id", req.ClientReferenceID)
	}
	if req.CustomerEmail != "" {
		form.Set("customer_email", req.CustomerEmail)
	}
	if req.ReceiptEmail != "" {
		form.Set("payment_intent_data[receipt_email]", req.ReceiptEmail)
	}
	if req.ExpiresAtUnix > 0 {
		form.Set("expires_at", strconv.FormatInt(req.ExpiresAtUnix, 10))
	}
	if loc := checkoutLocale(req.Locale); loc != "" {
		form.Set("locale", loc)
	}
	for k, v := range req.Metadata {
		form.Set("metadata["+k+"]", v)
		form.Set("payment_intent_data[metadata]["+k+"]", v)
	}

	endpoint := a.cfg.BaseURL + "/checkout/sessions"
	rawBody, _, err := a.doRequestVersion(ctx, http.MethodPost, endpoint, form, req.IdempotencyKey, stripeElementsAPIVersion)
	if err != nil {
		return nil, fmt.Errorf("stripe: CreateEmbeddedCheckoutSession: %w", checkoutRefusedError(err))
	}
	var cs stripeEmbeddedSession
	if err := json.Unmarshal(rawBody, &cs); err != nil {
		return nil, fmt.Errorf("stripe: CreateEmbeddedCheckoutSession: unmarshal response: %w", err)
	}
	if !strings.HasPrefix(cs.ID, "cs_") || cs.ClientSecret == "" {
		// Never echo the body: it carries the client secret when present.
		return nil, fmt.Errorf("stripe: CreateEmbeddedCheckoutSession: response has no session id or client secret")
	}
	return &payments.CreateEmbeddedCheckoutResponse{
		SessionID:     cs.ID,
		ClientSecret:  cs.ClientSecret,
		PaymentID:     string(cs.PaymentIntent),
		ExpiresAtUnix: cs.ExpiresAt,
	}, nil
}

// RetrieveEmbeddedCheckoutSession reads a session back
// (GET /v1/checkout/sessions/{id}) with the organizer's secret key. The
// client secret is returned only while the session is open.
//
// Implements payments.EmbeddedCheckoutProvider.
func (a *Adapter) RetrieveEmbeddedCheckoutSession(ctx context.Context, sessionID string) (payments.EmbeddedCheckoutSession, error) {
	id := strings.TrimSpace(sessionID)
	if !strings.HasPrefix(id, "cs_") {
		return payments.EmbeddedCheckoutSession{}, fmt.Errorf("stripe: RetrieveEmbeddedCheckoutSession: %q is not a checkout session id", id)
	}
	endpoint := a.cfg.BaseURL + "/checkout/sessions/" + url.PathEscape(id)
	rawBody, _, err := a.doRequestVersion(ctx, http.MethodGet, endpoint, nil, "", stripeElementsAPIVersion)
	if err != nil {
		return payments.EmbeddedCheckoutSession{}, fmt.Errorf("stripe: RetrieveEmbeddedCheckoutSession: %w", err)
	}
	var cs stripeEmbeddedSession
	if err := json.Unmarshal(rawBody, &cs); err != nil {
		return payments.EmbeddedCheckoutSession{}, fmt.Errorf("stripe: RetrieveEmbeddedCheckoutSession: unmarshal response: %w", err)
	}
	out := payments.EmbeddedCheckoutSession{ID: cs.ID, Status: cs.Status, PaymentStatus: cs.PaymentStatus}
	if cs.Status == payments.EmbeddedSessionOpen && cs.UIMode == stripeUIModeElements {
		out.ClientSecret = cs.ClientSecret
	}
	return out, nil
}

// ExpireEmbeddedCheckoutSession closes an embedded session
// (POST /v1/checkout/sessions/{id}/expire). Stripe expires only an `open`
// session; its definitive refusal (complete, already expired, a payment
// being processed) comes back wrapping payments.ErrCheckoutSessionNotExpirable.
//
// Implements payments.EmbeddedCheckoutProvider.
func (a *Adapter) ExpireEmbeddedCheckoutSession(ctx context.Context, sessionID string) error {
	id := strings.TrimSpace(sessionID)
	if !strings.HasPrefix(id, "cs_") {
		return fmt.Errorf("stripe: ExpireEmbeddedCheckoutSession: %q is not a checkout session id", id)
	}
	endpoint := a.cfg.BaseURL + "/checkout/sessions/" + url.PathEscape(id) + "/expire"
	if _, _, err := a.doRequestVersion(ctx, http.MethodPost, endpoint, url.Values{}, "", stripeElementsAPIVersion); err != nil {
		var e *apiError
		if errors.As(err, &e) && e.definitive() {
			return fmt.Errorf("stripe: ExpireEmbeddedCheckoutSession: %w: %w", payments.ErrCheckoutSessionNotExpirable, err)
		}
		return fmt.Errorf("stripe: ExpireEmbeddedCheckoutSession: %w", err)
	}
	return nil
}

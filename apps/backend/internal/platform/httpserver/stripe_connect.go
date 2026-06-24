// stripe_connect.go implements the Stripe Connect Standard OAuth onboarding
// endpoints (feature #135).
//
// Stripe Connect Standard allows organizers to connect their own Stripe account
// to the platform. The flow is:
//
//  1. Organizer visits GET /v1/stripe/connect/authorize?redirect_uri=<uri>&state=<state>.
//     The handler builds the Stripe OAuth URL and returns it as JSON.
//     Pass ?redirect=true to get a 302 redirect instead.
//
//  2. Stripe redirects the organizer to the callback URI with ?code=<code>.
//     GET /v1/stripe/connect/callback?code=<code>&state=<state> exchanges
//     the code for the connected account ID and returns it as JSON.
//
// Both endpoints require JWT authentication. The routes are only mounted when
// Options.StripeConnect is non-nil. The drift-test server omits this dependency,
// so these routes are absent from the chi.Walk output and from openapi.yaml
// (following the same convention as payment-intent and checkout routes).
package httpserver

import (
	"context"
	"net/http"
)

// stripeConnectHelper is the narrow interface consumed by the Stripe Connect
// handlers. *stripe.Adapter implements this interface; tests can supply a minimal
// stub without importing the concrete stripe adapter package.
type stripeConnectHelper interface {
	// ConnectAuthorizeURL builds the Stripe Connect Standard OAuth authorization
	// URL. Redirect the organizer's browser to this URL to begin onboarding.
	ConnectAuthorizeURL(redirectURI, state string) string
	// ConnectExchangeCode exchanges a Stripe Connect OAuth authorization code for
	// the connected account's Stripe user ID (acct_...).
	ConnectExchangeCode(ctx context.Context, code string) (accountID string, err error)
}

// handleStripeConnectAuthorize serves GET /v1/stripe/connect/authorize.
//
// Returns the Stripe Connect OAuth authorization URL that the front-end should
// redirect the organizer to. Pass redirect=true to get a 302 response instead
// of a JSON body.
//
// Query parameters:
//
//	redirect_uri  (required) — callback URI Stripe will redirect to after auth
//	state         (optional) — CSRF token; returned verbatim by Stripe
//	redirect      (optional) — if "true", respond with 302 instead of JSON
func (s *Server) handleStripeConnectAuthorize(w http.ResponseWriter, r *http.Request) {
	redirectURI := r.URL.Query().Get("redirect_uri")
	if redirectURI == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"stripe_connect.missing_redirect_uri",
			"redirect_uri query parameter is required",
			r,
		))
		return
	}

	state := r.URL.Query().Get("state")
	authorizeURL := s.stripeConnect.ConnectAuthorizeURL(redirectURI, state)

	if r.URL.Query().Get("redirect") == "true" {
		http.Redirect(w, r, authorizeURL, http.StatusFound)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"authorize_url": authorizeURL,
	})
}

// handleStripeConnectCallback serves GET /v1/stripe/connect/callback.
//
// Exchanges the Stripe Connect authorization code for the connected account's
// Stripe user ID and returns it as JSON. The caller should persist the
// account_id in the relevant sales_channel or organization record.
//
// Query parameters:
//
//	code   (required) — authorization code from Stripe's redirect
//	state  (optional) — CSRF state the caller passed; echoed back for verification
//	error  (optional) — set by Stripe when the organizer denies access
func (s *Server) handleStripeConnectCallback(w http.ResponseWriter, r *http.Request) {
	// Surface provider-level errors (e.g. organizer denied access).
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		errDesc := r.URL.Query().Get("error_description")
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"stripe_connect.oauth_error",
			"Stripe Connect authorization failed: "+errDesc,
			r,
			map[string]any{
				"error":             errParam,
				"error_description": errDesc,
			},
		))
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"stripe_connect.missing_code",
			"code query parameter is required",
			r,
		))
		return
	}

	accountID, err := s.stripeConnect.ConnectExchangeCode(r.Context(), code)
	if err != nil {
		s.logger.Error("stripe_connect: code exchange failed", "error", err.Error())
		writeJSON(w, http.StatusBadGateway, errorEnvelope(
			"stripe_connect.exchange_failed",
			"failed to exchange authorization code with Stripe",
			r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"account_id": accountID,
		"state":      r.URL.Query().Get("state"),
	})
}

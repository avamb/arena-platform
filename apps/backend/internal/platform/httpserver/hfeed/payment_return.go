// payment_return.go — GET|POST /v1/public/payment-return
//
// Where a provider-hosted payment page sends the buyer back to.
//
// Some providers send the buyer back with an HTTP POST (Flitt's portal default
// is POST; the setting lives in Merchant settings and is reachable only after
// the director's identification). The page the buyer should land on is the
// embedding site's ordinary web page — a static tickets page answers a POST
// with 405 — so pointing the provider's response_url straight at it makes the
// buyer's last screen depend on a switch in somebody else's portal.
//
// This route removes that dependency: the provider is pointed HERE, and the
// answer to either method is a 303 See Other to the buyer's own page, which
// the browser always follows with GET. The payment's outcome never rides on
// this redirect — it comes from the provider's server callback — so the body a
// provider POSTs here is deliberately never read.
//
// Open-redirect defence: the target is NOT taken on trust. It is re-run
// through the same ReturnURLPolicy that accepted it when the checkout was
// started (CORS origins + PUBLIC_TICKETS_BASE_URL); an origin that is not on
// that allow-list is replaced by the configured fallback, exactly as at start.
// Only arena's own checkout_token is ever appended, so a caller cannot smuggle
// extra query parameters onto the destination.
package hfeed

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// PaymentReturnPath is the route this file serves.
const PaymentReturnPath = "/v1/public/payment-return"

// returnTokenPattern bounds what may be echoed into the destination's query
// string: arena's own tokens are 64 hex characters, so anything longer or with
// other characters is not one of ours.
var returnTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// PaymentReturnURL builds the URL a provider is told to send the buyer to.
// apiBase is arena's public API origin, returnBase the (already policy-resolved)
// buyer page, token arena's checkout_token. An empty apiBase yields the direct
// buyer URL, which is the pre-existing behaviour.
func PaymentReturnURL(apiBase, returnBase, token string) string {
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if apiBase == "" {
		return ReturnURLWithToken(returnBase, token)
	}
	q := url.Values{}
	q.Set("r", returnBase)
	q.Set(checkoutTokenQueryParam, token)
	return apiBase + PaymentReturnPath + "?" + q.Encode()
}

// HandlePaymentReturn serves GET and POST /v1/public/payment-return.
func (h *Handler) HandlePaymentReturn(w http.ResponseWriter, r *http.Request) {
	// r.URL.Query() only: a provider's POST body carries the payment result
	// and is none of this route's business, so it is never parsed.
	q := r.URL.Query()

	token := strings.TrimSpace(q.Get(checkoutTokenQueryParam))
	if !returnTokenPattern.MatchString(token) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"checkout.invalid_return", "checkout_token is missing or malformed", r))
		return
	}

	base, err := h.returnURLPolicy.Resolve(q.Get("r"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			ErrCodeInvalidReturnURL, "return target is not an allowed origin and no fallback is configured", r))
		return
	}

	// The token is in the destination's query string; keep it out of any
	// Referer the buyer's next hop could leak, and out of caches.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// base came out of ReturnURLPolicy.Resolve: its origin is on the allow-list
	// or is the configured fallback, never the caller's raw input.
	http.Redirect(w, r, ReturnURLWithToken(base, token), http.StatusSeeOther) //nolint:gosec // G710: destination is allow-list validated above
}

package httputil

import (
	"crypto/subtle"
	"net/http"
)

// RequireStaticBearer guards a route that one trusted process calls with a
// shared secret (the Telegram bot, the CRM sync) instead of a user's token.
//
// An EMPTY token never opens the route: it answers 503 with the error code
// notConfiguredCode, so a deployment that forgot the secret cannot be used by
// anybody. A wrong or missing Authorization header answers 401. The compare is
// constant time and the answers do not say which part was wrong.
func RequireStaticBearer(token, notConfiguredCode string) func(http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" {
				WriteJSON(w, http.StatusServiceUnavailable, ErrorEnvelope(
					notConfiguredCode, "the service token is not configured", r,
				))
				return
			}
			got := []byte(r.Header.Get("Authorization"))
			if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
				WriteJSON(w, http.StatusUnauthorized, ErrorEnvelope(
					"auth_required", "authentication required", r,
				))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

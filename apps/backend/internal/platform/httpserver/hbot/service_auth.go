package hbot

import (
	"crypto/subtle"
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// RequireServiceToken guards the one route the bot process calls outside a
// linked user's identity. Unlike the /metrics guard, an EMPTY token never
// opens the route: it answers 503 bot.service_not_configured, so a
// deployment that forgot BOT_SERVICE_TOKEN cannot be used to bind Telegram
// accounts to arbitrary invitations.
func RequireServiceToken(token string) func(http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" {
				httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
					"bot.service_not_configured", "the bot service token is not configured", r,
				))
				return
			}
			got := []byte(r.Header.Get("Authorization"))
			if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
				httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
					"auth_required", "authentication required", r,
				))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

package hbot

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// RequireServiceToken guards the one route the bot process calls outside a
// linked user's identity. Unlike the /metrics guard, an EMPTY token never
// opens the route: it answers 503 bot.service_not_configured, so a
// deployment that forgot BOT_SERVICE_TOKEN cannot be used to bind Telegram
// accounts to arbitrary invitations. The check itself is
// httputil.RequireStaticBearer.
func RequireServiceToken(token string) func(http.Handler) http.Handler {
	return httputil.RequireStaticBearer(token, "bot.service_not_configured")
}

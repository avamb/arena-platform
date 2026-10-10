package httpserver

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// clientChannelMiddleware reads the optional X-Client-Channel header and, when
// it names a known channel (telegram_bot, admin_web, site_plugin), puts it on
// the request context. audit.WithVia then stamps it into metadata.via of every
// audit row the request writes, so no call site has to know the client.
//
// An absent or unknown value is ignored — never a 4xx: the header is a
// forensic hint and must not be able to break a request. It runs for every
// route, before any auth, because the channel is a property of the request,
// not of the caller's identity.
// decorateAuditWriter wraps the server's audit writer with the two request-
// scoped attributions every handler gets for free: API-key requests are
// attributed to `api_key:<id>` (spec §13.1) and every row carries the client
// channel as metadata.via. The decorators touch disjoint fields, so their
// order is functionally irrelevant; WithVia is outermost as the later layer.
func decorateAuditWriter(w audit.Writer) audit.Writer {
	return audit.WithVia(audit.WithServiceActor(w))
}

func clientChannelMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if channel, ok := audit.NormalizeClientChannel(r.Header.Get(audit.HeaderClientChannel)); ok {
			r = r.WithContext(audit.WithClientChannel(r.Context(), channel))
		}
		next.ServeHTTP(w, r)
	})
}

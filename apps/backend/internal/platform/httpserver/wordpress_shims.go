// wordpress_shims.go bridges the *Server god-object to the hwordpress
// sub-package. All handler bodies live in hwordpress/; these thin delegating
// methods preserve the unexported *Server method surface so mount_admin.go
// and the structural test files (wordpress_webhook_156_test.go,
// openapi_webhook_subscribers_277_test.go) compile unchanged.
package httpserver

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hwordpress"
)

// wordpressHandler constructs an hwordpress.Handler from the server's
// dependencies. A fresh handler per request keeps the wiring uniform with
// hgeo / hgdpr / hfeed and avoids stale captures when test code mutates
// *Server fields between calls.
func (s *Server) wordpressHandler() *hwordpress.Handler {
	return hwordpress.New(
		s.webhookSubQueries,
		s.pool,
		s.logger,
	)
}

// ─── webhook subscriber handler shims ─────────────────────────────────────────

func (s *Server) handleRegisterWebhookSubscriber(w http.ResponseWriter, r *http.Request) {
	s.wordpressHandler().HandleRegisterWebhookSubscriber(w, r)
}

func (s *Server) handleListWebhookSubscribers(w http.ResponseWriter, r *http.Request) {
	s.wordpressHandler().HandleListWebhookSubscribers(w, r)
}

func (s *Server) handleGetWebhookSubscriber(w http.ResponseWriter, r *http.Request) {
	s.wordpressHandler().HandleGetWebhookSubscriber(w, r)
}

func (s *Server) handleDeactivateWebhookSubscriber(w http.ResponseWriter, r *http.Request) {
	s.wordpressHandler().HandleDeactivateWebhookSubscriber(w, r)
}

func (s *Server) handleUpdateWebhookSubscriber(w http.ResponseWriter, r *http.Request) {
	s.wordpressHandler().HandleUpdateWebhookSubscriber(w, r)
}

func (s *Server) handleListRecentWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	s.wordpressHandler().HandleListRecentWebhookDeliveries(w, r)
}

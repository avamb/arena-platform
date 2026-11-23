// feed_embedded_shims.go — the *Server side of the embedded checkout
// (spec 65): the hosted fallback route.
package httpserver

import "net/http"

// handlePublicCheckoutHostedFallback serves
// POST /v1/public/checkout/{checkout_token}/hosted.
func (s *Server) handlePublicCheckoutHostedFallback(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicCheckoutHostedFallback(w, r)
}

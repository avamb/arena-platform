// feed_embedded_shims.go — the *Server side of the embedded checkout
// (spec 65): the hosted fallback route.
package httpserver

import "net/http"

// handlePublicCheckoutHostedFallback serves
// POST /v1/public/checkout/{checkout_token}/hosted.
func (s *Server) handlePublicCheckoutHostedFallback(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicCheckoutHostedFallback(w, r)
}

// handlePublicCheckoutBuyer serves
// POST /v1/public/checkout/{checkout_token}/buyer (spec 65 prepared order).
func (s *Server) handlePublicCheckoutBuyer(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicCheckoutBuyer(w, r)
}

// handlePublicCheckoutPaying serves
// POST /v1/public/checkout/{checkout_token}/paying (spec 67 section 4.8).
func (s *Server) handlePublicCheckoutPaying(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicCheckoutPaying(w, r)
}

// handlePublicCheckoutMarketing serves
// POST /v1/public/checkout/{checkout_token}/marketing (thank-you page subscription).
func (s *Server) handlePublicCheckoutMarketing(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicCheckoutMarketing(w, r)
}

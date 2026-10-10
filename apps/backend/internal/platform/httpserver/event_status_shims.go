package httpserver

import "net/http"

// handleEventDeleteImpact is the dry run of deleting an event (EC-10).
func (s *Server) handleEventDeleteImpact(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleEventDeleteImpact(w, r)
}

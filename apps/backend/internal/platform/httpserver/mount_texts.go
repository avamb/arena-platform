package httpserver

import "github.com/go-chi/chi/v5"

// mountTextRoutes wires the organizer-text surface of spec 44:
//
//	POST /v1/organizations/{org_id}/texts/format
//	                               event.update — a model adds markup to a text,
//	                               the code checks no word changed (RT-04)
//
// event.update is held by the owner (org_admin) and the manager (organizer)
// alike, and not by the agent role; the route has {org_id} in its path, so the
// gate decides inside that organization.
func (s *Server) mountTextRoutes(r chi.Router) {
	s.textFormatter = resolveTextFormatter(s.cfg)
	h := s.textFormatHandler()
	s.logTextFormatting()
	if !s.authEnabled() {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "event.update", "texts")
		pr.Post("/organizations/{org_id}/texts/format", h.Format)
	})
}

// mount_seating.go registers the /v1/venues/{venue_id}/seating-plans and
// /v1/seating-plans/{id}[/versions[/{n}]|/fork] routes served by the hseating
// sub-package (feature #304, Wave SEAT-A3). Every route is gated on the
// seating_plan.* permission family seeded in migration 0057; the
// applyAuth middleware attaches the stub JWT authenticator and enforces
// the permission at the boundary before delegation.
package httpserver

import (
	"github.com/go-chi/chi/v5"
)

// mountSeatingRoutes mounts the seating-plan surface. The mount self-gates
// on seatingQueries + pool so tests can compose a Server without a live DB;
// individual handlers additionally return 503 when hit with a nil pool.
func (s *Server) mountSeatingRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.seatingQueries == nil || s.pool == nil {
		return
	}

	// Read surface.
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "seating_plan.read", "seating")
		pr.Get("/venues/{venue_id}/seating-plans", s.handleListSeatingPlansByVenue)
		pr.Get("/seating-plans/{id}", s.handleGetSeatingPlan)
		pr.Get("/seating-plans/{id}/versions/{n}", s.handleGetSeatingPlanVersion)
	})

	// Create surface (new plans + new versions).
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "seating_plan.create", "seating")
		pr.Post("/venues/{venue_id}/seating-plans", s.handleCreateSeatingPlan)
	})

	// Own-mutation surface: metadata PATCH + version create.
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "seating_plan.update.own", "seating")
		pr.Patch("/seating-plans/{id}", s.handleUpdateSeatingPlan)
		pr.Post("/seating-plans/{id}/versions", s.handleCreateSeatingPlanVersion)
	})

	// Fork surface — gated on the dedicated seating_plan.fork permission.
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "seating_plan.fork", "seating")
		pr.Post("/seating-plans/{id}/fork", s.handleForkSeatingPlan)
	})
}

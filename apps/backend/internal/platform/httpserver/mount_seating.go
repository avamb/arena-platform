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
	// Public unauthenticated read surface (feature #307, Wave SEAT-B3).
	// These endpoints do not require the stub JWT provider — they mirror
	// the public feed visibility gate at the SQL layer.
	if s.seatingQueries != nil {
		r.Get("/event-sessions/{id}/schema", s.handleGetPublicSessionSchema)
		r.Get("/event-sessions/{id}/seat-status", s.handleGetPublicSessionSeatStatus)
		// SEAT-D3 (feature #314): BSS-compatible SVG export served
		// as image/svg+xml so legacy Bil24 widgets can consume the
		// plan verbatim. Same public visibility gate as the two
		// endpoints above.
		r.Get("/event-sessions/{id}/layout.svg", s.handleGetPublicSessionLayoutSVG)
	}

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

	// Bind surface (feature #306, Wave SEAT-B2). Binds a seating_plan_version
	// to an event session, materializes session_seats under a single tx,
	// applies the category → tier map, and recomputes capacity_total.
	// Requires the dedicated event_session.assign_seating_plan permission
	// (seeded in migration 0057). Distinct from seating_plan.* so an org can
	// grant "operate seated inventory" without granting "edit plan geometry".
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "event_session.assign_seating_plan", "seating")
		pr.Post(
			"/organizations/{org_id}/events/{event_id}/sessions/{id}/seating",
			s.handleBindSessionSeating,
		)
	})

	// Operator seat block/unblock surface (feature #308, Wave SEAT-B4). Same
	// permission family as the bind endpoint above — operators authorised to
	// assign a seating plan to a session are the same population authorised
	// to close individual seats / rows / sectors for sale (tech seats, house
	// holds, blocked sightlines).
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "event_session.assign_seating_plan", "seating")
		pr.Patch(
			"/organizations/{org_id}/events/{event_id}/sessions/{id}/seats",
			s.handlePatchSessionSeats,
		)
	})
}

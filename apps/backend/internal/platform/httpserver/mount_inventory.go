package httpserver

import "github.com/go-chi/chi/v5"

// mountInventoryRoutes mounts GA inventory ledger endpoints (feature #130).
func (s *Server) mountInventoryRoutes(r chi.Router) {
	if s.stub != nil && s.stub.Enabled() && s.inventoryQueries != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "inventory.read", "inventory")
			pr.Get("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory", s.handleListInventory)
		})
	}
	if s.stub != nil && s.stub.Enabled() && s.inventoryQueries != nil && s.pool != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "inventory.reserve", "inventory")
			pr.Post("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory", s.handleInitInventory)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "inventory.reserve", "inventory")
			pr.Post("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory/reserve", s.handleReserveCapacity)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "inventory.release", "inventory")
			pr.Post("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory/release", s.handleReleaseCapacity)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "inventory.confirm", "inventory")
			pr.Post("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory/confirm", s.handleConfirmCapacity)
		})
	}
}

// mountReservationRoutes mounts reservation state machine endpoints
// (feature #131).
func (s *Server) mountReservationRoutes(r chi.Router) {
	if s.stub != nil && s.stub.Enabled() && s.reservationQueries != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "reservation.read", "reservations")
			pr.Get("/reservations/{id}", s.handleGetReservation)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "reservation.activate", "reservations")
			pr.Patch("/reservations/{id}/activate", s.handleActivateReservation)
		})
	}
	if s.stub != nil && s.stub.Enabled() && s.reservationQueries != nil && s.inventoryQueries != nil && s.pool != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "reservation.create", "reservations")
			pr.Post("/reservations", s.handleCreateReservation)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "reservation.cancel", "reservations")
			pr.Delete("/reservations/{id}", s.handleCancelReservation)
		})
	}
}

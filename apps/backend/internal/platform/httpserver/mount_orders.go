package httpserver

import (
	"github.com/go-chi/chi/v5"
)

// mountOrderRoutes mounts the org-scoped orders surface (feature #489,
// W1-A6d, spec §14.2): list/detail require `order.read`, cancel requires
// `order.write`. Every handler additionally scopes lookups via
// enforceOrgMembership plus an org_id-qualified WHERE clause so an order
// belonging to another org is invisible (404) even by direct id lookup.
func (s *Server) mountOrderRoutes(r chi.Router) {
	if !s.authEnabled() || s.customerQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "order.read", "orders")
		pr.Get("/organizations/{org_id}/orders", s.handleListOrders)
		pr.Get("/organizations/{org_id}/orders/{id}", s.handleGetOrder)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "order.write", "orders")
		pr.Post("/organizations/{org_id}/orders/{id}/cancel", s.handleCancelOrder)
	})
}

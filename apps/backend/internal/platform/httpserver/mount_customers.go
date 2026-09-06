package httpserver

import (
	"github.com/go-chi/chi/v5"
)

// mountCustomerRoutes mounts the org-scoped customer read surface (feature
// #482, W1-A4d, spec §12.3): search/list and the customer card. Both routes
// require the `customer.read` permission; the handlers additionally scope
// every lookup via customer_org_links.org_id = org so a customer never
// linked to the caller's org is invisible even by direct id lookup.
func (s *Server) mountCustomerRoutes(r chi.Router) {
	if !s.authEnabled() || s.customerQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "customer.read", "customers")
		pr.Get("/organizations/{org_id}/customers", s.handleListCustomers)
		pr.Get("/organizations/{org_id}/customers/{id}", s.handleGetCustomer)
	})
}

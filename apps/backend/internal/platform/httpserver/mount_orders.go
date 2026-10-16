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
		// The same search with the filters in the body: a buyer's e-mail or
		// phone never reaches a URL that proxies log (spec 37 §13 item 5).
		pr.Post("/organizations/{org_id}/orders/search", s.handleSearchOrders)
		// The buyer's consent at checkout (P2): dispute evidence with the
		// buyer's IP address and browser, for the order's organization only.
		pr.Get("/organizations/{org_id}/orders/{order_id}/consents", s.handleOrderConsents)
		// Every open and click of the order's letters with the reader's
		// browser string (U-7): dispute evidence, same audience as consents.
		pr.Get("/organizations/{org_id}/orders/{order_id}/letter-interactions", s.handleOrderLetterInteractions)
		// One-screen session overview: places, categories, money, tickets, refunds.
		pr.Get("/organizations/{org_id}/sessions/{session_id}/summary", s.handleSessionSummary)
		// The tickets of a session with their entry at the door, filterable to
		// "has not come yet" (M2). Buyer data: an API key is refused.
		pr.Get("/organizations/{org_id}/sessions/{session_id}/tickets", s.handleSessionTickets)
		// The same overview for every session of an event, totals plus a list (EC-06).
		pr.Get("/organizations/{org_id}/events/{event_id}/summary", s.handleEventSummary)
		// The sales figures of a list of events in one request (WEB-LIST): the
		// same numbers as the event summary's top level, up to 50 ids.
		pr.Get("/organizations/{org_id}/events/sales-summary", s.handleEventSalesSummaries)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "order.write", "orders")
		// The web asks for the password again before it cancels (spec 37 4.6, task 4-2).
		pr.Use(s.requireWebStepUp)
		pr.Post("/organizations/{org_id}/orders/{id}/cancel", s.handleCancelOrder)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "ticket.update", "orders")
		// Send an order's tickets again, to its own address or a one-time one (EC-13).
		pr.Post("/organizations/{org_id}/orders/{order_id}/resend-tickets", s.handleResendOrderTickets)
	})
}

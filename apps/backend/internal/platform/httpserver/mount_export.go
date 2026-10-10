package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hexport"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
)

// mountExportRoutes mounts the organizer's CSV exports (EC-07, spec 35
// §5.8). The sales and summary files need BOTH `order.read` and
// `session.read` — they print buyers next to the session; the promo usage
// file needs `promo.read`. Every handler additionally runs
// enforceOrgMembership, and the header queries are org-scoped, so a row of
// another organization is the route's own 404.
func (s *Server) mountExportRoutes(r chi.Router) {
	if !s.authEnabled() || s.customerQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "order.read", "orders")
		pr.Use(permissions.RequirePermission(s.perms, "session.read", "sessions"))
		pr.Get("/organizations/{org_id}/sessions/{session_id}/sales.csv", s.handleExportSessionSales)
		pr.Get("/organizations/{org_id}/events/{event_id}/sales.csv", s.handleExportEventSales)
		pr.Get("/organizations/{org_id}/sessions/{session_id}/summary.csv", s.handleExportSessionSummary)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "promo.read", "promo_codes")
		pr.Get("/organizations/{org_id}/promo-codes/{promo_code_id}/redemptions.csv", s.handleExportPromoRedemptions)
	})
}

// exportHandler builds the hexport.Handler per request, like the other
// per-domain handlers (orders_shims.go), so a test may lower the row cap
// through exportMaxRows without rebuilding the server.
func (s *Server) exportHandler() *hexport.Handler {
	h := hexport.New(s.customerQueries, s.logger)
	if s.exportMaxRows > 0 {
		h.WithMaxRows(s.exportMaxRows)
	}
	return h
}

func (s *Server) handleExportSessionSales(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.exportHandler().HandleSessionSales(w, r)
}

func (s *Server) handleExportEventSales(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.exportHandler().HandleEventSales(w, r)
}

func (s *Server) handleExportSessionSummary(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.exportHandler().HandleSessionSummary(w, r)
}

func (s *Server) handleExportPromoRedemptions(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.exportHandler().HandlePromoRedemptions(w, r)
}

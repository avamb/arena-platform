package httpserver

// mount_sales_funnel.go — spec 50, stage A7: the organizer's read of the buyer
// sales funnel of an event. It needs report.read (held by the owner and, since
// migration 0129, the manager; no new permission) and membership of {org_id},
// and ties the event to the path organization inside the handler (a foreign or
// deleted event is the route's own 404 event.not_found). The public beacon that
// feeds it (POST /v1/public/funnel) is mounted in mount_catalog.go.

import (
	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hsalesfunnel"
)

func (s *Server) mountSalesFunnelRoutes(r chi.Router) {
	// Mounted without a database too: the handler answers 503 then.
	if !s.authEnabled() {
		return
	}
	h := hsalesfunnel.New(s.customerQueries, s.logger)
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "report.read", "reports")
		pr.Get("/organizations/{org_id}/events/{event_id}/funnel", s.orgGated(h.HandleGet))
	})
}

package httpserver

// mount_page_stats.go — task 2-3 (HP-2a): the organizer's read of the hosted
// page's statistics, JSON and CSV. Both need event.read and membership of
// {org_id}, and tie the event to the path organization inside the handler
// (a foreign or deleted event is the route's own 404 event.not_found). The
// public counter route lives in mount_catalog.go.

import (
	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hpagestats"
)

func (s *Server) mountPageStatsRoutes(r chi.Router) {
	// Mounted without a database too: the handlers answer 503 then.
	if !s.authEnabled() {
		return
	}
	jsonH := hpagestats.New(s.customerQueries, s.logger)
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "event.read", "events")
		pr.Get("/organizations/{org_id}/events/{event_id}/page-stats", s.orgGated(jsonH.HandleGet))
		pr.Get("/organizations/{org_id}/events/{event_id}/page-stats.csv", s.orgGated(s.exportHandler().HandlePageStats))
	})
}

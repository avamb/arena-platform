package httpserver

// mount_metainsights.go - the organizer's Meta ads reports (spec
// 08_architecture/66 section 5, stage M2). They need report.read (the owner
// and the manager; an organization API key with report.read reads its own
// organization, like the sales funnel) and membership of {org_id}; the event
// report ties the event to the path organization inside the handler (a
// foreign or deleted event is the route's own 404 event.not_found). The
// routes are always mounted; when META_* is incomplete they answer 503
// meta.disabled.

import (
	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hmetainsights"
)

func (s *Server) mountMetaInsightsRoutes(r chi.Router) {
	if !s.authEnabled() {
		return
	}
	h := hmetainsights.New(s.pgxPool, s.cfg.MetaEnabled(), s.logger)
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "report.read", "reports")
		pr.Get("/organizations/{org_id}/events/{event_id}/ads/meta", s.orgGated(h.HandleEvent))
		pr.Get("/organizations/{org_id}/ads/meta/summary", s.orgGated(h.HandleSummary))
		pr.Post("/organizations/{org_id}/ads/meta/refresh", s.orgGated(h.HandleRefresh))
	})
}

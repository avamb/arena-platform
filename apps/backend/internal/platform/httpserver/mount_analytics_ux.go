package httpserver

// mount_analytics_ux.go — the read-only UX dashboards of the admin console
// (spec 50 section 10): flows, errors and a person's timeline. Platform
// superadmin only (superadmin.read); the routes read the analytics schema and
// nothing else.

import (
	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hanalytics"
)

func (s *Server) mountAnalyticsUXRoutes(r chi.Router) {
	if !s.authEnabled() {
		return
	}
	// Without a database the routes still exist and answer 503; a nil pool
	// must not be wrapped into a non-nil interface.
	opts := hanalytics.UXOptions{Logger: s.logger}
	if s.pgxPool != nil {
		opts.DB = s.pgxPool
	}
	h := hanalytics.NewUX(opts)
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "superadmin.read", "analytics")
		pr.Get("/admin/analytics/flows", h.HandleFlows)
		pr.Get("/admin/analytics/errors", h.HandleErrors)
		pr.Get("/admin/analytics/people/{person_id}/timeline", h.HandleTimeline)
	})
}

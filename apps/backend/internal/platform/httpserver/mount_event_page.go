package httpserver

import (
	"log/slog"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventpage"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/heventpage"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ratelimit"
)

// mountEventPageRoutes wires the event-page reading of spec 59:
//
//	POST /v1/organizations/{org_id}/event-page-facts
//	                               event.update — read the organizer's own
//	                               event page into wizard hints
//
// event.update is held by the owner (org_admin) and the manager (organizer)
// alike, as for the poster reading; the route has {org_id} in its path, so
// the gate decides inside that organization. The page is fetched only
// through the egress guard (eventpage.Fetcher on egress.Fetch).
func (s *Server) mountEventPageRoutes(r chi.Router) {
	h := heventpage.New(s.logger).
		WithModel(eventPageModel(s.cfg, s.logger), eventPageModelQuota(s.cfg)).
		WithMedia(s.media).
		WithRowOrgAccess(s.rowOrgAccess)
	if !s.authEnabled() {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "event.update", "event_page")
		pr.Post("/organizations/{org_id}/event-page-facts", h.PageFacts)
	})
}

// eventPageModel is the text model of the reading: the text-formatting model
// with the poster key (both on arena-api only), or nil without a key, which
// leaves the model step off (the reading then adds a warning).
func eventPageModel(cfg *config.Config, logger *slog.Logger) eventpage.TextReader {
	if cfg == nil {
		return nil
	}
	m := eventpage.NewAnthropic(cfg.PosterLLMAPIKey, cfg.TextFormatLLMModel, cfg.PosterLLMBaseURL, nil).WithLogger(logger)
	if !m.Configured() {
		return nil
	}
	return m
}

// eventPageModelQuota is the per-organization daily cap of the model step,
// nil when EVENT_PAGE_READS_PER_ORG_PER_DAY is 0 (or the config was built by
// hand without it).
func eventPageModelQuota(cfg *config.Config) heventpage.Quota {
	if cfg == nil || cfg.EventPageReadsPerOrgPerDay <= 0 {
		return nil
	}
	return ratelimit.New(ratelimit.Config{MaxAttempts: cfg.EventPageReadsPerOrgPerDay, Window: heventpage.ModelReadsWindow})
}

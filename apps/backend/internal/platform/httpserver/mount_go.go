package httpserver

// mount_go.go — the public short-link redirect GET|HEAD /go/{code} of the
// organizer sales links (spec 43 section 4.1, task QL-3). The route lives on
// the ROOT router, outside /v1: go.arenasoldout.com/<code> reaches it through
// a Traefik rule that adds the /go prefix, and it answers the same way on any
// host (spec 43 section 7).

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hgo"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salelinks"
)

// mountShortLinkRoutes registers the redirect when the public feed queries are
// wired (the nil-guard of the other public routes). Called once from
// mountOperationalRoutes, so the handler and its scan de-duplication live for
// the whole process.
func (s *Server) mountShortLinkRoutes() {
	if s.publicFeedQueries == nil {
		return
	}
	h := s.shortLinkHandler()
	s.router.Method(http.MethodGet, "/go/{code}", h)
	s.router.Method(http.MethodHead, "/go/{code}", h)
}

// shortLinkHandler builds the one hgo.Handler of this Server.
func (s *Server) shortLinkHandler() *hgo.Handler {
	if s.shortLinks != nil {
		return s.shortLinks
	}
	opts := hgo.Options{
		Queries: s.publicFeedQueries,
		Dedup:   salelinks.NewDedup(0, 0),
		Logger:  s.logger,
		// typedMetrics is read at call time: tests replace it after New.
		Observe: func(outcome string) { s.typedMetrics.RecordShortLink(outcome) },
	}
	if s.publicFeedRL != nil {
		opts.Limiter = s.publicFeedRL
	}
	if s.cfg != nil {
		opts.Base = s.cfg.PublicTicketsBaseURL
		opts.Fallback = s.cfg.ShortLinkFallback()
		opts.TrustedProxies = s.cfg.TrustedProxyCount
	}
	s.shortLinks = hgo.New(opts)
	return s.shortLinks
}

package httpserver

// mount_analytics.go — the receivers of the platform's own product analytics
// (spec 50 section 4.2): POST /v1/t (public, the website) and POST /v1/me/t
// (the event center, JWT). The buffered Writer is built here, once per
// process, from the raw pgx pool; Shutdown flushes it. Without a database the
// public route still answers 204 (the contract does not depend on whether a
// write is possible) and stores nothing.

import (
	"context"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/analytics"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hanalytics"
)

// pgContactCodes answers "does this contact code exist" from the database.
type pgContactCodes struct{ s *Server }

func (p pgContactCodes) ContactCodeExists(ctx context.Context, code string) (bool, error) {
	var ok bool
	err := p.s.pgxPool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM analytics.contact_codes WHERE code = $1)", code).Scan(&ok)
	return ok, err
}

// analyticsWriter returns the process-wide Writer, or nil without a database
// (a nil Writer is a safe no-op).
func (s *Server) analyticsWriter() *analytics.Writer {
	if s.analytics != nil || s.pgxPool == nil {
		return s.analytics
	}
	o := analytics.Options{DB: s.pgxPool, Logger: s.logger}
	if s.typedMetrics != nil {
		o.Dropped = s.typedMetrics.AnalyticsDroppedTotal
		o.FlushFailed = s.typedMetrics.AnalyticsFlushFailedTotal
	}
	s.analytics = analytics.NewWriter(o)
	return s.analytics
}

// analyticsOrgAccess decides whether the signed-in caller may attach orgID to
// its events: a member of the organization, or a platform superadmin.
func (s *Server) analyticsOrgAccess(ctx context.Context, orgID uuid.UUID) (bool, error) {
	if s.membershipQueries != nil {
		member, err := actorIsMemberOfOrgServer(ctx, s.membershipQueries, orgID)
		if err != nil {
			return false, err
		}
		if member {
			return true, nil
		}
	}
	actor, ok := auth.ActorFromContext(ctx)
	return ok && s.superadminOrgBypass(ctx, actor), nil
}

func (s *Server) mountAnalyticsRoutes(r chi.Router) {
	opts := hanalytics.Options{Logger: s.logger, OrgAccess: s.analyticsOrgAccess}
	if w := s.analyticsWriter(); w != nil {
		opts.Tracker = w
		opts.Codes = pgContactCodes{s: s}
	} else {
		opts.Tracker = (*analytics.Writer)(nil)
	}
	h := hanalytics.New(opts)
	r.Post("/t", h.HandleSite)
	if !s.authEnabled() {
		return
	}
	r.Group(func(pr chi.Router) {
		pr.Use(auth.Middleware(s.authProvider(), auth.MiddlewareOptions{Logger: s.logger}))
		pr.Post("/me/t", h.HandleMe)
	})
}

package httpserver

// mount_analytics.go — the receivers of the platform's own product analytics
// (spec 50 section 4.2): POST /v1/t (public, the website) and POST /v1/me/t
// (the event center, JWT). The buffered Writer is built here, once per
// process, from the raw pgx pool; Shutdown flushes it. Without a database the
// public route still answers 204 (the contract does not depend on whether a
// write is possible) and stores nothing.

import (
	"context"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/analytics"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hanalytics"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// pgContactCodes answers "does this contact code exist" from the database.
type pgContactCodes struct{ s *Server }

func (p pgContactCodes) ContactCodeExists(ctx context.Context, code string) (bool, error) {
	var ok bool
	err := p.s.pgxPool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM analytics.contact_codes WHERE code = $1)", code).Scan(&ok)
	return ok, err
}

// analyticsWriter returns the process-wide Writer, built on first use so a
// server that never receives an analytics batch never starts its flusher
// goroutine. It is nil without a database or after shutdown (a nil Writer is a
// safe no-op).
func (s *Server) analyticsWriter() *analytics.Writer {
	s.analyticsMu.Lock()
	defer s.analyticsMu.Unlock()
	if s.analytics != nil || s.pgxPool == nil || s.analyticsClosed {
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

// lazyTracker hands events to the Writer, building it at the first one.
type lazyTracker struct{ s *Server }

func (l lazyTracker) Track(ctx context.Context, ev analytics.Event) {
	l.s.analyticsWriter().Track(ctx, ev)
}

// analyticsFlushBudget is how long shutdown waits for the buffered events. It
// is its own deadline: the shutdown context may already be spent by draining
// requests, and the events of those last requests are worth a few seconds.
const analyticsFlushBudget = 5 * time.Second

// closeAnalytics flushes and stops the Writer. Safe when there is none.
func (s *Server) closeAnalytics(parent context.Context) error {
	s.analyticsMu.Lock()
	w := s.analytics
	s.analyticsClosed = true
	s.analyticsMu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), analyticsFlushBudget)
	defer cancel()
	return w.Close(ctx)
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
	opts := hanalytics.Options{Logger: s.logger, OrgAccess: s.analyticsOrgAccess, Tracker: lazyTracker{s: s}}
	if s.pgxPool != nil {
		opts.Codes = pgContactCodes{s: s}
	}
	h := hanalytics.New(opts)
	r.Post("/t", h.HandleSite)
	s.mountAnalyticsServiceRoutes(r)
	if !s.authEnabled() {
		return
	}
	r.Group(func(pr chi.Router) {
		pr.Use(auth.Middleware(s.authProvider(), auth.MiddlewareOptions{Logger: s.logger}))
		pr.Post("/me/t", h.HandleMe)
	})
	s.mountAnalyticsAdminRoutes(r)
}

// analyticsSyncLabel is the actor label the audit log shows for the CRM sync.
const analyticsSyncLabel = "n8n_crm_sync"

func (s *Server) crmHandler(service string) *hanalytics.CRMHandler {
	base := ""
	if s.cfg != nil {
		base = s.cfg.AppPublicURL
	}
	return hanalytics.NewCRM(hanalytics.CRMOptions{
		Pool: s.pgxPool, Audit: s.audit, TimelineBaseURL: base, Logger: s.logger, ServiceLabel: service,
	})
}

// mountAnalyticsAdminRoutes mounts the CRM side for a human superadmin (spec
// 50 section 9): contact codes and the changed profiles. Both need
// superadmin.read; issuing codes also needs X-Admin-Reason and is audited on
// its own transaction. Always mounted: without a database the handler
// answers 503, so the routes stay part of the documented contract (the drift
// test builds a server with no pool).
func (s *Server) mountAnalyticsAdminRoutes(r chi.Router) {
	h := s.crmHandler("")
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "superadmin.read", "analytics")
		pr.Post("/admin/analytics/contact-codes", h.HandleContactCodes)
		pr.Get("/admin/analytics/profiles", h.HandleProfiles)
	})
}

// mountAnalyticsServiceRoutes mounts the SAME two handlers for the n8n CRM
// sync under /v1/service/analytics, behind the static bearer ANALYTICS_SYNC_TOKEN
// instead of a user's JWT (a human token lives 15 minutes and an organization
// API key can never hold superadmin.*). Only these two routes accept that
// token, the audit row says actor type "service" / actor_label n8n_crm_sync,
// and an unset token answers 503, never open.
func (s *Server) mountAnalyticsServiceRoutes(r chi.Router) {
	token := ""
	if s.cfg != nil {
		token = s.cfg.AnalyticsSyncToken
	}
	h := s.crmHandler(analyticsSyncLabel)
	r.Group(func(sr chi.Router) {
		sr.Use(httputil.RequireStaticBearer(token, "analytics.sync_not_configured"))
		sr.Post("/service/analytics/contact-codes", h.HandleContactCodes)
		sr.Get("/service/analytics/profiles", h.HandleProfiles)
	})
}

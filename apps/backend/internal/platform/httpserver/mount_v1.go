package httpserver

import (
	"context"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
)

// mountV1Routes is the thin orchestrator for the /v1 subtree. Each business
// subsystem registers its own routes via a focused mountXxxRoutes(r) helper
// in a sibling mount_*.go file.
func (s *Server) mountV1Routes() {
	s.router.Route("/v1", func(r chi.Router) {
		s.mountInfoRoutes(r)
		s.mountDebugRoutes(r)
		s.mountDevTokenRoutes(r)
		s.mountEchoRoute(r)
		s.mountAuthRoutes(r)
		s.mountMeRoutes(r)
		s.mountGeoRoutes(r)
		s.mountOrgRoutes(r)
		s.mountChannelRoutes(r)
		s.mountPaymentConfigRoutes(r)
		s.mountBankAccountRoutes(r)
		s.mountMembershipRoutes(r)
		s.mountVenueRoutes(r)
		s.mountFeedTokenRoutes(r)
		s.mountEventRoutes(r)
		s.mountSessionRoutes(r)
		s.mountTierRoutes(r)
		s.mountInventoryRoutes(r)
		s.mountReservationRoutes(r)
		s.mountPublicationRoutes(r)
		s.mountPromoRoutes(r)
		s.mountPricingRoutes(r)
		s.mountCheckoutRoutes(r)
		s.mountPaymentIntentRoutes(r)
		s.mountStripeConnectRoutes(r)
		s.mountTicketRoutes(r)
		s.mountCredentialRoutes(r)
		s.mountGDPRRoutes(r)
		s.mountRefundRoutes(r)
		s.mountBarcodeRoutes(r)
		s.mountScannerRoutes(r)
		s.mountScannerCallbackRoutes(r)
		s.mountReportRoutes(r)
		s.mountPublicFeedRoutes(r)
		s.mountBillingRoutes(r)
		s.mountStripeBillingRoutes(r)
		s.mountSuperadminRoutes(r)
		s.mountAdminOrgRoutes(r)
		s.mountAdminTicketDeliveryRoutes(r)
		s.mountAdminMembershipRoutes(r)
		s.mountAdminUserRoutes(r)
		s.mountImpersonationRoutes(r)
		s.mountAllocationRoutes(r)
		s.mountComplimentaryRoutes(r)
		s.mountBarcodeBatchRoutes(r)
		s.mountWebhookSubscriberRoutes(r)
		s.mountReconciliationRoutes(r)
		s.mountOperatorNetworkRoutes(r)
		s.mountMediaRoutes(r)
		s.mountSeatingRoutes(r)
	})
}

// applyAuth adds the canonical auth + permission middleware pair to pr.
// Use inside a r.Group(...) closure to scope the middleware to a sub-tree.
func (s *Server) applyAuth(pr chi.Router, perm, scope string) {
	pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
	pr.Use(permissions.RequirePermission(s.perms, perm, scope))
}

// mountInfoRoutes mounts /v1/info, /v1/server-info, and /v1/info-slow.
func (s *Server) mountInfoRoutes(r chi.Router) {
	r.Get("/info", s.handleInfo)
	// GET /v1/server-info — minimal public endpoint demonstrating the full
	// router → handler → sqlc → response chain (feature #104). No auth required.
	r.Get("/server-info", s.handleServerInfo)
	// /v1/info-slow simulates long-running requests for graceful-shutdown tests.
	r.Get("/info-slow", s.handleInfoSlow)
}

// mountDebugRoutes mounts /v1/debug/* when DEBUG_ROUTES_ENABLED=true.
// MUST NOT be enabled in production.
func (s *Server) mountDebugRoutes(r chi.Router) {
	if !s.debugRoutesEnabled {
		return
	}
	r.Get("/debug/panic", s.handleDebugPanic)
	// GET /v1/debug/slow — sleeps for debugSlowDelay (default 35s) to exercise
	// the per-request timeout (feature #53).
	r.Get("/debug/slow", s.handleDebugSlow)
}

// mountDevTokenRoutes mounts the dev-only JWT mint endpoints when the stub
// auth provider is enabled.
func (s *Server) mountDevTokenRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() {
		return
	}
	// /v1/dev/token — original endpoint using the manual HMAC issuer.
	r.Post("/dev/token", s.handleDevToken)
	// /v1/dev/auth/token — jwt/v5-backed IssueJWT issuer (feature #96).
	r.Post("/dev/auth/token", s.handleDevAuthToken)
}

// mountEchoRoute mounts the JWT-protected, idempotent echo endpoint used as
// the canonical transactional command example.
func (s *Server) mountEchoRoute(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.idem == nil || s.audit == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
		idemOpts := idempotency.Options{
			Scope: "POST /v1/echo",
			TTL:   24 * time.Hour,
			ActorID: func(ctx context.Context) string {
				if a, ok := auth.ActorFromContext(ctx); ok {
					return a.ID
				}
				return ""
			},
		}
		if s.typedMetrics != nil {
			idemOpts.OnReplay = func() {
				s.typedMetrics.IdempotencyReplaysTotal.Inc()
			}
		}
		pr.Use(idempotency.Middleware(s.idem, idemOpts))
		pr.Post("/echo", s.handleEcho)
	})
}

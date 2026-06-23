// Package httpserver wires the chi-based HTTP listener, standard middleware
// chain, and the operational and /v1 endpoints required by the foundation
// milestone.
//
// The server exposes:
//
//   - /healthz, /readyz       — operational probes (liveness + readiness)
//   - /v1/info                — service metadata + real SELECT against PG
//   - /v1/dev/token           — dev-only JWT mint (StubProvider)
//   - /v1/echo                — example transactional command (audit + outbox
//                                + idempotency, JWT-protected)
//
// Additional /v1 routes can be attached by later features through Router().
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	httpadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/http"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/clock"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pinger is the legacy readiness-probe contract kept for backward
// compatibility with database.Pool (IsHealthy / LastError). New code should
// prefer ReadinessProbe; Server wraps a Pinger in pingerProbe automatically.
type Pinger interface {
	IsHealthy() bool
	LastError() string
}

// ReadinessProbe is a named health check included in the /readyz response.
// Each probe corresponds to a single downstream dependency (e.g. "database",
// "redis"). Server iterates all registered probes and aggregates their results
// into the /readyz checks map; if any probe returns a non-nil error the
// response is 503.
type ReadinessProbe interface {
	// ProbeName returns the stable key used in the checks map.
	// Example values: "database", "redis", "outbox".
	ProbeName() string
	// Ping returns nil when the dependency is reachable, or any non-nil
	// error to indicate the dependency is unhealthy.
	Ping(ctx context.Context) error
}

// pingerProbe adapts the legacy Pinger interface to ReadinessProbe so callers
// that pass Options.DB continue to work without changes.
type pingerProbe struct {
	name string
	p    Pinger
}

func (pp *pingerProbe) ProbeName() string { return pp.name }
func (pp *pingerProbe) Ping(_ context.Context) error {
	if pp.p.IsHealthy() {
		return nil
	}
	msg := pp.p.LastError()
	if msg == "" {
		msg = "unhealthy"
	}
	return errors.New(msg)
}

// compile-time guard
var _ ReadinessProbe = (*pingerProbe)(nil)

// PoolDB is the narrow subset of *pgxpool.Pool consumed by /v1 handlers
// (info, echo). Defining it as an interface keeps the package testable —
// unit tests can supply a fake without spinning up PostgreSQL — while the
// production wiring still passes the real *pgxpool.Pool from database.Open.
type PoolDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// Server is the long-lived HTTP listener that hosts the arena-api.
//
// All wired-in dependencies are nilable at construction time so tests can
// build a Server with only the pieces they need (e.g. a fake DB or a
// disabled auth stub). The route mounts guard against missing dependencies
// rather than panicking at startup.
type Server struct {
	cfg     *config.Config
	logger  *slog.Logger
	router  chi.Router
	srv     *http.Server
	probes  []ReadinessProbe
	pool    PoolDB
	stub    *auth.StubProvider
	audit   audit.Writer
	idem    idempotency.Store
	metrics       http.Handler
	typedMetrics  *observability.Metrics
	outboxWriter  outbox.Writer
	perms         permissions.Checker

	// clock provides the wall-clock time used by handleServerInfo (and any
	// future handler that needs deterministic time in tests). Defaults to
	// clock.New() (real system clock) when nil.
	clk clock.Clock

	// siQueries is the sqlc Queries instance used by handleServerInfo to
	// execute SelectServerTime. Nil when no PgxPool was supplied — in that
	// case handleServerInfo falls back to the server's clock.
	siQueries *gen.Queries

	// geoQueries is the sqlc Queries instance used by the geo reference
	// endpoints (GET /v1/geo/countries, GET /v1/geo/cities, and the admin
	// POST/PATCH endpoints). Nil when no PgxPool was supplied.
	geoQueries *gen.Queries

	// orgQueries is the sqlc Queries instance used by the organization CRUD
	// endpoints (POST/GET/PATCH/DELETE /v1/organizations). Nil when no
	// PgxPool was supplied. Feature #119.
	orgQueries *gen.Queries

	// channelQueries is the sqlc Queries instance used by the sales channel
	// CRUD endpoints (POST/GET/PATCH/DELETE /v1/organizations/{org_id}/channels).
	// Nil when no PgxPool was supplied. Feature #121.
	channelQueries *gen.Queries

	// membershipQueries is the sqlc Queries instance used by the membership
	// grant/revoke/list endpoints (POST/GET/DELETE /v1/organizations/{org_id}/members).
	// Nil when no PgxPool was supplied. Feature #120.
	membershipQueries *gen.Queries

	// venueQueries is the sqlc Queries instance used by the venue CRUD endpoints.
	// Read endpoints (GET /v1/venues, GET /v1/venues/{id}) are shared across orgs.
	// Write endpoints (POST/PATCH/DELETE /v1/organizations/{org_id}/venues/*) are
	// gated to the owning org. Nil when no PgxPool was supplied. Feature #124.
	venueQueries *gen.Queries

	// feedTokenQueries is the sqlc Queries instance used by the agent feed token
	// management endpoints and the public feed read endpoint.
	// Nil when no PgxPool was supplied. Feature #122.
	feedTokenQueries *gen.Queries

	// eventQueries is the sqlc Queries instance used by the event CRUD endpoints.
	// Read endpoints (GET /v1/events, GET /v1/events/{id}) are shared across orgs.
	// Write endpoints (POST/PATCH/DELETE /v1/organizations/{org_id}/events/*) are
	// gated to the owning org. Nil when no PgxPool was supplied. Feature #125.
	eventQueries *gen.Queries

	// faultInjectOutboxAfterAudit is a dev/test-only fault injection flag.
	// When true, handleEcho forces a transaction rollback immediately after
	// the audit_events INSERT succeeds, before writing to outbox_events.
	// This proves that both writes are in the same transaction: neither row
	// persists when the fault fires. Enabled by FAULT_INJECT_OUTBOX_AFTER_AUDIT=true.
	faultInjectOutboxAfterAudit bool

	// slowDelay is the artificial sleep used by GET /v1/info-slow to simulate
	// long-running requests for graceful-shutdown testing. Defaults to 5s when
	// zero. Only meaningful in development/test environments.
	slowDelay time.Duration

	// debugRoutesEnabled controls whether the /v1/debug/* routes are mounted.
	// These routes exist solely to facilitate integration tests and developer
	// tooling. In particular, GET /v1/debug/panic intentionally panics to
	// exercise the Recoverer middleware. They MUST NOT be enabled in production.
	// Corresponds to env var DEBUG_ROUTES_ENABLED=true.
	debugRoutesEnabled bool

	// debugSlowDelay is the artificial sleep used by GET /v1/debug/slow to
	// simulate a long-running request for request-timeout testing. Defaults to
	// 35s when zero (longer than the 30s REQUEST_TIMEOUT_SECONDS default so the
	// timeout always fires in the default configuration). Only meaningful in
	// development/test environments and only when debugRoutesEnabled is true.
	debugSlowDelay time.Duration
}

// Options bundles the dependencies that New requires. Using a struct rather
// than positional parameters keeps the constructor stable as more boundaries
// are bolted on by later features (PermissionBoundary, OutboxDispatcher, …).
type Options struct {
	Config *config.Config
	Logger *slog.Logger
	// DB carries the legacy Pinger contract used by /readyz. When non-nil it
	// is wrapped as a "database" ReadinessProbe and prepended to Probes.
	// Prefer Probes for new callers.
	DB Pinger
	// Probes is the ordered list of ReadinessProbe implementations whose
	// results are aggregated into the /readyz response. When empty and DB is
	// also nil, /readyz always returns 200 {checks:{}}.
	Probes []ReadinessProbe
	// Pool is the concrete pgxpool used by /v1 handlers. It is typically
	// the same *database.Pool passed as DB (database.Pool embeds *pgxpool.Pool
	// and exposes both contracts).
	Pool PoolDB
	// Auth is the dev-stub JWT provider. Pass nil to disable /v1/echo and
	// /v1/dev/token entirely.
	Auth *auth.StubProvider
	// Audit is the AuditWriter implementation. Defaults to a Postgres
	// writer constructed from a *pgxpool.Pool when Audit is nil and
	// PgxPool is non-nil.
	Audit audit.Writer
	// Idem is the idempotency Store implementation. Defaults to a Postgres
	// store constructed from PgxPool when Idem is nil and PgxPool is non-nil.
	Idem idempotency.Store
	// PgxPool is the concrete pool used to lazily construct PG-backed
	// Audit and Idem writers when those fields are not supplied. Optional.
	PgxPool *pgxpool.Pool
	// MetricsHandler is the Prometheus scrape handler exposed at /metrics.
	// When nil, the /metrics route is not mounted — useful for tests and for
	// deployments where metrics are scraped from a sidecar instead.
	MetricsHandler http.Handler
	// Metrics is the typed *observability.Metrics whose HTTP histogram +
	// counter back the prometheusMiddleware in the adapter chain. When nil
	// the middleware is omitted, so unit tests that don't care about
	// metrics can leave this unset without polluting a shared registry.
	Metrics *observability.Metrics
	// FaultInjectOutboxAfterAudit enables fault injection for transaction
	// atomicity testing. When true, handleEcho rolls back the transaction
	// after writing the audit_events row and before writing outbox_events,
	// returning 500 with code='internal.transaction_failed'. This proves
	// that both rows are in the same transaction (neither persists on fault).
	// Only meaningful in development/test environments.
	// Corresponds to env var FAULT_INJECT_OUTBOX_AFTER_AUDIT=true.
	FaultInjectOutboxAfterAudit bool

	// SlowDelay overrides the sleep duration used by GET /v1/info-slow.
	// Defaults to 5s when zero. Set to a small value in tests so graceful-
	// shutdown assertions complete quickly. Only meaningful in development/test.
	SlowDelay time.Duration

	// DebugRoutesEnabled mounts the /v1/debug/* routes when true. These routes
	// exist for integration tests and developer tooling. In particular,
	// GET /v1/debug/panic intentionally panics to exercise the Recoverer
	// middleware. MUST NOT be enabled in production.
	// Corresponds to env var DEBUG_ROUTES_ENABLED=true.
	DebugRoutesEnabled bool

	// DebugSlowDelay overrides the sleep duration used by GET /v1/debug/slow.
	// Defaults to 35s when zero (longer than the default 30s request timeout so
	// the timeout always fires in the default configuration). Set to a small value
	// in tests so request-timeout assertions complete quickly. Only meaningful in
	// development/test environments when DebugRoutesEnabled=true.
	DebugSlowDelay time.Duration

	// Bundle is the go-i18n/v2 message catalog bundle used by LocaleMiddleware
	// to localize error messages. When non-nil, LocaleMiddleware is added to
	// the middleware chain (after requestContext, before route handlers) so that
	// every request carries a locale-aware Localizer in its context.
	// When nil, locale negotiation still occurs (for the active_locale response
	// field in /v1/info) but error messages fall back to hardcoded English strings.
	Bundle *i18n.Bundle

	// Outbox is the outbox.Writer used by POST /v1/scaffold/echo to append
	// domain events within the same transaction as the scaffold_echo INSERT.
	// When nil and PgxPool is non-nil, a PGWriter is constructed lazily.
	Outbox outbox.Writer

	// Permissions is the permissions.Checker used by POST /v1/scaffold/echo.
	// When nil, AllowAllChecker is used (foundation milestone placeholder).
	Permissions permissions.Checker

	// Clock overrides the time source used by handleServerInfo. When nil,
	// clock.New() (real system clock) is used. Inject clock.NewFake in tests
	// to make time deterministic.
	Clock clock.Clock

	// GeoQueries injects a pre-constructed *gen.Queries for the geo reference
	// endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject an explicit value in tests that need geo routes mounted without a
	// real *pgxpool.Pool (e.g. passing gen.New(nil) to exercise auth guards).
	GeoQueries *gen.Queries

	// OrgQueries injects a pre-constructed *gen.Queries for the organization
	// CRUD endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need org routes mounted without a real pool.
	// Feature #119.
	OrgQueries *gen.Queries

	// ChannelQueries injects a pre-constructed *gen.Queries for the sales channel
	// CRUD endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need channel routes mounted without a real pool.
	// Feature #121.
	ChannelQueries *gen.Queries

	// MembershipQueries injects a pre-constructed *gen.Queries for the membership
	// grant/revoke/list endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need membership routes mounted without a real pool.
	// Feature #120.
	MembershipQueries *gen.Queries

	// VenueQueries injects a pre-constructed *gen.Queries for the venue CRUD endpoints.
	// When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need venue routes mounted without a real pool.
	// Feature #124.
	VenueQueries *gen.Queries

	// FeedTokenQueries injects a pre-constructed *gen.Queries for the agent feed
	// token management endpoints and the public feed read endpoint.
	// When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need feed token routes without a real pool.
	// Feature #122.
	FeedTokenQueries *gen.Queries

	// EventQueries injects a pre-constructed *gen.Queries for the event CRUD
	// endpoints. When nil and PgxPool is non-nil, gen.New(PgxPool) is used.
	// Inject gen.New(nil) in tests that need event routes mounted without a real pool.
	// Feature #125.
	EventQueries *gen.Queries
}

// New constructs (but does not start) the HTTP server.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Build the chi router via the adapter so the canonical middleware
	// chain (panicRecoverer → RealIP → RequestID → requestContext → logger →
	// prometheus → tracer → Timeout → bodyLimit) is applied uniformly
	// across every arena_new HTTP listener. The Server is responsible only
	// for the lifecycle (http.Server, listen, graceful shutdown) and for
	// mounting the operational + /v1 routes on the returned router.
	r := httpadapter.NewRouter(httpadapter.Deps{
		Logger:         logger,
		RequestTimeout: opts.Config.RequestTimeout,
		BodyLimitBytes: opts.Config.BodyLimitBytes,
		Metrics:        opts.Metrics,
		AppEnv:         string(opts.Config.AppEnv),
	})

	// Wire locale middleware when a Bundle is provided. The middleware runs
	// inside the existing chi router after all cross-cutting middlewares
	// (request_id, trace_id, logger) so the locale-negotiated Localizer is
	// available to every handler via i18n.Localize(r.Context(), ...).
	if opts.Bundle != nil {
		defaultLocale := ""
		var supported []string
		if opts.Config != nil {
			defaultLocale = opts.Config.DefaultLocale
			supported = opts.Config.ActiveLocales
		}
		r.Use(i18n.LocaleMiddleware(opts.Bundle, defaultLocale, supported))
	}

	// Lazily construct PG-backed audit + idempotency + outbox stores when
	// the caller didn't supply concrete implementations.
	auditWriter := opts.Audit
	if auditWriter == nil && opts.PgxPool != nil {
		auditWriter = audit.NewPGWriter(opts.PgxPool)
	}
	idemStore := opts.Idem
	if idemStore == nil && opts.PgxPool != nil {
		idemStore = idempotency.NewPGStore(opts.PgxPool)
	}
	outboxWriter := opts.Outbox
	if outboxWriter == nil && opts.PgxPool != nil {
		outboxWriter = outbox.NewPGWriter(opts.PgxPool)
	}
	permsChecker := opts.Permissions
	if permsChecker == nil && opts.PgxPool != nil {
		// Wire the real RBAC engine when a PgxPool is available (feature #117).
		// The DBChecker reads roles/permissions/role_permissions created by
		// migration 0008_rbac and resolves permissions from the actor's JWT roles.
		permsChecker = permissions.NewDBChecker(gen.New(opts.PgxPool))
	} else if permsChecker == nil {
		// No pool → fall back to AllowAll for dev/test environments that run
		// without a database (e.g. unit tests with fake pool adapters).
		permsChecker = permissions.AllowAll()
	}

	// Clock defaults to the real system clock.
	clk := opts.Clock
	if clk == nil {
		clk = clock.New()
	}

	// sqlc Queries for GET /v1/server-info — constructed from PgxPool when available.
	var siQueries *gen.Queries
	if opts.PgxPool != nil {
		siQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for geo reference endpoints.
	// Prefer the explicitly injected value (opts.GeoQueries) so tests can wire
	// a gen.New(nil) to exercise auth guards without a real *pgxpool.Pool.
	geoQueries := opts.GeoQueries
	if geoQueries == nil && opts.PgxPool != nil {
		geoQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for organization CRUD endpoints (feature #119).
	orgQueries := opts.OrgQueries
	if orgQueries == nil && opts.PgxPool != nil {
		orgQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for sales channel CRUD endpoints (feature #121).
	channelQueries := opts.ChannelQueries
	if channelQueries == nil && opts.PgxPool != nil {
		channelQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for membership grant/revoke/list endpoints (feature #120).
	membershipQueries := opts.MembershipQueries
	if membershipQueries == nil && opts.PgxPool != nil {
		membershipQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for venue CRUD endpoints (feature #124).
	venueQueries := opts.VenueQueries
	if venueQueries == nil && opts.PgxPool != nil {
		venueQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for agent feed token management endpoints (feature #122).
	feedTokenQueries := opts.FeedTokenQueries
	if feedTokenQueries == nil && opts.PgxPool != nil {
		feedTokenQueries = gen.New(opts.PgxPool)
	}

	// sqlc Queries for event CRUD endpoints (feature #125).
	eventQueries := opts.EventQueries
	if eventQueries == nil && opts.PgxPool != nil {
		eventQueries = gen.New(opts.PgxPool)
	}

	// Extend the permission checker with membership-derived role resolution
	// (feature #120 step 3). When a PgxPool is available, the DBChecker is
	// augmented so that each Check() call unions the JWT roles with the user's
	// active membership roles fetched fresh from the DB. This makes grant/revoke
	// operations take effect on the next request without a new JWT.
	if dbChecker, ok := permsChecker.(*permissions.DBChecker); ok && opts.PgxPool != nil {
		permsChecker = dbChecker.WithMembershipQuerier(gen.New(opts.PgxPool))
	}

	// Assemble the readiness probe list.
	// If the legacy DB Pinger is set, prepend it as a "database" probe so
	// existing callers (main.go, integration tests) continue to work without
	// any changes at the call site.
	probes := make([]ReadinessProbe, 0, 1+len(opts.Probes))
	if opts.DB != nil {
		probes = append(probes, &pingerProbe{name: "database", p: opts.DB})
	}
	probes = append(probes, opts.Probes...)

	s := &Server{
		cfg:          opts.Config,
		logger:       logger,
		router:       r,
		probes:       probes,
		pool:         opts.Pool,
		stub:         opts.Auth,
		audit:        auditWriter,
		idem:         idemStore,
		metrics:      opts.MetricsHandler,
		typedMetrics: opts.Metrics,
		outboxWriter: outboxWriter,
		perms:        permsChecker,
		clk:          clk,
		siQueries:    siQueries,

		faultInjectOutboxAfterAudit: opts.FaultInjectOutboxAfterAudit,
		slowDelay:                   opts.SlowDelay,
		debugRoutesEnabled:          opts.DebugRoutesEnabled,
		debugSlowDelay:              opts.DebugSlowDelay,
		geoQueries:                  geoQueries,
		orgQueries:                  orgQueries,
		channelQueries:              channelQueries,
		membershipQueries:           membershipQueries,
		venueQueries:                venueQueries,
		feedTokenQueries:            feedTokenQueries,
		eventQueries:                eventQueries,
	}

	s.mountOperationalRoutes()
	s.mountV1Routes()

	s.srv = &http.Server{
		Addr:              opts.Config.HTTPListenAddr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       opts.Config.RequestTimeout + 5*time.Second,
		WriteTimeout:      opts.Config.RequestTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	return s
}

// Router exposes the underlying chi router so additional routes can be
// attached by later features.
func (s *Server) Router() chi.Router { return s.router }

// ListenAndServe starts the listener. Blocks until the underlying http.Server
// returns. http.ErrServerClosed signals a clean shutdown and should be
// treated as a non-error by the caller.
func (s *Server) ListenAndServe() error {
	s.logger.Info("http server listening", "addr", s.cfg.HTTPListenAddr)
	return s.srv.ListenAndServe()
}

// Shutdown attempts a graceful shutdown bounded by ctx.
// It logs "shutdown initiated" before stopping the listener and
// "shutdown complete" once all in-flight requests have drained.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("shutdown initiated")
	err := s.srv.Shutdown(ctx)
	s.logger.Info("shutdown complete")
	return err
}

// -----------------------------------------------------------------------------
// route mounts
// -----------------------------------------------------------------------------

func (s *Server) mountOperationalRoutes() {
	s.router.Get("/healthz", s.handleHealthz)
	s.router.Get("/readyz", s.handleReadyz)
	// /metrics is only mounted when the caller supplies a handler. The
	// scrape endpoint is intentionally unauthenticated for the foundation
	// milestone — Dokploy's reverse proxy enforces network-level
	// restriction (only the Prometheus scraper VLAN reaches it).
	if s.metrics != nil {
		s.router.Method(http.MethodGet, "/metrics", s.metrics)
	}
	// Custom 404 handler: returns the standard JSON error envelope instead of
	// chi's default plain-text "404 page not found\n" response. Every unknown
	// path therefore still carries Content-Type: application/json, X-Request-Id,
	// and the structured error body that clients can parse uniformly.
	s.router.NotFound(handleNotFound)
	// Custom 405 handler: when the path is known but the HTTP method is not
	// supported, chi populates the Allow response header (listing the methods
	// that ARE registered) and then calls this handler.  We keep the Allow
	// header intact and wrap the 405 in the standard JSON error envelope so
	// clients receive a parseable, machine-readable error body (feature #13).
	s.router.MethodNotAllowed(handleMethodNotAllowed)
}

func (s *Server) mountV1Routes() {
	s.router.Route("/v1", func(r chi.Router) {
		// Anonymous (or authenticated) routes
		r.Get("/info", s.handleInfo)
		// GET /v1/server-info — minimal public endpoint demonstrating the full
		// router → handler → sqlc → response chain (feature #104). No auth required.
		r.Get("/server-info", s.handleServerInfo)

		// Debug routes — only mounted when DEBUG_ROUTES_ENABLED=true. These
		// routes exist solely for integration tests and developer tooling; they
		// MUST NOT be enabled in production. The panic endpoint intentionally
		// triggers a panic to exercise the Recoverer middleware.
		if s.debugRoutesEnabled {
			r.Get("/debug/panic", s.handleDebugPanic)
			// GET /v1/debug/slow — sleeps for debugSlowDelay (default 35s) to
			// exercise the per-request timeout (feature #53). Returns 503 with
			// code='http.request_timeout' when the context deadline fires.
			r.Get("/debug/slow", s.handleDebugSlow)
		}

		// /v1/info-slow is a synthetic endpoint used to test graceful shutdown.
		// It sleeps for slowDelay (default 5s) before responding so integration
		// tests can verify that in-flight requests complete when SIGTERM is sent.
		// This endpoint is always mounted (not guarded by stub auth) because it
		// must be reachable without credentials during graceful-shutdown tests.
		r.Get("/info-slow", s.handleInfoSlow)

		// Dev-only token mint endpoints — only mounted when the stub provider is on.
		if s.stub != nil && s.stub.Enabled() {
			// /v1/dev/token — original endpoint using the manual HMAC issuer (StubProvider).
			r.Post("/dev/token", s.handleDevToken)
			// /v1/dev/auth/token — new endpoint using the jwt/v5-backed IssueJWT issuer
			// (AuthContext boundary placeholder, feature #96).
			r.Post("/dev/auth/token", s.handleDevAuthToken)
		}

		// Authenticated transactional routes (echo). Requires:
		//   - stub auth enabled (real IdP not in scope this milestone)
		//   - idempotency store wired
		//   - audit writer wired
		//   - pgx pool wired (echo writes audit + outbox in a single tx)
		if s.stub != nil && s.stub.Enabled() && s.idem != nil && s.audit != nil && s.pool != nil {
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

		// POST /v1/auth/register              — public registration endpoint (feature #114).
		// GET  /v1/auth/verify                — email verification (feature #114).
		// POST /v1/auth/login                 — email+password → JWT + refresh token (feature #115).
		// POST /v1/auth/refresh               — refresh token → new JWT access token (feature #115).
		// POST /v1/auth/password-reset/request  — request a password-reset link (feature #116).
		// POST /v1/auth/password-reset/confirm  — confirm reset with token + new password (feature #116).
		// All require the pool to be wired; no auth header needed (they are
		// intentionally public endpoints used before or during authentication).
		if s.pool != nil {
			r.Post("/auth/register", s.handleAuthRegister)
			r.Get("/auth/verify", s.handleAuthVerifyEmail)
			r.Post("/auth/login", s.handleAuthLogin)
			r.Post("/auth/refresh", s.handleAuthRefresh)
			r.Post("/auth/password-reset/request", s.handleAuthPasswordResetRequest)
			r.Post("/auth/password-reset/confirm", s.handleAuthPasswordResetConfirm)
		}

		// ── Geo reference data (feature #123) ──────────────────────────────────
		//
		// Public read routes: no authentication required.
		//   GET /v1/geo/countries — list all countries with localized names
		//   GET /v1/geo/cities   — list cities (optional ?country_id= filter)
		//
		// Admin write routes: mounted only when stub auth + pool are available.
		//   POST  /v1/admin/geo/countries
		//   PATCH /v1/admin/geo/countries/{iso2}
		//   POST  /v1/admin/geo/cities
		//   PATCH /v1/admin/geo/cities/{id}
		if s.geoQueries != nil {
			r.Get("/geo/countries", s.handleListCountries)
			r.Get("/geo/cities", s.handleListCities)
		}
		if s.stub != nil && s.stub.Enabled() && s.geoQueries != nil && s.pool != nil {
			r.Route("/admin/geo", func(ar chi.Router) {
				ar.Group(func(pr chi.Router) {
					pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
					pr.Use(permissions.RequirePermission(s.perms, "geo.admin", "geo"))
					pr.Post("/countries", s.handleCreateCountry)
					pr.Patch("/countries/{iso2}", s.handleUpdateCountry)
					pr.Post("/cities", s.handleCreateCity)
					pr.Patch("/cities/{id}", s.handleUpdateCity)
				})
			})
		}

		// ── Organizations (feature #119) ──────────────────────────────────────
		//
		// All org endpoints require JWT auth. Write endpoints require specific
		// permissions. Read endpoints require "org.read" to keep the org
		// registry non-enumerable without authentication.
		//
		//   POST   /v1/organizations        — create (org.create)
		//   GET    /v1/organizations        — list   (org.read)
		//   GET    /v1/organizations/{id}   — get    (org.read)
		//   PATCH  /v1/organizations/{id}   — update (org.update)
		//   DELETE /v1/organizations/{id}   — delete (org.delete)
		//
		// Routes are registered directly (not via r.Route) to avoid trailing-slash
		// path canonicalization by chi. Each permission is enforced in a separate
		// group so GET and POST on the same base path can carry different permissions.
		if s.stub != nil && s.stub.Enabled() && s.orgQueries != nil && s.pool != nil {
			// GET /v1/organizations and GET /v1/organizations/{id} (org.read)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "org.read", "organizations"))
				pr.Get("/organizations", s.handleListOrgs)
				pr.Get("/organizations/{id}", s.handleGetOrg)
			})
			// POST /v1/organizations (org.create)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "org.create", "organizations"))
				pr.Post("/organizations", s.handleCreateOrg)
			})
			// PATCH /v1/organizations/{id} (org.update)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "org.update", "organizations"))
				pr.Patch("/organizations/{id}", s.handleUpdateOrg)
			})
			// DELETE /v1/organizations/{id} (org.delete)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "org.delete", "organizations"))
				pr.Delete("/organizations/{id}", s.handleDeleteOrg)
			})
		}

		// ── Sales Channels (feature #121) ────────────────────────────────────
		//
		// All channel endpoints require JWT auth + a named permission.
		// Routes are nested under /v1/organizations/{org_id}/channels so the
		// org scope is enforced at the path level and in every query.
		//
		//   POST   /v1/organizations/{org_id}/channels        — create (channel.create)
		//   GET    /v1/organizations/{org_id}/channels        — list   (channel.read)
		//   GET    /v1/organizations/{org_id}/channels/{id}   — get    (channel.read)
		//   PATCH  /v1/organizations/{org_id}/channels/{id}   — update (channel.update)
		//   DELETE /v1/organizations/{org_id}/channels/{id}   — delete (channel.delete)
		if s.stub != nil && s.stub.Enabled() && s.channelQueries != nil && s.pool != nil {
			// GET /v1/organizations/{org_id}/channels and GET …/{id} (channel.read)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "channel.read", "channels"))
				pr.Get("/organizations/{org_id}/channels", s.handleListChannels)
				pr.Get("/organizations/{org_id}/channels/{id}", s.handleGetChannel)
			})
			// POST /v1/organizations/{org_id}/channels (channel.create)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "channel.create", "channels"))
				pr.Post("/organizations/{org_id}/channels", s.handleCreateChannel)
			})
			// PATCH /v1/organizations/{org_id}/channels/{id} (channel.update)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "channel.update", "channels"))
				pr.Patch("/organizations/{org_id}/channels/{id}", s.handleUpdateChannel)
			})
			// DELETE /v1/organizations/{org_id}/channels/{id} (channel.delete)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "channel.delete", "channels"))
				pr.Delete("/organizations/{org_id}/channels/{id}", s.handleDeleteChannel)
			})
		}

		// ── Memberships (feature #120) ──────────────────────────────────────
		//
		// All membership endpoints require JWT auth + a named permission.
		// Routes are nested under /v1/organizations/{org_id}/members.
		//
		//   POST   /v1/organizations/{org_id}/members           — grant (membership.grant)
		//   GET    /v1/organizations/{org_id}/members           — list  (membership.read)
		//   DELETE /v1/organizations/{org_id}/members/{user_id} — revoke (membership.revoke)
		if s.stub != nil && s.stub.Enabled() && s.membershipQueries != nil && s.pool != nil {
			// GET /v1/organizations/{org_id}/members (membership.read)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "membership.read", "memberships"))
				pr.Get("/organizations/{org_id}/members", s.handleListMembers)
			})
			// POST /v1/organizations/{org_id}/members (membership.grant)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "membership.grant", "memberships"))
				pr.Post("/organizations/{org_id}/members", s.handleGrantMembership)
			})
			// DELETE /v1/organizations/{org_id}/members/{user_id} (membership.revoke)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "membership.revoke", "memberships"))
				pr.Delete("/organizations/{org_id}/members/{user_id}", s.handleRevokeMembership)
			})
		}

		// ── Venues (feature #124) ────────────────────────────────────────────
		//
		// Read endpoints are shared across all organizations (any authenticated
		// user with venue.read can list/get any active venue). Write endpoints
		// are owner-gated: the org_id in the path must match the venue's owning org.
		//
		//   POST   /v1/organizations/{org_id}/venues        — create (venue.create)
		//   GET    /v1/venues                               — list all (venue.read, shared)
		//   GET    /v1/venues/{id}                          — get by ID (venue.read, shared)
		//   GET    /v1/organizations/{org_id}/venues        — list by org (venue.read)
		//   PATCH  /v1/organizations/{org_id}/venues/{id}   — update (venue.update, owner only)
		//   DELETE /v1/organizations/{org_id}/venues/{id}   — soft-delete (venue.delete, owner only)
		if s.stub != nil && s.stub.Enabled() && s.venueQueries != nil {
			// Shared read routes (venue.read) — any org can read any venue.
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "venue.read", "venues"))
				pr.Get("/venues", s.handleListVenues)
				pr.Get("/venues/{id}", s.handleGetVenue)
				pr.Get("/organizations/{org_id}/venues", s.handleListVenuesByOrg)
			})
		}
		if s.stub != nil && s.stub.Enabled() && s.venueQueries != nil && s.pool != nil {
			// POST /v1/organizations/{org_id}/venues (venue.create)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "venue.create", "venues"))
				pr.Post("/organizations/{org_id}/venues", s.handleCreateVenue)
			})
			// PATCH /v1/organizations/{org_id}/venues/{id} (venue.update)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "venue.update", "venues"))
				pr.Patch("/organizations/{org_id}/venues/{id}", s.handleUpdateVenue)
			})
			// DELETE /v1/organizations/{org_id}/venues/{id} (venue.delete)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "venue.delete", "venues"))
				pr.Delete("/organizations/{org_id}/venues/{id}", s.handleDeleteVenue)
			})
		}

		// ── Agent Feed Tokens (feature #122) ────────────────────────────────
		//
		// Management endpoints require JWT auth + a named permission.
		// Routes are nested under /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens.
		//
		//   POST   .../feed-tokens        — issue token (feed_token.create)
		//   GET    .../feed-tokens        — list tokens (feed_token.read)
		//   GET    .../feed-tokens/{id}   — get single  (feed_token.read)
		//   DELETE .../feed-tokens/{id}   — revoke token (feed_token.delete)
		//
		// Public feed read endpoint (no JWT required):
		//   GET /v1/feeds/{token} — validates token, updates last_used_at
		if s.feedTokenQueries != nil {
			// Public feed read (no auth — token in path is the credential).
			r.Get("/feeds/{token}", s.handlePublicFeed)
		}
		if s.stub != nil && s.stub.Enabled() && s.feedTokenQueries != nil {
			// GET .../feed-tokens and GET .../feed-tokens/{id} (feed_token.read)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "feed_token.read", "feed_tokens"))
				pr.Get("/organizations/{org_id}/channels/{channel_id}/feed-tokens", s.handleListFeedTokens)
				pr.Get("/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}", s.handleGetFeedToken)
			})
		}
		if s.stub != nil && s.stub.Enabled() && s.feedTokenQueries != nil && s.pool != nil {
			// POST .../feed-tokens (feed_token.create)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "feed_token.create", "feed_tokens"))
				pr.Post("/organizations/{org_id}/channels/{channel_id}/feed-tokens", s.handleCreateFeedToken)
			})
			// DELETE .../feed-tokens/{id} (feed_token.delete)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "feed_token.delete", "feed_tokens"))
				pr.Delete("/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}", s.handleRevokeFeedToken)
			})
		}

		// ── Events (feature #125) ────────────────────────────────────────────
		//
		// Read endpoints are shared across all organizations (any authenticated
		// user with event.read can list/get any active event). Write endpoints
		// are owner-gated: the org_id in the path must match the event's owning org.
		//
		//   POST   /v1/organizations/{org_id}/events            — create (event.create)
		//   GET    /v1/events                                   — list public events (event.read, shared)
		//   GET    /v1/events/{id}                              — get by ID (event.read, shared)
		//   GET    /v1/organizations/{org_id}/events            — list by org (event.read)
		//   PATCH  /v1/organizations/{org_id}/events/{id}       — update (event.update, owner only)
		//   POST   /v1/organizations/{org_id}/events/{id}/status — status transition (event.publish)
		//   DELETE /v1/organizations/{org_id}/events/{id}       — soft-delete (event.delete, owner only)
		if s.stub != nil && s.stub.Enabled() && s.eventQueries != nil {
			// Shared read routes (event.read) — any org can read any active event.
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "event.read", "events"))
				pr.Get("/events", s.handleListEvents)
				pr.Get("/events/{id}", s.handleGetEvent)
				pr.Get("/organizations/{org_id}/events", s.handleListEventsByOrg)
			})
		}
		if s.stub != nil && s.stub.Enabled() && s.eventQueries != nil && s.pool != nil {
			// POST /v1/organizations/{org_id}/events (event.create)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "event.create", "events"))
				pr.Post("/organizations/{org_id}/events", s.handleCreateEvent)
			})
			// PATCH /v1/organizations/{org_id}/events/{id} (event.update)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "event.update", "events"))
				pr.Patch("/organizations/{org_id}/events/{id}", s.handleUpdateEvent)
			})
			// POST /v1/organizations/{org_id}/events/{id}/status (event.publish)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "event.publish", "events"))
				pr.Post("/organizations/{org_id}/events/{id}/status", s.handleUpdateEventStatus)
			})
			// DELETE /v1/organizations/{org_id}/events/{id} (event.delete)
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
				pr.Use(permissions.RequirePermission(s.perms, "event.delete", "events"))
				pr.Delete("/organizations/{org_id}/events/{id}", s.handleDeleteEvent)
			})
		}

		// POST /v1/scaffold/echo — scaffolding example demonstrating the full
		// cross-cutting boundary stack:
		//   auth → permission('scaffold.echo.create') → idempotency →
		//   BEGIN tx → InsertScaffoldEcho → audit → outbox → COMMIT → 201
		//
		// Mounted when all four dependencies are wired (pool, audit, idem, outbox).
		// This endpoint is a scaffolding example and will be removed when real
		// domain command endpoints arrive.
		if s.stub != nil && s.stub.Enabled() && s.idem != nil && s.audit != nil && s.pool != nil && s.outboxWriter != nil {
			r.Route("/scaffold", func(sr chi.Router) {
				sr.Group(func(pr chi.Router) {
					pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
					pr.Use(permissions.RequirePermission(s.perms, "scaffold.echo.create", "scaffold_echo"))
					scaffoldIdemOpts := idempotency.Options{
						Scope: "POST /v1/scaffold/echo",
						TTL:   24 * time.Hour,
						ActorID: func(ctx context.Context) string {
							if a, ok := auth.ActorFromContext(ctx); ok {
								return a.ID
							}
							return ""
						},
					}
					if s.typedMetrics != nil {
						scaffoldIdemOpts.OnReplay = func() {
							s.typedMetrics.IdempotencyReplaysTotal.Inc()
						}
					}
					pr.Use(idempotency.Middleware(s.idem, scaffoldIdemOpts))
					pr.Post("/echo", s.handleScaffoldEcho)
				})
			})
		}
	})
}

// handleNotFound is the chi NotFound handler. It replaces chi's built-in
// plain-text "404 page not found\n" response with the project-standard JSON
// error envelope (feature #12). The handler is invoked after the full
// middleware chain, so X-Request-Id, X-Trace-Id, and the locale-aware
// Localizer (when LocaleMiddleware is wired) are already present in ctx.
func handleNotFound(w http.ResponseWriter, r *http.Request) {
	msg := i18n.Localize(r.Context(), "error.not_found",
		"the requested resource does not exist", nil)
	writeJSON(w, http.StatusNotFound, errorEnvelope("http.not_found", msg, r))
}

// handleMethodNotAllowed is the chi MethodNotAllowed handler. It replaces
// chi's default plain-text 405 response with the project-standard JSON error
// envelope (feature #13).
//
// chi v5 does NOT set the Allow header when a custom MethodNotAllowed handler
// is registered (the default handler sets Allow, but a custom handler bypasses
// that code path). We therefore build the Allow header ourselves by probing
// the chi Routes interface stored in the current RouteContext: for each
// candidate HTTP method, we ask Routes.Match whether it would route that
// method on the same path. Matched methods are joined into the Allow value.
//
// Standard candidates for Allow probing (per RFC 9110 §9):
//
//	GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS
//
// HEAD is always included alongside GET when GET is matched because go's
// net/http automatically handles HEAD on any GET route (RFC requirement).
func handleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	// Probe the chi router for methods that ARE allowed on this path.
	// chi.RouteContext(r.Context()).Routes is the live chi.Routes that matched
	// this request; calling Match on a fresh Context is non-destructive.
	rctx := chi.RouteContext(r.Context())
	if rctx != nil && rctx.Routes != nil {
		candidates := []string{
			http.MethodGet,
			http.MethodHead,
			http.MethodPost,
			http.MethodPut,
			http.MethodPatch,
			http.MethodDelete,
			http.MethodOptions,
		}
		var allowed []string
		for _, m := range candidates {
			if m == r.Method {
				// skip the method that was just rejected
				continue
			}
			testCtx := chi.NewRouteContext()
			if rctx.Routes.Match(testCtx, m, r.URL.Path) {
				allowed = append(allowed, m)
			}
		}
		if len(allowed) > 0 {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
		}
	}
	msg := i18n.Localize(r.Context(), "http.method_not_allowed",
		"method not allowed", nil)
	writeJSON(w, http.StatusMethodNotAllowed, errorEnvelope("http.method_not_allowed", msg, r))
}

// handleHealthz is a liveness probe: returns 200 unconditionally while the
// process is alive and able to serve HTTP.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// handleReadyz is a readiness probe: iterates through all registered
// ReadinessProbes and aggregates their results into the /readyz checks map.
// Returns 200 {"status":"ready","checks":{...}} when every probe passes, or
// 503 {"status":"not_ready","checks":{...}} when any probe fails.
// When no probes are registered the server is always considered ready (useful
// during integration tests that wire no dependencies).
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	checks := make(map[string]string, len(s.probes))
	failed := false
	for _, p := range s.probes {
		if err := p.Ping(ctx); err != nil {
			checks[p.ProbeName()] = err.Error()
			failed = true
		} else {
			checks[p.ProbeName()] = "ok"
		}
	}
	if failed {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "not_ready",
			"checks": checks,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ready",
		"checks": checks,
	})
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

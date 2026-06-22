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
	"time"

	httpadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/http"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
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
	metrics http.Handler
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
}

// New constructs (but does not start) the HTTP server.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Build the chi router via the adapter so the canonical middleware
	// chain (Recoverer → RealIP → RequestID → requestContext → logger →
	// prometheus → tracer → Timeout → bodyLimit) is applied uniformly
	// across every arena_new HTTP listener. The Server is responsible only
	// for the lifecycle (http.Server, listen, graceful shutdown) and for
	// mounting the operational + /v1 routes on the returned router.
	r := httpadapter.NewRouter(httpadapter.Deps{
		Logger:         logger,
		RequestTimeout: opts.Config.RequestTimeout,
		BodyLimitBytes: opts.Config.BodyLimitBytes,
		Metrics:        opts.Metrics,
	})

	// Lazily construct PG-backed audit + idempotency stores when the caller
	// didn't supply concrete implementations.
	auditWriter := opts.Audit
	if auditWriter == nil && opts.PgxPool != nil {
		auditWriter = audit.NewPGWriter(opts.PgxPool)
	}
	idemStore := opts.Idem
	if idemStore == nil && opts.PgxPool != nil {
		idemStore = idempotency.NewPGStore(opts.PgxPool)
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
		cfg:     opts.Config,
		logger:  logger,
		router:  r,
		probes:  probes,
		pool:    opts.Pool,
		stub:    opts.Auth,
		audit:   auditWriter,
		idem:    idemStore,
		metrics: opts.MetricsHandler,
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
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("http server shutting down")
	return s.srv.Shutdown(ctx)
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
}

func (s *Server) mountV1Routes() {
	s.router.Route("/v1", func(r chi.Router) {
		// Anonymous (or authenticated) routes
		r.Get("/info", s.handleInfo)

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
				pr.Use(idempotency.Middleware(s.idem, idempotency.Options{
					Scope: "POST /v1/echo",
					TTL:   24 * time.Hour,
					ActorID: func(ctx context.Context) string {
						if a, ok := auth.ActorFromContext(ctx); ok {
							return a.ID
						}
						return ""
					},
				}))
				pr.Post("/echo", s.handleEcho)
			})
		}
	})
}

// handleNotFound is the chi NotFound handler. It replaces chi's built-in
// plain-text "404 page not found\n" response with the project-standard JSON
// error envelope (feature #12). The handler is invoked after the full
// middleware chain, so X-Request-Id and X-Trace-Id are already present in
// the response headers when errorEnvelope reads them from ctx.
func handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, errorEnvelope("http.not_found", "the requested resource does not exist", r))
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

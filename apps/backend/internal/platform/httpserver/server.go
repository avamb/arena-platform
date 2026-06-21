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
	"log/slog"
	"net/http"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pinger is the contract required by the readiness probe — anything that can
// answer "is my dependency healthy right now". The database pool implements
// this directly.
type Pinger interface {
	IsHealthy() bool
	LastError() string
}

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
	router  *chi.Mux
	srv     *http.Server
	db      Pinger
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
	// DB carries the readiness Pinger contract used by /readyz.
	DB Pinger
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
}

// New constructs (but does not start) the HTTP server.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	r := chi.NewRouter()

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

	s := &Server{
		cfg:     opts.Config,
		logger:  logger,
		router:  r,
		db:      opts.DB,
		pool:    opts.Pool,
		stub:    opts.Auth,
		audit:   auditWriter,
		idem:    idemStore,
		metrics: opts.MetricsHandler,
	}

	// Standard middleware chain. RequestID is first so chimw's per-request id
	// is available to every downstream piece. requestContext copies it onto
	// ctx via logging.WithRequestID. traceContext attaches a new trace_id and
	// emits the request-start/end log pair.
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(requestContext)
	r.Use(traceContext)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Timeout(opts.Config.RequestTimeout))
	r.Use(jsonBodyLimit(opts.Config.BodyLimitBytes))

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
}

func (s *Server) mountV1Routes() {
	s.router.Route("/v1", func(r chi.Router) {
		// Anonymous (or authenticated) routes
		r.Get("/info", s.handleInfo)

		// Dev-only token mint — only mounted when the stub provider is on.
		if s.stub != nil && s.stub.Enabled() {
			r.Post("/dev/token", s.handleDevToken)
		}

		// Authenticated transactional routes (echo). Requires:
		//   - stub auth enabled (real IdP not in scope this milestone)
		//   - idempotency store wired
		//   - audit writer wired
		//   - pgx pool wired (echo writes audit + outbox in a single tx)
		if s.stub != nil && s.stub.Enabled() && s.idem != nil && s.audit != nil && s.pool != nil {
			r.Group(func(pr chi.Router) {
				pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{}))
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

// handleHealthz is a liveness probe: returns 200 unconditionally while the
// process is alive and able to serve HTTP.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// handleReadyz is a readiness probe: returns 200 only if every dependency
// required to serve real traffic is healthy. For the foundation milestone
// the sole dependency is the PostgreSQL pool.
func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "not_ready",
			"db":     "unconfigured",
		})
		return
	}
	if s.db.IsHealthy() {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ready",
			"db":     "ok",
		})
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"status": "not_ready",
		"db":     "down",
		"reason": s.db.LastError(),
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

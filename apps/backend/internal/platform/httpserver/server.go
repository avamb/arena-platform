// Package httpserver wires the chi-based HTTP listener, standard middleware
// chain, and the operational and /v1 endpoints required by the foundation
// milestone.
//
// The server exposes:
//
//   - /healthz, /readyz       — operational probes (liveness + readiness)
//   - /metrics                — Prometheus scrape (when MetricsHandler wired)
//   - /v1/*                   — business + auth endpoints (see mount_*.go)
//   - /compat/bil24/*         — legacy Bil24 compat gateway (bil24_compat.go)
//
// Dev-only routes (/v1/dev/*, /v1/debug/*) are runtime-gated by ENABLE_DEV_AUTH
// and DEBUG_ROUTES_ENABLED respectively.
//
// Additional /v1 routes can be attached by later features through Router().
//
// Source organisation:
//
//   - server.go        — package doc, Pinger/ReadinessProbe interfaces, PoolDB,
//     lifecycle (Router/ListenAndServe/Shutdown), operational
//     routes, 404/405 handlers, writeJSON helper.
//   - server_struct.go — Server struct definition.
//   - wire.go          — Options + New constructor (DI assembly).
//   - mount_v1.go      — /v1 orchestrator + info/debug/dev/echo routes.
//   - mount_*.go       — per-domain route registration.
package httpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
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
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
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
// Operational routes
// -----------------------------------------------------------------------------

func (s *Server) mountOperationalRoutes() {
	s.router.Get("/healthz", s.handleHealthz)
	s.router.Get("/readyz", s.handleReadyz)
	// /metrics is only mounted when the caller supplies a handler. The
	// scrape endpoint is intentionally unauthenticated for the foundation
	// milestone — Dokploy's reverse proxy enforces network-level restriction.
	if s.metrics != nil {
		s.router.Method(http.MethodGet, "/metrics", s.metrics)
	}
	// Custom 404/405 handlers return the standard JSON error envelope
	// instead of chi's default plain-text responses (features #12, #13).
	s.router.NotFound(handleNotFound)
	s.router.MethodNotAllowed(handleMethodNotAllowed)
}

// mountCompatRoutes is defined in bil24_compat.go (feature #157).
// mountV1Routes is defined in mount_v1.go.

// -----------------------------------------------------------------------------
// 404 / 405 handlers
// -----------------------------------------------------------------------------

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

// -----------------------------------------------------------------------------
// Liveness / readiness handlers
// -----------------------------------------------------------------------------

// handleHealthz is a liveness probe: returns 200 unconditionally while the
// process is alive and able to serve HTTP.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// handleReadyz is a readiness probe: iterates through all registered
// ReadinessProbes and aggregates their results into the /readyz checks map.
// Returns 200 {"status":"ready","checks":{...}} when every probe passes, or
// 503 {"status":"not_ready","checks":{...}} when any probe fails.
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

// writeJSON delegates to httputil.WriteJSON. Kept as an unexported alias so
// that existing handler methods on *Server require no import changes.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	httputil.WriteJSON(w, status, payload)
}

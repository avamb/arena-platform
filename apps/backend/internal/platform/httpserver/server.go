// Package httpserver wires the chi-based HTTP listener, standard middleware
// chain, and operational endpoints (/healthz, /readyz).
//
// This milestone wires the minimal surface required by feature #1 of the
// scaffold spec. Additional routes (/v1/info, /v1/echo, /metrics) will be
// attached in later features through the Router accessor.
package httpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

// Pinger is the contract required by the readiness probe — anything that can
// answer "is my dependency healthy right now". The database pool implements
// this directly.
type Pinger interface {
	IsHealthy() bool
	LastError() string
}

// Server is the long-lived HTTP listener that hosts the arena-api.
type Server struct {
	cfg    *config.Config
	logger *slog.Logger
	router *chi.Mux
	srv    *http.Server
	db     Pinger
}

// New constructs (but does not start) the HTTP server.
func New(cfg *config.Config, logger *slog.Logger, db Pinger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	r := chi.NewRouter()

	// Standard middleware chain. We intentionally keep this minimal for the
	// foundation milestone — request-id, real-ip, recoverer, and timeout are
	// the production basics required for /readyz to behave correctly.
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Timeout(cfg.RequestTimeout))

	s := &Server{
		cfg:    cfg,
		logger: logger,
		router: r,
		db:     db,
	}

	s.mountOperationalRoutes()

	s.srv = &http.Server{
		Addr:              cfg.HTTPListenAddr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       cfg.RequestTimeout + 5*time.Second,
		WriteTimeout:      cfg.RequestTimeout + 5*time.Second,
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
// operational routes
// -----------------------------------------------------------------------------

func (s *Server) mountOperationalRoutes() {
	s.router.Get("/healthz", s.handleHealthz)
	s.router.Get("/readyz", s.handleReadyz)
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

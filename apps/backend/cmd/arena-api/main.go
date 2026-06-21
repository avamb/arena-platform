// Package main is the entry point for the arena-api HTTP server.
//
// Responsibilities of the Wave-2 arena-api boot (feature #88):
//
//   - Load and validate runtime configuration from environment variables.
//   - Initialize structured logging (slog with JSON or text handler).
//   - Initialize observability: a Prometheus registry that backs /metrics,
//     and an OpenTelemetry TracerProvider (OTLP/gRPC) with a flushable
//     shutdown hook.
//   - Open a pgx/v5 connection pool to PostgreSQL with exponential backoff
//     on initial connect so container startup races do not crash the
//     process.
//   - Construct the chi HTTP server (Server). The server exposes /healthz,
//     /readyz, /metrics, /v1/info, /v1/dev/token, and /v1/echo with
//     explicit ReadHeaderTimeout / ReadTimeout / WriteTimeout / IdleTimeout
//     applied at the http.Server level.
//   - Run a signal-bound root context (signal.NotifyContext for SIGINT and
//     SIGTERM) and drive a graceful shutdown bounded by cfg.ShutdownTimeout.
//   - On any fatal startup error return a non-zero exit code with the cause
//     written to stderr; never leak goroutines or hold the port on exit.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/database"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
)

func main() {
	if err := run(); err != nil {
		// Fail-fast: print the cause to stderr and exit non-zero so the
		// container runtime (Dokploy / Docker / Kubernetes) restarts us
		// and the operator sees the reason in the pod log.
		fmt.Fprintf(os.Stderr, "arena-api: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. Configuration ---------------------------------------------------------
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// 2. Logger ----------------------------------------------------------------
	// app / env / version are attached by NewWithOptions; commit is layered
	// on top via With so every record carries the deploy fingerprint.
	logger := logging.NewWithOptions(logging.Options{
		Writer:  os.Stdout,
		Format:  cfg.LogFormat,
		Level:   cfg.LogLevel,
		App:     cfg.AppName,
		Env:     string(cfg.AppEnv),
		Version: cfg.AppVersion,
	}).With(slog.String("commit", cfg.AppCommit))
	slog.SetDefault(logger)

	logger.Info("arena-api starting",
		"listen_addr", cfg.HTTPListenAddr,
		"go_env", string(cfg.AppEnv),
	)

	// 3. Signal-bound root context --------------------------------------------
	// signal.NotifyContext installs handlers for SIGINT and SIGTERM and
	// cancels rootCtx when either is received. The deferred stop()
	// releases the signal handler so subsequent SIGINT/SIGTERM signals
	// fall through to the Go default (terminate the process) — important
	// because a buggy shutdown sequence must never be uninterruptible.
	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 4. Observability --------------------------------------------------------
	// Metrics: a shared *prometheus.Registry exposes baseline collectors
	// (HTTP latency/count, DB pool, worker job lag, outbox backlog) and
	// the Go runtime + process collectors. The handler is mounted at
	// /metrics by the HTTP server below.
	//
	// Tracing: a TracerProvider is installed as the global provider.
	// When OTEL_EXPORTER_OTLP_ENDPOINT is empty the provider is wired in
	// disabled mode (NeverSample, no exporter) so call sites that already
	// open spans remain valid no-ops. The returned tracerShutdown is
	// always called on exit — it flushes any buffered batches and closes
	// the gRPC exporter.
	metrics := observability.MustNew(nil)
	tracerShutdownCtx, cancelTracerInit := context.WithTimeout(rootCtx, 10*time.Second)
	tp, tracerShutdown, err := observability.InitTracer(tracerShutdownCtx, observability.TracingOptions{
		Endpoint:       cfg.OTLPEndpoint,
		Insecure:       cfg.OTELInsecure,
		ServiceName:    coalesce(cfg.OTELServiceName, cfg.AppName),
		ServiceVersion: cfg.AppVersion,
		Environment:    string(cfg.AppEnv),
		SamplerRatio:   cfg.OTELTracesSampler,
	})
	cancelTracerInit()
	if err != nil {
		return fmt.Errorf("init tracer: %w", err)
	}
	_ = tp // provider is also registered as the global; we keep the handle for testability.
	defer func() {
		// Drain spans with a bounded budget so a slow collector cannot
		// stall the exit path past cfg.ShutdownTimeout.
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tracerShutdown(flushCtx); err != nil {
			logger.Warn("tracer shutdown failed", "error", err.Error())
		}
	}()

	logger.Info("observability initialized",
		"otlp_endpoint", cfg.OTLPEndpoint,
		"sampler_ratio", cfg.OTELTracesSampler,
	)

	// 5. Database pool (retry on first connect) -------------------------------
	connectCtx, cancelConnect := context.WithTimeout(rootCtx, 60*time.Second)
	pool, err := database.Open(connectCtx, cfg, logger)
	cancelConnect()
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	// 6. Auth provider (dev stub; production swaps for real IdP) -------------
	stubAuth, err := auth.NewStubProvider(auth.StubConfig{
		Secret:     cfg.JWTSecretStub,
		Issuer:     "arena-dev",
		Audience:   "arena-api",
		DefaultTTL: time.Hour,
		Enabled:    cfg.EnableStubAuth,
	})
	if err != nil {
		return fmt.Errorf("init stub auth: %w", err)
	}
	if stubAuth.Enabled() {
		logger.Info("stub auth provider enabled",
			"issuer", stubAuth.Issuer(),
			"audience", stubAuth.Audience(),
		)
	} else {
		logger.Info("stub auth provider disabled")
	}

	// 7. HTTP server -----------------------------------------------------------
	srv := httpserver.New(httpserver.Options{
		Config:         cfg,
		Logger:         logger,
		DB:             pool,
		Pool:           pool.Pool,
		PgxPool:        pool.Pool,
		Auth:           stubAuth,
		MetricsHandler: metrics.Handler(),
	})

	listenErrCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			listenErrCh <- err
			return
		}
		listenErrCh <- nil
	}()

	// 8. Wait for signal or fatal listen error --------------------------------
	select {
	case <-rootCtx.Done():
		logger.Info("shutdown signal received")
	case err := <-listenErrCh:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
	}

	// 9. Graceful shutdown -----------------------------------------------------
	// The shutdown context is NOT derived from rootCtx — rootCtx is already
	// cancelled at this point — so we hand http.Server a fresh deadline
	// equal to cfg.ShutdownTimeout. http.Server.Shutdown stops accepting
	// new connections and waits for in-flight handlers up to the deadline.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancelShutdown()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("graceful shutdown failed", "error", err.Error())
	}

	// Drain the goroutine so the test/process exits with no live handler.
	select {
	case <-listenErrCh:
	case <-time.After(cfg.ShutdownTimeout):
	}

	logger.Info("arena-api stopped cleanly")
	return nil
}

// coalesce returns the first non-empty argument; used to pick OTEL service
// name fallback without sprinkling a strings.TrimSpace + ternary at the call
// site.
func coalesce(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

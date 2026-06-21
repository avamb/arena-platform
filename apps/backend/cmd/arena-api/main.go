// Package main is the entry point for the arena-api HTTP server.
//
// Responsibilities (Step 1 of the scaffold milestone):
//   - Load and validate runtime configuration from environment variables.
//   - Initialize structured logging (slog with JSON or text handler).
//   - Open a pgx/v5 connection pool to PostgreSQL with exponential backoff
//     on initial connect; tolerate container startup races.
//   - Mount /healthz (liveness) and /readyz (readiness, includes DB ping).
//   - Listen on HTTP_LISTEN_ADDR.
//   - Graceful shutdown on SIGTERM/SIGINT.
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

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/database"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

func main() {
	if err := run(); err != nil {
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
	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 4. Database pool (retry on first connect) -------------------------------
	connectCtx, cancelConnect := context.WithTimeout(rootCtx, 60*time.Second)
	pool, err := database.Open(connectCtx, cfg, logger)
	cancelConnect()
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	// 5. HTTP server -----------------------------------------------------------
	srv := httpserver.New(cfg, logger, pool)

	listenErrCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			listenErrCh <- err
			return
		}
		listenErrCh <- nil
	}()

	// 6. Wait for signal or fatal listen error --------------------------------
	select {
	case <-rootCtx.Done():
		logger.Info("shutdown signal received")
	case err := <-listenErrCh:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
	}

	// 7. Graceful shutdown -----------------------------------------------------
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancelShutdown()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("graceful shutdown failed", "error", err.Error())
	}

	logger.Info("arena-api stopped cleanly")
	return nil
}

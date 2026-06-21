// Package main is the entry point for the arena-worker background worker.
//
// FOUNDATION-MILESTONE STUB. The full worker (job registry, Postgres-backed
// queue with FOR UPDATE SKIP LOCKED, idempotent execution, retry/backoff,
// outbox dispatcher) is implemented by subsequent features. The current
// implementation only loads configuration, opens the database pool, and
// blocks until a shutdown signal is received — enough to keep the container
// alive in docker-compose and exercise the shared platform packages.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/database"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "arena-worker: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := logging.NewWithOptions(logging.Options{
		Writer:  os.Stdout,
		Format:  cfg.LogFormat,
		Level:   cfg.LogLevel,
		App:     "arena-worker",
		Env:     string(cfg.AppEnv),
		Version: cfg.AppVersion,
	}).With(slog.String("commit", cfg.AppCommit))
	slog.SetDefault(logger)

	logger.Info("arena-worker starting (foundation stub)")

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	connectCtx, cancelConnect := context.WithTimeout(rootCtx, 60*time.Second)
	pool, err := database.Open(connectCtx, cfg, logger)
	cancelConnect()
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	logger.Info("arena-worker ready; waiting for shutdown signal")
	<-rootCtx.Done()
	logger.Info("arena-worker stopped cleanly")
	return nil
}

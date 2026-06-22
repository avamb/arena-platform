// Package main is the entry point for the arena-worker background worker.
//
// arena-worker polls the worker_jobs table for ready rows, dispatches
// each one to a handler registered against its job_type, and writes the
// outcome (done / retry / failed) back to the row. Jobs survive the
// worker being offline — that is the contract the platform tables
// (feature #20) are designed to fulfil.
//
// This binary is intentionally lean: it loads configuration, opens a
// pgx pool, builds a handler registry, runs the worker loop, and exits
// cleanly on SIGINT/SIGTERM. The HTTP surface (metrics, healthz) is
// served by arena-api in the same compose stack.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/database"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
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

	logger.Info("arena-worker starting")

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Database pool. Use a bounded connect deadline so the container
	// fails fast if Postgres is genuinely unreachable rather than
	// hanging on the docker-compose dependency.
	connectCtx, cancelConnect := context.WithTimeout(rootCtx, 60*time.Second)
	pool, err := database.Open(connectCtx, cfg, logger)
	cancelConnect()
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	// Handler registry. The platform foundation milestone only ships
	// one handler — noop.test — used by the worker_jobs persistence
	// test (feature #20). Real business handlers register themselves
	// here as later milestones add them.
	registry := worker.NewRegistry()
	registerBuiltinHandlers(registry, logger)

	w, err := worker.New(worker.Options{
		Pool:            pool,
		Registry:        registry,
		Logger:          logger,
		PollInterval:    time.Second,
		ShutdownTimeout: cfg.ShutdownTimeout,
	})
	if err != nil {
		return fmt.Errorf("construct worker: %w", err)
	}

	logger.Info("arena-worker ready",
		"instance_id", w.InstanceID(),
		"poll_interval", "1s",
	)

	// Run the loop in a goroutine so we can react to the signal-bound
	// rootCtx without blocking the main goroutine on Run.
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- w.Run(rootCtx) }()

	select {
	case <-rootCtx.Done():
		logger.Info("shutdown signal received; stopping worker")
	case err := <-runErrCh:
		// Worker.Run returned on its own — propagate any unexpected
		// error and bail out.
		if err != nil {
			return fmt.Errorf("worker run: %w", err)
		}
		logger.Info("worker run exited cleanly without signal")
		return nil
	}

	if err := w.Stop(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		logger.Warn("worker stop returned error", "error", err.Error())
	}

	// Drain runErrCh so the goroutine doesn't leak after Stop returns.
	select {
	case err := <-runErrCh:
		if err != nil {
			logger.Warn("worker exited with error", "error", err.Error())
		}
	case <-time.After(cfg.ShutdownTimeout):
		logger.Warn("worker goroutine did not exit within shutdown timeout")
	}

	logger.Info("arena-worker stopped cleanly")
	return nil
}

// registerBuiltinHandlers attaches every job type the foundation
// milestone ships with. The list is intentionally tiny — additional
// handlers belong to their owning modules, not here.
func registerBuiltinHandlers(reg *worker.Registry, logger *slog.Logger) {
	// noop.test exists for feature #20 (worker job persistence) and for
	// any future smoke test that wants to prove the queue plumbing
	// without exercising business code. It always succeeds.
	reg.Register("noop.test", func(ctx context.Context, payload []byte) error {
		logger.Info("noop.test handler invoked", "payload_bytes", len(payload))
		return nil
	})
}

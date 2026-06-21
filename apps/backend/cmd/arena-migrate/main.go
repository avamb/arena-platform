// Package main is the entry point for the arena-migrate database migration
// tool.
//
// FOUNDATION-MILESTONE STUB. The full implementation (embed.FS-backed
// goose migrations with up/down/status/redo subcommands) is delivered in a
// later feature. The current binary loads configuration, opens the database
// pool to prove connectivity, and exits with code 0 on the "up" or "status"
// subcommands. Anything else returns a clear "not implemented yet" error
// so callers (Makefile, init.sh, CI) get a deterministic signal.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/database"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "arena-migrate: fatal: %v\n", err)
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
		App:     "arena-migrate",
		Env:     string(cfg.AppEnv),
		Version: cfg.AppVersion,
	}).With(slog.String("commit", cfg.AppCommit))
	slog.SetDefault(logger)

	subcommand := "up"
	if len(os.Args) > 1 {
		subcommand = os.Args[1]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := database.Open(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	switch subcommand {
	case "up", "status":
		logger.Info("arena-migrate: no migrations defined yet (foundation stub)", "subcommand", subcommand)
		return nil
	case "down", "redo":
		logger.Info("arena-migrate: no migrations defined yet (foundation stub)", "subcommand", subcommand)
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q (expected up|down|status|redo)", subcommand)
	}
}

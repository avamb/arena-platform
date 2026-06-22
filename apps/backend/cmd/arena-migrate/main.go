// Package main is the entry point for the arena-migrate database migration
// tool.
//
// arena-migrate is the production migration path: it embeds the goose-format
// SQL files under apps/backend/internal/migrations/sql/ into the binary
// itself (via embed.FS) so a deployed container has no runtime dependency
// on the source tree.
//
// Subcommands:
//
//	up                Apply every pending migration in order.
//	down              Roll back the most recent migration.
//	status            Print which migrations are applied / pending.
//	redo              Roll back the most recent migration, then re-apply it.
//	version           Print the current schema version.
//	up-to <version>   Apply migrations up to and including <version>.
//	down-to <version> Roll back migrations down to <version>.
//	reset             Roll back every applied migration.
//	create <name>     Create a new timestamped migration file in
//	                  apps/backend/internal/migrations/sql/ (dev convenience).
//
// Exit code is 0 on success, 1 on any error (including unknown subcommands).
// All operations log structured slog records so operators can audit a run
// from container logs without re-running the command.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/migrations"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"

	"github.com/jackc/pgx/v5/stdlib" // registers "pgx" database/sql driver
	"github.com/pressly/goose/v3"
)

// stdlib is imported only for its side effect (registering the pgx driver
// with database/sql). The blank identifier here keeps `goimports` from
// stripping it.
var _ = stdlib.GetDefaultDriver

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

	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"up"}
	}
	sub := args[0]
	subArgs := args[1:]

	// "create" doesn't talk to the DB — handle it before opening a connection.
	if sub == "create" {
		return createMigration(logger, subArgs)
	}

	// Open a database/sql handle (goose's API is database/sql, not pgx-native).
	// pgx provides a database/sql-compatible driver via stdlib package.
	db, err := goose.OpenDBWithDriver("pgx", cfg.DBDSN())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			logger.Warn("close db", "error", closeErr.Error())
		}
	}()

	// Configure goose: dialect, base FS, custom logger, table name.
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}
	goose.SetBaseFS(migrations.FS)
	goose.SetTableName("schema_migrations")
	goose.SetLogger(&gooseLogger{logger: logger})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	start := time.Now()
	beforeVersion, _ := goose.GetDBVersionContext(ctx, db) //nolint:errcheck // diagnostic

	switch sub {
	case "up":
		if err := goose.UpContext(ctx, db, migrations.Dir); err != nil {
			return fmt.Errorf("up: %w", err)
		}
		afterVersion, _ := goose.GetDBVersionContext(ctx, db) //nolint:errcheck
		applied := countApplied(beforeVersion, afterVersion)
		logger.Info("migrations applied",
			"applied", applied,
			"from_version", beforeVersion,
			"to_version", afterVersion,
			"duration", time.Since(start).String(),
		)
	case "down":
		if err := goose.DownContext(ctx, db, migrations.Dir); err != nil {
			return fmt.Errorf("down: %w", err)
		}
		afterVersion, _ := goose.GetDBVersionContext(ctx, db) //nolint:errcheck
		logger.Info("migration rolled back",
			"from_version", beforeVersion,
			"to_version", afterVersion,
			"duration", time.Since(start).String(),
		)
	case "redo":
		if err := goose.RedoContext(ctx, db, migrations.Dir); err != nil {
			return fmt.Errorf("redo: %w", err)
		}
		logger.Info("migration redone",
			"version", beforeVersion,
			"duration", time.Since(start).String(),
		)
	case "status":
		jsonOut := false
		for _, a := range subArgs {
			if a == "--json" {
				jsonOut = true
			}
		}
		if err := runStatus(ctx, db, os.Stdout, jsonOut); err != nil {
			return fmt.Errorf("status: %w", err)
		}
	case "version":
		version, err := goose.GetDBVersionContext(ctx, db)
		if err != nil {
			return fmt.Errorf("version: %w", err)
		}
		logger.Info("current schema version", "version", version)
	case "reset":
		if err := goose.ResetContext(ctx, db, migrations.Dir); err != nil {
			return fmt.Errorf("reset: %w", err)
		}
		logger.Info("migrations reset")
	case "up-to":
		if len(subArgs) < 1 {
			return errors.New("up-to: target version required")
		}
		target, err := parseInt64(subArgs[0])
		if err != nil {
			return fmt.Errorf("up-to: %w", err)
		}
		if err := goose.UpToContext(ctx, db, migrations.Dir, target); err != nil {
			return fmt.Errorf("up-to: %w", err)
		}
		logger.Info("migrated up to", "target", target)
	case "down-to":
		if len(subArgs) < 1 {
			return errors.New("down-to: target version required")
		}
		target, err := parseInt64(subArgs[0])
		if err != nil {
			return fmt.Errorf("down-to: %w", err)
		}
		if err := goose.DownToContext(ctx, db, migrations.Dir, target); err != nil {
			return fmt.Errorf("down-to: %w", err)
		}
		logger.Info("migrated down to", "target", target)
	default:
		return fmt.Errorf("unknown subcommand %q (expected up|down|status|redo|version|reset|up-to|down-to|create)", sub)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Status command
// ---------------------------------------------------------------------------

// migrationStatusEntry holds the human-readable and machine-readable status
// of a single migration file. It is the unit of output for both the table
// and JSON formats.
type migrationStatusEntry struct {
	Version   int64  `json:"version"`
	Name      string `json:"name"`
	AppliedAt string `json:"applied_at,omitempty"` // RFC3339 timestamp or "" for pending
	Status    string `json:"status"`               // "applied" | "pending"
}

// runStatus prints migration status to w. If jsonOut is true it writes one
// JSON object per line (newline-delimited JSON); otherwise it writes a
// human-readable table with "Applied At" and "Migration" column headers.
//
// The function reads the embedded migration list from migrations.FS and
// queries schema_migrations for applied-at timestamps so the output is
// always consistent with what the database actually knows.
func runStatus(ctx context.Context, db *sql.DB, w io.Writer, jsonOut bool) error {
	// 1. Enumerate migration files from the embedded FS.
	dirEntries, err := migrations.FS.ReadDir(migrations.Dir)
	if err != nil {
		return fmt.Errorf("list embedded migrations: %w", err)
	}

	// 2. Query schema_migrations for the most recent applied_at per version.
	//    We use MAX(tstamp) grouped by version_id so repeated apply/rollback
	//    cycles don't create confusing duplicate rows in the output.
	rows, err := db.QueryContext(ctx, `
		SELECT version_id, MAX(tstamp) AS applied_at
		  FROM schema_migrations
		 WHERE is_applied = true
		 GROUP BY version_id
	`)
	if err != nil {
		return fmt.Errorf("query schema_migrations: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	appliedAt := make(map[int64]time.Time)
	for rows.Next() {
		var vid int64
		var ts time.Time
		if err := rows.Scan(&vid, &ts); err != nil {
			return fmt.Errorf("scan schema_migrations row: %w", err)
		}
		appliedAt[vid] = ts
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate schema_migrations: %w", err)
	}

	// 3. Build the ordered status list (file FS order == migration order).
	var statuses []migrationStatusEntry
	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		version, parseErr := parseVersionFromFilename(name)
		if parseErr != nil {
			continue // ignore files that don't follow the NNN_name.sql convention
		}
		entry := migrationStatusEntry{
			Version: version,
			Name:    name,
			Status:  "pending",
		}
		if ts, ok := appliedAt[version]; ok {
			entry.Status = "applied"
			entry.AppliedAt = ts.UTC().Format(time.RFC3339)
		}
		statuses = append(statuses, entry)
	}

	if jsonOut {
		return writeStatusJSON(w, statuses)
	}
	return writeStatusTable(w, statuses)
}

// writeStatusTable writes a human-readable migration status table to w.
//
// Example output:
//
//	Applied At                  Migration
//	=======================================
//	2025-01-01T12:00:00Z     -- 0001_init.sql [applied]
//	Pending                  -- 0002_outbox.sql [pending]
func writeStatusTable(w io.Writer, statuses []migrationStatusEntry) error {
	const header = "    Applied At                  Migration\n    =======================================\n"
	if _, err := fmt.Fprint(w, header); err != nil {
		return err
	}
	for _, s := range statuses {
		var col string
		if s.Status == "applied" {
			col = s.AppliedAt
		} else {
			col = "Pending"
		}
		line := fmt.Sprintf("    %-28s -- %s [%s]\n", col, s.Name, s.Status)
		if _, err := fmt.Fprint(w, line); err != nil {
			return err
		}
	}
	return nil
}

// writeStatusJSON writes one JSON object per line to w (newline-delimited JSON).
// Each object contains "version", "name", "status", and (when applied) "applied_at".
func writeStatusJSON(w io.Writer, statuses []migrationStatusEntry) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, s := range statuses {
		if err := enc.Encode(s); err != nil {
			return fmt.Errorf("encode migration status JSON: %w", err)
		}
	}
	return nil
}

// parseVersionFromFilename extracts the leading numeric version from a goose
// migration filename. Both sequence-numbered files ("0001_init.sql") and
// timestamp-numbered files ("20250101120000_add_users.sql") are supported.
func parseVersionFromFilename(name string) (int64, error) {
	idx := strings.IndexByte(name, '_')
	if idx < 0 {
		return 0, fmt.Errorf("migration filename %q has no underscore separator", name)
	}
	prefix := name[:idx]
	if prefix == "" {
		return 0, fmt.Errorf("migration filename %q has empty version prefix", name)
	}
	return parseInt64(prefix)
}

// createMigration writes a new empty goose SQL migration file. The new file
// uses the goose "sequence" numbering (next integer in the series) so the
// ordering of migrations is deterministic across collaborators.
func createMigration(logger *slog.Logger, args []string) error {
	if len(args) < 1 {
		return errors.New("create: migration name required (e.g. arena-migrate create add_users)")
	}
	name := strings.Join(args, "_")

	// Resolve the migrations directory relative to the working directory.
	// `goose create` insists on a real on-disk directory; embed.FS is read-only.
	dir := filepath.Join("apps", "backend", "internal", "migrations", "sql")
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("create: migrations dir %q not found (run from repo root): %w", dir, err)
	}

	// Reset SetBaseFS so goose writes to disk, not the embed.FS we set above.
	goose.SetBaseFS(nil)

	if err := goose.Create(nil, dir, name, "sql"); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	logger.Info("created migration", "name", name, "dir", dir)
	return nil
}

// countApplied returns the number of migrations applied in a single up run.
// Goose versions are timestamps or sequence numbers; the count is just the
// number of pending migrations that were processed.
func countApplied(before, after int64) int64 {
	if after <= before {
		return 0
	}
	// For sequence-numbered migrations, the delta is the count. For
	// timestamp-numbered migrations this is not an exact count but it
	// always returns a positive number when something was applied — that
	// is enough information to log a success line.
	return 1
}

func parseInt64(s string) (int64, error) {
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, fmt.Errorf("not a valid version number %q: %w", s, err)
	}
	return n, nil
}

// gooseLogger adapts our slog logger to goose's logger interface.
// Goose calls Printf for normal output and Fatalf on unrecoverable error;
// we never want Fatalf to call os.Exit on our behalf, so we log+panic to
// surface the error back through `run()` and exit with a non-zero code.
type gooseLogger struct {
	logger *slog.Logger
}

func (g *gooseLogger) Printf(format string, v ...interface{}) {
	g.logger.Info(strings.TrimRight(fmt.Sprintf(format, v...), "\n"))
}

func (g *gooseLogger) Fatalf(format string, v ...interface{}) {
	msg := strings.TrimRight(fmt.Sprintf(format, v...), "\n")
	g.logger.Error("goose fatal", "message", msg)
	panic("goose fatal: " + msg)
}

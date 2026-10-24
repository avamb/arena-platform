// Package main is the arena-richtext-backfill command (spec 43, RT-02): it
// converts the formatted texts that are still stored as the old plain text
// ("**bold**, _italic_, - list") into "text Arena" (ART) and marks them 'art'.
//
// The texts: events.description, every i18n_text row of namespace
// event.description (per key and locale) and event_email_notes.note. The
// conversion is richtext.Normalize(value, "text", profile), the same one the
// write path uses. A text that cannot be converted (longer than its limit
// after conversion, rejected characters) is NOT changed and is listed in the
// report; because the format flag of an event covers its translations, the
// other texts of that event stay as they are too.
//
//	--dry-run (default)  report only, nothing is written
//	--apply              write: per event (or note) one transaction that first
//	                     copies the old values to richtext_backfill_backup and
//	                     then stores the new value and the format 'art'
//	--rollback <run_id>  put back the old values and the format 'text' of that
//	                     run (a dry run unless --apply is given too); a text
//	                     written since the run is left alone
//	--limit N            look at no more than N events and notes (0 = all)
//	--org <org_id>       only the events (and notes) of this organization
//	--report <file>      write the markdown report there (default: stdout)
//
// Only rows marked 'text' are considered, so a second run changes nothing.
//
//	DATABASE_URL=... JWT_SIGNING_SECRET=... go run ./apps/backend/cmd/arena-richtext-backfill --report report.md
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "arena-richtext-backfill: fatal: %v\n", err)
		os.Exit(1)
	}
}

// flags is the parsed command line.
type flags struct {
	apply    bool
	rollback uuid.UUID
	isRoll   bool
	limit    int
	org      uuid.UUID
	report   string
}

func parseFlags(args []string) (flags, error) {
	var f flags
	fs := flag.NewFlagSet("arena-richtext-backfill", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", true, "report what would change and write nothing (the default)")
	apply := fs.Bool("apply", false, "write the changes (copies the old values to richtext_backfill_backup first)")
	rollback := fs.String("rollback", "", "restore the old values of the run with this id (with --apply; otherwise a dry run)")
	limit := fs.Int("limit", 0, "look at no more than this many events and notes (0 = all)")
	report := fs.String("report", "", "write the markdown report to this file (default: stdout)")
	org := fs.String("org", "", "only the events of this organization id (default: all)")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	dryExplicit := false
	fs.Visit(func(x *flag.Flag) {
		if x.Name == "dry-run" {
			dryExplicit = true
		}
	})
	if *apply && dryExplicit && *dryRun {
		return f, fmt.Errorf("--apply and --dry-run=true contradict each other")
	}
	if dryExplicit && !*dryRun {
		*apply = true
	}
	if *limit < 0 {
		return f, fmt.Errorf("--limit must not be negative")
	}
	f.apply, f.limit, f.report = *apply, *limit, *report
	if *org != "" {
		id, err := uuid.Parse(*org)
		if err != nil {
			return f, fmt.Errorf("--org wants an organization id (uuid): %w", err)
		}
		f.org = id
	}
	if *rollback != "" {
		id, err := uuid.Parse(*rollback)
		if err != nil {
			return f, fmt.Errorf("--rollback wants a run id (uuid): %w", err)
		}
		f.rollback, f.isRoll = id, true
	}
	return f, nil
}

func run(args []string, stdout io.Writer) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	cfg, err := config.LoadFor(config.RoleMigrate)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := logging.NewWithOptions(logging.Options{
		Writer:  os.Stderr,
		Format:  cfg.LogFormat,
		Level:   cfg.LogLevel,
		App:     "arena-richtext-backfill",
		Env:     string(cfg.AppEnv),
		Version: cfg.AppVersion,
	}).With(slog.String("commit", cfg.AppCommit))
	slog.SetDefault(logger)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DBDSN())
	if err != nil {
		return fmt.Errorf("open pgx pool: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	var rep *Report
	if f.isRoll {
		rep, err = rollbackRun(ctx, pool, f.rollback, f.apply)
	} else {
		rep, err = runBackfill(ctx, pool, options{Apply: f.apply, Limit: f.limit, OrgID: f.org})
	}
	if rep != nil {
		if werr := writeReport(stdout, f.report, rep); werr != nil && err == nil {
			err = werr
		}
	}
	if err != nil {
		return err
	}
	logger.Info("richtext backfill finished",
		slog.String("run_id", rep.RunID.String()), slog.Bool("apply", rep.Apply), slog.Bool("rollback", rep.Rollback),
		slog.Int("changed", len(rep.Changed)), slog.Int("skipped", len(rep.Skipped)))
	return nil
}

// writeReport puts the report in the file, or on stdout when there is none.
func writeReport(stdout io.Writer, path string, rep *Report) error {
	md := rep.Markdown()
	if path == "" {
		_, err := io.WriteString(stdout, md)
		return err
	}
	if err := os.WriteFile(path, []byte(md), 0o600); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

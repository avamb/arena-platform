// Package main is the arena-richtext-backfill command (spec 44, RT-02): it
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
// A text of the old kind that holds HTML tags of the white list (<p>,
// <strong>, <br>, <a ...>) was almost certainly written by a site that never
// named a format. Read as plain text the tags would become visible words, so
// such a text is left as it is, with the reason literal_tags, and so is its
// event. --html-as-art converts those texts as ART instead.
//
//	--dry-run (default)  report only, nothing is written
//	--apply              write: per event (or note) one transaction that first
//	                     copies the old values to richtext_backfill_backup and
//	                     then stores the new value and the format 'art'
//	--html-as-art        read a text with HTML tags as ART, not as plain text
//	--rollback <run_id>  put back the old values and the format 'text' of that
//	                     run (a dry run unless --apply is given too); a text
//	                     written since the run is left alone; --org restricts
//	                     it to one organization
//	--limit N            look at no more than N events (each with its note)
//	--after <event_id>   start after this event id; a run stopped by --limit
//	                     prints the id to continue with, and events left as
//	                     they are never block the next page
//	--org <org_id>       only the events (and notes) of this organization
//	--report <file>      write the markdown report there (default: stdout)
//	--show-text          put the full before and after texts in the report;
//	                     only with --report, the file is created with mode 0600.
//	                     By default the report has ids, locales, lengths and
//	                     reasons only (notes can hold phone numbers)
//	--purge-backup-older-than <days>
//	                     delete the backup rows older than that (a dry run
//	                     unless --apply is given); runs alone
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
	"runtime"
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
	apply     bool
	rollback  uuid.UUID
	isRoll    bool
	limit     int
	after     uuid.UUID
	org       uuid.UUID
	report    string
	htmlAsART bool
	showText  bool
	purgeDays int
}

func parseFlags(args []string) (flags, error) {
	var f flags
	fs := flag.NewFlagSet("arena-richtext-backfill", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", true, "report what would change and write nothing (the default)")
	apply := fs.Bool("apply", false, "write the changes (copies the old values to richtext_backfill_backup first)")
	rollback := fs.String("rollback", "", "restore the old values of the run with this id (with --apply; otherwise a dry run)")
	limit := fs.Int("limit", 0, "look at no more than this many events (0 = all)")
	after := fs.String("after", "", "start after this event id (the cursor a run stopped by --limit prints)")
	report := fs.String("report", "", "write the markdown report to this file (default: stdout)")
	org := fs.String("org", "", "only the events of this organization id (default: all)")
	htmlAsART := fs.Bool("html-as-art", false, "read a text that holds HTML tags as ART instead of leaving it as it is")
	showText := fs.Bool("show-text", false, "put the full texts in the report (only with --report, the file gets mode 0600)")
	purge := fs.Int("purge-backup-older-than", 0, "delete the backup rows older than this many days (with --apply; otherwise a dry run)")
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
	if *purge < 0 {
		return f, fmt.Errorf("--purge-backup-older-than must not be negative")
	}
	f.apply, f.limit, f.report, f.htmlAsART, f.showText, f.purgeDays = *apply, *limit, *report, *htmlAsART, *showText, *purge
	if *org != "" {
		id, err := uuid.Parse(*org)
		if err != nil {
			return f, fmt.Errorf("--org wants an organization id (uuid): %w", err)
		}
		f.org = id
	}
	if *after != "" {
		id, err := uuid.Parse(*after)
		if err != nil {
			return f, fmt.Errorf("--after wants an event id (uuid): %w", err)
		}
		f.after = id
	}
	if *rollback != "" {
		id, err := uuid.Parse(*rollback)
		if err != nil {
			return f, fmt.Errorf("--rollback wants a run id (uuid): %w", err)
		}
		f.rollback, f.isRoll = id, true
	}
	if f.showText && f.report == "" {
		return f, fmt.Errorf("--show-text needs --report <file>: the texts are never printed to stdout")
	}
	if f.isRoll && (f.limit != 0 || f.after != uuid.Nil || f.htmlAsART) {
		return f, fmt.Errorf("--limit, --after and --html-as-art do not apply to --rollback")
	}
	if f.purgeDays > 0 && (f.isRoll || f.limit != 0 || f.after != uuid.Nil || f.htmlAsART || f.org != uuid.Nil || f.showText) {
		return f, fmt.Errorf("--purge-backup-older-than runs alone")
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
	switch {
	case f.purgeDays > 0:
		rep, err = purgeBackups(ctx, pool, f.purgeDays, f.apply)
	case f.isRoll:
		rep, err = rollbackRun(ctx, pool, f.rollback, f.apply, f.org)
	default:
		rep, err = runBackfill(ctx, pool, options{
			Apply: f.apply, Limit: f.limit, After: f.after, OrgID: f.org, HTMLAsART: f.htmlAsART, ShowText: f.showText,
		})
	}
	if rep != nil {
		rep.ShowText = rep.ShowText && f.report != ""
		if werr := writeReport(stdout, f.report, rep); werr != nil && err == nil {
			err = werr
		}
	}
	if err != nil {
		return err
	}
	logger.Info("richtext backfill finished",
		slog.String("run_id", rep.RunID.String()), slog.Bool("apply", rep.Apply), slog.Bool("rollback", rep.Rollback),
		slog.Int("changed", len(rep.Changed)), slog.Int("skipped", len(rep.Skipped)),
		slog.String("next_after", rep.NextAfter.String()))
	return nil
}

// writeReport puts the report in the file, or on stdout when there is none.
// The file is created with mode 0600 (and an existing one is narrowed to it):
// with --show-text it holds the texts of events and notes.
func writeReport(stdout io.Writer, path string, rep *Report) error {
	md := rep.Markdown()
	if path == "" {
		_, err := io.WriteString(stdout, md)
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	if err := file.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		_ = file.Close()
		return fmt.Errorf("write report: %w", err)
	}
	if _, err := io.WriteString(file, md); err != nil {
		_ = file.Close()
		return fmt.Errorf("write report: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

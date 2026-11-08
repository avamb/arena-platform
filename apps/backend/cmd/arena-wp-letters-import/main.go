// Package main is the arena-wp-letters-import command (Vino&Co cutover): it
// files the ticket-letter history WordPress kept (when each letter went out
// through the old site's Brevo account and what Brevo reported) in the
// platform's email_letters table, so the event center's order screen shows it.
//
//	--org <uuid>     the organization (required)
//	--file <json>    the output of ops/vino-cutover/wp-letters-export.php (required)
//	--dry-run        the default: count and print, write nothing
//	--apply          write
//
// A Bil24-era order (a 7-digit bil24_external_order_id) is matched to the
// order arena-legacy-tickets-import filed for it, through wp_order_links.
//
// Idempotent: a letter whose message id is already filed is skipped, so the
// tool can run before and after the switch. A second run only moves an
// imported letter forward (sent -> delivered -> opened), never back.
//
//	DATABASE_URL=... JWT_SIGNING_SECRET=x go run ./apps/backend/cmd/arena-wp-letters-import --org <uuid> --file wp-letters.json
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/wpletters"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "arena-wp-letters-import: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("arena-wp-letters-import", flag.ContinueOnError)
	org := fs.String("org", "", "organization id (uuid)")
	file := fs.String("file", "", "JSON file of wp-letters-export.php")
	dry := fs.Bool("dry-run", true, "count and print, write nothing (the default)")
	apply := fs.Bool("apply", false, "write the rows")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *apply && flagSet(fs, "dry-run") && *dry {
		return fmt.Errorf("--apply and --dry-run contradict each other")
	}
	orgID, err := uuid.Parse(*org)
	if err != nil {
		return fmt.Errorf("--org wants an organization id (uuid): %w", err)
	}
	if *file == "" {
		return fmt.Errorf("--file is required")
	}
	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer f.Close()
	doc, err := wpletters.Parse(f)
	if err != nil {
		return err
	}

	cfg, err := config.LoadFor(config.RoleMigrate)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DBDSN())
	if err != nil {
		return fmt.Errorf("open pgx pool: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	rep, err := wpletters.Import(ctx, pool, orgID, doc.Letters, !*apply)
	if err != nil {
		return err
	}
	mode := "DRY RUN"
	verb := "would insert"
	if *apply {
		mode, verb = "APPLY", "inserted"
	}
	fmt.Fprintf(out, "%s: records=%d matched=%d matched_bil24_era=%d unmatched=%d no_order_number=%d unusable=%d already_present=%d %s=%d updated=%d\n",
		mode, rep.Records, rep.Matched, rep.MatchedLegacy, rep.Unmatched, rep.NoOrderNumber, rep.Skipped, rep.AlreadyThere, verb, rep.Inserted, rep.Updated)
	for i, u := range rep.UnmatchedList {
		if i == 50 {
			fmt.Fprintf(out, "unmatched ... and %d more\n", len(rep.UnmatchedList)-i)
			break
		}
		fmt.Fprintf(out, "unmatched %s\n", u)
	}
	return nil
}

func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

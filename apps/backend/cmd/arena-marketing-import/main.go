// Package main is the arena-marketing-import command (P-18, spec 62 10.1): it
// imports the Vino&Co mailing base from two exports.
//
//	--org-id <uuid>      the organization (required)
//	--brevo <csv>        Brevo contacts export (optional)
//	--elementor <csv>    Elementor club-form export (optional)
//	--dry-run            do everything, print the counts, write nothing
//
// At least one file is required. The file formats, the rules (suppressions
// win, no consent data = skipped) and the re-run for late sign-ups are in
// docs/ops/vinoandco_marketing_import.md. The import is idempotent.
//
//	DATABASE_URL=... JWT_SIGNING_SECRET=x go run ./apps/backend/cmd/arena-marketing-import \
//	    --org-id <uuid> --brevo brevo.csv --elementor club.csv --dry-run
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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/marketingimport"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "arena-marketing-import: fatal: %v\n", err)
		os.Exit(1)
	}
}

func readFile(path string, parse func(io.Reader) ([]marketingimport.Record, []marketingimport.Problem, error)) ([]marketingimport.Record, []marketingimport.Problem, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	return parse(f)
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("arena-marketing-import", flag.ContinueOnError)
	org := fs.String("org-id", "", "organization id (uuid)")
	brevo := fs.String("brevo", "", "Brevo contacts export (CSV)")
	elementor := fs.String("elementor", "", "Elementor club-form export (CSV)")
	dry := fs.Bool("dry-run", false, "print the counts, write nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	orgID, err := uuid.Parse(*org)
	if err != nil {
		return fmt.Errorf("--org-id wants an organization id (uuid): %w", err)
	}
	if *brevo == "" && *elementor == "" {
		return fmt.Errorf("give at least one of --brevo and --elementor")
	}
	var recs []marketingimport.Record
	var probs []marketingimport.Problem
	if *brevo != "" {
		r, p, err := readFile(*brevo, marketingimport.ParseBrevo)
		if err != nil {
			return err
		}
		recs, probs = append(recs, r...), append(probs, p...)
	}
	if *elementor != "" {
		r, p, err := readFile(*elementor, marketingimport.ParseElementor)
		if err != nil {
			return err
		}
		recs, probs = append(recs, r...), append(probs, p...)
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
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM organizations WHERE id = $1)`, orgID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("organization %s not found", orgID)
	}

	c, err := marketingimport.Run(ctx, pool, orgID, recs, *dry, time.Now().UTC())
	if err != nil {
		return err
	}
	c.InvalidDate = len(probs)
	mode := "APPLY"
	if *dry {
		mode = "DRY RUN"
	}
	fmt.Fprintf(out, "%s for organization %s\n", mode, orgID)
	fmt.Fprintf(out, "rows read:            brevo=%d elementor=%d\n", c.BrevoRows, c.ElementorRows)
	fmt.Fprintf(out, "invalid e-mail:       %d (skipped)\n", c.InvalidEmail)
	fmt.Fprintf(out, "unreadable date:      %d (treated as no consent data)\n", c.InvalidDate)
	fmt.Fprintf(out, "duplicates merged:    %d\n", c.DuplicatesMerged)
	fmt.Fprintf(out, "no consent data:      %d (skipped, not subscribed)\n", c.NoConsent)
	fmt.Fprintf(out, "suppressions:         new=%d withdrawn-existing=%d already=%d\n", c.SuppressionsNew, c.SuppressionsApplied, c.SuppressionsUnchanged)
	fmt.Fprintf(out, "subscribers:          new=%d already-present=%d blocked-by-suppression=%d\n", c.SubscribersNew, c.SubscribersPresent, c.SubscribersSuppressed)
	for _, p := range probs {
		fmt.Fprintf(out, "problem %s line %d %s: %s\n", p.File, p.Line, p.Email, p.Reason)
	}
	return nil
}

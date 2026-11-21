// Package main is the arena-wp-orders-import command (P-14, spec 62 14.6): it
// loads the correspondence "WooCommerce order <-> AllPay order <-> platform
// order <-> tickets" exported when WordPress is stopped.
//
//	--org-id <uuid>     the organization (required)
//	--file <csv>        columns wc_order_id, allpay_order_id,
//	                    platform_order_system_id, ticket_system_ids
//	                    (ticket ids separated by ; , | or a space)
//	--dry-run           check and print the counts and every mismatch, write nothing
//	--allow-mismatches  store the valid rows even if some rows do not check out
//	                    (default: any mismatch stops the run before it writes)
//
// Idempotent: a second run of the same file reports every row as unchanged.
//
//	DATABASE_URL=... JWT_SIGNING_SECRET=x go run ./apps/backend/cmd/arena-wp-orders-import --org-id <uuid> --file orders.csv --dry-run
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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/wporders"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "arena-wp-orders-import: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("arena-wp-orders-import", flag.ContinueOnError)
	org := fs.String("org-id", "", "organization id (uuid)")
	file := fs.String("file", "", "CSV file")
	dry := fs.Bool("dry-run", false, "check and print, write nothing")
	allow := fs.Bool("allow-mismatches", false, "store the valid rows although some rows have mismatches")
	if err := fs.Parse(args); err != nil {
		return err
	}
	orgID, err := uuid.Parse(*org)
	if err != nil {
		return fmt.Errorf("--org-id wants an organization id (uuid): %w", err)
	}
	if *file == "" {
		return fmt.Errorf("--file is required")
	}
	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer f.Close()
	rows, bad, err := wporders.Parse(f)
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

	// Always a checking pass first, so a mismatch stops the run before any write.
	rep, err := wporders.Import(ctx, pool, orgID, rows, bad, true)
	if err != nil {
		return err
	}
	written := false
	if !*dry && (len(rep.Mismatches) == 0 || *allow) {
		if rep, err = wporders.Import(ctx, pool, orgID, rows, bad, false); err != nil {
			return err
		}
		written = true
	}
	mode := "DRY RUN"
	if written {
		mode = "APPLY"
	}
	fmt.Fprintf(out, "%s: rows=%d inserted=%d updated=%d unchanged=%d mismatches=%d\n",
		mode, rep.Rows, rep.Inserted, rep.Updated, rep.Unchanged, len(rep.Mismatches))
	for _, m := range rep.Mismatches {
		fmt.Fprintf(out, "mismatch line %d wc_order=%s %s: %s\n", m.Line, m.WCOrderID, m.Code, m.Detail)
	}
	if !*dry && !written {
		return fmt.Errorf("%d row(s) do not check out, nothing was written (fix the file or pass --allow-mismatches)", len(rep.Mismatches))
	}
	return nil
}

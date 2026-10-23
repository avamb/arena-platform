// Command billing-reconcile prints the billing reconcile report (task B2-5)
// for the database in DATABASE_URL as JSON. By default it only READS.
//
// With --collect and/or --rate it first runs the billing collect / rate
// passes until they are drained: that WRITES billable events and charges, so
// it is meant for a scratch copy of a stand and refuses to run without
// --copy, a promise that the database is such a copy.
//
//	DATABASE_URL=... go run ./apps/backend/tools/billing-reconcile --since 2026-10-01T00:00:00Z
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/collect"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/rate"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/reconcile"
)

func main() {
	since := flag.String("since", "", "the date X, RFC 3339 (required)")
	doCollect := flag.Bool("collect", false, "run billing collect passes first (writes; needs --copy)")
	doRate := flag.Bool("rate", false, "run billing rate passes first (writes; needs --copy)")
	isCopy := flag.Bool("copy", false, "confirm DATABASE_URL is a scratch copy")
	flag.Parse()
	if err := run(*since, *doCollect, *doRate, *isCopy); err != nil {
		fmt.Fprintln(os.Stderr, "billing-reconcile:", err)
		os.Exit(1)
	}
}

func run(sinceStr string, doCollect, doRate, isCopy bool) error {
	x, err := time.Parse(time.RFC3339, sinceStr)
	if err != nil {
		return fmt.Errorf("--since: %w", err)
	}
	if (doCollect || doRate) && !isCopy {
		return fmt.Errorf("--collect and --rate write to the database: add --copy only for a scratch copy")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	if doCollect {
		c := collect.New(gen.New(pool), collect.Options{Since: x})
		for i := 0; i < 200; i++ {
			res, err := c.Run(ctx)
			if err != nil {
				return fmt.Errorf("collect: %w", err)
			}
			inserted := 0
			for _, sr := range res {
				inserted += sr.Inserted
			}
			fmt.Fprintf(os.Stderr, "collect pass %d: inserted %d %+v\n", i+1, inserted, res)
			if inserted == 0 {
				break
			}
		}
	}
	if doRate {
		r := rate.New(pool, rate.Options{Since: x})
		for i := 0; i < 200; i++ {
			res, err := r.Run(ctx)
			if err != nil {
				return fmt.Errorf("rate: %w", err)
			}
			fmt.Fprintf(os.Stderr, "rate pass %d: rated=%d reversed=%d skipped=%d\n", i+1, res.Rated, res.Reversed, res.Skipped)
			if res.Rated+res.Reversed == 0 {
				break
			}
		}
	}

	rep, err := reconcile.New(pool, reconcile.Options{Since: x}).Run(ctx)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if encErr := enc.Encode(rep); encErr != nil {
		return encErr
	}
	return err
}

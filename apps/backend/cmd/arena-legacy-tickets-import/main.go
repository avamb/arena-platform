// Package main is the arena-legacy-tickets-import command (Vino&Co cutover,
// owner decision 2026-11-08): it files the tickets sold in the Bil24 era,
// which exist only in WordPress order meta, as platform orders, so the buyer
// sees them under "My tickets", the guest list and its automatic sends carry
// them, and the session counts them as sold.
//
//	--org-id <uuid>       the organization (required)
//	--file <json>         the output of ops/vino-cutover/wp-legacy-tickets-export.php (required)
//	--dry-run             the default: check, count and print, write nothing
//	--apply               write
//	--only-future         skip sessions that have already started (default true)
//	--channel-id <uuid>   file the orders under this sales channel instead of
//	                      the session's Bil24 gateway channel
//
// Each order is one transaction: the sold places are ADDED to the category
// (they were never among the platform's available places), the order
// (source legacy_import, paid), its tickets with the original barcode in the
// legacy_bil24 authority, and the wp_order_links row. Nothing is delivered:
// no outbox event, no webhook or MACS push, no letter, no billing usage.
//
// Idempotent: an order already filed (wp_order_links) is reported unchanged.
//
//	DATABASE_URL=... JWT_SIGNING_SECRET=x go run ./apps/backend/cmd/arena-legacy-tickets-import --org-id <uuid> --file legacy.json
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/legacytickets"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "arena-legacy-tickets-import: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("arena-legacy-tickets-import", flag.ContinueOnError)
	org := fs.String("org-id", "", "organization id (uuid)")
	file := fs.String("file", "", "JSON file of wp-legacy-tickets-export.php")
	dry := fs.Bool("dry-run", true, "check and print, write nothing (the default)")
	apply := fs.Bool("apply", false, "write the orders")
	onlyFuture := fs.Bool("only-future", true, "skip sessions that have already started")
	channel := fs.String("channel-id", "", "sales channel to file the orders under (default: the session's gateway channel)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *apply && flagSet(fs, "dry-run") && *dry {
		return fmt.Errorf("--apply and --dry-run contradict each other")
	}
	orgID, err := uuid.Parse(*org)
	if err != nil {
		return fmt.Errorf("--org-id wants an organization id (uuid): %w", err)
	}
	var channelID uuid.UUID
	if strings.TrimSpace(*channel) != "" {
		if channelID, err = uuid.Parse(*channel); err != nil {
			return fmt.Errorf("--channel-id wants a sales channel id (uuid): %w", err)
		}
	}
	if *file == "" {
		return fmt.Errorf("--file is required")
	}
	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer f.Close()
	doc, err := legacytickets.Parse(f)
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

	rep, err := legacytickets.Run(ctx, pool, legacytickets.Options{
		OrgID: orgID, ChannelID: channelID, OnlyFuture: *onlyFuture, Apply: *apply,
	}, doc)
	if err != nil {
		return err
	}
	Print(out, rep)
	for _, r := range rep.Refusals {
		if r.Code == legacytickets.RefuseWriteFailed {
			return fmt.Errorf("%d order(s) failed to write, see write_failed above", countCode(rep, legacytickets.RefuseWriteFailed))
		}
	}
	return nil
}

// Print writes the report: per session the counts, the sum and the places
// before and after, then every skip, refusal and warning.
func Print(out io.Writer, rep legacytickets.Report) {
	mode := "DRY RUN"
	if rep.Apply {
		mode = "APPLY"
	}
	fmt.Fprintf(out, "%s: orders_in_file=%d unchanged=%d refused=%d skipped=%d\n",
		mode, rep.Orders, rep.Unchanged, len(rep.Refusals), len(rep.SkipList))
	switch {
	case rep.AuthorityCreated:
		fmt.Fprintf(out, "barcode authority %s: created\n", legacytickets.AuthorityType)
	case rep.AuthorityMissing:
		fmt.Fprintf(out, "barcode authority %s: missing, --apply creates it\n", legacytickets.AuthorityType)
	}
	for _, s := range rep.Sessions {
		fmt.Fprintf(out, "\nsession %s  %s  %s  channel %s\n", s.SessionID, s.StartAt.UTC().Format(time.RFC3339), s.EventName, s.ChannelID)
		fmt.Fprintf(out, "  orders=%d tickets=%d sum=%s %s unchanged=%d\n", s.Orders, s.Tickets, minor(s.SumMinor), s.Currency, s.Unchanged)
		for _, t := range s.Tiers {
			fmt.Fprintf(out, "  category %q tickets=%d\n", t.Name, t.Tickets)
			fmt.Fprintf(out, "    before: capacity=%d places=%d sold=%d sold_upstream=%d available=%d\n",
				t.Before.Capacity, t.Before.Places, t.Before.Sold, t.Before.SoldUpstream, t.Before.Available)
			fmt.Fprintf(out, "    after:  capacity=%d places=%d sold=%d sold_upstream=%d available=%d\n",
				t.After.Capacity, t.After.Places, t.After.Sold, t.After.SoldUpstream, t.After.Available)
		}
		fmt.Fprintf(out, "  ledger (total, sold): before=%v after=%v\n", s.LedgerBefore, s.LedgerAfter)
		if len(s.SystemIDs) > 0 {
			fmt.Fprintf(out, "  order numbers written: %v\n", s.SystemIDs)
		}
	}
	if len(rep.Refusals) > 0 {
		fmt.Fprintln(out, "\nrefused:")
		for _, r := range rep.Refusals {
			fmt.Fprintf(out, "  wc_order=%s bil24_order=%d %s: %s\n", r.WCOrderID, r.Bil24OrderID, r.Code, r.Detail)
		}
	}
	if len(rep.SkipList) > 0 {
		fmt.Fprintf(out, "\nskipped: %v\n", rep.Skipped)
		for _, r := range rep.SkipList {
			if r.Code == legacytickets.SkipNotBil24Era {
				continue
			}
			fmt.Fprintf(out, "  wc_order=%s bil24_order=%d %s: %s\n", r.WCOrderID, r.Bil24OrderID, r.Code, r.Detail)
		}
	}
	if len(rep.Warnings) > 0 {
		fmt.Fprintln(out, "\nwarnings:")
		for _, w := range rep.Warnings {
			fmt.Fprintf(out, "  %s\n", w)
		}
	}
}

func minor(v int64) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)
}

func countCode(rep legacytickets.Report, code string) int {
	n := 0
	for _, r := range rep.Refusals {
		if r.Code == code {
			n++
		}
	}
	return n
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

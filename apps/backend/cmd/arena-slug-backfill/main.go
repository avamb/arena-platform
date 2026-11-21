// Package main is the arena-slug-backfill command (P-5): it gives a public
// page address (events.slug) to every live event that has none, by the rule
// the import and publication use (eventslug.Assign: the transliterated name,
// then -2, -3 … while the organization already has that slug).
//
//	--apply        write the slugs (default: a dry run that only prints them)
//	--org <uuid>   only this organization's events
//	--published    only events with status 'published' (default: every status)
//
// Idempotent: an event that already has a slug is never selected, and
// SetEventSlugIfEmpty refuses to overwrite one, so a second run changes nothing.
//
//	DATABASE_URL=... JWT_SIGNING_SECRET=x go run ./apps/backend/cmd/arena-slug-backfill --apply
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

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventslug"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "arena-slug-backfill: fatal: %v\n", err)
		os.Exit(1)
	}
}

type candidate struct {
	ID    uuid.UUID
	OrgID uuid.UUID
	Name  string
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("arena-slug-backfill", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "write the slugs (default: dry run)")
	org := fs.String("org", "", "only this organization id")
	published := fs.Bool("published", false, "only published events")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var orgID *uuid.UUID
	if *org != "" {
		id, err := uuid.Parse(*org)
		if err != nil {
			return fmt.Errorf("--org wants an organization id (uuid): %w", err)
		}
		orgID = &id
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

	rows, err := pool.Query(ctx, `
		SELECT id, org_id, name FROM events
		WHERE slug IS NULL AND deleted_at IS NULL
		  AND ($1::uuid IS NULL OR org_id = $1::uuid)
		  AND (NOT $2::boolean OR status = 'published')
		ORDER BY created_at, id`, orgID, *published)
	if err != nil {
		return fmt.Errorf("list events without a slug: %w", err)
	}
	var todo []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.ID, &c.OrgID, &c.Name); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	q := gen.New(pool)
	mode := "DRY RUN"
	if *apply {
		mode = "APPLY"
	}
	fmt.Fprintf(out, "%s: %d event(s) without a slug\n", mode, len(todo))
	set := 0
	for _, c := range todo {
		if !*apply {
			slug, err := eventslug.Free(ctx, q, c.OrgID, c.Name)
			if err != nil {
				return fmt.Errorf("event %s: %w", c.ID, err)
			}
			fmt.Fprintf(out, "would set %s  org=%s  %q -> %s\n", c.ID, c.OrgID, c.Name, slug)
			continue
		}
		slug, err := eventslug.Assign(ctx, q, c.OrgID, c.ID, c.Name)
		if err != nil {
			return fmt.Errorf("event %s: %w", c.ID, err)
		}
		if slug == "" {
			fmt.Fprintf(out, "skipped %s (got a slug meanwhile)\n", c.ID)
			continue
		}
		set++
		fmt.Fprintf(out, "set %s  org=%s  %q -> %s\n", c.ID, c.OrgID, c.Name, slug)
	}
	if *apply {
		fmt.Fprintf(out, "done: %d slug(s) set\n", set)
	}
	return nil
}

//go:build integration

// event_series_0221_integration_test.go — migration 0221 (spec 63 Л-2, tours):
// the composite foreign key keeps an event and its tour in one organization,
// tour and event slugs share one namespace (trigger, 23505
// event_series_slug_namespace) both ways, deleting a tour clears only
// events.series_id, and the Down section followed by the Up section works.
// Everything runs in one transaction that is rolled back.
package migrations_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func section0221(t *testing.T, which string) string {
	t.Helper()
	body, err := os.ReadFile("sql/0221_event_series.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(string(body), "-- +goose Down")
	if !ok {
		t.Fatal("no Down section")
	}
	_, up, ok = strings.Cut(up, "-- +goose Up")
	if !ok {
		t.Fatal("no Up section")
	}
	if which == "up" {
		return up
	}
	return down
}

// expectPgErr runs fn inside a savepoint and checks it fails with code and
// constraint; the savepoint is rolled back so the outer transaction goes on.
func expectPgErr(t *testing.T, ctx context.Context, tx pgx.Tx, what, code, constraint string, fn func(q *gen.Queries) error) {
	t.Helper()
	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Rollback(ctx)
	err = fn(gen.New(sp))
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("%s: want PgError %s, got %v", what, code, err)
	}
	if pgErr.Code != code || pgErr.ConstraintName != constraint {
		t.Fatalf("%s: got code %s constraint %q, want %s %q (%s)", what, pgErr.Code, pgErr.ConstraintName, code, constraint, pgErr.Message)
	}
}

func TestMigration0221_EventSeries(t *testing.T) {
	ctx := context.Background()
	conn := connectDB(t)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)

	stamp := time.Now().Format("150405.000000000")
	newOrg := func(tag string) uuid.UUID {
		var id uuid.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO organizations (name, slug) VALUES ($1, $2) RETURNING id`,
			"mig0221 "+tag, "mig0221-"+tag+"-"+stamp).Scan(&id); err != nil {
			t.Fatalf("org %s: %v", tag, err)
		}
		return id
	}
	newEvent := func(org uuid.UUID, slug string) uuid.UUID {
		var id uuid.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO events (org_id, name, slug) VALUES ($1, 'mig 0221', $2) RETURNING id`,
			org, slug).Scan(&id); err != nil {
			t.Fatalf("event %s: %v", slug, err)
		}
		return id
	}

	orgA, orgB := newOrg("a"), newOrg("b")
	q := gen.New(tx)

	tourA, err := q.CreateEventSeries(ctx, orgA, gen.EventSeriesParams{Slug: "world-tour", Name: "World tour", HideChildrenInLists: true})
	if err != nil {
		t.Fatalf("create tour A: %v", err)
	}
	tourB, err := q.CreateEventSeries(ctx, orgB, gen.EventSeriesParams{Slug: "other-tour", Name: "Other tour"})
	if err != nil {
		t.Fatalf("create tour B: %v", err)
	}
	eventA := newEvent(orgA, "prague")

	// A tour of another organization cannot be linked.
	expectPgErr(t, ctx, tx, "cross-org link", "23503", "events_series_fk", func(q *gen.Queries) error {
		_, err := q.SetEventSeries(ctx, eventA, orgA, &tourB.ID)
		return err
	})

	// The own tour links, and the batched readers see it.
	if ok, err := q.SetEventSeries(ctx, eventA, orgA, &tourA.ID); err != nil || !ok {
		t.Fatalf("link own tour: ok=%v err=%v", ok, err)
	}
	m, err := q.ListSeriesIDsForEvents(ctx, []uuid.UUID{eventA})
	if err != nil || m[eventA] != tourA.ID {
		t.Fatalf("ListSeriesIDsForEvents = %v, %v", m, err)
	}
	ids, err := q.ListEventIDsForSeries(ctx, tourA.ID, orgA)
	if err != nil || len(ids) != 1 || ids[0] != eventA {
		t.Fatalf("ListEventIDsForSeries = %v, %v", ids, err)
	}

	// Slug namespace, tour -> event: a tour may not take an active event's slug.
	expectPgErr(t, ctx, tx, "tour takes event slug", "23505", "event_series_slug_namespace", func(q *gen.Queries) error {
		_, err := q.CreateEventSeries(ctx, orgA, gen.EventSeriesParams{Slug: "prague", Name: "Clash"})
		return err
	})
	// ... nor by a rename.
	expectPgErr(t, ctx, tx, "tour renamed to event slug", "23505", "event_series_slug_namespace", func(q *gen.Queries) error {
		_, err := q.UpdateEventSeries(ctx, tourA.ID, orgA, gen.EventSeriesParams{Slug: "prague", Name: "World tour"})
		return err
	})
	// Event -> tour: an event may not take a tour's slug (any case).
	expectPgErr(t, ctx, tx, "event takes tour slug", "23505", "event_series_slug_namespace", func(q *gen.Queries) error {
		_, err := q.DB().Exec(ctx, `INSERT INTO events (org_id, name, slug) VALUES ($1, 'clash', 'World-Tour')`, orgA)
		return err
	})
	expectPgErr(t, ctx, tx, "event renamed to tour slug", "23505", "event_series_slug_namespace", func(q *gen.Queries) error {
		_, err := q.DB().Exec(ctx, `UPDATE events SET slug = 'world-tour' WHERE id = $1`, eventA)
		return err
	})
	// The same slug in another organization is free, and the readers agree.
	newEvent(orgB, "world-tour")
	if taken, err := q.SeriesSlugTaken(ctx, orgA, "PRAGUE", nil); err != nil || !taken {
		t.Fatalf("SeriesSlugTaken(prague) = %v, %v", taken, err)
	}
	if taken, err := q.SeriesSlugTaken(ctx, orgA, "world-tour", &tourA.ID); err != nil || taken {
		t.Fatalf("SeriesSlugTaken(own slug) = %v, %v", taken, err)
	}
	if taken, err := q.EventSlugTaken(ctx, orgA, "world-tour"); err != nil || !taken {
		t.Fatalf("EventSlugTaken(tour slug) = %v, %v", taken, err)
	}

	// Archive keeps the link; delete clears only series_id.
	if a, err := q.ArchiveEventSeries(ctx, tourA.ID, orgA); err != nil || a.ArchivedAt == nil {
		t.Fatalf("archive: %v", err)
	}
	if list, err := q.ListEventSeries(ctx, orgA, false); err != nil || len(list) != 0 {
		t.Fatalf("active list after archive = %d, %v", len(list), err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM event_series WHERE id = $1`, tourA.ID); err != nil {
		t.Fatalf("delete tour: %v", err)
	}
	var seriesID *uuid.UUID
	var orgID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT series_id, org_id FROM events WHERE id = $1`, eventA).Scan(&seriesID, &orgID); err != nil {
		t.Fatal(err)
	}
	if seriesID != nil || orgID != orgA {
		t.Fatalf("after delete: series_id=%v org_id=%v, want NULL and %v", seriesID, orgID, orgA)
	}

	// Down, then Up again.
	if _, err := tx.Exec(ctx, section0221(t, "down")); err != nil {
		t.Fatalf("down: %v", err)
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('event_series') IS NOT NULL
	    OR EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'events' AND column_name = 'series_id')`).Scan(&exists); err != nil || exists {
		t.Fatalf("after down: still present=%v err=%v", exists, err)
	}
	if _, err := tx.Exec(ctx, section0221(t, "up")); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if _, err := gen.New(tx).CreateEventSeries(ctx, orgA, gen.EventSeriesParams{Slug: "again", Name: "Again"}); err != nil {
		t.Fatalf("create after up again: %v", err)
	}
}

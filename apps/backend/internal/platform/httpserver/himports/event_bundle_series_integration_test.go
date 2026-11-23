//go:build integration

// event_bundle_series_integration_test.go — action.arenaSeriesId of the
// event-bundle import (migration 0222, spec 63 Л-2): absent keeps the tour,
// a UUID links an active tour, null or "" unlinks, and an unknown, foreign or
// archived tour answers 422 import.invalid_series at /action/arenaSeriesId
// (the dry run reports it too). The read-back bundle names the tour.
package himports

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestEventBundleSeries_LinkUnlinkInvalid(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)
	rig := newDryRunRig(t, pool)
	q := gen.New(pool)

	suffix := uuid.NewString()[:8]
	tour, err := q.CreateEventSeries(ctx, f.orgID, gen.EventSeriesParams{Slug: "bundle-tour-" + suffix, Name: "Bundle tour"})
	if err != nil {
		t.Fatal(err)
	}
	archived, err := q.CreateEventSeries(ctx, f.orgID, gen.EventSeriesParams{Slug: "bundle-old-" + suffix, Name: "Old tour"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.ArchiveEventSeries(ctx, archived.ID, f.orgID); err != nil {
		t.Fatal(err)
	}
	// A tour of ANOTHER organization.
	other := newBundle525Fixture(t, ctx, pool)
	defer other.cleanup()
	foreign, err := q.CreateEventSeries(ctx, other.orgID, gen.EventSeriesParams{Slug: "bundle-foreign-" + suffix, Name: "Foreign tour"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM event_series WHERE org_id = ANY($1)`, []uuid.UUID{f.orgID, other.orgID})
	}()

	seriesOf := func(eventID uuid.UUID) *uuid.UUID {
		t.Helper()
		var id *uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT series_id FROM events WHERE id = $1`, eventID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	val := func(s string) bil24compat.NullableString {
		return bil24compat.NullableString{Present: true, Value: &s}
	}

	// Create linked to the tour.
	body := f.payload()
	body.Action.ArenaSeriesID = val(tour.ID.String())
	rec, created := f.call(h, body)
	if rec.Code != http.StatusOK || !created.Created {
		t.Fatalf("create with a tour: %d %s", rec.Code, rec.Body.String())
	}
	if got := seriesOf(created.EventID); got == nil || *got != tour.ID {
		t.Fatalf("series_id after link = %v, want %s", got, tour.ID)
	}

	// The read-back bundle names the tour.
	read, raw := f.readBundles(h, created.EventID)
	if len(read.Bundles) != 1 {
		t.Fatalf("read: %s", raw)
	}
	if s := read.Bundles[0].Action.ArenaSeriesID; !s.Present || s.Value == nil || *s.Value != tour.ID.String() {
		t.Errorf("read-back arenaSeriesId = %+v, want %s", s, tour.ID)
	}
	if !strings.Contains(raw, `"arenaSeriesId":"`+tour.ID.String()+`"`) {
		t.Errorf("read-back JSON lacks arenaSeriesId: %s", raw)
	}

	// Absent keeps the link.
	body = f.payload()
	if rec, _ = f.call(h, body); rec.Code != http.StatusOK {
		t.Fatalf("absent: %d %s", rec.Code, rec.Body.String())
	}
	if got := seriesOf(created.EventID); got == nil || *got != tour.ID {
		t.Fatalf("absent arenaSeriesId changed the link: %v", got)
	}

	// null unlinks.
	body.Action.ArenaSeriesID = bil24compat.NullableString{Present: true}
	if rec, _ = f.call(h, body); rec.Code != http.StatusOK {
		t.Fatalf("null: %d %s", rec.Code, rec.Body.String())
	}
	if got := seriesOf(created.EventID); got != nil {
		t.Fatalf("null did not unlink: %v", got)
	}
	// An event without a tour is read back as "".
	read, raw = f.readBundles(h, created.EventID)
	if s := read.Bundles[0].Action.ArenaSeriesID; !s.Present || s.Value == nil || *s.Value != "" {
		t.Errorf("read-back of a lone event = %+v: %s", s, raw)
	}

	// "" unlinks too: link again, then clear with an empty string.
	body.Action.ArenaSeriesID = val(tour.ID.String())
	if rec, _ = f.call(h, body); rec.Code != http.StatusOK {
		t.Fatalf("relink: %d %s", rec.Code, rec.Body.String())
	}
	body.Action.ArenaSeriesID = val("")
	if rec, _ = f.call(h, body); rec.Code != http.StatusOK {
		t.Fatalf("empty string: %d %s", rec.Code, rec.Body.String())
	}
	if got := seriesOf(created.EventID); got != nil {
		t.Fatalf("empty string did not unlink: %v", got)
	}

	// Invalid: unknown, foreign, archived, not a UUID. Nothing changes; the
	// dry run reports each at the pointer.
	for name, bad := range map[string]string{
		"unknown":  uuid.NewString(),
		"foreign":  foreign.ID.String(),
		"archived": archived.ID.String(),
		"garbage":  "not-a-uuid",
	} {
		body.Action.ArenaSeriesID = val(bad)
		rec, _ := f.call(h, body)
		if rec.Code != http.StatusUnprocessableEntity || !(strings.Contains(rec.Body.String(), "import.invalid_series")) {
			t.Errorf("%s: import answered %d %s", name, rec.Code, rec.Body.String())
		}
		out := dryRun(t, f, rig.h, body)
		found := false
		for _, is := range out.Errors {
			if is.Field == "/action/arenaSeriesId" && is.Code == "import.invalid_series" {
				found = true
			}
		}
		if out.Valid || !found {
			t.Errorf("%s: dry run did not report the tour: %+v", name, out.Errors)
		}
		if got := seriesOf(created.EventID); got != nil {
			t.Errorf("%s: a refused import changed the link: %v", name, got)
		}
	}
}

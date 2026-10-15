//go:build integration

package himports

// event_bundle_dry_run_bil24_integration_test.go — review of task 3-1: the
// dry run of a source=bil24 bundle (the Bil24-format executor, which the
// arena tests do not reach) writes nothing either. It moves an imported
// session that has a paying buyer, asks for publication and a poster, and
// checks the database-wide outbox and job queue as well as the org's rows.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
)

// globalSideEffects counts the rows a leaked effect would add anywhere in the
// database, not just under the fixture's organization.
func globalSideEffects(ctx context.Context, t *testing.T, pool *pgxpool.Pool) map[string]int {
	t.Helper()
	out := map[string]int{}
	for name, sql := range map[string]string{
		"outbox_events":   `SELECT count(*) FROM outbox_events`,
		"worker_jobs":     `SELECT count(*) FROM worker_jobs`,
		"session_changes": `SELECT count(*) FROM session_changes`,
		"compat_ids":      `SELECT count(*) FROM compatibility_id_map`,
		"media_objects":   `SELECT count(*) FROM media_objects`,
	} {
		var n int
		if err := pool.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatalf("global count %s: %v", name, err)
		}
		out[name] = n
	}
	return out
}

func TestEventBundleDryRun_Bil24SourceWritesNothing(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newImport517Fixture(t, ctx, pool)
	defer f.cleanup()
	rig := newDryRunRig(t, pool)
	shim := &bundle525Fixture{t: t, pool: pool, orgID: f.orgID, userID: f.userID}

	// A brand-new Bil24 session: valid, and nothing is registered.
	fresh := f.payload()
	fresh.Source = bil24compat.SourceBil24
	fresh.Publish = true
	fresh.Action.BigPosterURL = "https://posters.example.test/bil24.png"
	globalBefore := globalSideEffects(ctx, t, pool)
	orgBefore := orgFootprint(ctx, t, pool, f.orgID)
	out := dryRun(t, shim, rig.h, fresh)
	if !out.Valid || out.EventID != nil || out.SessionID != nil {
		t.Fatalf("new bil24 session dry run: %+v", out)
	}
	assertSameFootprint(t, "bil24 new (global)", globalBefore, globalSideEffects(ctx, t, pool))
	assertSameFootprint(t, "bil24 new (org)", orgBefore, orgFootprint(ctx, t, pool, f.orgID))
	rig.assertNoOutsideEffects(t, "bil24 new")

	// Import it for real, then give it a paying buyer.
	rec, created := f.call(rig.h, f.payload())
	if rec.Code != 200 {
		t.Fatalf("real import: %d %s", rec.Code, rec.Body.String())
	}
	defer shim.seedPaidTicket(ctx, t, created.SessionID, created.EventID)()
	rig.audit.calls.Store(0)

	move := f.payload()
	move.Source = bil24compat.SourceBil24
	move.Publish = true
	move.Action.BigPosterURL = "https://posters.example.test/bil24.png"
	move.ActionEvent.Day = "03.05.2026"
	move.ChangeMessage = "Moved by a week."

	globalBefore = globalSideEffects(ctx, t, pool)
	orgBefore = orgFootprint(ctx, t, pool, f.orgID)
	out = dryRun(t, shim, rig.h, move)
	if out.Valid || len(out.Errors) != 1 || out.Errors[0].Code != "organization.contact_missing" || out.Errors[0].Status != 422 {
		t.Fatalf("bil24 move without a contact: %+v", out.Errors)
	}
	if _, err := pool.Exec(ctx, `UPDATE organizations SET contact_email = 'dry-run@example.com' WHERE id = $1`, f.orgID); err != nil {
		t.Fatal(err)
	}
	out = dryRun(t, shim, rig.h, move)
	if !out.Valid || out.SessionID == nil || *out.SessionID != created.SessionID {
		t.Fatalf("bil24 allowed move: %+v", out)
	}
	assertSameFootprint(t, "bil24 move (global)", globalBefore, globalSideEffects(ctx, t, pool))
	assertSameFootprint(t, "bil24 move (org)", orgBefore, orgFootprint(ctx, t, pool, f.orgID))
	rig.assertNoOutsideEffects(t, "bil24 move")

	var start string
	if err := pool.QueryRow(ctx, `SELECT to_char(start_at AT TIME ZONE 'UTC', 'YYYY-MM-DD') FROM sessions WHERE id = $1`,
		created.SessionID).Scan(&start); err != nil {
		t.Fatal(err)
	}
	if start != "2026-04-26" {
		t.Fatalf("the dry run moved the session to %s", start)
	}
}

// TestEventBundleDryRun_Bil24ForeignVenueLeaksNoTimezone: a bil24 bundle that
// names another organization's Bil24 venue id is refused with
// import.venue_owned_by_other_org, exactly like the import, and the report
// carries nothing read from that venue: neither its stored zone as a
// venue_timezone default nor the "venue keeps its stored timezone" warning.
func TestEventBundleDryRun_Bil24ForeignVenueLeaksNoTimezone(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	owner := newImport517Fixture(t, ctx, pool)
	defer owner.cleanup()
	if _, err := pool.Exec(ctx, `UPDATE organizations SET name = name || ' owner ' || id::text WHERE id = $1`, owner.orgID); err != nil {
		t.Fatal(err)
	}
	rig := newDryRunRig(t, pool)
	if rec, _ := owner.call(rig.h, owner.payload()); rec.Code != 200 {
		t.Fatalf("owner import: %d %s", rec.Code, rec.Body.String())
	}

	other := newImport517Fixture(t, ctx, pool)
	defer other.cleanup()
	shim := &bundle525Fixture{t: t, pool: pool, orgID: other.orgID, userID: other.userID}

	for _, tz := range []string{"", "Europe/Prague"} {
		body := other.payload()
		body.Source = bil24compat.SourceBil24
		body.Venue.VenueID = owner.venueID
		body.Venue.Timezone = tz
		out := dryRun(t, shim, rig.h, body)
		found := false
		for _, is := range out.Errors {
			if is.Code == "import.venue_owned_by_other_org" && is.Field == "/venue/venueId" && is.Status == 409 {
				found = true
			}
		}
		if out.Valid || !found {
			t.Fatalf("tz %q: foreign venue not refused: %+v", tz, out.Errors)
		}
		for _, d := range out.Defaults {
			if d.Name == "venue_timezone" {
				t.Errorf("tz %q: the foreign venue's zone leaked as a default: %+v", tz, d)
			}
		}
		for _, w := range out.Warnings {
			if w.Code == WarnVenueTimezoneKept {
				t.Errorf("tz %q: the foreign venue's zone leaked in a warning: %+v", tz, w)
			}
		}
	}
}

//go:build integration

package himports

// event_bundle_email_note_integration_test.go — task 7-1 (EC-31): the
// organizer's own letter text travels in the event-bundle as
// action.emailNote: absent keeps it, "" removes it, a string replaces it
// after the same filter as PATCH email_note, and the bundle read gives it
// back so a read-then-import round trip changes nothing.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func (f *bundle525Fixture) storedNote(ctx context.Context, t *testing.T, eventID uuid.UUID) (string, bool) {
	t.Helper()
	var note string
	err := f.pool.QueryRow(ctx, `SELECT note FROM event_email_notes WHERE event_id = $1`, eventID).Scan(&note)
	return note, err == nil
}

func TestEventBundleEmailNote_ImportReadAndRoundTrip(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	// Create with a note: <b> is filtered out, paragraphs are kept.
	body := f.payload()
	text := "Come <b>early</b>.\n\nMap: https://example.com/map"
	body.Action.EmailNote = &text
	rec, created := f.call(h, body)
	if rec.Code != http.StatusOK || !created.Created {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	const want = "Come early.\n\nMap: https://example.com/map"
	if got, ok := f.storedNote(ctx, t, created.EventID); !ok || got != want {
		t.Fatalf("stored = %q (%v), want %q", got, ok, want)
	}

	// The read gives it back.
	first, raw := f.readBundles(h, created.EventID)
	if len(first.Bundles) != 1 || first.Bundles[0].Action.EmailNote == nil || *first.Bundles[0].Action.EmailNote != want {
		t.Fatalf("read does not carry the note: %s", raw)
	}

	// Round trip: what was read is accepted by the dry run, re-importing it
	// changes no row (the notes table is part of the snapshot) and a second
	// read equals the first.
	before := catalogSnapshot(ctx, t, pool, f.orgID)
	if !strings.Contains(before, "Come early.") {
		t.Fatal("the snapshot does not include the event notes")
	}
	if dry := dryRun(t, f, h, first.Bundles[0]); !dry.Valid {
		t.Fatalf("dry run refuses the bundle that was read: %+v", dry.Errors)
	}
	f.reimport(h, first)
	if after := catalogSnapshot(ctx, t, pool, f.orgID); after != before {
		t.Fatalf("re-importing the read bundle changed rows:\nbefore %s\nafter  %s", before, after)
	}

	// Absent keeps.
	keep := first.Bundles[0]
	keep.Action.EmailNote = nil
	if rec, _ := f.call(h, keep); rec.Code != http.StatusOK {
		t.Fatalf("keep: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := f.storedNote(ctx, t, created.EventID); got != want {
		t.Fatalf("an absent emailNote changed the note to %q", got)
	}

	// A refused text is 422 import.invalid_email_note and writes nothing,
	// found by the dry run too.
	bad := first.Bundles[0]
	evil := "x javascript:alert(1)"
	bad.Action.EmailNote = &evil
	rec, _ = f.call(h, bad)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "import.invalid_email_note") {
		t.Fatalf("refused text: %d %s", rec.Code, rec.Body.String())
	}
	dry := dryRun(t, f, h, bad)
	found := false
	for _, e := range dry.Errors {
		if e.Code == "import.invalid_email_note" && e.Field == "/action/emailNote" {
			found = true
		}
	}
	if dry.Valid || !found {
		t.Fatalf("dry run missed the refused text: %+v", dry.Errors)
	}
	if got, _ := f.storedNote(ctx, t, created.EventID); got != want {
		t.Fatalf("a refused import changed the note to %q", got)
	}

	// "" removes; the read then answers "".
	clear := first.Bundles[0]
	empty := ""
	clear.Action.EmailNote = &empty
	if rec, _ := f.call(h, clear); rec.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := f.storedNote(ctx, t, created.EventID); ok {
		t.Fatal(`"" did not remove the note`)
	}
	again, _ := f.readBundles(h, created.EventID)
	if got := again.Bundles[0].Action.EmailNote; got == nil || *got != "" {
		t.Fatalf("read after clear: %v", got)
	}
}

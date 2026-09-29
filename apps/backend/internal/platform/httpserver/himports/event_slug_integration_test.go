//go:build integration

// event_slug_integration_test.go — an event created by an import gets a
// public slug at once, unique within the organization, and keeps it when
// the event is re-saved under its UUID (the Telegram bot's "Edit").
//
// Run with:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci?sslmode=disable \
//	    go test -tags integration -p 1 ./apps/backend/internal/platform/httpserver/himports/ \
//	    -run TestEventBundle_NewEventGetsAUniqueSlug
package himports

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/geoslug"
)

func TestEventBundle_NewEventGetsAUniqueSlug(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	slugOf := func(eventID uuid.UUID) string {
		t.Helper()
		var slug *string
		if err := pool.QueryRow(ctx, `SELECT slug FROM events WHERE id = $1`, eventID).Scan(&slug); err != nil {
			t.Fatalf("read slug: %v", err)
		}
		if slug == nil {
			return ""
		}
		return *slug
	}

	first := f.payload()
	first.Action.ActionName = "Ночь открытой сцены " + uuid.NewString()[:6]
	first.Action.FullActionName = "" // a.Name() prefers the full name when set
	rec, created := f.call(h, first)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	want := geoslug.Slugify(first.Action.ActionName)
	if got := slugOf(created.EventID); got == "" || got != want {
		t.Fatalf("slug of a new event = %q, want %q (transliterated name)", got, want)
	}

	// A second event with the SAME name gets the next free address.
	second := f.payload()
	second.ExternalRef = f.externalRef + ":twin"
	second.Action.ActionName = first.Action.ActionName
	second.Action.FullActionName = ""
	rec, twin := f.call(h, second)
	if rec.Code != http.StatusOK || twin.EventID == created.EventID {
		t.Fatalf("twin: %d %s", rec.Code, rec.Body.String())
	}
	if got := slugOf(twin.EventID); got != want+"-2" {
		t.Errorf("slug of the twin = %q, want %q", got, want+"-2")
	}

	// A re-save under the UUID with a new name keeps the address.
	edit := f.payload()
	edit.ExternalRef = ""
	edit.Action.ArenaEventID = created.EventID.String()
	edit.Action.ActionName = first.Action.ActionName + " (ред.)"
	edit.Action.FullActionName = ""
	edit.ActionEvent.ArenaSessionID = created.SessionID.String()
	rec, _ = f.call(h, edit)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}
	if got := slugOf(created.EventID); got != want {
		t.Errorf("slug after a rename = %q, want the original %q", got, want)
	}
}

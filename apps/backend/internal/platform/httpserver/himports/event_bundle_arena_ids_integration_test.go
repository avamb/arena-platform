//go:build integration

package himports

// An event center that never kept compat ids (the Telegram bot) edits an
// event by the arena UUIDs it listed a moment ago: action.arenaEventId,
// actionEvent.arenaSessionId and categoryList[].arenaTierId. With
// arenaSessionId the externalRef may be omitted and the session keeps the
// key it was created under; a foreign or unknown UUID is invalid, and a key
// that names another session is a conflict.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
)

func TestEventBundle_ArenaIDs_EditByUUIDWithoutExternalRef(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	rec, created := f.call(h, f.payload())
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	tiers := map[string]uuid.UUID{}
	rows, err := pool.Query(ctx, `SELECT id, name FROM ticket_tiers WHERE session_id = $1`, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id uuid.UUID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			t.Fatal(err)
		}
		tiers[name] = id
	}
	rows.Close()
	if len(tiers) != 2 {
		t.Fatalf("tiers after create = %v", tiers)
	}

	// The edit: no externalRef, everything addressed by UUID, one category
	// renamed and re-priced, the event renamed.
	edit := f.payload()
	edit.ExternalRef = ""
	edit.Action.ArenaEventID = created.EventID.String()
	edit.Action.ActionName = "Bundle525 Renamed"
	edit.Action.FullActionName = ""
	edit.ActionEvent.ArenaSessionID = created.SessionID.String()
	edit.CategoryList = []bil24compat.ImportSessionCategory{
		{ArenaTierID: tiers["Parter"].String(), CategoryPriceName: "Parterre", Price: 30, Availability: 100},
		{ArenaTierID: tiers["Balcony"].String(), CategoryPriceName: "Balcony", Price: 12.5, Availability: 40},
	}
	rec, edited := f.call(h, edit)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}
	if edited.EventID != created.EventID || edited.SessionID != created.SessionID || edited.Created {
		t.Fatalf("edit addressed the wrong rows: %+v vs %+v", edited, created)
	}
	var evName, ref string
	var tierName string
	var tierPrice int64
	var tierCount int
	if err := pool.QueryRow(ctx, `SELECT name FROM events WHERE id = $1`, created.EventID).Scan(&evName); err != nil || evName != "Bundle525 Renamed" {
		t.Fatalf("event name = %q (%v)", evName, err)
	}
	if err := pool.QueryRow(ctx, `SELECT external_ref FROM session_external_refs WHERE session_id = $1`, created.SessionID).Scan(&ref); err != nil || ref != f.externalRef {
		t.Fatalf("external ref after edit = %q (%v), want the original %q", ref, err, f.externalRef)
	}
	if err := pool.QueryRow(ctx, `SELECT name, price_amount FROM ticket_tiers WHERE id = $1`, tiers["Parter"]).Scan(&tierName, &tierPrice); err != nil || tierName != "Parterre" || tierPrice != 3000 {
		t.Fatalf("renamed tier = %q %d (%v)", tierName, tierPrice, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ticket_tiers WHERE session_id = $1 AND deleted_at IS NULL`, created.SessionID).Scan(&tierCount); err != nil || tierCount != 2 {
		t.Fatalf("tier count after rename = %d (%v), want 2 (no category minted afresh)", tierCount, err)
	}

	// Unknown or foreign UUIDs are invalid, never resolved.
	bad := func(label string, mutate func(*bil24compat.ImportSessionRequest), wantStatus int, wantCode string) {
		t.Helper()
		body := f.payload()
		body.ExternalRef = ""
		body.Action.ArenaEventID = created.EventID.String()
		body.ActionEvent.ArenaSessionID = created.SessionID.String()
		mutate(&body)
		rec, _ := f.call(h, body)
		if rec.Code != wantStatus || !strings.Contains(rec.Body.String(), wantCode) {
			t.Errorf("%s: status = %d body=%s, want %d %s", label, rec.Code, rec.Body.String(), wantStatus, wantCode)
		}
	}
	bad("unknown session", func(b *bil24compat.ImportSessionRequest) { b.ActionEvent.ArenaSessionID = uuid.NewString() },
		http.StatusUnprocessableEntity, "import.invalid_session")
	bad("session id not a uuid", func(b *bil24compat.ImportSessionRequest) { b.ActionEvent.ArenaSessionID = "nope" },
		http.StatusUnprocessableEntity, "import.invalid_session")
	bad("unknown event", func(b *bil24compat.ImportSessionRequest) { b.Action.ArenaEventID = uuid.NewString() },
		http.StatusUnprocessableEntity, "import.invalid_event")
	bad("unknown tier", func(b *bil24compat.ImportSessionRequest) {
		b.CategoryList[0].ArenaTierID = uuid.NewString()
	}, http.StatusUnprocessableEntity, "import.invalid_category")
	bad("no key and no session id", func(b *bil24compat.ImportSessionRequest) { b.ActionEvent.ArenaSessionID = "" },
		http.StatusUnprocessableEntity, "import.external_ref_required")

	// A second session of the same event, then a bundle whose key names the
	// first session while arenaSessionId names the second: a conflict, not a
	// silent move of the key.
	second := f.payload()
	second.ExternalRef = f.externalRef + ":second"
	second.Action.ArenaEventID = created.EventID.String()
	second.ActionEvent.Day = "27.04.2026"
	rec, secondRes := f.call(h, second)
	if rec.Code != http.StatusOK || secondRes.EventID != created.EventID || secondRes.SessionID == created.SessionID {
		t.Fatalf("second session: %d %s (%+v)", rec.Code, rec.Body.String(), secondRes)
	}
	bad("key of another session", func(b *bil24compat.ImportSessionRequest) {
		b.ExternalRef = f.externalRef
		b.ActionEvent.ArenaSessionID = secondRes.SessionID.String()
	}, http.StatusConflict, "import.external_ref_conflict")

	// A session addressed by UUID under the WRONG event is a mismatch.
	other := f.payload()
	other.ExternalRef = f.externalRef + ":other-event"
	other.Action.ActionName = "Another production"
	rec, otherRes := f.call(h, other)
	if rec.Code != http.StatusOK || otherRes.EventID == created.EventID {
		t.Fatalf("other event: %d %s", rec.Code, rec.Body.String())
	}
	bad("session of another event", func(b *bil24compat.ImportSessionRequest) {
		b.Action.ArenaEventID = otherRes.EventID.String()
	}, http.StatusConflict, "import.action_mismatch")
}

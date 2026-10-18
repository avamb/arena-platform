//go:build integration

package httpserver

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func publicationSessionStatus(t *testing.T, f *evStatusFixture, id uuid.UUID) string {
	t.Helper()
	var status string
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM sessions WHERE id=$1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestEventStatusHTTP_PublishSchedulesDraftSessions(t *testing.T) {
	f := newEvStatusFixture(t)
	eventID, draft := f.event("draft")
	f.exec(`UPDATE sessions SET status='draft' WHERE id=$1`, draft)
	_, foreignSession := f.event("draft")
	f.exec(`UPDATE sessions SET status='draft' WHERE id=$1`, foreignSession)

	// Copy complete, priced sessions into the same event; only active drafts
	// may change. A cancelled/completed session must never be revived.
	states := map[uuid.UUID]string{draft: "scheduled", foreignSession: "draft"}
	for _, status := range []string{"draft", "scheduled", "cancelled", "completed"} {
		id := uuid.New()
		f.exec(`INSERT INTO sessions (id,event_id,venue_id,start_at,end_at,capacity_total,status,currency,currency_source)
		 SELECT $1,event_id,venue_id,start_at,end_at,capacity_total,$3,currency,currency_source FROM sessions WHERE id=$2`, id, draft, status)
		f.exec(`INSERT INTO ticket_tiers (id,session_id,name,pricing_mode,price_amount,currency,capacity,unit_seq,is_open)
		 VALUES ($1,$2,'Standing','fixed',2500,'EUR',5,1,true)`, uuid.New(), id)
		states[id] = status
		if status == "draft" {
			states[id] = "scheduled"
		}
	}
	deleted := uuid.New()
	f.exec(`INSERT INTO sessions (id,event_id,venue_id,start_at,end_at,capacity_total,status,currency,currency_source,deleted_at)
	 SELECT $1,event_id,venue_id,start_at,end_at,capacity_total,'draft',currency,currency_source,now() FROM sessions WHERE id=$2`, deleted, draft)
	states[deleted] = "draft"

	if st, _ := f.do(f.readKey, http.MethodPost, f.base(eventID)+"/status", `{"status":"published"}`); st != http.StatusForbidden {
		t.Fatalf("read-only key published event: %d", st)
	}
	if got := publicationSessionStatus(t, f, draft); got != "draft" {
		t.Fatalf("denied publish changed session to %q", got)
	}
	if st, body := f.do(f.key, http.MethodPost, f.base(eventID)+"/status", `{"status":"published"}`); st != http.StatusOK {
		t.Fatalf("publish: %d %v", st, body)
	}
	for id, want := range states {
		if got := publicationSessionStatus(t, f, id); got != want {
			t.Errorf("session %s status %q, want %q", id, got, want)
		}
	}
	if f.status(eventID) != "published" || !f.feedHas(eventID) {
		t.Error("published event must remain visible in its existing feed")
	}
	var scheduled int
	if err := f.pool.QueryRow(context.Background(), `SELECT (metadata->>'scheduled_sessions')::int FROM audit_events WHERE action='v1.event.status_update' AND resource_id=$1`, eventID.String()).Scan(&scheduled); err != nil || scheduled != 2 {
		t.Fatalf("publication audit must record two scheduled dates: count=%d err=%v", scheduled, err)
	}
	// A repeated publish remains a no-op, rather than publishing a date added
	// later behind the operator's back.
	f.exec(`UPDATE sessions SET status='draft' WHERE id=$1`, draft)
	if st, _ := f.do(f.key, http.MethodPost, f.base(eventID)+"/status", `{"status":"published"}`); st != http.StatusOK {
		t.Fatalf("repeat: %d", st)
	}
	if publicationSessionStatus(t, f, draft) != "draft" || len(f.auditMoves(eventID)) != 1 {
		t.Error("repeated publish must not change sessions or add an audit row")
	}
}

func TestEventStatusHTTP_PublishGateKeepsSessionsDraft(t *testing.T) {
	f := newEvStatusFixture(t)
	eventID, sessionID := f.event("draft")
	f.exec(`UPDATE sessions SET status='draft' WHERE id=$1`, sessionID)
	f.exec(`DELETE FROM ticket_tiers WHERE session_id=$1`, sessionID)
	st, body := f.do(f.key, http.MethodPost, f.base(eventID)+"/status", `{"status":"published"}`)
	if st != http.StatusUnprocessableEntity || evCode(body) != "event.publish_requires_priced_tier" {
		t.Fatalf("missing category: %d %v", st, body)
	}
	if f.status(eventID) != "draft" || publicationSessionStatus(t, f, sessionID) != "draft" || len(f.auditMoves(eventID)) != 0 {
		t.Error("failed gate must leave event, session and audit unchanged")
	}
}

func TestEventStatusHTTP_SessionFailureRollsBackPublish(t *testing.T) {
	f := newEvStatusFixture(t)
	eventID, sessionID := f.event("draft")
	f.exec(`UPDATE sessions SET status='draft' WHERE id=$1`, sessionID)
	name := "test_publish_" + f.suffix
	f.exec(`CREATE FUNCTION ` + name + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'controlled session update failure'; END; $$`)
	f.exec(`CREATE TRIGGER ` + name + ` BEFORE UPDATE OF status ON sessions FOR EACH ROW WHEN (OLD.id='` + sessionID.String() + `'::uuid) EXECUTE FUNCTION ` + name + `()`)
	t.Cleanup(func() {
		f.exec(`DROP TRIGGER IF EXISTS ` + name + ` ON sessions`)
		f.exec(`DROP FUNCTION IF EXISTS ` + name + `()`)
	})
	st, body := f.do(f.key, http.MethodPost, f.base(eventID)+"/status", `{"status":"published"}`)
	if st != http.StatusInternalServerError || evCode(body) != "event.schedule_sessions_failed" {
		t.Fatalf("forced session failure: %d %v", st, body)
	}
	if f.status(eventID) != "draft" || publicationSessionStatus(t, f, sessionID) != "draft" || len(f.auditMoves(eventID)) != 0 {
		t.Error("session failure must roll back event publication and audit")
	}
}

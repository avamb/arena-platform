//go:build integration

// control_session_integration_test.go — MA-40 (migration 0228): the event
// watcher never tracks or announces a control_only session. A container event
// that was (against the invariant) published produces no snapshot row, while an
// ordinary published event next to it does; the same session written as a
// platform session IS tracked, which proves the check can see it.
package eventwatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWatcher_ControlSession_NoSnapshot(t *testing.T) {
	pool := watchPool(t)
	ctx := context.Background()
	nonce := strings.ReplaceAll(uuid.NewString()[:8], "-", "")

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", strings.Join(strings.Fields(sql)[:3], " "), err)
		}
	}
	// replicaExec writes with a connection that fires no trigger (the guards
	// of migration 0228 refuse a published control container). The connection
	// is closed, never returned to the pool.
	replicaExec := func(sql string, args ...any) {
		t.Helper()
		pc, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		conn := pc.Hijack()
		defer func() { _ = conn.Close(ctx) }()
		if _, err := conn.Exec(ctx, `SET session_replication_role = replica`); err != nil {
			t.Fatalf("cannot switch triggers off (needs a superuser): %v", err)
		}
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%.60s: %v", sql, err)
		}
	}

	orgID, venueID := uuid.New(), uuid.New()
	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "Watch Ctl Org "+nonce, "watch-ctl-"+nonce)
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, venueID, orgID, "Watch Ctl Venue "+nonce)
	ordEvent, ordSession, ctlEvent, ctlSession := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	t.Cleanup(func() {
		bg := context.Background()
		for _, id := range []uuid.UUID{ordEvent, ctlEvent} {
			_, _ = pool.Exec(bg, `DELETE FROM event_watch_snapshots WHERE event_id = $1`, id)
			_, _ = pool.Exec(bg, `DELETE FROM ticket_tiers WHERE session_id IN (SELECT id FROM sessions WHERE event_id = $1)`, id)
			_, _ = pool.Exec(bg, `DELETE FROM sessions WHERE event_id = $1`, id)
			_, _ = pool.Exec(bg, `DELETE FROM events WHERE id = $1`, id)
		}
		_, _ = pool.Exec(bg, `DELETE FROM venues WHERE id = $1`, venueID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	ctlName, ordName := "Ctl Box "+nonce, "Ordinary Show "+nonce
	exec(`INSERT INTO events (id, org_id, name, status, slug) VALUES ($1, $2, $3, 'published', $4)`, ordEvent, orgID, ordName, "ord-"+nonce)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
	      VALUES ($1, $2, $3, '2031-03-01T19:00:00Z', '2031-03-01T21:00:00Z', 100, 'EUR', 'override')`, ordSession, ordEvent, venueID)
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency) VALUES ($1, $2, 'General', 'fixed', 2500, 'EUR')`, uuid.New(), ordSession)
	// The control container: a draft first (what the API makes), then forced to published.
	exec(`INSERT INTO events (id, org_id, name, status, slug) VALUES ($1, $2, $3, 'draft', $4)`, ctlEvent, orgID, ctlName, "ctl-"+nonce)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source, sales_mode)
	      VALUES ($1, $2, $3, '2031-03-02T19:00:00Z', '2031-03-02T22:00:00Z', 100, 'EUR', 'override', 'control_only')`, ctlSession, ctlEvent, venueID)
	replicaExec(`UPDATE events SET status = 'published' WHERE id = $1`, ctlEvent)

	ann := &captureAnnouncer{}
	w := NewWatcher(Options{Pool: pool, Announcer: ann, TicketsBaseURL: "https://tickets.test"})
	t0 := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	pass := func(at time.Time) {
		t.Helper()
		if _, err := w.RunOnce(ctx, at); err != nil {
			t.Fatalf("RunOnce at %s: %v", at.Format(time.RFC3339), err)
		}
	}
	rowFor := func(id uuid.UUID) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_watch_snapshots WHERE event_id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Load: the ordinary event is read, the control container is not.
	events, err := Load(ctx, pool)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var sawOrd, sawCtl bool
	for _, e := range events {
		sawOrd = sawOrd || e.ID == ordEvent.String()
		sawCtl = sawCtl || e.ID == ctlEvent.String()
	}
	if !sawOrd || sawCtl {
		t.Fatalf("Load: ordinary event read %v, control container read %v, want true and false", sawOrd, sawCtl)
	}

	// Passes: whichever of them seeds or observes, the control event never
	// gets a row and is never announced; the ordinary one does.
	pass(t0)
	pass(t0.Add(10 * time.Minute))
	pass(t0.Add(12 * time.Minute))
	if n := rowFor(ordEvent); n != 1 {
		t.Errorf("the ordinary event has %d snapshot rows, want 1", n)
	}
	if n := rowFor(ctlEvent); n != 0 {
		t.Errorf("the control event has %d snapshot rows, want none", n)
	}
	if got := ann.about(ctlName); len(got) != 0 {
		t.Errorf("the control event was announced: %v", got)
	}

	// Positive control: the same session as a platform session is tracked.
	replicaExec(`UPDATE sessions SET sales_mode = 'platform' WHERE id = $1`, ctlSession)
	pass(t0.Add(20 * time.Minute))
	if n := rowFor(ctlEvent); n != 1 {
		t.Errorf("the platform-mode session's event has %d snapshot rows, want 1: the check cannot see a leak", n)
	}
}

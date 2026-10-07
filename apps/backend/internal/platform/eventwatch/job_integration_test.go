//go:build integration

// job_integration_test.go — the watcher against a live PostgreSQL: what it
// records silently on its first pass, when a new event is announced, what a
// change message says, that a half-finished edit is not announced, and that
// nothing is ever said twice. It drives the real Watcher and the real tables;
// only the Telegram delivery is a capture.
//
// It resets event_watch_snapshots / event_watch_state, so run it on a scratch
// database (the CI-Integration recipe in AGENTS.md), never on the shared dev
// stand:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci_watch?sslmode=disable \
//	    go test -tags integration -p 1 ./apps/backend/internal/platform/eventwatch/
package eventwatch

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type captureAnnouncer struct {
	mu    sync.Mutex
	texts []string
}

func (c *captureAnnouncer) AnnounceEventChange(_ context.Context, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.texts = append(c.texts, text)
}

// about returns (and forgets) the messages that mention marker.
func (c *captureAnnouncer) about(marker string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out, rest []string
	for _, t := range c.texts {
		if strings.Contains(t, marker) {
			out = append(out, t)
		} else {
			rest = append(rest, t)
		}
	}
	c.texts = rest
	return out
}

func watchPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("cannot connect to PostgreSQL (%v); skipping", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestWatcher_Integration(t *testing.T) {
	pool := watchPool(t)
	ctx := context.Background()
	nonce := strings.ReplaceAll(uuid.NewString()[:8], "-", "")

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", strings.Join(strings.Fields(sql)[:3], " "), err)
		}
	}
	exec(`DELETE FROM event_watch_snapshots`)
	exec(`DELETE FROM event_watch_state`)

	orgID := uuid.New()
	orgSlug := "watch-" + nonce
	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "Watch Org "+nonce, orgSlug)
	venueID := uuid.New()
	exec(`INSERT INTO venues (id, org_id, name, city_id, timezone, address_line1)
	      VALUES ($1, $2, $3, (SELECT id FROM cities WHERE slug = 'tallinn'), 'Europe/Madrid', 'Calle Mayor 1')`,
		venueID, orgID, "Watch Venue "+nonce)
	var created []uuid.UUID
	t.Cleanup(func() {
		bg := context.Background()
		for _, id := range created {
			_, _ = pool.Exec(bg, `DELETE FROM event_watch_snapshots WHERE event_id = $1`, id)
			_, _ = pool.Exec(bg, `DELETE FROM ticket_tiers WHERE session_id IN (SELECT id FROM sessions WHERE event_id = $1)`, id)
			_, _ = pool.Exec(bg, `DELETE FROM sessions WHERE event_id = $1`, id)
			_, _ = pool.Exec(bg, `DELETE FROM events WHERE id = $1`, id)
		}
		_, _ = pool.Exec(bg, `DELETE FROM venues WHERE id = $1`, venueID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	newEvent := func(name, status, slug string) (eventID, sessionID, tierID uuid.UUID) {
		t.Helper()
		eventID, sessionID, tierID = uuid.New(), uuid.New(), uuid.New()
		created = append(created, eventID)
		exec(`INSERT INTO events (id, org_id, name, status, slug) VALUES ($1, $2, $3, $4, $5)`, eventID, orgID, name, status, slug)
		exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
		      VALUES ($1, $2, $3, '2031-03-01T19:00:00Z', '2031-03-01T21:00:00Z', 100, 'EUR', 'override')`, sessionID, eventID, venueID)
		exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency)
		      VALUES ($1, $2, 'General', 'fixed', 2500, 'EUR')`, tierID, sessionID)
		return
	}

	ann := &captureAnnouncer{}
	w := NewWatcher(Options{Pool: pool, Announcer: ann, TicketsBaseURL: "https://tickets.test"})
	t0 := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	pass := func(at time.Time) int {
		t.Helper()
		n, err := w.RunOnce(ctx, at)
		if err != nil {
			t.Fatalf("RunOnce at %s: %v", at.Format(time.RFC3339), err)
		}
		return n
	}

	// ── 1. The first pass records what exists and says nothing. ─────────────
	oldName := "Old Show " + nonce
	oldID, oldSession, oldTier := newEvent(oldName, "published", "old-"+nonce)
	if n := pass(t0); n != 0 {
		t.Fatalf("the first pass announced %d messages, want 0", n)
	}
	if got := ann.about(oldName); len(got) != 0 {
		t.Fatalf("an event that existed before the watcher must not be announced: %v", got)
	}
	if n := pass(t0.Add(5 * time.Minute)); n != 0 {
		t.Fatalf("an unchanged catalogue announced %d messages", n)
	}

	// ── 2. A new published event is announced once, after it stayed put. ────
	freshName := "Fresh Show " + nonce
	newEvent(freshName, "published", "fresh-"+nonce)
	pass(t0.Add(10 * time.Minute))                // first sight: the clock starts
	pass(t0.Add(10*time.Minute + 30*time.Second)) // not stable for long enough
	if got := ann.about(freshName); len(got) != 0 {
		t.Fatalf("announced too early: %v", got)
	}
	pass(t0.Add(11*time.Minute + 5*time.Second)) // stable for over a minute
	got := ann.about(freshName)
	if len(got) != 1 {
		t.Fatalf("want one 'new event' message, got %d: %v", len(got), got)
	}
	for _, want := range []string{"New event published", "Watch Org " + nonce, "Sat 01 Mar 2031, 20:00", "General 25.00 EUR", "poster: none", "https://tickets.test/" + orgSlug + "/fresh-" + nonce} {
		if !strings.Contains(got[0], want) {
			t.Errorf("new-event message lacks %q:\n%s", want, got[0])
		}
	}
	pass(t0.Add(15 * time.Minute))
	pass(t0.Add(16 * time.Minute))
	if got := ann.about(freshName); len(got) != 0 {
		t.Fatalf("an announced event was announced again: %v", got)
	}

	// ── 3. A change to what the buyer sees is announced once. ───────────────
	t1 := t0.Add(30 * time.Minute)
	renamed := "Old Show Renamed " + nonce
	exec(`UPDATE events SET name = $2, image_url = 'https://cdn.test/p.png' WHERE id = $1`, oldID, renamed)
	exec(`UPDATE sessions SET start_at = '2031-03-02T19:00:00Z', end_at = '2031-03-02T21:00:00Z' WHERE id = $1`, oldSession)
	exec(`UPDATE ticket_tiers SET price_amount = 2750 WHERE id = $1`, oldTier)
	pass(t1)
	pass(t1.Add(61 * time.Second))
	got = ann.about(renamed)
	if len(got) != 1 {
		t.Fatalf("want one 'changed' message, got %d: %v", len(got), got)
	}
	for _, want := range []string{
		"Event changed", "Name: «" + oldName + "» → «" + renamed + "»", "Poster: added",
		"Date moved:", "Price «General»: 25.00 EUR → 27.50 EUR", "https://tickets.test/" + orgSlug + "/old-" + nonce,
	} {
		if !strings.Contains(got[0], want) {
			t.Errorf("changed message lacks %q:\n%s", want, got[0])
		}
	}
	pass(t1.Add(5 * time.Minute))
	if got := ann.about(renamed); len(got) != 0 {
		t.Fatalf("a change was announced twice: %v", got)
	}

	// ── 4. A change that is still being made is announced once, as it ends. ─
	t2 := t0.Add(60 * time.Minute)
	exec(`UPDATE events SET name = $2 WHERE id = $1`, oldID, "Half "+nonce)
	pass(t2)
	exec(`UPDATE events SET name = $2 WHERE id = $1`, oldID, "Final "+nonce)
	pass(t2.Add(30 * time.Second))
	pass(t2.Add(60 * time.Second)) // only 30 s since the last edit
	if got := ann.about(nonce); len(got) != 0 {
		t.Fatalf("a still-changing event was announced: %v", got)
	}
	pass(t2.Add(91 * time.Second))
	got = ann.about(nonce)
	if len(got) != 1 || !strings.Contains(got[0], "Final "+nonce) || strings.Contains(got[0], "Half "+nonce) {
		t.Fatalf("want one message that ends at the final name, got %v", got)
	}

	// ── 5. A draft is silent until it is published; republishing is silent. ─
	t3 := t0.Add(90 * time.Minute)
	draftName := "Draft Show " + nonce
	draftID, _, _ := newEvent(draftName, "draft", "draft-"+nonce)
	pass(t3)
	pass(t3.Add(2 * time.Minute))
	if got := ann.about(draftName); len(got) != 0 {
		t.Fatalf("a draft was announced: %v", got)
	}
	exec(`UPDATE events SET status = 'published' WHERE id = $1`, draftID)
	pass(t3.Add(3 * time.Minute))
	pass(t3.Add(4*time.Minute + 5*time.Second))
	if got := ann.about(draftName); len(got) != 1 || !strings.Contains(got[0], "New event published") {
		t.Fatalf("a published draft is a new event: %v", got)
	}
	exec(`UPDATE events SET status = 'archived' WHERE id = $1`, draftID)
	pass(t3.Add(6 * time.Minute))
	exec(`UPDATE events SET status = 'published' WHERE id = $1`, draftID)
	pass(t3.Add(7 * time.Minute))
	pass(t3.Add(9 * time.Minute))
	if got := ann.about(draftName); len(got) != 0 {
		t.Fatalf("republishing an unchanged event was announced: %v", got)
	}
}

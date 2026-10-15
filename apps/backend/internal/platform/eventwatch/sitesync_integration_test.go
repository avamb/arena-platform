//go:build integration

// sitesync_integration_test.go — the storefront status block and the site
// follow-ups (migration 0152) against a live PostgreSQL: an announcement lists
// the live channel as updated and the site as waiting, a catalog fetch after
// the change brings exactly one confirmation, a silent site gets exactly one
// warning after 30 minutes (and still a confirmation later), a newer change
// supersedes the older one, a site that fetched before the announcement is
// "updated" at once, and an unpublished event drops its pending rows.
//
// Like job_integration_test.go it resets the watcher's tables, so run it on a
// scratch database only (AGENTS.md CI-Integration recipe).
package eventwatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWatcher_SiteSync_Integration(t *testing.T) {
	pool := watchPool(t)
	ctx := context.Background()
	nonce := strings.ReplaceAll(uuid.NewString()[:8], "-", "")

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", strings.Join(strings.Fields(sql)[:3], " "), err)
		}
	}
	exec(`DELETE FROM event_watch_site_sync`)
	exec(`DELETE FROM event_watch_snapshots`)
	exec(`DELETE FROM event_watch_state`)

	orgID := uuid.New()
	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "Sync Org "+nonce, "sync-"+nonce)
	venueID := uuid.New()
	exec(`INSERT INTO venues (id, org_id, name, city_id, timezone, address_line1)
	      VALUES ($1, $2, $3, (SELECT id FROM cities WHERE slug = 'tallinn'), 'Europe/Madrid', 'Calle Mayor 1')`,
		venueID, orgID, "Sync Venue "+nonce)

	siteName := "site-" + nonce + ".test"
	idleName := "idle-" + nonce + ".test"
	liveName := "Arena page " + nonce
	siteID, idleID, deadID, liveID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO sales_channels (id, org_id, name, settings) VALUES ($1, $2, $3, '{"gateway":{"token_hash":"h1","enabled":true}}')`, siteID, orgID, siteName)
	// A legacy-shape site the event is published to that never fetched: listed
	// as idle, never tracked.
	exec(`INSERT INTO sales_channels (id, org_id, name, settings) VALUES ($1, $2, $3, '{"gateway_token_hash":"h2"}')`, idleID, orgID, idleName)
	// A site that is neither published to nor fetching: not listed at all.
	exec(`INSERT INTO sales_channels (id, org_id, name, settings) VALUES ($1, $2, $3, '{"gateway":{"token_hash":"h3"}}')`, deadID, orgID, "dead-"+nonce+".test")
	exec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`, liveID, orgID, liveName)
	liveToken, idleToken := uuid.New(), uuid.New()
	exec(`INSERT INTO agent_feed_tokens (id, token, sales_channel_id) VALUES ($1, $2, $3)`, liveToken, "tok-live-"+nonce, liveID)
	exec(`INSERT INTO agent_feed_tokens (id, token, sales_channel_id) VALUES ($1, $2, $3)`, idleToken, "tok-idle-"+nonce, idleID)

	eventID, sessionID, tierID := uuid.New(), uuid.New(), uuid.New()
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM event_watch_site_sync WHERE event_id = $1`, eventID)
		_, _ = pool.Exec(bg, `DELETE FROM event_watch_snapshots WHERE event_id = $1`, eventID)
		_, _ = pool.Exec(bg, `DELETE FROM event_publications WHERE event_id = $1`, eventID)
		_, _ = pool.Exec(bg, `DELETE FROM ticket_tiers WHERE id = $1`, tierID)
		_, _ = pool.Exec(bg, `DELETE FROM sessions WHERE id = $1`, sessionID)
		_, _ = pool.Exec(bg, `DELETE FROM events WHERE id = $1`, eventID)
		_, _ = pool.Exec(bg, `DELETE FROM agent_feed_tokens WHERE id IN ($1, $2)`, liveToken, idleToken)
		_, _ = pool.Exec(bg, `DELETE FROM channel_catalog_fetches WHERE channel_id IN ($1, $2, $3, $4)`, siteID, idleID, deadID, liveID)
		_, _ = pool.Exec(bg, `DELETE FROM sales_channels WHERE id IN ($1, $2, $3, $4)`, siteID, idleID, deadID, liveID)
		_, _ = pool.Exec(bg, `DELETE FROM venues WHERE id = $1`, venueID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	name := "Sync Show " + nonce
	exec(`INSERT INTO events (id, org_id, name, status, slug) VALUES ($1, $2, $3, 'published', $4)`, eventID, orgID, name, "sync-show-"+nonce)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
	      VALUES ($1, $2, $3, '2031-03-01T19:00:00Z', '2031-03-01T21:00:00Z', 100, 'EUR', 'override')`, sessionID, eventID, venueID)
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency)
	      VALUES ($1, $2, 'General', 'fixed', 2500, 'EUR')`, tierID, sessionID)
	exec(`INSERT INTO event_publications (event_id, feed_token_id) VALUES ($1, $2), ($1, $3)`, eventID, liveToken, idleToken)

	t0 := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	fetchedAt := func(at time.Time) {
		t.Helper()
		exec(`INSERT INTO channel_catalog_fetches (channel_id, last_fetched_at) VALUES ($1, $2)
		      ON CONFLICT (channel_id) DO UPDATE SET last_fetched_at = EXCLUDED.last_fetched_at`, siteID, at)
	}
	// The site is active: it fetched an hour before anything happens.
	fetchedAt(t0.Add(-time.Hour))

	ann := &captureAnnouncer{}
	w := NewWatcher(Options{Pool: pool, Announcer: ann, TicketsBaseURL: "https://tickets.test"})
	pass := func(at time.Time) {
		t.Helper()
		if _, err := w.RunOnce(ctx, at); err != nil {
			t.Fatalf("RunOnce at %s: %v", at.Format(time.RFC3339), err)
		}
	}
	price := func(amount int) {
		t.Helper()
		exec(`UPDATE ticket_tiers SET price_amount = $2 WHERE id = $1`, tierID, amount)
	}
	pending := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_watch_site_sync WHERE event_id = $1 AND confirmed_at IS NULL`, eventID).Scan(&n); err != nil {
			t.Fatalf("count pending: %v", err)
		}
		return n
	}

	pass(t0) // seeds silently
	if got := ann.about(nonce); len(got) != 0 {
		t.Fatalf("seeding announced: %v", got)
	}

	// ── 1. The announcement lists every storefront; the site is waiting. ────
	t1 := t0.Add(10 * time.Minute)
	price(2750)
	pass(t1)
	pass(t1.Add(61 * time.Second))
	got := ann.about(nonce)
	if len(got) != 1 {
		t.Fatalf("want one 'changed' message, got %d: %v", len(got), got)
	}
	for _, want := range []string{
		"Price «General»: 25.00 EUR → 27.50 EUR",
		"🛒 <b>Storefronts</b>",
		"• " + liveName + ": updated",
		"• " + siteName + ": waiting for sync",
		"• " + idleName + ": no catalog fetch for over 7 days",
	} {
		if !strings.Contains(got[0], want) {
			t.Errorf("announcement lacks %q:\n%s", want, got[0])
		}
	}
	if strings.Contains(got[0], "dead-"+nonce) {
		t.Errorf("a site that neither fetches nor is published to must not be listed:\n%s", got[0])
	}
	if n := pending(); n != 1 {
		t.Fatalf("pending site rows = %d, want 1 (the idle site is not tracked)", n)
	}
	pass(t1.Add(2 * time.Minute))
	if got := ann.about(nonce); len(got) != 0 {
		t.Fatalf("no fetch yet, but a follow-up went out: %v", got)
	}

	// ── 2. A fetch that started before the change does not confirm it. ─────
	fetchedAt(t1.Add(-10 * time.Second))
	pass(t1.Add(3 * time.Minute))
	if got := ann.about(nonce); len(got) != 0 {
		t.Fatalf("a fetch older than the change confirmed it: %v", got)
	}

	// ── 3. A fetch after the change: exactly one confirmation. ──────────────
	fetchedAt(t1.Add(7 * time.Minute))
	pass(t1.Add(8 * time.Minute))
	got = ann.about(nonce)
	if len(got) != 1 || got[0] != "✅ <b>"+name+"</b> — "+siteName+" picked up the change (after 7 min)" {
		t.Fatalf("want one confirmation after 7 min, got %v", got)
	}
	pass(t1.Add(9 * time.Minute))
	pass(t1.Add(60 * time.Minute))
	if got := ann.about(nonce); len(got) != 0 {
		t.Fatalf("a confirmed site was reported again: %v", got)
	}

	// ── 4. A newer change supersedes the pending older one. ─────────────────
	t2 := t0.Add(2 * time.Hour)
	price(3000)
	pass(t2)
	pass(t2.Add(61 * time.Second))
	if got := ann.about(nonce); len(got) != 1 || !strings.Contains(got[0], siteName+": waiting for sync") {
		t.Fatalf("want one announcement with the site waiting, got %v", got)
	}
	t3 := t2.Add(10 * time.Minute)
	price(3200)
	pass(t3)
	pass(t3.Add(61 * time.Second))
	if got := ann.about(nonce); len(got) != 1 || !strings.Contains(got[0], "30.00 EUR → 32.00 EUR") {
		t.Fatalf("want one announcement of the newer change, got %v", got)
	}
	if n := pending(); n != 1 {
		t.Fatalf("pending site rows = %d, want 1 after the newer change", n)
	}
	// 31 minutes after the OLDER change: nothing, the newer one is only 21 min old.
	pass(t2.Add(31 * time.Minute))
	if got := ann.about(nonce); len(got) != 0 {
		t.Fatalf("the superseded change was warned about: %v", got)
	}

	// ── 5. Silent for 30 minutes: exactly one warning. ──────────────────────
	pass(t3.Add(31 * time.Minute))
	got = ann.about(nonce)
	if len(got) != 1 || got[0] != "⚠️ <b>"+name+"</b> — "+siteName+" has not picked up the change for 30 min" {
		t.Fatalf("want one warning, got %v", got)
	}
	pass(t3.Add(40 * time.Minute))
	pass(t3.Add(50 * time.Minute))
	if got := ann.about(nonce); len(got) != 0 {
		t.Fatalf("the warning was repeated: %v", got)
	}
	// ...and a confirmation once the site finally fetches, measured from the change.
	fetchedAt(t3.Add(45 * time.Minute))
	pass(t3.Add(51 * time.Minute))
	got = ann.about(nonce)
	if len(got) != 1 || got[0] != "✅ <b>"+name+"</b> — "+siteName+" picked up the change (after 45 min)" {
		t.Fatalf("want one late confirmation after 45 min, got %v", got)
	}

	// ── 6. A site that fetched before the announcement is updated at once. ──
	t4 := t0.Add(4 * time.Hour)
	price(3500)
	pass(t4)
	fetchedAt(t4.Add(20 * time.Second))
	pass(t4.Add(61 * time.Second))
	got = ann.about(nonce)
	if len(got) != 1 || !strings.Contains(got[0], "• "+siteName+": updated") {
		t.Fatalf("want the site listed as updated, got %v", got)
	}
	if n := pending(); n != 0 {
		t.Fatalf("pending site rows = %d, want 0", n)
	}
	pass(t4.Add(5 * time.Minute))
	pass(t4.Add(40 * time.Minute))
	if got := ann.about(nonce); len(got) != 0 {
		t.Fatalf("an already-updated site got a follow-up: %v", got)
	}

	// ── 7. An unpublished event drops its pending rows silently. ────────────
	t5 := t0.Add(6 * time.Hour)
	price(4000)
	pass(t5)
	pass(t5.Add(61 * time.Second))
	if got := ann.about(nonce); len(got) != 1 {
		t.Fatalf("want one announcement, got %v", got)
	}
	if n := pending(); n != 1 {
		t.Fatalf("pending site rows = %d, want 1", n)
	}
	exec(`UPDATE events SET status = 'archived' WHERE id = $1`, eventID)
	pass(t5.Add(40 * time.Minute))
	if got := ann.about(nonce); len(got) != 0 {
		t.Fatalf("an unpublished event got a follow-up: %v", got)
	}
	if n := pending(); n != 0 {
		t.Fatalf("pending site rows of an unpublished event = %d, want 0", n)
	}
}

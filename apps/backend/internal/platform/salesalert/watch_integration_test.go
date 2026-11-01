//go:build integration

// watch_integration_test.go — the sales state watch against a live database:
// the last place sold tells the organization's chat and queues the e-mail
// once; an unpaid hold on the last place tells nobody; places coming back
// arm the next sell-out; "sold out" set by hand is told with who set it; a
// change the WordPress site did not take in 15 minutes is told once. The
// e-mail job then reaches the owner and the manager (sell-out) or the owner
// only (stale site), never an agent.
//
// Prerequisites: DATABASE_URL against a migrated scratch database.
package salesalert_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesalert"
)

type chatCapture struct {
	mu    sync.Mutex
	texts map[string][]string // org id -> texts
	chats int
}

func (c *chatCapture) AnnounceToOrg(_ context.Context, orgID, text string) (int, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.texts[orgID] = append(c.texts[orgID], text)
	return c.chats, c.chats, nil
}

func (c *chatCapture) take(orgID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.texts[orgID]
	delete(c.texts, orgID)
	return out
}

type mailCapture struct {
	mu   sync.Mutex
	sent []email.Message
}

func (m *mailCapture) Send(_ context.Context, msg email.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

func TestSalesAlertWatch_Integration(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to PostgreSQL (%v); skipping", err)
	}
	defer pool.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", strings.Join(strings.Fields(sql)[:3], " "), err)
		}
	}

	nonce := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	orgID, venueID, eventID, sessionID, tierID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	chanID, tokenID := uuid.New(), uuid.New()
	eventName := "Alert Show " + nonce
	start := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Hour)
	exec(`INSERT INTO organizations (id, name, slug, country, default_locale) VALUES ($1, $2, $3, 'ES', 'ru')`, orgID, "Alert Org "+nonce, "alert-"+nonce)
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'Alert Hall', 'Europe/Madrid')`, venueID, orgID)
	exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, eventID, orgID, eventName)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
	      VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 2, 'scheduled', 'general_admission', 'EUR', 'override')`, sessionID, eventID, venueID, start)
	exec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, 2)`, sessionID)
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, sort_order)
	      VALUES ($1, $2, 'Standing', 'fixed', 1000, 'EUR', 2, 0)`, tierID, sessionID)
	if err := gaquota.CreateCategory(ctx, gen.New(pool), sessionID, tierID, 2); err != nil {
		t.Fatalf("places: %v", err)
	}
	people := map[string]uuid.UUID{}
	for _, role := range []string{"org_admin", "organizer", "agent"} {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`,
			role+"-"+nonce+"@alert.test").Scan(&id); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, $3)`, id, orgID, role)
		people[role] = id
	}
	defer func() {
		for _, s := range []struct {
			sql string
			arg any
		}{
			{`DELETE FROM worker_jobs WHERE job_type = 'sales_state.alert_email' AND payload->>'session_id' = $1`, sessionID.String()},
			{`DELETE FROM outbox_events WHERE aggregate_id = $1`, eventID.String()},
			{`DELETE FROM session_seats WHERE session_id = $1`, sessionID},
			{`DELETE FROM ticket_tiers WHERE session_id = $1`, sessionID},
			{`DELETE FROM inventory_ledger WHERE session_id = $1`, sessionID},
			{`DELETE FROM sessions WHERE id = $1`, sessionID},
			{`DELETE FROM event_publications WHERE event_id = $1`, eventID},
			{`DELETE FROM webhook_subscribers WHERE channel_id = $1`, chanID},
			{`DELETE FROM agent_feed_tokens WHERE id = $1`, tokenID},
			{`DELETE FROM sales_channels WHERE id = $1`, chanID},
			{`DELETE FROM events WHERE id = $1`, eventID},
			{`DELETE FROM venues WHERE id = $1`, venueID},
			{`DELETE FROM memberships WHERE org_id = $1`, orgID},
			{`DELETE FROM users WHERE email LIKE $1`, "%-" + nonce + "@alert.test"},
			{`DELETE FROM organizations WHERE id = $1`, orgID},
		} {
			if _, err := pool.Exec(ctx, s.sql, s.arg); err != nil {
				t.Logf("cleanup %.40s: %v", s.sql, err)
			}
		}
	}()

	// Another package's pass may have seeded the watch already; either way
	// this test runs on a seeded watch, so the first run below talks.
	exec(`INSERT INTO sales_alert_watch_state (id) VALUES (1) ON CONFLICT DO NOTHING`)
	chat := &chatCapture{texts: map[string][]string{}, chats: 1}
	w := salesalert.NewWatcher(salesalert.Options{Pool: pool, Announcer: chat})
	run := func(now time.Time) {
		t.Helper()
		if _, err := w.RunOnce(ctx, now); err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
	}
	jobs := func() []salesalert.AlertEmailPayload {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT payload FROM worker_jobs
			WHERE job_type = 'sales_state.alert_email' AND payload->>'session_id' = $1 ORDER BY created_at`, sessionID.String())
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []salesalert.AlertEmailPayload
		for rows.Next() {
			var raw []byte
			var p salesalert.AlertEmailPayload
			if err := rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			_ = json.Unmarshal(raw, &p)
			out = append(out, p)
		}
		return out
	}
	setPlaces := func(statuses ...string) {
		t.Helper()
		exec(`UPDATE session_seats SET status = 'available' WHERE session_id = $1`, sessionID)
		for i, st := range statuses {
			exec(`UPDATE session_seats SET status = $2 WHERE id = (
				SELECT id FROM session_seats WHERE session_id = $1 ORDER BY seat_key OFFSET $3 LIMIT 1)`, sessionID, st, i)
		}
	}
	org := orgID.String()

	run(time.Now().UTC())
	if got := chat.take(org); len(got) != 0 {
		t.Fatalf("open session told: %v", got)
	}

	// The last place is in an unpaid hold: the storefront says sold out, the
	// organizer is not told yet.
	setPlaces("sold", "held")
	run(time.Now().UTC())
	if got := chat.take(org); len(got) != 0 {
		t.Fatalf("a hold on the last place told the organizer: %v", got)
	}

	// The hold is paid: sold out, told once in Russian, one e-mail queued.
	setPlaces("sold", "sold")
	run(time.Now().UTC())
	got := chat.take(org)
	if len(got) != 1 || !strings.Contains(got[0], "Распродано") || !strings.Contains(got[0], eventName) {
		t.Fatalf("sell-out messages = %v", got)
	}
	run(time.Now().UTC())
	if again := chat.take(org); len(again) != 0 {
		t.Fatalf("a sell-out was told twice: %v", again)
	}
	if j := jobs(); len(j) != 1 || j[0].Kind != salesalert.KindSoldOut || j[0].Manual {
		t.Fatalf("e-mail jobs = %+v", j)
	}

	// The e-mail reaches the owner and the manager, not the agent.
	mail := &mailCapture{}
	handler := delivery.NewSalesAlertHandler(delivery.SalesAlertOptions{Sender: mail, DB: pool})
	body, _ := json.Marshal(jobs()[0])
	if err := handler(ctx, body); err != nil {
		t.Fatalf("e-mail handler: %v", err)
	}
	to := map[string]bool{}
	for _, m := range mail.sent {
		to[m.To] = true
	}
	if len(to) != 2 || !to["org_admin-"+nonce+"@alert.test"] || !to["organizer-"+nonce+"@alert.test"] {
		t.Fatalf("sell-out e-mail went to %v", to)
	}

	// A refund puts a place back: the next sell-out is told again.
	setPlaces("sold")
	run(time.Now().UTC())
	var notices int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM session_soldout_notices WHERE session_id = $1`, sessionID).Scan(&notices)
	if notices != 0 {
		t.Fatalf("the notice survived a free place")
	}

	// "Sold out" set by hand with a place still free: told with who set it.
	changeID := uuid.New()
	exec(`INSERT INTO session_sales_state_changes (id, session_id, event_id, org_id, old_state, new_state, source, actor_type, actor_name, created_at)
	      VALUES ($1, $2, $3, $4, 'open', 'sold_out', 'telegram_bot', 'user', 'Ana Manager', now())`, changeID, sessionID, eventID, orgID)
	exec(`INSERT INTO session_sales_states (session_id, state, change_id) VALUES ($1, 'sold_out', $2)`, sessionID, changeID)
	run(time.Now().UTC())
	got = chat.take(org)
	if len(got) != 1 || !strings.Contains(got[0], "Ana Manager") || !strings.Contains(got[0], "Свободных мест в системе: 1") {
		t.Fatalf("manual sell-out messages = %v", got)
	}
	if j := jobs(); len(j) != 2 || !j[1].Manual || j[1].Actor != "Ana Manager" {
		t.Fatalf("manual e-mail job = %+v", j)
	}

	// A WordPress site sells the event, and the change was not delivered.
	exec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, 'Alert Site')`, chanID, orgID)
	exec(`INSERT INTO agent_feed_tokens (id, token, sales_channel_id) VALUES ($1, $2, $3)`, tokenID, "alert-"+nonce, chanID)
	exec(`INSERT INTO event_publications (event_id, feed_token_id, published_at) VALUES ($1, $2, now())`, eventID, tokenID)
	exec(`INSERT INTO webhook_subscribers (site_url, callback_url, signing_secret, kind, channel_id, active)
	      VALUES ('https://alert.example', $2, 'secret', 'bil24_wp', $1, true)`, chanID, "https://alert.example/hook/"+nonce)
	var outboxID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type, payload)
		VALUES ('event', $1, 'v1.session.updated', '{}'::jsonb) RETURNING id`, eventID.String()).Scan(&outboxID); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE session_sales_state_changes SET outbox_event_id = $2 WHERE id = $1`, changeID, outboxID)

	run(time.Now().UTC()) // under 15 minutes: nothing yet
	if got := chat.take(org); len(got) != 0 {
		t.Fatalf("a fresh change was told stale: %v", got)
	}
	later := time.Now().UTC().Add(16 * time.Minute)
	run(later)
	got = chat.take(org)
	if len(got) != 1 || !strings.Contains(got[0], "Сайт не обновился") {
		t.Fatalf("stale-site messages = %v", got)
	}
	run(later.Add(time.Minute))
	if again := chat.take(org); len(again) != 0 {
		t.Fatalf("a stale site was told twice: %v", again)
	}
	if j := jobs(); len(j) != 2 {
		t.Fatalf("a chat took the stale alert, yet an e-mail was queued: %+v", j)
	}

	// Without a chat the stale alert goes to the owner by e-mail only.
	chat.chats = 0
	changeID2 := uuid.New()
	exec(`INSERT INTO session_sales_state_changes (id, session_id, event_id, org_id, old_state, new_state, source, outbox_event_id, created_at)
	      VALUES ($1, $2, $3, $4, 'sold_out', 'open', 'api', $5, now())`, changeID2, sessionID, eventID, orgID, outboxID)
	exec(`DELETE FROM session_sales_states WHERE session_id = $1`, sessionID)
	run(later.Add(20 * time.Minute))
	j := jobs()
	if len(j) != 3 || j[2].Kind != salesalert.KindSiteStale || j[2].State != "open" || j[2].Sites != 1 {
		t.Fatalf("stale e-mail job = %+v", j)
	}
	mail.sent = nil
	body, _ = json.Marshal(j[2])
	if err := handler(ctx, body); err != nil {
		t.Fatalf("e-mail handler: %v", err)
	}
	if len(mail.sent) != 1 || mail.sent[0].To != "org_admin-"+nonce+"@alert.test" {
		t.Fatalf("stale e-mail went to %+v", mail.sent)
	}
	_ = people
}

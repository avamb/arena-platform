//go:build integration

// addon_alerts_integration_test.go — the alerts of migration 0197 against a
// live database, on the shape of "Серебряное копытце": a date with a main
// category and an "extra" one. The main category selling out is told as a
// category sell-out naming what is left; marking the extra category as an
// add-on makes the date sold out (and told so, with the add-on place still
// free); a "coming soon" set in the bot is told with who set it; the sales
// end passing is told with the tally. Every alert is told once.
//
// Prerequisites: DATABASE_URL against a migrated scratch database.
package salesalert_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesalert"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesstate"
)

func TestSalesAlertWatch_AddonAndMoreAlerts_Integration(t *testing.T) {
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
	orgID, venueID, eventID, sessionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	mainID, extraID := uuid.New(), uuid.New()
	start := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Hour)
	exec(`INSERT INTO organizations (id, name, slug, country, default_locale) VALUES ($1, $2, $3, 'ES', 'ru')`, orgID, "Addon Org "+nonce, "addon-"+nonce)
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'Sala', 'Europe/Madrid')`, venueID, orgID)
	exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, eventID, orgID, "Копытце "+nonce)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
	      VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 3, 'scheduled', 'general_admission', 'EUR', 'override')`, sessionID, eventID, venueID, start)
	exec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, 3)`, sessionID)
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, sort_order)
	      VALUES ($1, $3, 'Ребёнок + взрослый', 'fixed', 5000, 'EUR', 2, 0),
	             ($2, $3, 'Дополнительный детский', 'fixed', 3500, 'EUR', 1, 1)`, mainID, extraID, sessionID)
	q := gen.New(pool)
	for id, n := range map[uuid.UUID]int32{mainID: 2, extraID: 1} {
		if err := gaquota.CreateCategory(ctx, q, sessionID, id, n); err != nil {
			t.Fatalf("places: %v", err)
		}
	}
	defer func() {
		for _, s := range []struct {
			sql string
			arg any
		}{
			{`DELETE FROM worker_jobs WHERE job_type = 'sales_state.alert_email' AND payload->>'session_id' = $1`, sessionID.String()},
			{`DELETE FROM session_seats WHERE session_id = $1`, sessionID},
			{`DELETE FROM ticket_tiers WHERE session_id = $1`, sessionID},
			{`DELETE FROM inventory_ledger WHERE session_id = $1`, sessionID},
			{`DELETE FROM sessions WHERE id = $1`, sessionID},
			{`DELETE FROM events WHERE id = $1`, eventID},
			{`DELETE FROM venues WHERE id = $1`, venueID},
			{`DELETE FROM organizations WHERE id = $1`, orgID},
		} {
			if _, err := pool.Exec(ctx, s.sql, s.arg); err != nil {
				t.Logf("cleanup %.40s: %v", s.sql, err)
			}
		}
	}()

	exec(`INSERT INTO sales_alert_watch_state (id) VALUES (1) ON CONFLICT DO NOTHING`)
	chat := &chatCapture{texts: map[string][]string{}, chats: 1}
	w := salesalert.NewWatcher(salesalert.Options{Pool: pool, Announcer: chat})
	run := func(now time.Time) []string {
		t.Helper()
		if _, err := w.RunOnce(ctx, now); err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
		return chat.take(orgID.String())
	}
	effective := func(now time.Time) string {
		t.Helper()
		facts, err := q.ListSessionSalesFacts(ctx, []uuid.UUID{sessionID}, now)
		if err != nil {
			t.Fatal(err)
		}
		f := facts[sessionID]
		return salesstate.Effective(f.Manual, salesstate.FactsFromRow(f), now)
	}

	now := time.Now().UTC()
	if got := run(now); len(got) != 0 {
		t.Fatalf("nothing happened yet, chat got %q", got)
	}

	// 1. The main category sells out; the extra place keeps the date on sale.
	exec(`UPDATE session_seats SET status = 'sold' WHERE tier_id = $1`, mainID)
	got := run(now)
	if len(got) != 1 || !strings.Contains(got[0], "Категория распродана") ||
		!strings.Contains(got[0], "Ребёнок + взрослый") || !strings.Contains(got[0], "Дополнительный детский — 1") {
		t.Fatalf("category sell-out: chat got %q", got)
	}
	if st := effective(now); st != salesstate.Open {
		t.Fatalf("before the add-on mark the date is %q, want open", st)
	}
	if got := run(now); len(got) != 0 {
		t.Fatalf("a category sell-out is told once, chat got %q", got)
	}

	// 2. The extra category is an add-on: the date is sold out, told so.
	if err := q.SetTicketTierAddon(ctx, extraID, true); err != nil {
		t.Fatal(err)
	}
	if st := effective(now); st != salesstate.SoldOut {
		t.Fatalf("with the add-on mark the date is %q, want sold_out", st)
	}
	got = run(now)
	if len(got) != 1 || !strings.Contains(got[0], "Распродано") || !strings.Contains(got[0], "дополнительных мест: 1") {
		t.Fatalf("date sell-out: chat got %q", got)
	}

	// 3. "Coming soon" set in the bot is told with who set it.
	exec(`INSERT INTO session_sales_state_changes (session_id, event_id, org_id, old_state, new_state, source, actor_type, actor_name, actor_role)
	      VALUES ($1, $2, $3, 'open', 'soon', 'telegram_bot', 'user', 'Вера', 'org_admin')`, sessionID, eventID, orgID)
	got = run(now)
	if len(got) != 1 || !strings.Contains(got[0], "Скоро в продаже") || !strings.Contains(got[0], "Вера · Telegram-бот") {
		t.Fatalf("manual change: chat got %q", got)
	}
	if got := run(now); len(got) != 0 {
		t.Fatalf("a manual change is told once, chat got %q", got)
	}

	// 4. The sales end passes: told once with the tally of every place.
	exec(`UPDATE sessions SET sales_end_at = $2 WHERE id = $1`, sessionID, now.Add(-time.Minute))
	got = run(now)
	if len(got) != 1 || !strings.Contains(got[0], "Продажи закрыты") || !strings.Contains(got[0], "Продано мест: 2 из 3") {
		t.Fatalf("sales end: chat got %q", got)
	}
	if got := run(now); len(got) != 0 {
		t.Fatalf("a sales end is told once, chat got %q", got)
	}
}

//go:build integration

package gen_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestListActionEventsByOrg_SellEndIsTheLastTierToSell pins sell_end_at, the
// value GET_ALL_ACTIONS sends the sites as sellEndTime and the sites read as
// the end of the WHOLE session's sale. It used to be the EARLIEST tier end,
// so a chain of price steps closed after its first step: on 2026-10-02 the
// Vino event center showed an event selling at its second price as "coming
// soon", and the session picker hid it.
func TestListActionEventsByOrg_SellEndIsTheLastTierToSell(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, mustPoolConfig(t, dsn, 4))
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	suffix := uuid.NewString()[:8]
	orgID, venueID, eventID := uuid.New(), uuid.New(), uuid.New()
	steps, openEnded, noTiers := uuid.New(), uuid.New(), uuid.New()
	start := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Second)

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	defer func() {
		for _, q := range []string{
			`DELETE FROM ticket_tiers WHERE session_id IN (SELECT id FROM sessions WHERE event_id = $1)`,
			`DELETE FROM sessions WHERE event_id = $1`,
			`DELETE FROM events WHERE id = $1`,
		} {
			if _, err := pool.Exec(ctx, q, eventID); err != nil {
				t.Logf("cleanup: %v", err)
			}
		}
		if _, err := pool.Exec(ctx, `DELETE FROM venues WHERE id = $1`, venueID); err != nil {
			t.Logf("cleanup venue: %v", err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID); err != nil {
			t.Logf("cleanup org: %v", err)
		}
	}()

	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "SE Org "+suffix, "se-"+suffix)
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Asia/Jerusalem')`, venueID, orgID, "SE Venue "+suffix)
	exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, eventID, orgID, "SE Event "+suffix)
	for _, id := range []uuid.UUID{steps, openEnded, noTiers} {
		exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status,
		          admission_mode, currency, currency_source)
		      VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 80, 'scheduled',
		          'general_admission', 'ILS', 'override')`, id, eventID, venueID, start)
	}
	tier := func(session uuid.UUID, name string, end *time.Time) {
		exec(`INSERT INTO ticket_tiers (session_id, name, pricing_mode, price_amount, currency, sale_window_end)
		      VALUES ($1, $2, 'fixed', 19900, 'ILS', $3)`, session, name, end)
	}
	firstEnd := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second) // the first step already closed
	lastEnd := start.Add(-time.Hour)
	// Price steps: the first ended yesterday, the last sells until an hour before the start.
	tier(steps, "Early", &firstEnd)
	tier(steps, "Last minute", &lastEnd)
	// One step bounded, one without an end: the session sells until it starts.
	tier(openEnded, "Early", &firstEnd)
	tier(openEnded, "Door", nil)

	rows, err := gen.New(pool).ListActionEventsByOrg(ctx, orgID)
	if err != nil {
		t.Fatalf("ListActionEventsByOrg: %v", err)
	}
	got := map[uuid.UUID]*time.Time{}
	for _, r := range rows {
		got[r.SessionID] = r.SellEndAt
	}
	if len(got) != 3 {
		t.Fatalf("want 3 sessions, got %d", len(got))
	}
	want := func(name string, id uuid.UUID, w *time.Time) {
		t.Helper()
		g := got[id]
		switch {
		case w == nil && g != nil:
			t.Errorf("%s: sell_end_at = %v, want NULL (the handler falls back to the start)", name, *g)
		case w != nil && (g == nil || !g.Equal(*w)):
			t.Errorf("%s: sell_end_at = %v, want %v", name, g, *w)
		}
	}
	want("price steps", steps, &lastEnd)
	want("open-ended step", openEnded, &start)
	want("no tiers", noTiers, nil)
}

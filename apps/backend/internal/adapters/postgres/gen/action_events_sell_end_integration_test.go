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

// TestListActionEventsByOrg_SellEndIsTheSessionSalesEnd pins sell_end_at,
// the value GET_ALL_ACTIONS sends the sites as sellEndTime and the sites read
// as the end of the WHOLE session's sale. Since migration 0128 it is the
// session's own sales_end_at (the start unless the organizer moved it), not
// derived from the categories: a chain of price steps whose first step closed
// still sells (2026-10-02, Vino), and a sale extended past the start keeps the
// session in the catalog after it has begun.
func TestListActionEventsByOrg_SellEndIsTheSessionSalesEnd(t *testing.T) {
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
	steps, extended, noTiers := uuid.New(), uuid.New(), uuid.New()
	start := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Second)
	begun := time.Now().UTC().Add(-8 * time.Hour).Truncate(time.Second)
	extendedEnd := time.Now().UTC().Add(time.Hour).Truncate(time.Second)

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
	session := func(id uuid.UUID, at time.Time) {
		exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status,
		          admission_mode, currency, currency_source)
		      VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 80, 'scheduled',
		          'general_admission', 'ILS', 'override')`, id, eventID, venueID, at)
	}
	session(steps, start)
	session(noTiers, start)
	session(extended, begun)
	tier := func(session uuid.UUID, name string, end *time.Time) {
		exec(`INSERT INTO ticket_tiers (session_id, name, pricing_mode, price_amount, currency, sale_window_end)
		      VALUES ($1, $2, 'fixed', 19900, 'ILS', $3)`, session, name, end)
	}
	firstEnd := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second) // the first step already closed
	lastEnd := start.Add(-time.Hour)
	tier(steps, "Early", &firstEnd)
	tier(steps, "Last minute", &lastEnd)
	tier(extended, "Door", nil)

	q := gen.New(pool)
	// The organizer extended the sale of a session that began 8 hours ago.
	if _, err := q.SetSessionSaleTimes(ctx, extended, eventID, &extendedEnd, nil, false); err != nil {
		t.Fatalf("SetSessionSaleTimes: %v", err)
	}

	rows, err := q.ListActionEventsByOrg(ctx, orgID)
	if err != nil {
		t.Fatalf("ListActionEventsByOrg: %v", err)
	}
	got := map[uuid.UUID]*time.Time{}
	for _, r := range rows {
		got[r.SessionID] = r.SellEndAt
	}
	if len(got) != 3 {
		t.Fatalf("want 3 sessions (the extended one included), got %d", len(got))
	}
	want := func(name string, id uuid.UUID, w time.Time) {
		t.Helper()
		if g := got[id]; g == nil || !g.Equal(w) {
			t.Errorf("%s: sell_end_at = %v, want %v", name, g, w)
		}
	}
	want("price steps", steps, start)
	want("no tiers", noTiers, start)
	want("extended past the start", extended, extendedEnd)
}

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

// TestSessionSaleTimes_DefaultsMoveAndRelease pins migration 0128's rules on
// a live database: a new session sells until its start, a moved start carries
// the sales end and the doors time along, an explicit write wins, and moving
// the sales end drops category windows that merely repeated the old one (a
// price step that closes earlier keeps its own end).
func TestSessionSaleTimes_DefaultsMoveAndRelease(t *testing.T) {
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
	orgID, venueID, eventID, sessionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	start := time.Now().UTC().Add(20 * 24 * time.Hour).Truncate(time.Second)

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
		_, _ = pool.Exec(ctx, `DELETE FROM venues WHERE id = $1`, venueID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID)
	}()

	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "ST Org "+suffix, "st-"+suffix)
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, venueID, orgID, "ST Venue "+suffix)
	exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, eventID, orgID, "ST Event "+suffix)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status,
	          admission_mode, currency, currency_source)
	      VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 50, 'scheduled',
	          'general_admission', 'CZK', 'override')`, sessionID, eventID, venueID, start)

	read := func() (time.Time, *time.Time) {
		t.Helper()
		var end time.Time
		var doors *time.Time
		if err := pool.QueryRow(ctx, `SELECT sales_end_at, doors_open_at FROM sessions WHERE id = $1`, sessionID).Scan(&end, &doors); err != nil {
			t.Fatal(err)
		}
		return end, doors
	}
	if end, doors := read(); !end.Equal(start) || doors != nil {
		t.Fatalf("new session: sales end %v doors %v, want the start and none", end, doors)
	}

	q := gen.New(pool)
	doorsAt := start.Add(-45 * time.Minute)
	if _, err := q.SetSessionSaleTimes(ctx, sessionID, eventID, nil, &doorsAt, true); err != nil {
		t.Fatalf("set doors: %v", err)
	}

	// A move by a day carries both along.
	moved := start.Add(24 * time.Hour)
	exec(`UPDATE sessions SET start_at = $2, end_at = $2::timestamptz + interval '2 hours' WHERE id = $1`, sessionID, moved)
	if end, doors := read(); !end.Equal(moved) || doors == nil || !doors.Equal(moved.Add(-45*time.Minute)) {
		t.Fatalf("after the move: sales end %v doors %v", end, doors)
	}

	// Doors after the start are refused by the CHECK.
	if _, err := pool.Exec(ctx, `UPDATE sessions SET doors_open_at = start_at + interval '1 minute' WHERE id = $1`, sessionID); err == nil {
		t.Fatal("doors after the start must violate sessions_doors_before_start")
	}

	// Two categories: one inherited the session's sales end, one is a price
	// step that closes a week earlier.
	stepEnd := moved.Add(-7 * 24 * time.Hour)
	var inherited, step uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO ticket_tiers (session_id, name, pricing_mode, price_amount, currency, sale_window_end)
	      VALUES ($1, 'Door', 'fixed', 50000, 'CZK', $2) RETURNING id`, sessionID, moved).Scan(&inherited); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO ticket_tiers (session_id, name, pricing_mode, price_amount, currency, sale_window_end)
	      VALUES ($1, 'Early', 'fixed', 40000, 'CZK', $2) RETURNING id`, sessionID, stepEnd).Scan(&step); err != nil {
		t.Fatal(err)
	}

	extended := moved.Add(90 * time.Minute)
	row, err := q.SetSessionSaleTimes(ctx, sessionID, eventID, &extended, nil, false)
	if err != nil {
		t.Fatalf("extend: %v", err)
	}
	if !row.SalesEndAt.Equal(extended) || row.DoorsOpenAt == nil {
		t.Fatalf("extend returned sales end %v doors %v (doors must be kept)", row.SalesEndAt, row.DoorsOpenAt)
	}
	windowEnd := func(id uuid.UUID) *time.Time {
		var w *time.Time
		if err := pool.QueryRow(ctx, `SELECT sale_window_end FROM ticket_tiers WHERE id = $1`, id).Scan(&w); err != nil {
			t.Fatal(err)
		}
		return w
	}
	if w := windowEnd(inherited); w != nil {
		t.Fatalf("inherited category window = %v, want released (NULL)", *w)
	}
	if w := windowEnd(step); w == nil || !w.Equal(stepEnd) {
		t.Fatalf("price step window = %v, want its own %v", w, stepEnd)
	}
	if got, err := q.GetSessionSalesEnd(ctx, sessionID); err != nil || !got.Equal(extended) {
		t.Fatalf("GetSessionSalesEnd = %v, %v", got, err)
	}

	// Clearing the doors.
	if row, err := q.SetSessionSaleTimes(ctx, sessionID, eventID, nil, nil, true); err != nil || row.DoorsOpenAt != nil || !row.SalesEndAt.Equal(extended) {
		t.Fatalf("clear doors: %+v, %v", row.DoorsOpenAt, err)
	}
}

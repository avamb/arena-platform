//go:build integration

package gen_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestAB39_BulkSetSessionSeatTier_LiveDB is the AB-39 (feature #429)
// round-trip integration test the feature description mandates
// ("browser test on the Palac Akropolis plan" is covered separately via
// playwright-cli; this asserts the DB layer: sqlc query correctness,
// seat_status_version monotonicity, tier binding update).
//
// The test exercises:
//  1. Seat materialization with initial tier_id nil.
//  2. BulkSetSessionSeatTier flips two of three seats to a specific
//     tier in one round-trip and leaves the third untouched.
//  3. IncrementSessionSeatStatusVersion bumps monotonically.
//  4. A second BulkSetSessionSeatTier flips the third seat to a
//     different tier while leaving the earlier two in place.
//  5. Unknown seat keys simply don't match (rows-affected = 0).
//
// Since the pass-4 review, BulkSetSessionSeatTier is column-side gated
// (kind='seat', status available/unavailable): held/sold seats and GA
// units are skipped by the UPDATE itself, and the handler treats a
// rows-affected shortfall as a 409 (TOCTOU guard). All seats in this
// test stay 'available', so the gate is invisible here; the gate's own
// behaviour is asserted by TestAB49Integration_* (httpserver).
func TestAB39_BulkSetSessionSeatTier_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, mustPoolConfig(t, dsn, 8))
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	f := createSessionSeatsFixture(t, ctx, pool)
	defer f.cleanup()

	q := gen.New(pool)

	// Materialize three seats with nil tier binding.
	tag, err := q.InsertSessionSeats(ctx, f.sessionID,
		[]string{"A|1|1", "A|1|2", "A|1|3"},
		[]string{"A", "A", "A"},
		[]string{"1", "1", "1"},
		[]string{"1", "2", "3"},
		[]*string{nil, nil, nil},
	)
	if err != nil {
		t.Fatalf("InsertSessionSeats: %v", err)
	}
	if tag != 3 {
		t.Fatalf("InsertSessionSeats rows = %d, want 3", tag)
	}

	// Bump seat_status_version once.
	v1, err := q.IncrementSessionSeatStatusVersion(ctx, f.sessionID)
	if err != nil {
		t.Fatalf("IncrementSessionSeatStatusVersion: %v", err)
	}
	if v1 <= 0 {
		t.Fatalf("v1 = %d, want > 0", v1)
	}

	// Bulk reassign seats 1 and 2 to tierA.
	n, err := q.BulkSetSessionSeatTier(ctx, f.sessionID,
		[]string{"A|1|1", "A|1|2"}, f.tierA)
	if err != nil {
		t.Fatalf("BulkSetSessionSeatTier round 1: %v", err)
	}
	if n != 2 {
		t.Fatalf("bulk round 1 affected = %d, want 2", n)
	}
	seats, err := q.ListSessionSeats(ctx, f.sessionID)
	if err != nil {
		t.Fatalf("ListSessionSeats: %v", err)
	}
	if len(seats) != 3 {
		t.Fatalf("ListSessionSeats len = %d, want 3", len(seats))
	}
	for _, s := range seats {
		switch s.SeatKey {
		case "A|1|1", "A|1|2":
			if s.TierID == nil || *s.TierID != f.tierA {
				t.Fatalf("%s tier = %v, want %s", s.SeatKey, s.TierID, f.tierA)
			}
		case "A|1|3":
			if s.TierID != nil {
				t.Fatalf("A|1|3 tier = %v, want nil (untouched)", s.TierID)
			}
		}
	}

	// Bump again; assert monotonicity.
	v2, err := q.IncrementSessionSeatStatusVersion(ctx, f.sessionID)
	if err != nil {
		t.Fatalf("IncrementSessionSeatStatusVersion #2: %v", err)
	}
	if v2 <= v1 {
		t.Fatalf("v2 = %d not > v1 = %d", v2, v1)
	}

	// Reassign the third seat to tierB.
	n, err = q.BulkSetSessionSeatTier(ctx, f.sessionID,
		[]string{"A|1|3"}, f.tierB)
	if err != nil {
		t.Fatalf("BulkSetSessionSeatTier round 2: %v", err)
	}
	if n != 1 {
		t.Fatalf("bulk round 2 affected = %d, want 1", n)
	}
	seats, err = q.ListSessionSeats(ctx, f.sessionID)
	if err != nil {
		t.Fatalf("ListSessionSeats: %v", err)
	}
	for _, s := range seats {
		if s.SeatKey == "A|1|3" {
			if s.TierID == nil || *s.TierID != f.tierB {
				t.Fatalf("A|1|3 tier = %v, want %s", s.TierID, f.tierB)
			}
		}
		if s.SeatKey == "A|1|1" && (s.TierID == nil || *s.TierID != f.tierA) {
			t.Fatalf("A|1|1 lost tierA binding after round 2: %v", s.TierID)
		}
	}

	// Unknown seat keys are silently ignored by the SQL (they simply
	// don't match) — the handler layer surfaces them separately.
	n, err = q.BulkSetSessionSeatTier(ctx, f.sessionID,
		[]string{"typo|xxx"}, f.tierA)
	if err != nil {
		t.Fatalf("BulkSetSessionSeatTier unknown: %v", err)
	}
	if n != 0 {
		t.Fatalf("unknown affected = %d, want 0", n)
	}
}

// sessionSeatsFixture seeds the minimum row graph needed for the AB-39
// integration tests: organization → venue → event → session, plus two
// active ticket_tiers under that session so the reassignment target FKs
// resolve.
type sessionSeatsFixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	orgID     uuid.UUID
	venueID   uuid.UUID
	eventID   uuid.UUID
	sessionID uuid.UUID
	tierA     uuid.UUID
	tierB     uuid.UUID
}

func createSessionSeatsFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *sessionSeatsFixture {
	f := &sessionSeatsFixture{
		t: t, pool: pool,
		orgID:     uuid.New(),
		venueID:   uuid.New(),
		eventID:   uuid.New(),
		sessionID: uuid.New(),
		tierA:     uuid.New(),
		tierB:     uuid.New(),
	}
	suffix := f.orgID.String()[:8]
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			[]any{f.orgID, "AB39 Org " + suffix, "ab39-" + suffix}},
		{`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
			[]any{f.venueID, f.orgID, "AB39 Venue " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility)
		  VALUES ($1, $2, $3, 'draft', 'private')`,
			[]any{f.eventID, f.orgID, "AB39 Event " + suffix}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
		    capacity_total, status, admission_mode, currency, currency_source)
		  VALUES ($1, $2, $3, now() + interval '45 days',
		    now() + interval '45 days 3 hours', 100, 'draft',
		    'general_admission', 'EUR', 'override')`,
			[]any{f.sessionID, f.eventID, f.venueID}},
		{`INSERT INTO ticket_tiers
		    (id, session_id, name, pricing_mode, price_amount,
		     currency, sort_order)
		  VALUES ($1, $2, 'AB39 Tier A', 'fixed', 1500, 'EUR', 0)`,
			[]any{f.tierA, f.sessionID}},
		{`INSERT INTO ticket_tiers
		    (id, session_id, name, pricing_mode, price_amount,
		     currency, sort_order)
		  VALUES ($1, $2, 'AB39 Tier B', 'fixed', 3500, 'EUR', 1)`,
			[]any{f.tierB, f.sessionID}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("AB39 fixture step %d failed: %v", i, err)
		}
	}
	return f
}

func (f *sessionSeatsFixture) cleanup() {
	ctx := context.Background()
	for _, step := range []struct {
		sql string
		arg uuid.UUID
	}{
		{`DELETE FROM session_seats WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM ticket_tiers WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM sessions WHERE id = $1`, f.sessionID},
		{`DELETE FROM events WHERE id = $1`, f.eventID},
		{`DELETE FROM venues WHERE id = $1`, f.venueID},
		{`DELETE FROM organizations WHERE id = $1`, f.orgID},
	} {
		if _, err := f.pool.Exec(ctx, step.sql, step.arg); err != nil {
			f.t.Logf("AB39 cleanup: %v", err)
		}
	}
	// silence unused-var warning when the pgx import stays only for the pool.
	_ = pgx.ErrNoRows
}

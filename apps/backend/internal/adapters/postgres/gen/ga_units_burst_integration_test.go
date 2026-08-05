//go:build integration

package gen_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestGAUnits_OnSaleBurst_LiveDB is the AB-51 step-6 requirement: GA
// hold/sell must not serialise the whole category — benchmark an
// on-sale burst before this ships, and record the numbers.
//
// Fixture: a plan-less GA session sized like Palác Akropolis (~590
// units). 130 concurrent buyers race for 5 tickets each (650 requested
// > 590 available). Asserts:
//
//   - exactly 118 holds succeed (590/5) and 12 fail over-capacity;
//   - session_seats rows and the inventory_ledger rollup agree exactly
//     (rows are the truth, the ledger is the same-tx rollup — AB-51
//     step 3: "it must not be possible for the counter and the rows to
//     disagree");
//   - the burst completes without deadlocks (SKIP LOCKED allocation).
//
// The wall-clock number is logged for the runbook record.
func TestGAUnits_OnSaleBurst_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, mustPoolConfig(t, dsn, 24))
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	const (
		capacity   = 590
		perHold    = 5
		buyers     = 130
		wantWins   = capacity / perHold // 118
		wantLosses = buyers - wantWins  // 12
	)

	f := createGAFixture(t, ctx, pool, capacity)
	defer f.cleanup()

	q := gen.New(pool)

	var wins, losses, failures atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < buyers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := func() error {
				tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
				if err != nil {
					return err
				}
				defer func() { _ = tx.Rollback(ctx) }()
				txq := q.WithTx(tx)

				if _, err := txq.ReserveCapacity(ctx, f.sessionID, nil, perHold); err != nil {
					if err == pgx.ErrNoRows {
						losses.Add(1)
						return nil
					}
					return err
				}
				res, err := txq.InsertReservation(ctx, f.orgID, f.channelID, f.sessionID,
					nil, nil, perHold, time.Now().Add(20*time.Minute))
				if err != nil {
					return err
				}
				ver, err := txq.IncrementSessionSeatStatusVersion(ctx, f.sessionID)
				if err != nil {
					return err
				}
				units, err := txq.AllocateGAUnitsForHold(ctx, f.sessionID, res.ID,
					nil, ver, nil, perHold)
				if err != nil {
					return err
				}
				if len(units) != perHold {
					// Over-capacity at the unit level — roll back (this also
					// rolls back the ledger reserve above).
					losses.Add(1)
					return nil
				}
				for _, u := range units {
					if err := txq.InsertReservationSeat(ctx, res.ID, u.ID); err != nil {
						return err
					}
				}
				if err := tx.Commit(ctx); err != nil {
					return err
				}
				wins.Add(1)
				return nil
			}()
			if err != nil {
				failures.Add(1)
				t.Errorf("buyer goroutine failed: %v", err)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if failures.Load() > 0 {
		t.Fatalf("%d buyer goroutines errored", failures.Load())
	}
	if wins.Load() != wantWins || losses.Load() != wantLosses {
		t.Fatalf("wins=%d losses=%d; want wins=%d losses=%d",
			wins.Load(), losses.Load(), wantWins, wantLosses)
	}

	// Consistency: unit rows vs the session-level ledger rollup.
	var heldUnits, ledgerHeld int64
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM session_seats
		 WHERE session_id=$1 AND kind='ga_unit' AND status='held'`,
		f.sessionID).Scan(&heldUnits); err != nil {
		t.Fatalf("count held units: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT capacity_held FROM inventory_ledger
		 WHERE session_id=$1 AND tier_id IS NULL`,
		f.sessionID).Scan(&ledgerHeld); err != nil {
		t.Fatalf("ledger read: %v", err)
	}
	if heldUnits != int64(capacity) || ledgerHeld != int64(capacity) {
		t.Fatalf("held units=%d ledger held=%d; want both %d — rows and counter disagree",
			heldUnits, ledgerHeld, capacity)
	}

	perHoldLatency := elapsed / time.Duration(buyers)
	t.Logf("AB-51 on-sale burst: %d buyers x %d tickets over %d units in %v (%v avg/hold, %d wins, %d over-capacity)",
		buyers, perHold, capacity, elapsed, perHoldLatency, wins.Load(), losses.Load())
}

// ─── fixture plumbing ────────────────────────────────────────────────────────

type gaFixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	orgID     uuid.UUID
	venueID   uuid.UUID
	eventID   uuid.UUID
	channelID uuid.UUID
	sessionID uuid.UUID
}

func mustPoolConfig(t *testing.T, dsn string, maxConns int32) *pgxpool.Config {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	cfg.MaxConns = maxConns
	return cfg
}

func createGAFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, capacity int) *gaFixture {
	f := &gaFixture{
		t: t, pool: pool,
		orgID:     uuid.New(),
		venueID:   uuid.New(),
		eventID:   uuid.New(),
		channelID: uuid.New(),
		sessionID: uuid.New(),
	}
	suffix := f.orgID.String()[:8]
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			[]any{f.orgID, "GA Burst Org " + suffix, "ga-burst-" + suffix}},
		{`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
			[]any{f.venueID, f.orgID, "GA Burst Venue " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility)
		  VALUES ($1, $2, $3, 'published', 'public')`,
			[]any{f.eventID, f.orgID, "GA Burst Event " + suffix}},
		{`INSERT INTO sales_channels (id, org_id, name, provider, payment_mode)
		  VALUES ($1, $2, $3, 'stripe', 'direct_merchant')`,
			[]any{f.channelID, f.orgID, "GA Burst Channel " + suffix}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
		    capacity_total, status, admission_mode, currency, currency_source)
		  VALUES ($1, $2, $3, now() + interval '30 days',
		    now() + interval '30 days 3 hours', $4, 'scheduled',
		    'general_admission', 'EUR', 'override')`,
			[]any{f.sessionID, f.eventID, f.venueID, capacity}},
		{`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total)
		  VALUES ($1, NULL, $2)`,
			[]any{f.sessionID, capacity}},
		{`INSERT INTO session_seats
		    (session_id, seat_key, sector_name, row_name, seat_number,
		     tier_id, status, kind)
		  SELECT $1, 'ga|pool|' || lpad(gs::text, 6, '0'), '', '', '',
		         NULL, 'available', 'ga_unit'
		  FROM generate_series(1, $2::int) gs`,
			[]any{f.sessionID, capacity}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("fixture step %d failed: %v", i, err)
		}
	}
	return f
}

func (f *gaFixture) cleanup() {
	ctx := context.Background()
	for _, sql := range []string{
		`DELETE FROM reservation_seats WHERE session_seat_id IN
		   (SELECT id FROM session_seats WHERE session_id = $1)`,
		`DELETE FROM session_seats WHERE session_id = $1`,
		`DELETE FROM reservations WHERE session_id = $1`,
		`DELETE FROM inventory_ledger WHERE session_id = $1`,
		`DELETE FROM sessions WHERE id = $1`,
	} {
		if _, err := f.pool.Exec(ctx, sql, f.sessionID); err != nil {
			f.t.Logf("cleanup: %v (sql: %.40s...)", err, sql)
		}
	}
	for _, step := range []struct {
		sql string
		arg uuid.UUID
	}{
		{`DELETE FROM events WHERE id = $1`, f.eventID},
		{`DELETE FROM sales_channels WHERE id = $1`, f.channelID},
		{`DELETE FROM venues WHERE id = $1`, f.venueID},
		{`DELETE FROM organizations WHERE id = $1`, f.orgID},
	} {
		if _, err := f.pool.Exec(ctx, step.sql, step.arg); err != nil {
			f.t.Logf("cleanup: %v", err)
		}
	}
}

var _ = fmt.Sprintf // keep fmt for future debugging without churn

//go:build integration

// hold_mutation_concurrency_integration_test.go — W1-A5a (feature #483)
// step 2: the live-PostgreSQL proof that the mutable-hold primitives keep
// the SEAT-C1 concurrency contract.
//
// Two races run against ONE session:
//
//   - GA burst: 20 goroutines, each owning its own cart, repeatedly
//     ExtendHold / ShrinkHold the same plan-less GA pool. The invariant is
//     that a GA unit is never held by two carts at once and that the
//     session_seats rows, the reservation_seats links, the
//     reservation_ga_items lines, reservations.quantity and the
//     inventory_ledger rollup all agree at the end.
//
//   - Seated contention: 20 goroutines each try to ExtendHold the SAME
//     seat keys into their own cart. Exactly one may win each seat; the
//     losers must see *SeatConflictsError and leave no trace.
//
// Requires DATABASE_URL against a migrated database:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable \
//	  go test -tags integration ./internal/platform/httpserver/hcheckout/...
package hcheckout_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

const (
	holdRacers  = 20
	holdRounds  = 6
	holdPerCall = 2
)

// TestW1A5a_ConcurrentExtendShrink_GA_LiveDB is the feature's headline
// requirement: 20 goroutines extending and shrinking the same session must
// never leave a GA unit held twice.
func TestW1A5a_ConcurrentExtendShrink_GA_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	ctx := context.Background()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	cfg.MaxConns = 24
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	// Capacity is deliberately smaller than the peak demand
	// (20 carts x 2 units) so over-capacity rejections are exercised too.
	f := newHoldFixture(t, ctx, pool, "general_admission", 30, 0)
	defer f.cleanup()

	q := gen.New(pool)
	reservations := make([]uuid.UUID, holdRacers)
	for i := range reservations {
		reservations[i] = f.newCart(ctx, q, 1)
	}

	var extends, shrinks, conflicts atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < holdRacers; i++ {
		resID := reservations[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < holdRounds; r++ {
				in := hcheckout.HoldMutationInput{
					ReservationID: resID,
					GATiers:       []hcheckout.HoldTierQuantity{{TierID: f.tierID, Quantity: holdPerCall}},
					TTL:           15 * time.Minute,
				}
				if _, err := hcheckout.ExtendHold(ctx, pool, q, in); err != nil {
					var capErr *hcheckout.CapacityError
					if errors.As(err, &capErr) {
						conflicts.Add(1)
					} else {
						t.Errorf("racer extend: %v", err)
						return
					}
				} else {
					extends.Add(1)
				}
				if _, err := hcheckout.ShrinkHold(ctx, pool, q, in); err != nil {
					t.Errorf("racer shrink: %v", err)
					return
				}
				shrinks.Add(1)
			}
		}()
	}
	wg.Wait()
	t.Logf("W1-A5a GA race: %d racers x %d rounds in %v (%d extends, %d shrinks, %d over-capacity)",
		holdRacers, holdRounds, time.Since(start), extends.Load(), shrinks.Load(), conflicts.Load())

	f.assertNoUnitHeldTwice(ctx)
	f.assertLedgerMatchesRows(ctx)
	f.assertQuantitiesMatchLinks(ctx)
}

// TestW1A5a_ConcurrentExtend_SameSeats_LiveDB proves the seated half: 20
// carts fighting over the same 4 seats produce exactly 4 held seats, each
// owned by one cart, and the losers get *SeatConflictsError.
func TestW1A5a_ConcurrentExtend_SameSeats_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	ctx := context.Background()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	cfg.MaxConns = 24
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	const seats = 4
	f := newHoldFixture(t, ctx, pool, "assigned_seats", 50, seats)
	defer f.cleanup()

	q := gen.New(pool)
	seatKeys := f.seatKeys(seats)

	var wins, losses atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < holdRacers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resID := f.newCart(ctx, q, 1)
			_, err := hcheckout.ExtendHold(ctx, pool, q, hcheckout.HoldMutationInput{
				ReservationID: resID,
				SeatKeys:      seatKeys,
				TTL:           15 * time.Minute,
			})
			switch {
			case err == nil:
				wins.Add(1)
			case isSeatConflict(err):
				losses.Add(1)
			default:
				t.Errorf("racer extend: %v", err)
			}
		}()
	}
	wg.Wait()

	if wins.Load() != 1 || losses.Load() != holdRacers-1 {
		t.Fatalf("wins=%d losses=%d; want exactly 1 winner of the %d-seat block",
			wins.Load(), losses.Load(), seats)
	}
	f.assertNoUnitHeldTwice(ctx)

	var held int64
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM session_seats WHERE session_id=$1 AND status='held'`,
		f.sessionID).Scan(&held); err != nil {
		t.Fatalf("count held seats: %v", err)
	}
	if held != seats {
		t.Fatalf("held seats = %d, want %d", held, seats)
	}
}

// TestW1A5a_ShrinkToEmptyCancels_LiveDB pins the state transition the
// feature calls out by name: a cart shrunk to nothing becomes 'cancelled'
// and returns every unit it held to the pool.
func TestW1A5a_ShrinkToEmptyCancels_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	f := newHoldFixture(t, ctx, pool, "general_admission", 10, 0)
	defer f.cleanup()

	q := gen.New(pool)
	resID := f.newCart(ctx, q, 1)
	line := []hcheckout.HoldTierQuantity{{TierID: f.tierID, Quantity: 3}}

	ext, err := hcheckout.ExtendHold(ctx, pool, q, hcheckout.HoldMutationInput{
		ReservationID: resID, GATiers: line, TTL: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("extend: %v", err)
	}
	if ext.Reservation.Quantity != 4 { // seeded 1 + 3
		t.Fatalf("quantity after extend = %d, want 4", ext.Reservation.Quantity)
	}
	if got := ext.LockedPrices[f.tierID]; got != holdTierPrice {
		t.Errorf("locked price = %d, want %d", got, holdTierPrice)
	}

	// Shrinking by more than the cart holds is clamped, not rejected, and
	// empties the cart → cancelled.
	shr, err := hcheckout.ShrinkHold(ctx, pool, q, hcheckout.HoldMutationInput{
		ReservationID: resID,
		GATiers:       []hcheckout.HoldTierQuantity{{TierID: f.tierID, Quantity: 99}},
	})
	if err != nil {
		t.Fatalf("shrink: %v", err)
	}
	if !shr.Cancelled || shr.Reservation.State != "cancelled" {
		t.Fatalf("cancelled=%v state=%q, want true/cancelled", shr.Cancelled, shr.Reservation.State)
	}

	// A cancelled cart is terminal: further mutation is refused.
	_, err = hcheckout.ExtendHold(ctx, pool, q, hcheckout.HoldMutationInput{
		ReservationID: resID, GATiers: line,
	})
	var notMutable *hcheckout.NotMutableError
	if !errors.As(err, &notMutable) {
		t.Fatalf("extend on cancelled cart err = %v, want *NotMutableError", err)
	}
	_, err = hcheckout.ReacquireHold(ctx, pool, q, hcheckout.HoldMutationInput{ReservationID: resID})
	if !errors.As(err, &notMutable) {
		t.Fatalf("reacquire on cancelled cart err = %v, want *NotMutableError", err)
	}

	var lines, links, heldUnits int64
	if err := pool.QueryRow(ctx,
		`SELECT (SELECT COUNT(*) FROM reservation_ga_items WHERE reservation_id=$1),
		        (SELECT COUNT(*) FROM reservation_seats   WHERE reservation_id=$1),
		        (SELECT COUNT(*) FROM session_seats WHERE session_id=$2 AND status='held')`,
		resID, f.sessionID).Scan(&lines, &links, &heldUnits); err != nil {
		t.Fatalf("post-cancel counts: %v", err)
	}
	if lines != 0 || links != 0 || heldUnits != 0 {
		t.Fatalf("after cancel: ga_lines=%d links=%d held_units=%d, want all 0", lines, links, heldUnits)
	}
	f.assertLedgerMatchesRows(ctx)
}

// TestW1A5a_RefreshHoldExpiry_LiveDB proves the TTL primitive slides open
// carts and silently skips closed ones.
func TestW1A5a_RefreshHoldExpiry_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	f := newHoldFixture(t, ctx, pool, "general_admission", 10, 0)
	defer f.cleanup()

	q := gen.New(pool)
	open := f.newCart(ctx, q, 1)
	closed := f.newCart(ctx, q, 1)
	if _, err := q.UpdateReservationStateGuarded(ctx, closed, "draft", "cancelled"); err != nil {
		t.Fatalf("close cart: %v", err)
	}

	rows, err := hcheckout.RefreshHoldExpiry(ctx, pool, q, []uuid.UUID{open, closed}, 42*time.Minute)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != open {
		t.Fatalf("refreshed %d rows (%+v), want only the open cart", len(rows), rows)
	}
	if d := time.Until(rows[0].ExpiresAt); d < 40*time.Minute || d > 43*time.Minute {
		t.Fatalf("expires_at in %v, want ~42m", d)
	}
	if _, err := hcheckout.RefreshHoldExpiry(ctx, pool, q, nil, time.Minute); !errors.Is(err, hcheckout.ErrHoldInvalidInput) {
		t.Errorf("refresh(no ids) err = %v, want ErrHoldInvalidInput", err)
	}
}

// ─── fixture plumbing ────────────────────────────────────────────────────────

const holdTierPrice int64 = 2500

type holdFixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	orgID     uuid.UUID
	venueID   uuid.UUID
	eventID   uuid.UUID
	channelID uuid.UUID
	sessionID uuid.UUID
	tierID    uuid.UUID
}

// newHoldFixture builds an isolated org/venue/event/channel/session with a
// fixed-price tier. gaUnits GA rows are created for a general_admission
// session; seatRows numbered seats for an assigned_seats one.
func newHoldFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, admission string, capacity, seatRows int) *holdFixture {
	t.Helper()
	f := &holdFixture{
		t: t, pool: pool,
		orgID:     uuid.New(),
		venueID:   uuid.New(),
		eventID:   uuid.New(),
		channelID: uuid.New(),
		sessionID: uuid.New(),
		tierID:    uuid.New(),
	}
	suffix := f.orgID.String()[:8]
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			[]any{f.orgID, "W1A5a Org " + suffix, "w1a5a-" + suffix}},
		{`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
			[]any{f.venueID, f.orgID, "W1A5a Venue " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility)
		  VALUES ($1, $2, $3, 'published', 'public')`,
			[]any{f.eventID, f.orgID, "W1A5a Event " + suffix}},
		{`INSERT INTO sales_channels (id, org_id, name, provider, payment_mode)
		  VALUES ($1, $2, $3, 'stripe', 'direct_merchant')`,
			[]any{f.channelID, f.orgID, "W1A5a Channel " + suffix}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
		    capacity_total, status, admission_mode, currency, currency_source)
		  VALUES ($1, $2, $3, now() + interval '30 days',
		    now() + interval '30 days 3 hours', $4, 'scheduled', $5, 'EUR', 'override')`,
			[]any{f.sessionID, f.eventID, f.venueID, capacity, admission}},
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency)
		  VALUES ($1, $2, 'W1A5a Tier', 'fixed', $3, 'EUR')`,
			[]any{f.tierID, f.sessionID, holdTierPrice}},
		{`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total)
		  VALUES ($1, NULL, $2)`,
			[]any{f.sessionID, capacity}},
	}
	if seatRows > 0 {
		steps = append(steps, struct {
			sql  string
			args []any
		}{`INSERT INTO session_seats
		     (session_id, seat_key, sector_name, row_name, seat_number,
		      tier_id, status, kind)
		   SELECT $1, 'Parter|A|' || gs::text, 'Parter', 'A', gs::text,
		          $3, 'available', 'seat'
		   FROM generate_series(1, $2::int) gs`,
			[]any{f.sessionID, seatRows, f.tierID}})
	} else {
		steps = append(steps, struct {
			sql  string
			args []any
		}{`INSERT INTO session_seats
		     (session_id, seat_key, sector_name, row_name, seat_number,
		      tier_id, status, kind)
		   SELECT $1, 'ga|pool|' || lpad(gs::text, 6, '0'), '', '', '',
		          NULL, 'available', 'ga_unit'
		   FROM generate_series(1, $2::int) gs`,
			[]any{f.sessionID, capacity}})
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("fixture step %d failed: %v", i, err)
		}
	}
	return f
}

// newCart inserts an open reservation holding `qty` of session-level
// capacity, mirroring what CreateGAHold/CreateSeatedHold would have left
// behind, so the mutation primitives have something to mutate.
func (f *holdFixture) newCart(ctx context.Context, q *gen.Queries, qty int32) uuid.UUID {
	f.t.Helper()
	if _, err := q.ReserveCapacity(ctx, f.sessionID, nil, qty); err != nil {
		f.t.Fatalf("seed cart capacity: %v", err)
	}
	res, err := q.InsertReservation(ctx, f.orgID, f.channelID, f.sessionID,
		nil, nil, qty, time.Now().Add(20*time.Minute))
	if err != nil {
		f.t.Fatalf("seed cart: %v", err)
	}
	return res.ID
}

func (f *holdFixture) seatKeys(n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, "Parter|A|"+strconv.Itoa(i))
	}
	return out
}

// assertNoUnitHeldTwice is the invariant the feature names: a session_seats
// row may be linked to at most one reservation, and a held row must be
// linked to exactly the reservation that owns it.
func (f *holdFixture) assertNoUnitHeldTwice(ctx context.Context) {
	f.t.Helper()

	var doubles int64
	if err := f.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM (
		   SELECT rs.session_seat_id
		   FROM   reservation_seats rs
		   JOIN   session_seats ss ON ss.id = rs.session_seat_id
		   WHERE  ss.session_id = $1
		   GROUP  BY rs.session_seat_id
		   HAVING COUNT(*) > 1
		 ) dup`, f.sessionID).Scan(&doubles); err != nil {
		f.t.Fatalf("double-hold probe: %v", err)
	}
	if doubles != 0 {
		f.t.Fatalf("%d session_seats rows are linked to more than one reservation", doubles)
	}

	var orphanHeld, orphanLinks int64
	if err := f.pool.QueryRow(ctx,
		`SELECT
		   (SELECT COUNT(*) FROM session_seats ss
		      LEFT JOIN reservation_seats rs
		        ON rs.session_seat_id = ss.id AND rs.reservation_id = ss.reservation_id
		    WHERE ss.session_id = $1 AND ss.status = 'held' AND rs.reservation_id IS NULL),
		   (SELECT COUNT(*) FROM reservation_seats rs
		      JOIN session_seats ss ON ss.id = rs.session_seat_id
		    WHERE ss.session_id = $1
		      AND (ss.status <> 'held' OR ss.reservation_id IS DISTINCT FROM rs.reservation_id))`,
		f.sessionID).Scan(&orphanHeld, &orphanLinks); err != nil {
		f.t.Fatalf("link-consistency probe: %v", err)
	}
	if orphanHeld != 0 || orphanLinks != 0 {
		f.t.Fatalf("held rows without a matching link: %d; links without a matching held row: %d",
			orphanHeld, orphanLinks)
	}
}

// assertLedgerMatchesRows checks the AB-51 rule that the row-level truth and
// the session-level ledger rollup can never disagree.
func (f *holdFixture) assertLedgerMatchesRows(ctx context.Context) {
	f.t.Helper()
	var heldRows, ledgerHeld int64
	if err := f.pool.QueryRow(ctx,
		`SELECT (SELECT COUNT(*) FROM session_seats WHERE session_id=$1 AND status='held'),
		        (SELECT capacity_held FROM inventory_ledger WHERE session_id=$1 AND tier_id IS NULL)`,
		f.sessionID).Scan(&heldRows, &ledgerHeld); err != nil {
		f.t.Fatalf("ledger probe: %v", err)
	}
	// Seeded carts hold session-level capacity without owning a row, so the
	// ledger is allowed to run ahead by exactly the seeded quantity.
	var seeded int64
	if err := f.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(r.quantity), 0) - COALESCE(
		          (SELECT COUNT(*) FROM reservation_seats rs
		             JOIN reservations r2 ON r2.id = rs.reservation_id
		           WHERE r2.session_id = $1 AND r2.state IN ('draft','active')), 0)
		 FROM reservations r
		 WHERE r.session_id = $1 AND r.state IN ('draft','active')`,
		f.sessionID).Scan(&seeded); err != nil {
		f.t.Fatalf("seeded-quantity probe: %v", err)
	}
	if ledgerHeld != heldRows+seeded {
		f.t.Fatalf("ledger held=%d but rows=%d (+%d seeded) — counter and rows disagree",
			ledgerHeld, heldRows, seeded)
	}
}

// assertQuantitiesMatchLinks checks that every open cart's quantity equals
// the seeded 1 plus the number of units it actually holds, and that its GA
// lines sum to the same number of units.
func (f *holdFixture) assertQuantitiesMatchLinks(ctx context.Context) {
	f.t.Helper()
	rows, err := f.pool.Query(ctx,
		`SELECT r.id, r.quantity,
		        (SELECT COUNT(*) FROM reservation_seats rs WHERE rs.reservation_id = r.id),
		        COALESCE((SELECT SUM(gi.quantity) FROM reservation_ga_items gi
		                  WHERE gi.reservation_id = r.id), 0)
		 FROM reservations r
		 WHERE r.session_id = $1 AND r.state IN ('draft','active')`, f.sessionID)
	if err != nil {
		f.t.Fatalf("quantity probe: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var quantity int32
		var links, gaQty int64
		if err := rows.Scan(&id, &quantity, &links, &gaQty); err != nil {
			f.t.Fatalf("quantity scan: %v", err)
		}
		if int64(quantity) != links+1 { // +1 = the seeded session-level unit
			f.t.Errorf("cart %s: quantity=%d but holds %d units (expected %d)",
				id, quantity, links, links+1)
		}
		if gaQty != links {
			f.t.Errorf("cart %s: ga_items sum=%d but holds %d units", id, gaQty, links)
		}
	}
	if err := rows.Err(); err != nil {
		f.t.Fatalf("quantity rows: %v", err)
	}
}

func isSeatConflict(err error) bool {
	var conflict *hcheckout.SeatConflictsError
	return errors.As(err, &conflict)
}

func (f *holdFixture) cleanup() {
	ctx := context.Background()
	for _, sql := range []string{
		`DELETE FROM reservation_ga_items WHERE reservation_id IN
		   (SELECT id FROM reservations WHERE session_id = $1)`,
		`DELETE FROM reservation_seats WHERE session_seat_id IN
		   (SELECT id FROM session_seats WHERE session_id = $1)`,
		`DELETE FROM session_seats WHERE session_id = $1`,
		`DELETE FROM reservations WHERE session_id = $1`,
		`DELETE FROM inventory_ledger WHERE session_id = $1`,
		`DELETE FROM ticket_tiers WHERE session_id = $1`,
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

//go:build integration

// reservation_expire_sweep_integration_test.go proves the production
// defect fixed by internal/platform/reservationexpiry against a live
// PostgreSQL database: nothing ever called
// hcheckout.ReservationProcessor.ProcessExpiredReservations outside a test,
// so a reservation's held inventory (session_seats/ga_unit rows and
// inventory_ledger.capacity_held) never actually expired.
//
// These tests drive the sweep through the SAME handler arena-worker
// registers (reservationexpiry.NewHandler wrapping the real
// hcheckout.ReservationProcessor), not the processor's exported method
// directly, so a regression in the worker wiring itself would fail here too.
//
// Requires DATABASE_URL against a migrated database:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable \
//	  go test -tags integration ./internal/platform/httpserver/hcheckout/...
package hcheckout_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/reservationexpiry"
)

func sweepQuietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// runOneSweep drives exactly one pass of the reservation.expire_sweep
// handler with no self-scheduling (Scheduler is nil, matching what a
// one-shot test wants), mirroring how arena-worker wires it in
// cmd/arena-worker/main.go's registerBuiltinHandlers.
func runOneSweep(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	q := gen.New(pool)
	proc := hcheckout.NewReservationProcessor(pool, q, sweepQuietLogger()).WithCheckoutQueries(q)
	handler := reservationexpiry.NewHandler(reservationexpiry.Options{
		Processor: proc,
		Logger:    sweepQuietLogger(),
		BatchSize: 100,
	})
	if err := handler(ctx, nil); err != nil {
		t.Fatalf("reservation expire sweep handler: %v", err)
	}
}

// expireNow backdates a reservation's expires_at so the next sweep picks it
// up as a GetExpiredReservations candidate (expires_at < now(), state IN
// draft/active).
func expireNow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, reservationID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`UPDATE reservations SET expires_at = now() - interval '1 minute' WHERE id = $1`,
		reservationID); err != nil {
		t.Fatalf("backdate reservation %s: %v", reservationID, err)
	}
}

// TestReservationExpireSweep_GAHold_ReleasesUnitsAndCapacity_LiveDB is the
// headline regression proof: a GA hold whose TTL passed must have its
// ga_unit rows, reservation_seats links, and inventory_ledger.capacity_held
// released by the sweep — and the pool must be fully re-holdable afterward.
func TestReservationExpireSweep_GAHold_ReleasesUnitsAndCapacity_LiveDB(t *testing.T) {
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

	const capacity = 5
	f := newHoldFixture(t, ctx, pool, "general_admission", capacity, 0)
	defer f.cleanup()

	q := gen.New(pool)
	hold, err := hcheckout.CreateGAHold(ctx, pool, q, hcheckout.GAHoldInput{
		OrgID:     f.orgID,
		ChannelID: f.channelID,
		SessionID: f.sessionID,
		Items:     []hcheckout.GAHoldItem{{TierID: f.tierID, Quantity: 3, UnitPrice: holdTierPrice}},
		ExpiresAt: time.Now().Add(20 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateGAHold: %v", err)
	}

	expireNow(t, ctx, pool, hold.ID)
	runOneSweep(t, ctx, pool)

	// 1. The reservation itself transitioned to 'expired'.
	got, err := q.GetReservationByID(ctx, hold.ID)
	if err != nil {
		t.Fatalf("reload reservation: %v", err)
	}
	if got.State != "expired" {
		t.Fatalf("reservation state = %q, want expired", got.State)
	}

	// 2. The 3 ga_unit rows are back to 'available' with reservation_id NULL.
	var available int64
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM session_seats
		 WHERE session_id = $1 AND kind = 'ga_unit' AND status = 'available' AND reservation_id IS NULL`,
		f.sessionID).Scan(&available); err != nil {
		t.Fatalf("count available ga_units: %v", err)
	}
	if available != capacity {
		t.Fatalf("available ga_units = %d, want the full pool of %d back", available, capacity)
	}

	var stillHeld int64
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM session_seats WHERE session_id = $1 AND status = 'held'`,
		f.sessionID).Scan(&stillHeld); err != nil {
		t.Fatalf("count held units: %v", err)
	}
	if stillHeld != 0 {
		t.Fatalf("held units = %d, want 0", stillHeld)
	}

	// reservation_seats links must be gone too.
	var links int64
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM reservation_seats WHERE reservation_id = $1`, hold.ID).Scan(&links); err != nil {
		t.Fatalf("count reservation_seats links: %v", err)
	}
	if links != 0 {
		t.Fatalf("reservation_seats links = %d, want 0", links)
	}

	// 3. inventory_ledger.capacity_held is back to 0.
	var heldCap int32
	if err := pool.QueryRow(ctx,
		`SELECT capacity_held FROM inventory_ledger WHERE session_id = $1 AND tier_id IS NULL`,
		f.sessionID).Scan(&heldCap); err != nil {
		t.Fatalf("read inventory_ledger: %v", err)
	}
	if heldCap != 0 {
		t.Fatalf("inventory_ledger.capacity_held = %d, want 0", heldCap)
	}

	// 4. A second hold for the FULL pool must now succeed — proves the
	// capacity truly came back, not just the reservation row's own state.
	second, err := hcheckout.CreateGAHold(ctx, pool, q, hcheckout.GAHoldInput{
		OrgID:     f.orgID,
		ChannelID: f.channelID,
		SessionID: f.sessionID,
		Items:     []hcheckout.GAHoldItem{{TierID: f.tierID, Quantity: capacity, UnitPrice: holdTierPrice}},
		ExpiresAt: time.Now().Add(20 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateGAHold for the full pool after sweep: %v", err)
	}
	if second.Quantity != capacity {
		t.Fatalf("second hold quantity = %d, want %d", second.Quantity, capacity)
	}
}

// TestReservationExpireSweep_SeatedHold_ReleasesSeatsAndCapacity_LiveDB is
// the seated-branch counterpart: expired seats must return to 'available'
// and session-level capacity must be released.
func TestReservationExpireSweep_SeatedHold_ReleasesSeatsAndCapacity_LiveDB(t *testing.T) {
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

	const seatRows = 4
	f := newHoldFixture(t, ctx, pool, "assigned_seats", 50, seatRows)
	defer f.cleanup()

	q := gen.New(pool)
	seatKeys := f.seatKeys(seatRows)
	hold, err := hcheckout.CreateSeatedHold(ctx, pool, q, hcheckout.SeatedHoldInput{
		OrgID:     f.orgID,
		ChannelID: f.channelID,
		SessionID: f.sessionID,
		SeatKeys:  seatKeys,
		ExpiresAt: time.Now().Add(20 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateSeatedHold: %v", err)
	}

	expireNow(t, ctx, pool, hold.Reservation.ID)
	runOneSweep(t, ctx, pool)

	got, err := q.GetReservationByID(ctx, hold.Reservation.ID)
	if err != nil {
		t.Fatalf("reload reservation: %v", err)
	}
	if got.State != "expired" {
		t.Fatalf("reservation state = %q, want expired", got.State)
	}

	var available int64
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM session_seats
		 WHERE session_id = $1 AND kind = 'seat' AND status = 'available' AND reservation_id IS NULL`,
		f.sessionID).Scan(&available); err != nil {
		t.Fatalf("count available seats: %v", err)
	}
	if available != seatRows {
		t.Fatalf("available seats = %d, want %d", available, seatRows)
	}

	var heldCap int32
	if err := pool.QueryRow(ctx,
		`SELECT capacity_held FROM inventory_ledger WHERE session_id = $1 AND tier_id IS NULL`,
		f.sessionID).Scan(&heldCap); err != nil {
		t.Fatalf("read inventory_ledger: %v", err)
	}
	if heldCap != 0 {
		t.Fatalf("inventory_ledger.capacity_held = %d, want 0", heldCap)
	}

	// The same seat_keys must be holdable again.
	if _, err := hcheckout.CreateSeatedHold(ctx, pool, q, hcheckout.SeatedHoldInput{
		OrgID:     f.orgID,
		ChannelID: f.channelID,
		SessionID: f.sessionID,
		SeatKeys:  seatKeys,
		ExpiresAt: time.Now().Add(20 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateSeatedHold after sweep: %v", err)
	}
}

// TestReservationExpireSweep_LeavesCancelledReservationsAlone_LiveDB proves
// the guarded state transition: a reservation already cancelled (or
// converted) before the sweep runs must be left untouched even though its
// expires_at is in the past — the sweep must never double-release capacity
// that ReleaseHold (or a conversion) already returned.
func TestReservationExpireSweep_LeavesCancelledReservationsAlone_LiveDB(t *testing.T) {
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

	const capacity = 5
	f := newHoldFixture(t, ctx, pool, "general_admission", capacity, 0)
	defer f.cleanup()

	q := gen.New(pool)
	hold, err := hcheckout.CreateGAHold(ctx, pool, q, hcheckout.GAHoldInput{
		OrgID:     f.orgID,
		ChannelID: f.channelID,
		SessionID: f.sessionID,
		Items:     []hcheckout.GAHoldItem{{TierID: f.tierID, Quantity: 3, UnitPrice: holdTierPrice}},
		ExpiresAt: time.Now().Add(20 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateGAHold: %v", err)
	}

	// Cancel it through the normal release path BEFORE it would have
	// expired — this already released the 3 units and their capacity.
	cancelled, err := hcheckout.ReleaseHold(ctx, pool, q, hold.ID)
	if err != nil {
		t.Fatalf("ReleaseHold: %v", err)
	}
	if cancelled.State != "cancelled" {
		t.Fatalf("state after ReleaseHold = %q, want cancelled", cancelled.State)
	}

	var heldCapAfterCancel int32
	if err := pool.QueryRow(ctx,
		`SELECT capacity_held FROM inventory_ledger WHERE session_id = $1 AND tier_id IS NULL`,
		f.sessionID).Scan(&heldCapAfterCancel); err != nil {
		t.Fatalf("read inventory_ledger after cancel: %v", err)
	}
	if heldCapAfterCancel != 0 {
		t.Fatalf("inventory_ledger.capacity_held after cancel = %d, want 0", heldCapAfterCancel)
	}

	// Backdate expires_at (as if the cancel happened right before the
	// original TTL) and run the sweep.
	expireNow(t, ctx, pool, hold.ID)
	runOneSweep(t, ctx, pool)

	got, err := q.GetReservationByID(ctx, hold.ID)
	if err != nil {
		t.Fatalf("reload reservation: %v", err)
	}
	if got.State != "cancelled" {
		t.Fatalf("reservation state after sweep = %q, want cancelled (untouched)", got.State)
	}

	// Capacity must still be exactly 0 — a double-release would drive it
	// negative or otherwise desync it from the row-level truth.
	var heldCapAfterSweep int32
	if err := pool.QueryRow(ctx,
		`SELECT capacity_held FROM inventory_ledger WHERE session_id = $1 AND tier_id IS NULL`,
		f.sessionID).Scan(&heldCapAfterSweep); err != nil {
		t.Fatalf("read inventory_ledger after sweep: %v", err)
	}
	if heldCapAfterSweep != 0 {
		t.Fatalf("inventory_ledger.capacity_held after sweep = %d, want 0 (unchanged)", heldCapAfterSweep)
	}

	// A fresh hold for the full pool must succeed — the pool was never
	// actually double-debited.
	if _, err := hcheckout.CreateGAHold(ctx, pool, q, hcheckout.GAHoldInput{
		OrgID:     f.orgID,
		ChannelID: f.channelID,
		SessionID: f.sessionID,
		Items:     []hcheckout.GAHoldItem{{TierID: f.tierID, Quantity: capacity, UnitPrice: holdTierPrice}},
		ExpiresAt: time.Now().Add(20 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateGAHold for the full pool after sweep: %v", err)
	}
}

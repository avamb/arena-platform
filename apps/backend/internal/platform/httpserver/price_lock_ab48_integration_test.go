//go:build integration

// price_lock_ab48_integration_test.go — live-DB coverage for AB-48:
// the GiST exclusion constraint on ticket_tier_prices, resolution through
// the one resolver, and the price lock at reservation creation (a cart
// held across a window boundary keeps the price it was quoted).
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestAB48Integration
package httpserver

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/priceresolve"
)

func TestAB48Integration_ScheduleExclusionAndLock(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping AB-48 integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	// Reuse the AB-49 fixture: org/venue/event/session/tier(2500 EUR)/channel/reservation.
	f := newAB49Fixture(t, ctx, pool, "general_admission")
	defer f.cleanup()
	q := gen.New(pool)

	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	boundary := now.Add(time.Hour)
	later := now.Add(48 * time.Hour)

	// Early-bird window covering "now", standard window after it.
	if _, err := q.InsertTierPriceWindow(ctx, f.tierID, past, &boundary, 1500); err != nil {
		t.Fatalf("insert early-bird: %v", err)
	}
	if _, err := q.InsertTierPriceWindow(ctx, f.tierID, boundary, &later, 2000); err != nil {
		t.Fatalf("insert standard (back-to-back must be legal): %v", err)
	}
	// Overlap is impossible at the DB level (SQLSTATE 23P01).
	mid := now.Add(30 * time.Minute)
	if _, err := q.InsertTierPriceWindow(ctx, f.tierID, mid, nil, 999); err == nil {
		t.Fatal("overlapping window was accepted — the GiST exclusion constraint is missing")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23P01" {
			t.Fatalf("overlap error = %v, want SQLSTATE 23P01", err)
		}
	}

	// Resolution through the ONE resolver: now -> 1500 with next change at
	// the boundary; after the boundary -> 2000; after the last window ->
	// base 2500 (gap policy).
	tier, err := q.GetTicketTierByID(ctx, f.tierID, f.sessionID)
	if err != nil {
		t.Fatalf("GetTicketTierByID: %v", err)
	}
	eff, err := priceresolve.ForTier(ctx, q, tier, now)
	if err != nil {
		t.Fatalf("ForTier now: %v", err)
	}
	if eff.Amount != 1500 || eff.NextChangeAt == nil || !eff.NextChangeAt.Equal(boundary.Truncate(time.Microsecond)) {
		t.Fatalf("now: amount=%d next=%v, want 1500 @ %v", eff.Amount, eff.NextChangeAt, boundary)
	}
	if eff2, _ := priceresolve.ForTier(ctx, q, tier, boundary.Add(time.Minute)); eff2.Amount != 2000 {
		t.Fatalf("after boundary amount = %d, want 2000", eff2.Amount)
	}
	if eff3, _ := priceresolve.ForTier(ctx, q, tier, later.Add(time.Minute)); eff3.Amount != 2500 || eff3.Scheduled {
		t.Fatalf("gap: amount=%d scheduled=%v, want base 2500 unscheduled", eff3.Amount, eff3.Scheduled)
	}

	// Price lock: a reservation created NOW locks 1500; reading the lock
	// after the boundary still yields 1500 (the cart keeps its quote).
	locked, err := hcheckout.WriteReservationPriceLinesTx(ctx, q, f.sessionID, f.resID,
		map[uuid.UUID]int32{f.tierID: 2}, now)
	if err != nil {
		t.Fatalf("WriteReservationPriceLinesTx: %v", err)
	}
	if locked[f.tierID] != 1500 {
		t.Fatalf("locked price = %d, want 1500", locked[f.tierID])
	}
	stored, err := hcheckout.LockedTierPrices(ctx, q, f.resID)
	if err != nil {
		t.Fatalf("LockedTierPrices: %v", err)
	}
	if stored[f.tierID] != 1500 {
		t.Fatalf("stored lock = %d, want 1500 regardless of the live window", stored[f.tierID])
	}
	if live, _ := priceresolve.ForTier(ctx, q, tier, boundary.Add(time.Minute)); live.Amount == stored[f.tierID] {
		t.Fatal("test precondition: live price after the boundary must differ from the lock")
	}

	// Replace-all wipes the schedule; resolution falls back to base.
	if _, err := q.DeleteTierPriceWindowsByTier(ctx, f.tierID); err != nil {
		t.Fatalf("DeleteTierPriceWindowsByTier: %v", err)
	}
	if eff4, _ := priceresolve.ForTier(ctx, q, tier, now); eff4.Amount != 2500 {
		t.Fatalf("after wipe amount = %d, want base 2500", eff4.Amount)
	}
}

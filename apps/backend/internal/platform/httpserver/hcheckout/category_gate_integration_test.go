//go:build integration

// category_gate_integration_test.go drives the category gate (plan
// 08_architecture/23 step 4) against a live database through the real hold
// primitives, plus the two quota invariants that only a real database can
// show: concurrent buyers never take more places than a category owns, and
// a released place goes back to ITS OWN category.
//
// Requires DATABASE_URL against a migrated database:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci?sslmode=disable \
//	  go test -tags integration ./internal/platform/httpserver/hcheckout/...
package hcheckout_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// gateWithheldCases is the table every entry point below is driven through:
// the two ways decisions 4 and 5 withdraw a category from sale.
var gateWithheldCases = []struct {
	name     string
	withdraw func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tierID uuid.UUID)
	wantErr  error
}{
	{
		name: "closed category",
		withdraw: func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tierID uuid.UUID) {
			gateSetOpen(t, ctx, pool, tierID, false)
		},
		wantErr: hcheckout.ErrCategoryClosed,
	},
	{
		name: "sale window already ended",
		withdraw: func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tierID uuid.UUID) {
			gateSetWindow(t, ctx, pool, tierID, nil, ptrTime(time.Now().UTC().Add(-time.Hour)))
		},
		wantErr: hcheckout.ErrCategoryNotOnSale,
	},
	{
		name: "sale window has not opened",
		withdraw: func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tierID uuid.UUID) {
			gateSetWindow(t, ctx, pool, tierID, ptrTime(time.Now().UTC().Add(time.Hour)), nil)
		},
		wantErr: hcheckout.ErrCategoryNotOnSale,
	},
}

func ptrTime(v time.Time) *time.Time { return &v }

func gateSetOpen(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tierID uuid.UUID, open bool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE ticket_tiers SET is_open=$2 WHERE id=$1`, tierID, open); err != nil {
		t.Fatalf("set is_open=%v: %v", open, err)
	}
}

func gateSetWindow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tierID uuid.UUID, start, end *time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`UPDATE ticket_tiers SET sale_window_start=$2, sale_window_end=$3 WHERE id=$1`,
		tierID, start, end); err != nil {
		t.Fatalf("set sale window: %v", err)
	}
}

func gatePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	cfg.MaxConns = 24
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	return pool
}

// TestCategoryGate_RefusesNewGAHold covers the GA entry point every
// gateway RESERVATION with a categoryList and the REST/widget GA carts go
// through (hcheckout.CreateGAHold).
func TestCategoryGate_RefusesNewGAHold_LiveDB(t *testing.T) {
	pool := gatePool(t)
	defer pool.Close()
	ctx := context.Background()

	for _, tc := range gateWithheldCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newHoldFixture(t, ctx, pool, "general_admission", 10, 0)
			defer f.cleanup()
			q := gen.New(pool)

			// Sanity: the same hold succeeds while the category is on sale.
			if _, err := hcheckout.CreateGAHold(ctx, pool, q, gateGAInput(f, 1)); err != nil {
				t.Fatalf("baseline GA hold failed: %v", err)
			}

			tc.withdraw(t, ctx, pool, f.tierID)

			_, err := hcheckout.CreateGAHold(ctx, pool, q, gateGAInput(f, 1))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("CreateGAHold after withdrawal = %v, want %v", err, tc.wantErr)
			}
			// The refusal must roll the whole transaction back: no place
			// held, no capacity reserved.
			if held := gateCountPlaces(t, ctx, pool, f.sessionID, "held"); held != 1 {
				t.Errorf("held places = %d, want 1 (only the baseline hold)", held)
			}
		})
	}
}

// TestCategoryGate_RefusesNewSeatedHold covers decision 10: a closed or
// off-sale SEATED category refuses a new hold of its seats too.
func TestCategoryGate_RefusesNewSeatedHold_LiveDB(t *testing.T) {
	pool := gatePool(t)
	defer pool.Close()
	ctx := context.Background()

	for _, tc := range gateWithheldCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newHoldFixture(t, ctx, pool, "assigned_seats", 10, 4)
			defer f.cleanup()
			q := gen.New(pool)
			keys := f.seatKeys(4)

			if _, err := hcheckout.CreateSeatedHold(ctx, pool, q, hcheckout.SeatedHoldInput{
				OrgID: f.orgID, ChannelID: f.channelID, SessionID: f.sessionID,
				SeatKeys: keys[:1], ExpiresAt: time.Now().Add(20 * time.Minute),
			}); err != nil {
				t.Fatalf("baseline seated hold failed: %v", err)
			}

			tc.withdraw(t, ctx, pool, f.tierID)

			_, err := hcheckout.CreateSeatedHold(ctx, pool, q, hcheckout.SeatedHoldInput{
				OrgID: f.orgID, ChannelID: f.channelID, SessionID: f.sessionID,
				SeatKeys: keys[1:2], ExpiresAt: time.Now().Add(20 * time.Minute),
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("CreateSeatedHold after withdrawal = %v, want %v", err, tc.wantErr)
			}
			if held := gateCountPlaces(t, ctx, pool, f.sessionID, "held"); held != 1 {
				t.Errorf("held seats = %d, want 1 (only the baseline hold)", held)
			}
		})
	}
}

// TestCategoryGate_RefusesCartExtension covers the cart path the gateway's
// RESERVATION and CREATE_ORDER_EXT drive (hcheckout.ExtendHold) — ADDING to
// an existing cart is a new hold and is gated; the part the cart already
// holds is untouched (decision 1 in miniature).
func TestCategoryGate_RefusesCartExtension_LiveDB(t *testing.T) {
	pool := gatePool(t)
	defer pool.Close()
	ctx := context.Background()

	for _, tc := range gateWithheldCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newHoldFixture(t, ctx, pool, "general_admission", 10, 0)
			defer f.cleanup()
			q := gen.New(pool)
			resID := f.newGACart(ctx, q, 2)

			tc.withdraw(t, ctx, pool, f.tierID)

			_, err := hcheckout.ExtendHold(ctx, pool, q, hcheckout.HoldMutationInput{
				ReservationID: resID,
				GATiers:       []hcheckout.HoldTierQuantity{{TierID: f.tierID, Quantity: 1}},
				TTL:           15 * time.Minute,
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ExtendHold after withdrawal = %v, want %v", err, tc.wantErr)
			}
			if held := gateCountPlaces(t, ctx, pool, f.sessionID, "held"); held != 2 {
				t.Errorf("held places = %d, want 2 — the cart keeps what it already held", held)
			}
		})
	}
}

// TestCategoryGate_ReacquireAndReleaseIgnoreTheGate is decision 1 on the
// hold layer: a cart that ALREADY holds its places can re-assert and give
// them up after the category is closed. PAY_ORDER rides on exactly this —
// an order already placed can still be paid.
func TestCategoryGate_ReacquireAndReleaseIgnoreTheGate_LiveDB(t *testing.T) {
	pool := gatePool(t)
	defer pool.Close()
	ctx := context.Background()

	f := newHoldFixture(t, ctx, pool, "general_admission", 10, 0)
	defer f.cleanup()
	q := gen.New(pool)
	resID := f.newGACart(ctx, q, 2)

	gateSetOpen(t, ctx, pool, f.tierID, false)
	gateSetWindow(t, ctx, pool, f.tierID, nil, ptrTime(time.Now().UTC().Add(-time.Hour)))

	if _, err := hcheckout.ReacquireHold(ctx, pool, q, hcheckout.HoldMutationInput{
		ReservationID: resID, TTL: 15 * time.Minute,
	}); err != nil {
		t.Fatalf("ReacquireHold on a closed category = %v, want nil (decision 1)", err)
	}
	if held := gateCountPlaces(t, ctx, pool, f.sessionID, "held"); held != 2 {
		t.Errorf("held places after reacquire = %d, want 2", held)
	}

	if _, err := hcheckout.ReleaseHold(ctx, pool, q, resID); err != nil {
		t.Fatalf("ReleaseHold on a closed category = %v, want nil", err)
	}
	if held := gateCountPlaces(t, ctx, pool, f.sessionID, "held"); held != 0 {
		t.Errorf("held places after release = %d, want 0", held)
	}
	// Released places stay in their category: nothing un-stamps tier_id.
	if free := gateCountPlacesForTier(t, ctx, pool, f.sessionID, f.tierID, "available"); free != 10 {
		t.Errorf("available places of the category = %d, want 10 — a released place stays in its category", free)
	}
}

// TestCategoryQuota_ConcurrentHoldsNeverExceedQuantity is the quota
// invariant: N buyers racing for a category that owns M places produce
// exactly M held places and M/perHold winners, no matter how the races
// interleave.
func TestCategoryQuota_ConcurrentHoldsNeverExceedQuantity_LiveDB(t *testing.T) {
	pool := gatePool(t)
	defer pool.Close()
	ctx := context.Background()

	const quantity = 24
	const perHold = 2
	const buyers = 30

	f := newHoldFixture(t, ctx, pool, "general_admission", quantity, 0)
	defer f.cleanup()
	q := gen.New(pool)

	var wins, losses atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < buyers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := hcheckout.CreateGAHold(ctx, pool, q, gateGAInput(f, perHold))
			var capErr *hcheckout.CapacityError
			switch {
			case err == nil:
				wins.Add(1)
			case errors.As(err, &capErr):
				losses.Add(1)
			default:
				t.Errorf("unexpected hold error: %v", err)
			}
		}()
	}
	wg.Wait()

	if wins.Load() != quantity/perHold || losses.Load() != buyers-quantity/perHold {
		t.Fatalf("wins=%d losses=%d; want wins=%d losses=%d",
			wins.Load(), losses.Load(), quantity/perHold, buyers-quantity/perHold)
	}
	if held := gateCountPlacesForTier(t, ctx, pool, f.sessionID, f.tierID, "held"); held != quantity {
		t.Fatalf("held places = %d, want exactly the category's %d — the quota was exceeded or short-sold",
			held, quantity)
	}
	if free := gateCountPlacesForTier(t, ctx, pool, f.sessionID, f.tierID, "available"); free != 0 {
		t.Fatalf("available places = %d, want 0", free)
	}
	f.assertNoUnitHeldTwice(ctx)
	f.assertLedgerMatchesRows(ctx)
}

func gateGAInput(f *holdFixture, qty int32) hcheckout.GAHoldInput {
	return hcheckout.GAHoldInput{
		OrgID:     f.orgID,
		ChannelID: f.channelID,
		SessionID: f.sessionID,
		Items:     []hcheckout.GAHoldItem{{TierID: f.tierID, Quantity: qty, UnitPrice: holdTierPrice}},
		ExpiresAt: time.Now().Add(20 * time.Minute),
	}
}

func gateCountPlaces(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID uuid.UUID, status string) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM session_seats WHERE session_id=$1 AND status=$2`,
		sessionID, status).Scan(&n); err != nil {
		t.Fatalf("count %s places: %v", status, err)
	}
	return n
}

func gateCountPlacesForTier(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID, tierID uuid.UUID, status string) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM session_seats
		 WHERE session_id=$1 AND tier_id=$2 AND status=$3`,
		sessionID, tierID, status).Scan(&n); err != nil {
		t.Fatalf("count %s places of the category: %v", status, err)
	}
	return n
}

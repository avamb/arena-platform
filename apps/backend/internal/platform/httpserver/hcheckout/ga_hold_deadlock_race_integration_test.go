//go:build integration

// ga_hold_deadlock_race_integration_test.go reproduces, against a live
// PostgreSQL database, the class of deadlock fixed by reordering
// CreateGAHold's IncrementSessionSeatStatusVersion / ReserveCapacity calls
// (see hold_api.go's createGAHoldTx doc comment and AGENTS.md's hold-mutation
// lock order gotcha).
//
// Root cause (HIGH-severity, live incident): CreateGAHold used to reserve
// inventory_ledger capacity BEFORE bumping sessions.seat_status_version,
// while every other hold mutation (CreateSeatedHold, ExtendHoldTx,
// ShrinkHoldTx, ReleaseHold) bumps the version FIRST. A buyer opening a new
// GA cart via CreateGAHold therefore took the two locks in the opposite
// order from a buyer extending/shrinking an existing cart on the SAME
// session, and Postgres periodically broke the resulting lock cycle with a
// 40P01 deadlock_detected — which surfaced on the Bil24 gateway wire as
// resultCode -99 (non-retryable) before this fix's error-mapping change too.
//
// This test drives exactly that contention shape — N goroutines opening and
// releasing brand-new GA holds racing N goroutines extending/shrinking a
// pre-existing hold, all against the same session — for a few seconds, and
// asserts that no 40P01/40001 ever escapes hcheckout's bounded retry, that
// every non-nil error is one of the expected typed hold errors, and that
// inventory_ledger's capacity_held rollup matches the session_seats rows
// still genuinely held at the end (no leaked or short capacity).
//
// Requires DATABASE_URL against a migrated database:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena?sslmode=disable \
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

// TestGAHoldDeadlockRace_LiveDB is the reproduction + regression guard for
// the CreateGAHold lock-order deadlock. Run it against pre-fix code (revert
// the reorder in hold_api.go's createGAHoldTx and drop the retry wrapping)
// to see it fail/log deadlocks; against the fixed code it must be clean.
func TestGAHoldDeadlockRace_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	ctx := context.Background()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	cfg.MaxConns = 40
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	// A pool of 200 ga_unit rows, per the task's reproduction recipe.
	const poolCapacity = 200
	f := newHoldFixture(t, ctx, pool, "general_admission", poolCapacity, 0)
	defer f.cleanup()

	q := gen.New(pool)

	const racers = 12
	const raceDuration = 3 * time.Second

	// One pre-existing cart per "mutate" racer so they never contend with
	// EACH OTHER over the same reservation row (lockOpenReservationTx would
	// just serialize same-row mutators, which is correct but is not the
	// cross-primitive contention this test targets — CreateGAHold/ReleaseHold
	// on fresh reservations racing ExtendHold/ShrinkHold on existing ones,
	// all fighting over the SAME session's seat_status_version row and
	// inventory_ledger row).
	mutateCarts := make([]uuid.UUID, racers)
	for i := range mutateCarts {
		mutateCarts[i] = f.newGACart(ctx, q, 2)
	}

	var deadlocks, newHoldOps, mutateOps, typedErrs atomic.Int64
	var wg sync.WaitGroup
	stop := time.Now().Add(raceDuration)

	recordErr := func(op string, err error) {
		switch {
		case hcheckout.IsSerializationFailure(err):
			deadlocks.Add(1)
			t.Errorf("%s: a 40P01/40001 serialization failure escaped the retry helper: %v", op, err)
		case isTypedHoldRaceErr(err):
			typedErrs.Add(1)
		default:
			t.Errorf("%s: unexpected error: %v", op, err)
		}
	}

	// Group A: open a brand-new GA hold (quantity 1) and immediately release
	// it — the exact CreateGAHold call shape from the live incident.
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(stop) {
				res, err := hcheckout.CreateGAHold(ctx, pool, q, hcheckout.GAHoldInput{
					OrgID:     f.orgID,
					ChannelID: f.channelID,
					SessionID: f.sessionID,
					Items:     []hcheckout.GAHoldItem{{TierID: f.tierID, Quantity: 1, UnitPrice: holdTierPrice}},
					ExpiresAt: time.Now().Add(15 * time.Minute),
				})
				if err != nil {
					recordErr("CreateGAHold", err)
					continue
				}
				newHoldOps.Add(1)
				if _, err := hcheckout.ReleaseHold(ctx, pool, q, res.ID); err != nil {
					recordErr("ReleaseHold", err)
				}
			}
		}()
	}

	// Group B: extend then shrink back a pre-existing hold on the SAME
	// session — the concurrent cart the live incident described.
	for i := 0; i < racers; i++ {
		resID := mutateCarts[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			line := []hcheckout.HoldTierQuantity{{TierID: f.tierID, Quantity: 1}}
			for time.Now().Before(stop) {
				in := hcheckout.HoldMutationInput{ReservationID: resID, GATiers: line, TTL: 15 * time.Minute}
				if _, err := hcheckout.ExtendHold(ctx, pool, q, in); err != nil {
					recordErr("ExtendHold", err)
					continue
				}
				mutateOps.Add(1)
				if _, err := hcheckout.ShrinkHold(ctx, pool, q, in); err != nil {
					recordErr("ShrinkHold", err)
				}
			}
		}()
	}

	wg.Wait()
	t.Logf("GA deadlock race: %d new-hold ops, %d mutate ops, %d typed hold errors, %d escaped serialization failures over %v",
		newHoldOps.Load(), mutateOps.Load(), typedErrs.Load(), deadlocks.Load(), raceDuration)

	if deadlocks.Load() != 0 {
		t.Fatalf("%d serialization failures escaped the retry helper — lock order regressed", deadlocks.Load())
	}
	if newHoldOps.Load() == 0 || mutateOps.Load() == 0 {
		t.Fatalf("race did not exercise both sides: new-hold ops=%d mutate ops=%d", newHoldOps.Load(), mutateOps.Load())
	}

	f.assertLedgerMatchesRows(ctx)
	assertCapacityHeldMatchesOpenReservations(ctx, t, pool, f.sessionID)
}

// isTypedHoldRaceErr reports whether err is one of the hcheckout typed hold
// errors a legitimate capacity/seat contest is expected to produce under
// this much concurrency, as opposed to an infrastructure failure.
func isTypedHoldRaceErr(err error) bool {
	var capErr *hcheckout.CapacityError
	var conflictErr *hcheckout.SeatConflictsError
	var notMutable *hcheckout.NotMutableError
	return errors.As(err, &capErr) || errors.As(err, &conflictErr) || errors.As(err, &notMutable) ||
		errors.Is(err, hcheckout.ErrHoldNotFound)
}

// assertCapacityHeldMatchesOpenReservations is the task's headline
// invariant: inventory_ledger.capacity_held (session-level, tier_id IS NULL
// — every AB-51 hold reserves there) must equal the number of session_seats
// rows genuinely held at the end, with no leaked or short capacity. Every
// cart in this test is seeded through the production ExtendHold primitive
// (newGACart / CreateGAHold), so — unlike the concurrency test's session-
// level-only newCart seed — quantity, links and the ledger must agree
// exactly, with no "seeded without rows" slack term.
func assertCapacityHeldMatchesOpenReservations(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sessionID uuid.UUID) {
	t.Helper()
	var ledgerHeld, heldUnits int64
	if err := pool.QueryRow(ctx,
		`SELECT
		   (SELECT capacity_held FROM inventory_ledger WHERE session_id=$1 AND tier_id IS NULL),
		   (SELECT COUNT(*) FROM session_seats WHERE session_id=$1 AND status='held')`,
		sessionID).Scan(&ledgerHeld, &heldUnits); err != nil {
		t.Fatalf("capacity probe: %v", err)
	}
	if ledgerHeld != heldUnits {
		t.Fatalf("inventory_ledger.capacity_held=%d but %d session_seats rows are held — leaked/short capacity",
			ledgerHeld, heldUnits)
	}
}

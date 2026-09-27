//go:build integration

package gaquota_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/tierchain"
)

// ─────────────────────────────────────────────────────────────────────────────
// Chain of categories (migration 0112): when a category's sale window closes,
// its free places move to the next one, which opens.
// ─────────────────────────────────────────────────────────────────────────────

func TestGAQuota_HandOver_MovesFreePlacesAndSwitchesCategories(t *testing.T) {
	ctx, pool, q := quotaDB(t)
	f := newQuotaFixture(t, ctx, pool, quotaFixtureOpts{})
	defer f.cleanup()

	best := f.addTier(ctx, "Best party", 0)
	last := f.addTier(ctx, "Last minute", 1)
	f.mustCreate(ctx, q, best, 77)
	f.mustCreate(ctx, q, last, 1)
	f.setOpen(ctx, last, false)
	f.markUnits(ctx, best, "sold", 2)
	f.markUnits(ctx, best, "held", 1)
	// And a place behind reservation_seats (a converted hold) never moves.
	pinned := f.referenceHighestPlace(ctx, best)

	f.closeWindow(ctx, best)
	f.chain(ctx, q, best, last)

	link := f.dueLink(ctx, q, best)
	res := f.mustHandOver(ctx, q, link)
	if !res.First {
		t.Errorf("first hand-over not flagged as first")
	}
	// 77 - 2 sold - 1 held - 1 pinned = 73 free places move.
	if res.Moved != 73 {
		t.Errorf("moved %d places, want 73", res.Moved)
	}
	f.wantUnitCount(ctx, best, 4)
	f.wantUnitCount(ctx, last, 74)
	f.wantTierCapacity(ctx, best, 4)
	f.wantTierCapacity(ctx, last, 74)
	f.wantTierOpen(ctx, best, false)
	f.wantTierOpen(ctx, last, true)
	// The hall does not grow.
	f.wantSessionCapacity(ctx, 78)
	f.wantLedgerTotal(ctx, 78)
	f.wantNoDuplicateKeys(ctx)
	f.wantKeyPrefix(ctx, last, "ga|t2|")
	if !f.placeInTier(ctx, pinned, best) {
		t.Errorf("a place behind reservation_seats moved")
	}

	// Nothing left to move: the link is no longer due.
	if links := f.dueLinks(ctx, q); containsLink(links, best) {
		t.Errorf("link still due after the hand-over")
	}
}

func TestGAQuota_HandOver_LaterSweepMovesAReturnedPlaceWithoutReopening(t *testing.T) {
	ctx, pool, q := quotaDB(t)
	f := newQuotaFixture(t, ctx, pool, quotaFixtureOpts{})
	defer f.cleanup()

	early := f.addTier(ctx, "Early", 0)
	std := f.addTier(ctx, "Standard", 1)
	f.mustCreate(ctx, q, early, 10)
	f.mustCreate(ctx, q, std, 5)
	f.markUnits(ctx, early, "held", 2)
	f.closeWindow(ctx, early)
	f.chain(ctx, q, early, std)
	f.mustHandOver(ctx, q, f.dueLink(ctx, q, early))
	f.wantUnitCount(ctx, std, 13)

	// The operator closes Standard by hand; then a held place of Early
	// comes back to the pool (its cart expired).
	f.setOpen(ctx, std, false)
	if _, err := pool.Exec(ctx,
		`UPDATE session_seats SET status='available' WHERE id = (
		   SELECT id FROM session_seats WHERE session_id=$1 AND tier_id=$2 AND status='held'
		   ORDER BY seat_key LIMIT 1)`, f.sessionID, early); err != nil {
		t.Fatalf("release a held place: %v", err)
	}
	res := f.mustHandOver(ctx, q, f.dueLink(ctx, q, early))
	if res.First || res.Moved != 1 {
		t.Errorf("second hand-over = %+v, want Moved 1, First false", res)
	}
	f.wantUnitCount(ctx, std, 14)
	f.wantTierOpen(ctx, std, false) // a later sweep never reopens it
	f.wantNoDuplicateKeys(ctx)
}

func TestGAQuota_HandOver_ChainCascadesThroughExpiredWindows(t *testing.T) {
	ctx, pool, q := quotaDB(t)
	f := newQuotaFixture(t, ctx, pool, quotaFixtureOpts{})
	defer f.cleanup()

	a := f.addTier(ctx, "Start", 0)
	b := f.addTier(ctx, "Friends", 1)
	c := f.addTier(ctx, "Party", 2)
	f.mustCreate(ctx, q, a, 10)
	f.mustCreate(ctx, q, b, 1)
	f.mustCreate(ctx, q, c, 1)
	f.closeWindowAt(ctx, a, time.Now().Add(-2*time.Hour))
	f.closeWindowAt(ctx, b, time.Now().Add(-1*time.Hour))
	f.chain(ctx, q, a, b)
	f.chain(ctx, q, b, c)

	// The real sweep handler, not HandOver directly.
	h := tierchain.NewHandler(tierchain.Options{Store: tierchain.NewPGStore(pool)})
	for i := 0; i < 2; i++ { // A->B, then B (now holding A's places) -> C
		if err := h(ctx, nil); err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
	}
	f.wantUnitCount(ctx, c, 12)
	f.wantTierOpen(ctx, c, true)
	f.wantTierOpen(ctx, a, false)
	f.wantTierOpen(ctx, b, false)
	f.wantSessionCapacity(ctx, 12)
}

func TestGAQuota_HandOver_RefusesSeatedTarget(t *testing.T) {
	ctx, pool, q := quotaDB(t)
	f := newQuotaFixture(t, ctx, pool, quotaFixtureOpts{seated: true, seats: 4})
	defer f.cleanup()

	seated := f.addTier(ctx, "Seated", 0)
	f.assignSeatsToTier(ctx, seated)
	ga := f.addTier(ctx, "Standing", 1)
	f.mustCreate(ctx, q, ga, 3)
	f.closeWindow(ctx, ga)
	f.chain(ctx, q, ga, seated)

	_, err := f.handOver(ctx, q, f.dueLink(ctx, q, ga))
	if !errors.Is(err, gaquota.ErrSeatedCategory) {
		t.Fatalf("hand-over to a seated category: err = %v, want ErrSeatedCategory", err)
	}
	f.wantUnitCount(ctx, ga, 3)
}

// ─── helpers ────────────────────────────────────────────────────────────────

func (f *quotaFixture) setOpen(ctx context.Context, tierID uuid.UUID, open bool) {
	f.t.Helper()
	if _, err := f.pool.Exec(ctx, `UPDATE ticket_tiers SET is_open=$2 WHERE id=$1`, tierID, open); err != nil {
		f.t.Fatalf("set open: %v", err)
	}
}

func (f *quotaFixture) closeWindow(ctx context.Context, tierID uuid.UUID) {
	f.closeWindowAt(ctx, tierID, time.Now().Add(-time.Minute))
}

func (f *quotaFixture) closeWindowAt(ctx context.Context, tierID uuid.UUID, at time.Time) {
	f.t.Helper()
	if _, err := f.pool.Exec(ctx,
		`UPDATE ticket_tiers SET sale_window_start=NULL, sale_window_end=$2 WHERE id=$1`, tierID, at); err != nil {
		f.t.Fatalf("close sale window: %v", err)
	}
}

func (f *quotaFixture) chain(ctx context.Context, q *gen.Queries, from, to uuid.UUID) {
	f.t.Helper()
	if err := q.UpsertTierChain(ctx, from, to); err != nil {
		f.t.Fatalf("upsert chain: %v", err)
	}
}

func (f *quotaFixture) dueLinks(ctx context.Context, q *gen.Queries) []gen.TierChainRow {
	f.t.Helper()
	links, err := q.ListDueTierHandOvers(ctx, 1000)
	if err != nil {
		f.t.Fatalf("list due hand-overs: %v", err)
	}
	return links
}

func (f *quotaFixture) dueLink(ctx context.Context, q *gen.Queries, from uuid.UUID) gen.TierChainRow {
	f.t.Helper()
	for _, l := range f.dueLinks(ctx, q) {
		if l.TierID == from {
			return l
		}
	}
	f.t.Fatalf("link from %s is not due", from)
	return gen.TierChainRow{}
}

func containsLink(links []gen.TierChainRow, from uuid.UUID) bool {
	for _, l := range links {
		if l.TierID == from {
			return true
		}
	}
	return false
}

func (f *quotaFixture) handOver(ctx context.Context, q *gen.Queries, link gen.TierChainRow) (gaquota.HandOverResult, error) {
	var res gaquota.HandOverResult
	err := gaquota.InTx(ctx, f.pool, q, func(txq *gen.Queries) error {
		var err error
		res, err = gaquota.HandOver(ctx, txq, link)
		return err
	})
	return res, err
}

func (f *quotaFixture) mustHandOver(ctx context.Context, q *gen.Queries, link gen.TierChainRow) gaquota.HandOverResult {
	f.t.Helper()
	res, err := f.handOver(ctx, q, link)
	if err != nil {
		f.t.Fatalf("HandOver: %v", err)
	}
	return res
}

func (f *quotaFixture) placeInTier(ctx context.Context, placeID, tierID uuid.UUID) bool {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM session_seats WHERE id=$1 AND tier_id=$2`, placeID, tierID).Scan(&n); err != nil {
		f.t.Fatalf("place tier: %v", err)
	}
	return n == 1
}

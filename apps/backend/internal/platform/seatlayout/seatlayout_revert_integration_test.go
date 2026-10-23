//go:build integration

package seatlayout_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/seatlayout"
)

// TestRevert_TierUnavailableAndNothingToRevert_LiveDB: a revert skips a seat
// whose previous category was deleted (tier_unavailable) and still puts the
// others back; a revert that can put back NO seat is refused with
// *NothingToRevertError and writes nothing, so the batch stays revertable.
func TestRevert_TierUnavailableAndNothingToRevert_LiveDB(t *testing.T) {
	f := newLayoutFixture(t, 4)
	ctx := context.Background()
	tier3 := uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, unit_seq, is_open)
	    VALUES ($1, $2, 'Loge', 'fixed', 3000, 'EUR', 4, 3, true)`, tier3, f.session); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE session_seats SET tier_id = $3 WHERE session_id = $1 AND seat_key = $2`, f.session, seatKey(2), tier3); err != nil {
		t.Fatal(err)
	}

	// Batch A: seats 1 (from tier 1) and 2 (from tier 3) into tier 2.
	a, err := seatlayout.ApplyBatch(ctx, f.pool, f.q, seatlayout.BatchInput{
		SessionID: f.session, Kind: seatlayout.KindPaint,
		Changes: []seatlayout.Change{{SeatKey: seatKey(1), TierID: &f.tier2}, {SeatKey: seatKey(2), TierID: &f.tier2}},
	})
	if err != nil || len(a.Changes) != 2 {
		t.Fatalf("paint: %v %+v", err, a)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE ticket_tiers SET deleted_at = now() WHERE id = $1`, tier3); err != nil {
		t.Fatal(err)
	}
	rev, err := seatlayout.Revert(ctx, f.pool, f.q, seatlayout.RevertInput{SessionID: f.session, BatchID: a.ID})
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if c := changedKeys(rev); len(c) != 1 || *c[seatKey(1)].To.TierID != f.tier1 {
		t.Fatalf("revert changes = %+v", rev.Changes)
	}
	if r := skippedReasons(rev); r[seatKey(2)] != seatlayout.ReasonTierUnavailable {
		t.Fatalf("revert skipped = %+v", rev.Skipped)
	}
	if tier, _, _ := f.seat(seatKey(2)); tier != f.tier2 {
		t.Fatal("seat 2 moved into a deleted category")
	}

	// Batch B paints seat 3; batch C then closes it. Reverting B puts back
	// nothing: refused, nothing written, B still revertable after C is.
	b, err := seatlayout.ApplyBatch(ctx, f.pool, f.q, seatlayout.BatchInput{
		SessionID: f.session, Kind: seatlayout.KindPaint, Changes: []seatlayout.Change{{SeatKey: seatKey(3), TierID: &f.tier2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := seatlayout.ApplyBatch(ctx, f.pool, f.q, seatlayout.BatchInput{
		SessionID: f.session, Kind: seatlayout.KindClose, Changes: []seatlayout.Change{{SeatKey: seatKey(3), Status: strp("unavailable")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	version, batches := f.version_(), f.batchCount()
	_, err = seatlayout.Revert(ctx, f.pool, f.q, seatlayout.RevertInput{SessionID: f.session, BatchID: b.ID})
	var nothing *seatlayout.NothingToRevertError
	if !errors.As(err, &nothing) || len(nothing.Skipped) != 1 || nothing.Skipped[0].Reason != seatlayout.ReasonChangedSince {
		t.Fatalf("revert of B: %v", err)
	}
	if f.version_() != version || f.batchCount() != batches {
		t.Fatal("a refused revert must leave no trace")
	}
	if _, err := seatlayout.Revert(ctx, f.pool, f.q, seatlayout.RevertInput{SessionID: f.session, BatchID: c.ID}); err != nil {
		t.Fatalf("revert of C: %v", err)
	}
	revB, err := seatlayout.Revert(ctx, f.pool, f.q, seatlayout.RevertInput{SessionID: f.session, BatchID: b.ID})
	if err != nil || len(revB.Changes) != 1 {
		t.Fatalf("revert of B after C: %v %+v", err, revB)
	}
	if tier, status, _ := f.seat(seatKey(3)); tier != f.tier1 || status != "available" {
		t.Fatalf("seat 3 = %s %s", tier, status)
	}
}

// TestApplyBatch_GAPlace_LiveDB: a GA place never changes category but
// opens and closes.
func TestApplyBatch_GAPlace_LiveDB(t *testing.T) {
	f := newLayoutFixture(t, 1)
	ctx := context.Background()
	const ga = "ga|t1|000001"
	if _, err := f.pool.Exec(ctx, `INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
	    VALUES ($1, $2, '', '', '', $3, 'available', 'ga_unit')`, f.session, ga, f.tier1); err != nil {
		t.Fatal(err)
	}
	paint, err := seatlayout.ApplyBatch(ctx, f.pool, f.q, seatlayout.BatchInput{
		SessionID: f.session, Kind: seatlayout.KindPaint, Changes: []seatlayout.Change{{SeatKey: ga, TierID: &f.tier2}},
	})
	if err != nil || len(paint.Changes) != 0 || skippedReasons(paint)[ga] != seatlayout.ReasonGAUnit {
		t.Fatalf("paint GA: %v %+v", err, paint)
	}
	closeB, err := seatlayout.ApplyBatch(ctx, f.pool, f.q, seatlayout.BatchInput{
		SessionID: f.session, Kind: seatlayout.KindClose, Changes: []seatlayout.Change{{SeatKey: ga, Status: strp("unavailable")}},
	})
	if err != nil || len(closeB.Changes) != 1 {
		t.Fatalf("close GA: %v %+v", err, closeB)
	}
	if tier, status, _ := f.seat(ga); tier != f.tier1 || status != "unavailable" {
		t.Fatalf("GA place = %s %s", tier, status)
	}
}

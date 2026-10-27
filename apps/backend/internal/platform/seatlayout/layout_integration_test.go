//go:build integration

// layout_integration_test.go — task S1d against a live PostgreSQL:
//
//   - applying a layout maps slots to categories (by id, by name, created),
//     skips sold and held seats, closes the seats the layout closes and bumps
//     seat_status_version exactly once, as a batch of kind apply_layout;
//   - a dry run answers the same diff and leaves no trace (no batch, no
//     category, no version bump);
//   - a slot with no category is refused whole, with the list of slots;
//   - the applied batch reverts like any other;
//   - a layout of another plan, a category of another session and a slot in
//     another currency are refused;
//   - a layout racing N buyers' holds on the same seats never deadlocks and
//     leaves every seat either held or repainted.
//
// Requires DATABASE_URL against a migrated database.
package seatlayout_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/seatlayout"
)

func layoutPayload(seats int) seatlayout.LayoutPayload {
	p := seatlayout.LayoutPayload{
		Slots: []seatlayout.LayoutSlot{
			{Key: "parter", Name: "parter", PriceAmount: 1000, SortOrder: 0},
			{Key: "gold", Name: "Gold", Color: "#FFD700", PriceAmount: 4000, SortOrder: 5},
		},
		Assignments: map[string]string{},
	}
	for n := 1; n <= seats; n++ {
		slot := "gold"
		if n%2 == 1 {
			slot = "parter"
		}
		p.Assignments[seatKey(n)] = slot
	}
	return p
}

func (f *layoutFixture) tierCount() int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ticket_tiers WHERE session_id = $1 AND deleted_at IS NULL`, f.session).Scan(&n); err != nil {
		f.t.Fatalf("count tiers: %v", err)
	}
	return n
}

func (f *layoutFixture) apply(in seatlayout.ApplyLayoutInput) (seatlayout.ApplyLayoutResult, error) {
	in.OrgID, in.SessionID, in.PlanID = f.org, f.session, f.plan
	return seatlayout.ApplyLayout(context.Background(), f.pool, f.q, in)
}

func slotActions(res seatlayout.ApplyLayoutResult) map[string]string {
	out := map[string]string{}
	for _, s := range res.Slots {
		out[s.SlotKey] = s.Action
	}
	return out
}

func TestApplyLayout_MapsSlotsSkipsFrozenAndRevertsLikeABatch_LiveDB(t *testing.T) {
	f := newLayoutFixture(t, 8)
	ctx := context.Background()

	// 1 sold upstream, 2 held by a buyer, 6 will be closed by the layout.
	if _, err := f.pool.Exec(ctx, `UPDATE session_seats SET status = 'sold' WHERE session_id = $1 AND seat_key = $2`, f.session, seatKey(1)); err != nil {
		t.Fatal(err)
	}
	if err := f.hold(seatKey(2)); err != nil {
		t.Fatalf("hold: %v", err)
	}
	payload := layoutPayload(8)
	payload.ClosedSeatKeys = []string{seatKey(6), "S|Z|404"}

	tiersBefore, versionBefore := f.tierCount(), f.version_()
	res, err := f.apply(seatlayout.ApplyLayoutInput{Actor: seatlayout.Actor{Label: "user"}, Payload: payload, CreateMissingTiers: true})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := slotActions(res); got["parter"] != seatlayout.ActionExisting || got["gold"] != seatlayout.ActionCreate {
		t.Fatalf("slot actions = %v (the name matches case-insensitively, the other is created)", got)
	}
	if f.tierCount() != tiersBefore+1 {
		t.Fatalf("tiers = %d, want one created", f.tierCount())
	}
	if res.Batch.Kind != seatlayout.KindApplyLayout || res.Batch.SeatStatusVersion != versionBefore+1 || f.version_() != versionBefore+1 {
		t.Fatalf("batch kind %s version %d (db %d), want apply_layout and exactly one bump from %d",
			res.Batch.Kind, res.Batch.SeatStatusVersion, f.version_(), versionBefore)
	}
	var goldID uuid.UUID
	var goldColor *string
	if err := f.pool.QueryRow(ctx, `SELECT id, color FROM ticket_tiers WHERE session_id = $1 AND name = 'Gold'`, f.session).Scan(&goldID, &goldColor); err != nil {
		t.Fatalf("created category: %v", err)
	}
	if goldColor == nil || *goldColor != "#ffd700" {
		t.Fatalf("created category colour = %v, want #ffd700", goldColor)
	}
	var price int64
	var currency, mode string
	if err := f.pool.QueryRow(ctx, `SELECT price_amount, currency, pricing_mode FROM ticket_tiers WHERE id = $1`, goldID).Scan(&price, &currency, &mode); err != nil || price != 4000 || currency != "EUR" || mode != "fixed" {
		t.Fatalf("created category = %d %s %s (%v)", price, currency, mode, err)
	}

	skipped := skippedReasons(res.Batch)
	if skipped[seatKey(1)] != "sold" || skipped[seatKey(2)] != "held" || skipped["S|Z|404"] != "seat_not_found" {
		t.Fatalf("skipped = %+v", res.Batch.Skipped)
	}
	// seat 3 (odd) stays tier1 = a no-op, seat 4 (even) goes to Gold, 6 is Gold AND closed.
	if tier, _, _ := f.seat(seatKey(4)); tier != goldID {
		t.Fatalf("seat 4 tier = %s, want Gold", tier)
	}
	if tier, status, _ := f.seat(seatKey(6)); tier != goldID || status != "unavailable" {
		t.Fatalf("seat 6 = %s %s, want Gold and closed", tier, status)
	}
	if tier, status, _ := f.seat(seatKey(2)); tier != f.tier1 || status != "held" {
		t.Fatalf("held seat touched: %s %s", tier, status)
	}
	if tier, status, _ := f.seat(seatKey(1)); tier != f.tier1 || status != "sold" {
		t.Fatalf("sold seat touched: %s %s", tier, status)
	}
	var kind string
	if err := f.pool.QueryRow(ctx, `SELECT kind FROM session_seat_batches WHERE id = $1`, res.Batch.ID).Scan(&kind); err != nil || kind != "apply_layout" {
		t.Fatalf("stored batch kind = %q (%v)", kind, err)
	}

	// The batch reverts like any other; the category it created stays.
	rev, err := seatlayout.Revert(ctx, f.pool, f.q, seatlayout.RevertInput{OrgID: f.org, SessionID: f.session, BatchID: res.Batch.ID})
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if tier, status, _ := f.seat(seatKey(4)); tier != f.tier1 || status != "available" {
		t.Fatalf("after revert seat 4 = %s %s", tier, status)
	}
	if tier, status, _ := f.seat(seatKey(6)); tier != f.tier1 || status != "available" {
		t.Fatalf("after revert seat 6 = %s %s", tier, status)
	}
	if rev.Kind != seatlayout.KindRevert || f.tierCount() != tiersBefore+1 {
		t.Fatalf("revert kind %s, tiers %d", rev.Kind, f.tierCount())
	}

	// Applying again maps Gold by name now: nothing is created twice.
	again, err := f.apply(seatlayout.ApplyLayoutInput{Payload: payload, CreateMissingTiers: true})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if got := slotActions(again); got["gold"] != seatlayout.ActionExisting || f.tierCount() != tiersBefore+1 {
		t.Fatalf("second apply: actions %v, tiers %d", got, f.tierCount())
	}
}

func TestApplyLayout_DryRunLeavesNoTrace_LiveDB(t *testing.T) {
	f := newLayoutFixture(t, 6)
	payload := layoutPayload(6)
	versionBefore, batchesBefore, tiersBefore := f.version_(), f.batchCount(), f.tierCount()

	res, err := f.apply(seatlayout.ApplyLayoutInput{Payload: payload, CreateMissingTiers: true, DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !res.DryRun || res.Batch.ID != uuid.Nil {
		t.Fatalf("dry run result = %+v", res)
	}
	if got := slotActions(res); got["gold"] != seatlayout.ActionCreate {
		t.Fatalf("a dry run still reports the category it would create: %v", got)
	}
	if len(res.Batch.Changes) != 3 { // the three even seats go to Gold, the odd ones are no-ops
		t.Fatalf("dry run changes = %d, want 3", len(res.Batch.Changes))
	}
	if f.version_() != versionBefore || f.batchCount() != batchesBefore || f.tierCount() != tiersBefore {
		t.Fatalf("a dry run left a trace: version %d->%d, batches %d->%d, tiers %d->%d",
			versionBefore, f.version_(), batchesBefore, f.batchCount(), tiersBefore, f.tierCount())
	}
	if tier, _, _ := f.seat(seatKey(2)); tier != f.tier1 {
		t.Fatal("a dry run repainted a seat")
	}
}

func TestApplyLayout_Refusals_LiveDB(t *testing.T) {
	f := newLayoutFixture(t, 4)
	ctx := context.Background()
	versionBefore, batchesBefore, tiersBefore := f.version_(), f.batchCount(), f.tierCount()
	nothingWritten := func(what string) {
		t.Helper()
		if f.version_() != versionBefore || f.batchCount() != batchesBefore || f.tierCount() != tiersBefore {
			t.Fatalf("%s left a trace", what)
		}
	}

	// A slot no category matches, and no permission to create one.
	_, err := f.apply(seatlayout.ApplyLayoutInput{Payload: layoutPayload(4)})
	var unmapped *seatlayout.UnmappedSlotsError
	if !errors.As(err, &unmapped) || len(unmapped.Slots) != 1 || unmapped.Slots[0].SlotKey != "gold" || unmapped.Slots[0].Reason != "no_match" {
		t.Fatalf("unmapped: %v", err)
	}
	nothingWritten("an unmapped slot")

	// Two categories share the name: ambiguous, never a guess.
	if _, err := f.pool.Exec(ctx, `INSERT INTO ticket_tiers (session_id, name, pricing_mode, price_amount, currency, unit_seq, is_open)
		VALUES ($1, 'GOLD', 'fixed', 1, 'EUR', 3, true), ($1, 'gold', 'fixed', 1, 'EUR', 4, true)`, f.session); err != nil {
		t.Fatal(err)
	}
	tiersBefore = f.tierCount()
	_, err = f.apply(seatlayout.ApplyLayoutInput{Payload: layoutPayload(4), CreateMissingTiers: true})
	if !errors.As(err, &unmapped) || unmapped.Slots[0].Reason != "ambiguous" {
		t.Fatalf("ambiguous: %v", err)
	}
	nothingWritten("an ambiguous slot")

	// An explicit map resolves it, to a live category of THIS session only.
	foreignTier := uuid.New()
	_, err = f.apply(seatlayout.ApplyLayoutInput{Payload: layoutPayload(4), SlotTiers: map[string]uuid.UUID{"gold": foreignTier}})
	if !errors.Is(err, seatlayout.ErrSlotTierNotInSession) {
		t.Fatalf("foreign category: %v", err)
	}
	if _, err := f.apply(seatlayout.ApplyLayoutInput{Payload: layoutPayload(4), SlotTiers: map[string]uuid.UUID{"ghost": f.tier2}}); err == nil {
		t.Fatal("a slot_tiers key that is no slot of the layout was accepted")
	}
	nothingWritten("a refused mapping")
	ok, err := f.apply(seatlayout.ApplyLayoutInput{Payload: layoutPayload(4), SlotTiers: map[string]uuid.UUID{"gold": f.tier2}})
	if err != nil {
		t.Fatalf("explicit map: %v", err)
	}
	if got := slotActions(ok); got["gold"] != seatlayout.ActionMapped {
		t.Fatalf("actions = %v", got)
	}
	if tier, _, _ := f.seat(seatKey(2)); tier != f.tier2 {
		t.Fatalf("seat 2 = %s, want the mapped category", tier)
	}

	// Another plan's layout.
	if _, err := seatlayout.ApplyLayout(ctx, f.pool, f.q, seatlayout.ApplyLayoutInput{
		OrgID: f.org, SessionID: f.session, PlanID: uuid.New(), Payload: layoutPayload(4), CreateMissingTiers: true,
	}); !errors.Is(err, seatlayout.ErrPlanMismatch) {
		t.Fatalf("plan mismatch: %v", err)
	}
	// Another organization's id: the session does not exist for it.
	if _, err := seatlayout.ApplyLayout(ctx, f.pool, f.q, seatlayout.ApplyLayoutInput{
		OrgID: f.foreign, SessionID: f.session, PlanID: f.plan, Payload: layoutPayload(4), CreateMissingTiers: true,
	}); !errors.Is(err, seatlayout.ErrSessionNotFound) {
		t.Fatalf("foreign org: %v", err)
	}
	// A slot priced in another currency cannot become a category.
	cur := layoutPayload(4)
	cur.Slots[1].Name, cur.Slots[1].Currency = "Platinum", "CZK"
	if _, err := f.apply(seatlayout.ApplyLayoutInput{Payload: cur, CreateMissingTiers: true}); err == nil {
		t.Fatal("a slot in CZK created a category on a EUR session")
	} else {
		var ce *seatlayout.SlotCurrencyError
		if !errors.As(err, &ce) || ce.Want != "EUR" {
			t.Fatalf("currency: %v", err)
		}
	}
	if f.tierCount() != tiersBefore {
		t.Fatal("a refused layout left a created category behind")
	}
}

func TestApplyLayout_SessionWithoutPlan_LiveDB(t *testing.T) {
	f := newLayoutFixture(t, 2)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET seating_plan_version_id = NULL, admission_mode = 'general_admission' WHERE id = $1`, f.session); err != nil {
		t.Fatal(err)
	}
	_, err := f.apply(seatlayout.ApplyLayoutInput{Payload: layoutPayload(2), CreateMissingTiers: true})
	if !errors.Is(err, seatlayout.ErrSessionHasNoPlan) {
		t.Fatalf("no plan: %v", err)
	}
}

// TestApplyLayout_RacesBuyersHolds_LiveDB: one layout over every seat and one
// buyer per seat, all started at once. Both sides lock the sessions row
// first, so they serialize: a seat is held (the layout skipped it) or
// repainted/closed (the buyer got a seat conflict or a repainted seat).
func TestApplyLayout_RacesBuyersHolds_LiveDB(t *testing.T) {
	const seats = 16
	f := newLayoutFixture(t, seats)
	payload := layoutPayload(seats)
	payload.ClosedSeatKeys = []string{seatKey(seats)}

	holdErr := make([]error, seats+1)
	var res seatlayout.ApplyLayoutResult
	var applyErr error
	var wg sync.WaitGroup
	start := make(chan struct{})
	for n := 1; n <= seats; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			holdErr[n] = f.hold(seatKey(n))
		}(n)
		if n == seats/2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				res, applyErr = f.apply(seatlayout.ApplyLayoutInput{Payload: payload, CreateMissingTiers: true})
			}()
		}
	}
	close(start)
	wg.Wait()
	if applyErr != nil {
		t.Fatalf("apply: %v", applyErr)
	}
	skipped := skippedReasons(res.Batch)
	var goldID uuid.UUID
	if err := f.pool.QueryRow(context.Background(), `SELECT id FROM ticket_tiers WHERE session_id = $1 AND name = 'Gold'`, f.session).Scan(&goldID); err != nil {
		t.Fatalf("Gold category: %v", err)
	}
	for n := 1; n <= seats; n++ {
		key := seatKey(n)
		tier, status, _ := f.seat(key)
		if holdErr[n] == nil {
			// The buyer won: the seat is held and the layout skipped it, or
			// the layout repainted it first and the buyer then held it.
			if status != "held" {
				t.Fatalf("seat %s: hold succeeded but status %s", key, status)
			}
			if reason, wasSkipped := skipped[key]; wasSkipped && reason != "held" {
				t.Fatalf("seat %s: skipped for %q", key, reason)
			}
			continue
		}
		// The layout won: the seat carries the layout's category (or is
		// already in it) and the buyer was refused with a seat conflict,
		// never a deadlock.
		var conflict *hcheckout.SeatConflictsError
		if !errors.As(holdErr[n], &conflict) {
			t.Fatalf("seat %s: hold failed with %v, want a seat conflict", key, holdErr[n])
		}
		want := f.tier1
		if n%2 == 0 {
			want = goldID
		}
		if tier != want {
			t.Fatalf("seat %s: tier %s, want %s", key, tier, want)
		}
	}
	if res.Batch.SeatStatusVersion == 0 {
		t.Fatal("the layout recorded no version")
	}
}

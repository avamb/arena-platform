// seat_checkout_test.go — unit tests for the SEAT-C2 pricing & seat-sell
// state-machine surface (feature #310).
//
// Coverage:
//
//	TestSeatC2_ComputePricingLines_*   — multi-line pricing math
//	TestSeatC2_BuildSeatedPricingLines — tier grouping + deterministic order
//	TestSeatC2_SellReservationSeats_*  — held→sold transitions, double-sell,
//	                                     expiry race, partial-conflict rollback
//
// No live database is required — the sell logic is exercised through a small
// in-memory fake that satisfies the seatSellQuerier interface declared in
// seat_checkout.go.
package hcheckout

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// ComputePricingLines
// ─────────────────────────────────────────────────────────────────────────────

func TestSeatC2_ComputePricingLines_SingleLine_MatchesSingleTier(t *testing.T) {
	t.Parallel()
	rules := PricingRules{PlatformFeeRate: 500, ProviderFeeRate: 200, TaxRate: 1700}

	single := ComputePricing(1000, 2, 200, "ILS", rules)
	multi := ComputePricingLines(
		[]PricingLineInput{{TierID: "t1", Quantity: 2, UnitPrice: 1000}},
		200, "ILS", rules,
	)

	if single.Subtotal != multi.Subtotal ||
		single.Discount != multi.Discount ||
		single.PlatformFee != multi.PlatformFee ||
		single.ProviderFee != multi.ProviderFee ||
		single.Tax != multi.Tax ||
		single.Total != multi.Total {
		t.Errorf("single-line multi-line mismatch:\n single=%+v\n multi=%+v", single, multi)
	}
	if got, want := len(multi.Lines), 1; got != want {
		t.Errorf("lines len: got %d, want %d", got, want)
	}
	if got, want := multi.Lines[0].Subtotal, int64(2000); got != want {
		t.Errorf("line[0].Subtotal: got %d, want %d", got, want)
	}
}

func TestSeatC2_ComputePricingLines_MultiTier_AggregatesSubtotal(t *testing.T) {
	t.Parallel()
	// Two tier groups: 2 × 1000 + 3 × 500 = 3500
	// Discount = 500 → discounted = 3000
	// 5 % platform = 150, 2 % provider = 60, 17 % tax = 510 → total = 3720.
	rules := PricingRules{PlatformFeeRate: 500, ProviderFeeRate: 200, TaxRate: 1700}
	bd := ComputePricingLines([]PricingLineInput{
		{TierID: "vip", Quantity: 2, UnitPrice: 1000},
		{TierID: "std", Quantity: 3, UnitPrice: 500},
	}, 500, "ILS", rules)

	if bd.Subtotal != 3500 {
		t.Errorf("subtotal: got %d, want 3500", bd.Subtotal)
	}
	if bd.Discount != 500 {
		t.Errorf("discount: got %d, want 500", bd.Discount)
	}
	if bd.PlatformFee != 150 {
		t.Errorf("platform_fee: got %d, want 150", bd.PlatformFee)
	}
	if bd.ProviderFee != 60 {
		t.Errorf("provider_fee: got %d, want 60", bd.ProviderFee)
	}
	if bd.Tax != 510 {
		t.Errorf("tax: got %d, want 510", bd.Tax)
	}
	if bd.Total != 3720 {
		t.Errorf("total: got %d, want 3720", bd.Total)
	}
	if bd.Quantity != 5 {
		t.Errorf("quantity: got %d, want 5", bd.Quantity)
	}
	// Weighted-average unit = 3500 / 5 = 700
	if bd.UnitPrice != 700 {
		t.Errorf("unit_price (weighted avg): got %d, want 700", bd.UnitPrice)
	}
	if len(bd.Lines) != 2 {
		t.Fatalf("lines len: got %d, want 2", len(bd.Lines))
	}
	// Verify each line carries its own subtotal.
	seen := map[string]int64{}
	for _, l := range bd.Lines {
		seen[l.TierID] = l.Subtotal
	}
	if seen["vip"] != 2000 || seen["std"] != 1500 {
		t.Errorf("per-line subtotals wrong: %+v", seen)
	}
}

func TestSeatC2_ComputePricingLines_AccountingInvariant(t *testing.T) {
	t.Parallel()
	rules := PricingRules{PlatformFeeRate: 300, ProviderFeeRate: 150, TaxRate: 1700}
	cases := [][]PricingLineInput{
		{{TierID: "a", Quantity: 1, UnitPrice: 999}},
		{{TierID: "a", Quantity: 3, UnitPrice: 1000}, {TierID: "b", Quantity: 1, UnitPrice: 250}},
		{{TierID: "free", Quantity: 5, UnitPrice: 0}},
		{{TierID: "x", Quantity: 10, UnitPrice: 1}},
	}
	for i, lines := range cases {
		lines := lines
		bd := ComputePricingLines(lines, 0, "ILS", rules)
		want := (bd.Subtotal - bd.Discount) + bd.PlatformFee + bd.ProviderFee + bd.Tax
		if bd.Total != want {
			t.Errorf("case %d: accounting invariant broken: total=%d want=%d bd=%+v", i, bd.Total, want, bd)
		}
	}
}

func TestSeatC2_ComputePricingLines_DiscountCappedAtSubtotal(t *testing.T) {
	t.Parallel()
	bd := ComputePricingLines(
		[]PricingLineInput{{TierID: "a", Quantity: 1, UnitPrice: 100}},
		9999, "USD", PricingRules{},
	)
	if bd.Discount != 100 {
		t.Errorf("discount: got %d, want 100 (capped)", bd.Discount)
	}
	if bd.Total != 0 {
		t.Errorf("total: got %d, want 0", bd.Total)
	}
}

func TestSeatC2_ComputePricingLines_NegativeDiscountClamped(t *testing.T) {
	t.Parallel()
	bd := ComputePricingLines(
		[]PricingLineInput{{TierID: "a", Quantity: 1, UnitPrice: 100}},
		-50, "USD", PricingRules{},
	)
	if bd.Discount != 0 {
		t.Errorf("discount: got %d, want 0 (clamped)", bd.Discount)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// buildSeatedPricingLines
// ─────────────────────────────────────────────────────────────────────────────

func TestSeatC2_BuildSeatedPricingLines_GroupsByTier(t *testing.T) {
	t.Parallel()
	tierA := uuid.New()
	tierB := uuid.New()

	seats := []gen.SessionSeatRow{
		{SeatKey: "A-1-1", TierID: &tierA},
		{SeatKey: "A-1-2", TierID: &tierA},
		{SeatKey: "B-1-1", TierID: &tierB},
		{SeatKey: "B-1-2", TierID: &tierB},
		{SeatKey: "B-1-3", TierID: &tierB},
	}
	tierPrice := map[string]int64{
		tierA.String(): 1000,
		tierB.String(): 500,
	}

	lines := buildSeatedPricingLines(seats, tierPrice)
	if len(lines) != 2 {
		t.Fatalf("lines len: got %d, want 2", len(lines))
	}

	// Order is deterministic by TierID ASC.
	byTier := map[string]PricingLineInput{}
	for _, l := range lines {
		byTier[l.TierID] = l
	}
	if la := byTier[tierA.String()]; la.Quantity != 2 || la.UnitPrice != 1000 {
		t.Errorf("tierA line wrong: %+v", la)
	}
	if lb := byTier[tierB.String()]; lb.Quantity != 3 || lb.UnitPrice != 500 {
		t.Errorf("tierB line wrong: %+v", lb)
	}
}

func TestSeatC2_BuildSeatedPricingLines_NilTierGroupedAsEmpty(t *testing.T) {
	t.Parallel()
	seats := []gen.SessionSeatRow{
		{SeatKey: "A-1-1", TierID: nil},
		{SeatKey: "A-1-2", TierID: nil},
	}
	lines := buildSeatedPricingLines(seats, map[string]int64{})
	if len(lines) != 1 {
		t.Fatalf("lines len: got %d, want 1", len(lines))
	}
	if lines[0].TierID != "" {
		t.Errorf("nil tier should group under empty string, got %q", lines[0].TierID)
	}
	if lines[0].Quantity != 2 || lines[0].UnitPrice != 0 {
		t.Errorf("nil-tier line wrong: %+v", lines[0])
	}
}

func TestSeatC2_BuildSeatedPricingLines_EmptySeats(t *testing.T) {
	t.Parallel()
	lines := buildSeatedPricingLines(nil, nil)
	if len(lines) != 0 {
		t.Errorf("empty seats should yield empty lines, got %d", len(lines))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// In-memory fake state machine for sellReservationSeatsTx
// ─────────────────────────────────────────────────────────────────────────────

// fakeSeatState is a tiny in-memory implementation of the seatSellQuerier
// interface. It models the §5.2 semantics well enough to exercise the
// double-sell, expiry-race and partial-conflict-rollback branches of
// sellReservationSeatsTx without a live PostgreSQL connection.
type fakeSeatState struct {
	// keyed by session_seats.id
	seats             map[uuid.UUID]*gen.SessionSeatRow
	byReservation     map[uuid.UUID][]uuid.UUID
	sessionVersion    map[uuid.UUID]int64
	incrementFails    bool
	listFails         bool
	sellReturnsNoRows map[uuid.UUID]bool // per-seat override to force expiry race
}

func newFakeSeatState() *fakeSeatState {
	return &fakeSeatState{
		seats:             map[uuid.UUID]*gen.SessionSeatRow{},
		byReservation:     map[uuid.UUID][]uuid.UUID{},
		sessionVersion:    map[uuid.UUID]int64{},
		sellReturnsNoRows: map[uuid.UUID]bool{},
	}
}

func (f *fakeSeatState) addSeat(sessionID, reservationID uuid.UUID, seatKey, status string) uuid.UUID {
	id := uuid.New()
	res := reservationID
	f.seats[id] = &gen.SessionSeatRow{
		ID:            id,
		SessionID:     sessionID,
		SeatKey:       seatKey,
		Status:        status,
		ReservationID: &res,
	}
	f.byReservation[reservationID] = append(f.byReservation[reservationID], id)
	return id
}

func (f *fakeSeatState) ListReservationSeats(_ context.Context, reservationID uuid.UUID) ([]gen.SessionSeatRow, error) {
	if f.listFails {
		return nil, errors.New("boom: list failed")
	}
	ids := f.byReservation[reservationID]
	out := make([]gen.SessionSeatRow, 0, len(ids))
	for _, id := range ids {
		out = append(out, *f.seats[id])
	}
	return out, nil
}

func (f *fakeSeatState) IncrementSessionSeatStatusVersion(_ context.Context, sessionID uuid.UUID) (int64, error) {
	if f.incrementFails {
		return 0, errors.New("boom: increment failed")
	}
	f.sessionVersion[sessionID]++
	return f.sessionVersion[sessionID], nil
}

func (f *fakeSeatState) SellSessionSeat(_ context.Context, id, reservationID uuid.UUID, statusVersion int64) (gen.SessionSeatRow, error) {
	if f.sellReturnsNoRows[id] {
		return gen.SessionSeatRow{}, pgx.ErrNoRows
	}
	s, ok := f.seats[id]
	if !ok {
		return gen.SessionSeatRow{}, pgx.ErrNoRows
	}
	// Real DB behaviour: conditional UPDATE only fires when status='held' AND
	// reservation_id matches. Any other combination yields pgx.ErrNoRows.
	if s.Status != seatStatusHeld ||
		s.ReservationID == nil || *s.ReservationID != reservationID {
		return gen.SessionSeatRow{}, pgx.ErrNoRows
	}
	s.Status = seatStatusSold
	s.StatusVersion = statusVersion
	return *s, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// sellReservationSeatsTx — state-machine tests
// ─────────────────────────────────────────────────────────────────────────────

func TestSeatC2_SellReservationSeats_HappyPath(t *testing.T) {
	t.Parallel()
	sess := uuid.New()
	res := uuid.New()

	f := newFakeSeatState()
	f.addSeat(sess, res, "A-1-1", seatStatusHeld)
	f.addSeat(sess, res, "A-1-2", seatStatusHeld)

	sold, alreadySold, err := sellReservationSeatsTx(t.Context(), f, sess, res)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sold != 2 || alreadySold != 0 {
		t.Errorf("sold=%d alreadySold=%d, want 2, 0", sold, alreadySold)
	}
	for _, id := range f.byReservation[res] {
		if got := f.seats[id].Status; got != seatStatusSold {
			t.Errorf("seat %s: got status %q, want sold", f.seats[id].SeatKey, got)
		}
	}
	if f.sessionVersion[sess] != 1 {
		t.Errorf("expected exactly one status_version bump, got %d", f.sessionVersion[sess])
	}
}

func TestSeatC2_SellReservationSeats_IdempotentReplay(t *testing.T) {
	t.Parallel()
	// All seats already sold → no-op, no version bump, no error.
	sess := uuid.New()
	res := uuid.New()

	f := newFakeSeatState()
	f.addSeat(sess, res, "A-1-1", seatStatusSold)
	f.addSeat(sess, res, "A-1-2", seatStatusSold)

	sold, alreadySold, err := sellReservationSeatsTx(t.Context(), f, sess, res)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sold != 0 || alreadySold != 2 {
		t.Errorf("sold=%d alreadySold=%d, want 0, 2", sold, alreadySold)
	}
	if f.sessionVersion[sess] != 0 {
		t.Errorf("expected no version bump on pure replay, got %d", f.sessionVersion[sess])
	}
}

func TestSeatC2_SellReservationSeats_PartialReplay_OnlyHeldSeatsFlip(t *testing.T) {
	t.Parallel()
	// Half the seats already sold (webhook retry after partial issuance).
	sess := uuid.New()
	res := uuid.New()

	f := newFakeSeatState()
	f.addSeat(sess, res, "A-1-1", seatStatusSold)
	f.addSeat(sess, res, "A-1-2", seatStatusHeld)

	sold, alreadySold, err := sellReservationSeatsTx(t.Context(), f, sess, res)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sold != 1 || alreadySold != 1 {
		t.Errorf("sold=%d alreadySold=%d, want 1, 1", sold, alreadySold)
	}
}

func TestSeatC2_SellReservationSeats_DoubleSellAttempt_Available(t *testing.T) {
	t.Parallel()
	// A seat sneaking through as 'available' means someone released it out of
	// band — the sell must abort so no partial-sell commits.
	sess := uuid.New()
	res := uuid.New()

	f := newFakeSeatState()
	f.addSeat(sess, res, "A-1-1", seatStatusHeld)
	f.addSeat(sess, res, "A-1-2", "available")

	sold, _, err := sellReservationSeatsTx(t.Context(), f, sess, res)
	if !errors.Is(err, errSeatDoubleSell) {
		t.Fatalf("expected errSeatDoubleSell, got %v", err)
	}
	if sold != 0 {
		t.Errorf("no sells should have been recorded before the abort, got %d", sold)
	}
	// No status_version bump on rejection.
	if f.sessionVersion[sess] != 0 {
		t.Errorf("expected no version bump on double-sell reject, got %d", f.sessionVersion[sess])
	}
}

func TestSeatC2_SellReservationSeats_DoubleSellAttempt_Blocked(t *testing.T) {
	t.Parallel()
	// A blocked seat inside a reservation is another double-sell indicator.
	sess := uuid.New()
	res := uuid.New()

	f := newFakeSeatState()
	f.addSeat(sess, res, "A-1-1", "blocked")

	_, _, err := sellReservationSeatsTx(t.Context(), f, sess, res)
	if !errors.Is(err, errSeatDoubleSell) {
		t.Fatalf("expected errSeatDoubleSell, got %v", err)
	}
}

func TestSeatC2_SellReservationSeats_ExpiryRace(t *testing.T) {
	t.Parallel()
	// SellSessionSeat returns pgx.ErrNoRows for a seat that was released by
	// the TTL worker between the list and the update. Sell must abort with
	// errSeatDoubleSell so the caller rolls back the tx.
	sess := uuid.New()
	res := uuid.New()

	f := newFakeSeatState()
	raceID := f.addSeat(sess, res, "A-1-1", seatStatusHeld)
	f.addSeat(sess, res, "A-1-2", seatStatusHeld)
	f.sellReturnsNoRows[raceID] = true

	sold, alreadySold, err := sellReservationSeatsTx(t.Context(), f, sess, res)
	if !errors.Is(err, errSeatDoubleSell) {
		t.Fatalf("expected errSeatDoubleSell, got %v", err)
	}
	// The first (non-racing) seat may or may not have flipped depending on
	// iteration order over toSell — the contract is only that we abort as
	// soon as the racing seat is reached. We only assert that the counters
	// reflect a partial rollback: sold+alreadySold < total.
	if sold+alreadySold >= 2 {
		t.Errorf("expected partial-rollback counters, got sold=%d alreadySold=%d", sold, alreadySold)
	}
}

func TestSeatC2_SellReservationSeats_GAReservation_NoOp(t *testing.T) {
	t.Parallel()
	// GA reservations have no reservation_seats rows — call is a no-op.
	sold, alreadySold, err := sellReservationSeatsTx(
		t.Context(), newFakeSeatState(), uuid.New(), uuid.New(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sold != 0 || alreadySold != 0 {
		t.Errorf("expected zero counters on GA reservation, got sold=%d alreadySold=%d", sold, alreadySold)
	}
}

func TestSeatC2_SellReservationSeats_ListFails(t *testing.T) {
	t.Parallel()
	f := newFakeSeatState()
	f.listFails = true
	_, _, err := sellReservationSeatsTx(t.Context(), f, uuid.New(), uuid.New())
	if err == nil {
		t.Fatalf("expected list error, got nil")
	}
}

func TestSeatC2_SellReservationSeats_IncrementFails(t *testing.T) {
	t.Parallel()
	sess := uuid.New()
	res := uuid.New()
	f := newFakeSeatState()
	f.addSeat(sess, res, "A-1-1", seatStatusHeld)
	f.incrementFails = true
	_, _, err := sellReservationSeatsTx(t.Context(), f, sess, res)
	if err == nil {
		t.Fatalf("expected increment error, got nil")
	}
}

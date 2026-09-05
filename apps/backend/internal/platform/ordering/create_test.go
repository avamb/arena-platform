package ordering

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// Proration
// ─────────────────────────────────────────────────────────────────────────────

// The invariant that matters everywhere: the shares always sum to the amount
// exactly, whatever the price mix. A per-item floor division would lose up to
// n-1 minor units and break the orders CHECK (total = subtotal - discount +
// charge) against the sum of order_items.
func TestProrate_SharesAlwaysSumToAmount(t *testing.T) {
	cases := []struct {
		name   string
		amount int64
		prices []int64
	}{
		{"even split", 300, []int64{1000, 1000, 1000}},
		{"indivisible across equal units", 100, []int64{1000, 1000, 1000}},
		{"weighted", 1000, []int64{1500, 500, 3000}},
		{"one unit", 777, []int64{5000}},
		{"prime amount over many units", 9973, []int64{100, 200, 300, 400, 500, 600, 700}},
		{"free units mixed in", 500, []int64{0, 1000, 0, 1000}},
		{"all free units", 7, []int64{0, 0, 0}},
		{"zero amount", 0, []int64{100, 200}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shares := prorate(tc.amount, tc.prices)
			if len(shares) != len(tc.prices) {
				t.Fatalf("got %d shares, want %d", len(shares), len(tc.prices))
			}
			var sum int64
			for _, s := range shares {
				if s < 0 {
					t.Fatalf("negative share in %v", shares)
				}
				sum += s
			}
			if sum != tc.amount {
				t.Fatalf("shares %v sum to %d, want %d", shares, sum, tc.amount)
			}
		})
	}
}

// A more expensive unit must never receive a smaller share than a cheaper one:
// that is the whole point of weighting by price rather than splitting evenly.
func TestProrate_WeightsByPrice(t *testing.T) {
	shares := prorate(1000, []int64{3000, 1000})
	if shares[0] <= shares[1] {
		t.Fatalf("expensive unit got %d, cheap unit %d — want the expensive one larger", shares[0], shares[1])
	}
	if shares[0] != 750 || shares[1] != 250 {
		t.Fatalf("got %v, want [750 250]", shares)
	}
}

func TestProrate_NoUnits(t *testing.T) {
	if got := prorate(100, nil); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

// Free units still absorb their share when there is no price to weight by —
// otherwise an all-comp order's charge would vanish.
func TestProrate_AllFreeUnitsSplitEvenly(t *testing.T) {
	shares := prorate(7, []int64{0, 0, 0})
	if shares[0] != 3 || shares[1] != 2 || shares[2] != 2 {
		t.Fatalf("got %v, want [3 2 2]", shares)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CreateOrderFromCheckout
// ─────────────────────────────────────────────────────────────────────────────

func pricedCheckout() gen.CheckoutSessionRow {
	return gen.CheckoutSessionRow{
		ID:            uuid.New(),
		OrgID:         uuid.New(),
		ChannelID:     uuid.New(),
		ReservationID: uuid.New(),
		State:         checkoutStatePricingConfirmed,
		Subtotal:      ptr(int64(6000)),
		Discount:      ptr(int64(600)),
		PlatformFee:   ptr(int64(100)),
		ProviderFee:   ptr(int64(50)),
		Tax:           ptr(int64(25)),
		Total:         ptr(int64(5575)),
		Currency:      ptr("EUR"),
	}
}

func storeWithGACart(t *testing.T) (*fakeStore, uuid.UUID) {
	t.Helper()
	f := newFakeStore()
	f.checkout = pricedCheckout()
	tierID := uuid.New()
	f.reservation = gen.ReservationRow{
		ID:        f.checkout.ReservationID,
		OrgID:     f.checkout.OrgID,
		SessionID: uuid.New(),
		State:     "active",
		ExpiresAt: time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC),
	}
	f.gaItems = []gen.ReservationGAItemRow{
		{ReservationID: f.reservation.ID, TierID: tierID, Quantity: 3, UnitPrice: 2000},
	}
	return f, tierID
}

func TestCreateOrderFromCheckout_WritesAggregateWithProratedItems(t *testing.T) {
	f, tierID := storeWithGACart(t)

	res, err := CreateOrderFromCheckout(context.Background(), f, CreateInput{
		CheckoutSessionID: f.checkout.ID,
		EventID:           uuid.New(),
		Source:            SourceBil24Gateway,
		Actor:             "gateway:42",
		ChargePercentBP:   125,
	})
	if err != nil {
		t.Fatalf("CreateOrderFromCheckout: %v", err)
	}

	// Order-level money: charge is platform+provider+tax, and the schema
	// CHECK must hold arithmetically.
	o := res.Order
	if o.Subtotal != 6000 || o.Discount != 600 || o.Charge != 175 || o.Total != 5575 {
		t.Fatalf("money snapshot = %d/%d/%d/%d, want 6000/600/175/5575",
			o.Subtotal, o.Discount, o.Charge, o.Total)
	}
	if o.Total != o.Subtotal-o.Discount+o.Charge {
		t.Fatalf("order violates total = subtotal - discount + charge")
	}
	if o.Status != StatusPendingPayment {
		t.Fatalf("status = %q, want %q", o.Status, StatusPendingPayment)
	}
	if o.ExpiresAt == nil || !o.ExpiresAt.Equal(f.reservation.ExpiresAt) {
		t.Fatalf("expires_at = %v, want the reservation deadline %v", o.ExpiresAt, f.reservation.ExpiresAt)
	}

	// One row PER UNIT, dense 1-based ordinals.
	if len(res.Items) != 3 {
		t.Fatalf("got %d items, want 3 (one per GA unit)", len(res.Items))
	}
	var sumUnit, sumDisc, sumCharge, sumTotal int64
	for i, it := range res.Items {
		if it.Ordinal != int32(i+1) {
			t.Fatalf("item %d has ordinal %d", i, it.Ordinal)
		}
		if it.TierID != tierID {
			t.Fatalf("item %d has tier %s, want %s", i, it.TierID, tierID)
		}
		if it.TicketID != nil {
			t.Fatalf("item %d already has a ticket id; issue backfills it later", i)
		}
		if it.Total != it.UnitPrice-it.Discount+it.Charge {
			t.Fatalf("item %d violates total = unit_price - discount + charge", i)
		}
		sumUnit += it.UnitPrice
		sumDisc += it.Discount
		sumCharge += it.Charge
		sumTotal += it.Total
	}
	if sumDisc != o.Discount || sumCharge != o.Charge {
		t.Fatalf("item discount/charge sums = %d/%d, want %d/%d", sumDisc, sumCharge, o.Discount, o.Charge)
	}
	if sumUnit != o.Subtotal || sumTotal != o.Total {
		t.Fatalf("item unit/total sums = %d/%d, want %d/%d", sumUnit, sumTotal, o.Subtotal, o.Total)
	}

	if len(f.events) != 1 || f.events[0].Type != EventCreated || f.events[0].Actor != "gateway:42" {
		t.Fatalf("events = %+v, want one 'created' by gateway:42", f.events)
	}
}

// Seats have no price column of their own, so the caller-supplied maps decide.
// Seat units are emitted in seat_key order and keep their session_seat_id.
func TestCreateOrderFromCheckout_SeatsPricedFromCallerMaps(t *testing.T) {
	f, _ := storeWithGACart(t)
	f.gaItems = nil
	seatTier := uuid.New()
	seatA := uuid.New()
	seatB := uuid.New()
	f.seats = []gen.SessionSeatRow{
		{ID: seatB, SeatKey: "B-2", TierID: &seatTier},
		{ID: seatA, SeatKey: "A-1", TierID: &seatTier},
	}
	// Subtotal must line up with the units for the sums to be meaningful.
	f.checkout.Subtotal = ptr(int64(1500 + 3000))
	f.checkout.Discount = ptr(int64(0))
	f.checkout.Total = ptr(int64(1500 + 3000 + 175))

	res, err := CreateOrderFromCheckout(context.Background(), f, CreateInput{
		CheckoutSessionID: f.checkout.ID,
		EventID:           uuid.New(),
		Source:            SourceCheckoutAPI,
		TierUnitPrices:    map[uuid.UUID]int64{seatTier: 1500},
		SeatUnitPrices:    map[uuid.UUID]int64{seatB: 3000},
	})
	if err != nil {
		t.Fatalf("CreateOrderFromCheckout: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(res.Items))
	}
	// Seats in seat_key order (A-1 before B-2).
	if res.Items[0].SessionSeatID == nil || *res.Items[0].SessionSeatID != seatA || res.Items[0].UnitPrice != 1500 {
		t.Fatalf("item 1 = %+v, want seat A-1 at the tier price 1500", res.Items[0])
	}
	// The per-seat override beats the tier price.
	if res.Items[1].SessionSeatID == nil || *res.Items[1].SessionSeatID != seatB || res.Items[1].UnitPrice != 3000 {
		t.Fatalf("item 2 = %+v, want seat B-2 at the per-seat override 3000", res.Items[1])
	}
}

// AB-48 made reservation_ga_items the price-lock record for SEATED holds too:
// two seats in tier T also produce a GA line (tier=T, quantity=2). Those lines
// must not be enumerated as units on top of the seats — that would double every
// seated order — and they are the price of last resort for a seat the caller
// did not price.
func TestCreateOrderFromCheckout_SeatedGALinesArePriceLocksNotUnits(t *testing.T) {
	f, _ := storeWithGACart(t)
	seatTier := uuid.New()
	seatA := uuid.New()
	seatB := uuid.New()
	f.gaItems = []gen.ReservationGAItemRow{
		{ReservationID: f.reservation.ID, TierID: seatTier, Quantity: 2, UnitPrice: 2500},
	}
	f.seats = []gen.SessionSeatRow{
		{ID: seatA, SeatKey: "A-1", TierID: &seatTier},
		{ID: seatB, SeatKey: "B-2", TierID: &seatTier},
	}
	f.checkout.Subtotal = ptr(int64(5000))
	f.checkout.Discount = ptr(int64(0))
	f.checkout.Total = ptr(int64(5000 + 175))

	res, err := CreateOrderFromCheckout(context.Background(), f, CreateInput{
		CheckoutSessionID: f.checkout.ID,
		EventID:           uuid.New(),
		Source:            SourceCheckoutAPI,
	})
	if err != nil {
		t.Fatalf("CreateOrderFromCheckout: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("got %d items, want 2 (one per seat, not 2 seats + 2 GA lock lines)", len(res.Items))
	}
	for i, it := range res.Items {
		if it.SessionSeatID == nil {
			t.Fatalf("item %d = %+v, want a seated unit", i, it)
		}
		if it.UnitPrice != 2500 {
			t.Fatalf("item %d unit_price = %d, want the locked 2500", i, it.UnitPrice)
		}
	}
}

func TestCreateOrderFromCheckout_RejectsUnpricedCheckout(t *testing.T) {
	f, _ := storeWithGACart(t)
	f.checkout.State = "created"

	_, err := CreateOrderFromCheckout(context.Background(), f, CreateInput{
		CheckoutSessionID: f.checkout.ID, EventID: uuid.New(), Source: SourceCheckoutAPI,
	})
	if !errors.Is(err, ErrCheckoutNotPriced) {
		t.Fatalf("err = %v, want ErrCheckoutNotPriced", err)
	}
	if len(f.insertedOrders) != 0 {
		t.Fatalf("wrote %d orders for an unpriced checkout", len(f.insertedOrders))
	}
}

func TestCreateOrderFromCheckout_RejectsDeadReservation(t *testing.T) {
	f, _ := storeWithGACart(t)
	f.reservation.State = "expired"

	_, err := CreateOrderFromCheckout(context.Background(), f, CreateInput{
		CheckoutSessionID: f.checkout.ID, EventID: uuid.New(), Source: SourceCheckoutAPI,
	})
	if !errors.Is(err, ErrReservationNotHeld) {
		t.Fatalf("err = %v, want ErrReservationNotHeld", err)
	}
}

func TestCreateOrderFromCheckout_RejectsEmptyCart(t *testing.T) {
	f, _ := storeWithGACart(t)
	f.gaItems = nil

	_, err := CreateOrderFromCheckout(context.Background(), f, CreateInput{
		CheckoutSessionID: f.checkout.ID, EventID: uuid.New(), Source: SourceCheckoutAPI,
	})
	if !errors.Is(err, ErrNoUnits) {
		t.Fatalf("err = %v, want ErrNoUnits", err)
	}
}

func TestCreateOrderFromCheckout_RejectsUnpricedSeat(t *testing.T) {
	f, _ := storeWithGACart(t)
	seatTier := uuid.New()
	f.seats = []gen.SessionSeatRow{{ID: uuid.New(), SeatKey: "A-1", TierID: &seatTier}}

	_, err := CreateOrderFromCheckout(context.Background(), f, CreateInput{
		CheckoutSessionID: f.checkout.ID, EventID: uuid.New(), Source: SourceCheckoutAPI,
	})
	if !errors.Is(err, ErrMissingUnitPrice) {
		t.Fatalf("err = %v, want ErrMissingUnitPrice", err)
	}
}

// The partner's own total is evidence, never arithmetic input (spec §7.7
// step 8): it belongs in the audit payload and nowhere else.
func TestCreateOrderFromCheckout_ClientReportedGoesOnlyIntoTheEvent(t *testing.T) {
	f, _ := storeWithGACart(t)

	res, err := CreateOrderFromCheckout(context.Background(), f, CreateInput{
		CheckoutSessionID: f.checkout.ID,
		EventID:           uuid.New(),
		Source:            SourceBil24Gateway,
		ClientReported:    map[string]any{"total": 99.5, "chargePercent": 1.25},
	})
	if err != nil {
		t.Fatalf("CreateOrderFromCheckout: %v", err)
	}
	if res.Order.Total != 5575 {
		t.Fatalf("order total = %d — the client-reported figure leaked into the money", res.Order.Total)
	}
	if len(f.events) != 1 {
		t.Fatalf("got %d events, want 1", len(f.events))
	}
	payload := string(f.events[0].Payload)
	if !strings.Contains(payload, "client_reported") || !strings.Contains(payload, "99.5") {
		t.Fatalf("created event payload = %s, want the client_reported echo", payload)
	}
}

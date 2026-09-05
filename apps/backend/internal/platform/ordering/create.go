package ordering

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// Input / output
// ─────────────────────────────────────────────────────────────────────────────

// CreateInput carries everything CreateOrderFromCheckout cannot derive from
// the checkout session itself.
//
// EventID is required because reservations only know their session; walking
// sessions → events here would mean a second query on a hot path where every
// caller (hcheckout confirm, hfeed confirmPublicCheckout, hbil24
// CREATE_ORDER_EXT) already holds the event id.
//
// Seat prices are supplied rather than read: session_seats has no price
// column, so callers resolve them through priceresolve.ForTiers exactly the
// way GET_CART does. SeatUnitPrices (by session_seat_id) wins over
// TierUnitPrices (by tier_id) when both cover a seat, which is what lets a
// per-seat override survive into the order. GA units need neither map — spec
// §7.7 step 7 makes reservation_ga_items.unit_price authoritative for them.
type CreateInput struct {
	CheckoutSessionID uuid.UUID
	EventID           uuid.UUID
	CustomerID        *uuid.UUID

	// Source is one of the SourceXxx constants; Actor is the order_events
	// actor string ('gateway:42' | 'user:<uuid>' | 'system').
	Source string
	Actor  string

	// ExternalRef is the partner-side order number (CREATE_ORDER_EXT.orderId
	// for the Bil24 gateway). Unique per channel when non-nil.
	ExternalRef *string

	// ChargePercentBP is the channel fee_percent snapshotted in basis points
	// (125 = 1.25%), kept next to the exact charge amount.
	ChargePercentBP int32

	BuyerName     *string
	BuyerEmail    *string
	BuyerPhone    *string
	PaymentMethod *string

	SeatUnitPrices map[uuid.UUID]int64
	TierUnitPrices map[uuid.UUID]int64

	// ClientReported is echoed verbatim into order_events.created under
	// "client_reported" (spec §7.7 step 8: a partner's own total/chargePercent
	// is evidence for a later dispute, never an input to our arithmetic).
	ClientReported any

	// Metadata lands on orders.metadata; nil means '{}'.
	Metadata json.RawMessage
}

// CreateResult is the freshly written aggregate.
type CreateResult struct {
	Order gen.OrderRow
	Items []gen.OrderItemRow
}

// unit is one to-be-written order_items row before proration assigns it a
// share of the order-level discount and charge.
type unit struct {
	tierID        uuid.UUID
	sessionSeatID *uuid.UUID
	unitPrice     int64
}

// ─────────────────────────────────────────────────────────────────────────────
// CreateOrderFromCheckout
// ─────────────────────────────────────────────────────────────────────────────

// CreateOrderFromCheckout materialises the explicit order aggregate for a
// pricing-confirmed checkout session (spec §14.1). It writes, in the caller's
// transaction: one orders row carrying the money snapshot, one order_items row
// PER UNIT (never per category — GET_CART.seatList and GET_ORDER_INFO.
// ticketList are per-unit on the wire), and one order_events 'created' row.
//
// The money on the order is copied from the checkout session, not recomputed:
// subtotal and discount verbatim, and charge as platform_fee + provider_fee +
// tax so that the schema-level CHECK (total = subtotal - discount + charge)
// holds against the same total ComputePricingLines already produced. Per-item
// discount and charge are that order-level pair spread across units by
// unit-price weight (see prorate), so the item sums are exactly the order
// sums — no cent evaporates into rounding.
//
// q must already be bound to the caller's transaction.
func CreateOrderFromCheckout(ctx context.Context, q CreateStore, in CreateInput) (CreateResult, error) {
	// No clock is taken here on purpose: created_at/updated_at come from the
	// column defaults (one authority for "when"), and expires_at is copied
	// from the reservation whose hold the order inherits.
	cs, err := q.GetCheckoutSessionByID(ctx, in.CheckoutSessionID)
	if err != nil {
		return CreateResult{}, fmt.Errorf("ordering: load checkout session: %w", err)
	}
	if cs.State != checkoutStatePricingConfirmed {
		return CreateResult{}, fmt.Errorf("%w (state=%s)", ErrCheckoutNotPriced, cs.State)
	}
	if cs.Subtotal == nil || cs.Discount == nil || cs.Total == nil || cs.Currency == nil {
		return CreateResult{}, fmt.Errorf("%w (money columns are null)", ErrCheckoutNotPriced)
	}

	res, err := q.GetReservationByID(ctx, cs.ReservationID)
	if err != nil {
		return CreateResult{}, fmt.Errorf("ordering: load reservation: %w", err)
	}
	if res.State != "active" && res.State != "draft" {
		return CreateResult{}, fmt.Errorf("%w (state=%s)", ErrReservationNotHeld, res.State)
	}

	units, err := collectUnits(ctx, q, res.ID, in)
	if err != nil {
		return CreateResult{}, err
	}
	if len(units) == 0 {
		return CreateResult{}, ErrNoUnits
	}

	subtotal := *cs.Subtotal
	discount := *cs.Discount
	charge := deref(cs.PlatformFee) + deref(cs.ProviderFee) + deref(cs.Tax)
	total := subtotal - discount + charge

	metadata := in.Metadata
	if len(metadata) == 0 {
		metadata = emptyJSON()
	}
	expiresAt := res.ExpiresAt

	order, err := q.InsertOrder(
		ctx,
		cs.OrgID, cs.ChannelID, in.EventID, res.SessionID,
		in.CustomerID,
		cs.ID, res.ID,
		in.ExternalRef,
		in.Source, StatusPendingPayment,
		*cs.Currency,
		subtotal, discount, charge, total,
		in.ChargePercentBP,
		cs.PromoCodeID,
		in.BuyerName, in.BuyerEmail, in.BuyerPhone, in.PaymentMethod,
		&expiresAt,
		metadata,
	)
	if err != nil {
		return CreateResult{}, fmt.Errorf("ordering: insert order: %w", err)
	}

	prices := make([]int64, len(units))
	for i, u := range units {
		prices[i] = u.unitPrice
	}
	discShares := prorate(discount, prices)
	chargeShares := prorate(charge, prices)

	items := make([]gen.OrderItemRow, 0, len(units))
	var ordinal int32 // 1-based and dense per order; int32 all the way to
	// avoid an int→int32 narrowing conversion
	for i, u := range units {
		ordinal++
		item, err := q.InsertOrderItem(
			ctx,
			order.ID,
			ordinal,
			"ticket",
			u.tierID,
			u.sessionSeatID,
			nil, // ticket_id is backfilled by IssueTicketsForCheckout
			u.unitPrice,
			discShares[i],
			chargeShares[i],
			u.unitPrice-discShares[i]+chargeShares[i],
		)
		if err != nil {
			return CreateResult{}, fmt.Errorf("ordering: insert order item %d: %w", i+1, err)
		}
		items = append(items, item)
	}

	payload := map[string]any{
		"source":    in.Source,
		"unitCount": len(units),
		"subtotal":  subtotal,
		"discount":  discount,
		"charge":    charge,
		"total":     total,
		"currency":  *cs.Currency,
	}
	if in.ClientReported != nil {
		payload["client_reported"] = in.ClientReported
	}
	if _, err := q.InsertOrderEvent(ctx, order.ID, EventCreated, actorOrSystem(in.Actor), marshalPayload(payload)); err != nil {
		return CreateResult{}, fmt.Errorf("ordering: insert created event: %w", err)
	}

	return CreateResult{Order: order, Items: items}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Unit enumeration
// ─────────────────────────────────────────────────────────────────────────────

// collectUnits expands the reservation into one entry per purchasable unit:
// GA lines are repeated `quantity` times at their locked unit_price, and each
// held seat contributes one entry priced from the caller's maps. The order is
// deterministic (GA lines first, in ListReservationGAItems' tier-name order,
// then seats by seat_key) so ordinals are stable across retries of the same
// checkout.
func collectUnits(ctx context.Context, q CreateStore, reservationID uuid.UUID, in CreateInput) ([]unit, error) {
	gaItems, err := q.ListReservationGAItems(ctx, reservationID)
	if err != nil {
		return nil, fmt.Errorf("ordering: list GA items: %w", err)
	}
	seats, err := q.ListReservationSeats(ctx, reservationID)
	if err != nil {
		return nil, fmt.Errorf("ordering: list reservation seats: %w", err)
	}

	units := make([]unit, 0, len(seats))
	for _, gi := range gaItems {
		for n := int32(0); n < gi.Quantity; n++ {
			units = append(units, unit{tierID: gi.TierID, unitPrice: gi.UnitPrice})
		}
	}

	sort.Slice(seats, func(i, j int) bool {
		if seats[i].SeatKey != seats[j].SeatKey {
			return seats[i].SeatKey < seats[j].SeatKey
		}
		return seats[i].ID.String() < seats[j].ID.String()
	})
	for _, s := range seats {
		price, ok := seatPrice(s, in)
		if !ok {
			return nil, fmt.Errorf("%w (seat_key=%s)", ErrMissingUnitPrice, s.SeatKey)
		}
		if s.TierID == nil {
			return nil, fmt.Errorf("ordering: seat %s has no tier", s.SeatKey)
		}
		seatID := s.ID
		units = append(units, unit{tierID: *s.TierID, sessionSeatID: &seatID, unitPrice: price})
	}
	return units, nil
}

// seatPrice resolves one seat's unit price: the per-seat override first, then
// the tier map. A zero price is legitimate (comps), so presence is reported
// separately from the value.
func seatPrice(s gen.SessionSeatRow, in CreateInput) (int64, bool) {
	if p, ok := in.SeatUnitPrices[s.ID]; ok {
		return p, true
	}
	if s.TierID != nil {
		if p, ok := in.TierUnitPrices[*s.TierID]; ok {
			return p, true
		}
	}
	return 0, false
}

// ─────────────────────────────────────────────────────────────────────────────
// Proration
// ─────────────────────────────────────────────────────────────────────────────

// prorate distributes `amount` (minor currency units) across units weighted by
// their prices, using largest-remainder so the shares sum EXACTLY to amount.
// That exactness is the point: order_items.total must reconcile with
// orders.total to the cent, and floor-per-item division would quietly lose up
// to len(prices)-1 units of currency.
//
// Degenerate inputs are handled the boring way: no units → nil; a zero amount
// → all zeros; a zero price sum (all-comp cart) → an even split with the
// remainder going to the earliest units.
func prorate(amount int64, prices []int64) []int64 {
	n := len(prices)
	if n == 0 {
		return nil
	}
	out := make([]int64, n)
	if amount == 0 {
		return out
	}

	var sum int64
	for _, p := range prices {
		if p > 0 {
			sum += p
		}
	}

	if sum == 0 {
		base := amount / int64(n)
		rem := amount - base*int64(n)
		for i := range out {
			out[i] = base
			if int64(i) < rem {
				out[i]++
			}
		}
		return out
	}

	// Floor share per unit, plus the fractional remainder kept as a
	// numerator so ranking never needs floating point.
	type remainder struct {
		idx int
		num int64
	}
	rems := make([]remainder, 0, n)
	var assigned int64
	for i, p := range prices {
		w := p
		if w < 0 {
			w = 0
		}
		numerator := amount * w
		share := numerator / sum
		out[i] = share
		assigned += share
		rems = append(rems, remainder{idx: i, num: numerator - share*sum})
	}

	left := amount - assigned
	if left <= 0 {
		return out
	}
	// Largest remainder first; ties break on the lower index so the result is
	// deterministic for identically priced units.
	sort.SliceStable(rems, func(i, j int) bool {
		if rems[i].num != rems[j].num {
			return rems[i].num > rems[j].num
		}
		return rems[i].idx < rems[j].idx
	})
	for i := int64(0); i < left; i++ {
		out[rems[i%int64(n)].idx]++
	}
	return out
}

func deref(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func actorOrSystem(actor string) string {
	if actor == "" {
		return ActorSystem
	}
	return actor
}

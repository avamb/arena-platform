package ordering

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ErrCapacityUnavailable wraps whatever the extend mutator returned when a
// requested quantity cannot be held. The Bil24 gateway maps this to result
// code 101 ("not enough tickets"), spec §7.7 step 3.
var ErrCapacityUnavailable = errors.New("ordering: requested quantity unavailable")

// ─────────────────────────────────────────────────────────────────────────────
// Injected hold mutation
// ─────────────────────────────────────────────────────────────────────────────
//
// ReconcileLines has to grow and shrink the hold, but the hold primitives live
// in internal/platform/httpserver/hcheckout, which will itself import ordering
// once the checkout-confirm path starts creating orders. Importing hcheckout
// here would close that cycle. So the two mutators are injected as plain
// functions: production wires them to hcheckout.ExtendHoldTx /
// hcheckout.ShrinkHoldTx (one thin closure at the call site, which is also
// where the reservation TTL policy lives), and tests wire fakes.

// TierQuantity is a (tier, count) pair — a delta for the mutators, never an
// absolute target.
type TierQuantity struct {
	TierID   uuid.UUID
	Quantity int32
}

// HoldMutators are the two capacity operations ReconcileLines needs. Both take
// deltas relative to the current hold and must be executed inside the caller's
// transaction.
type HoldMutators struct {
	// Extend adds Quantity more units of each tier to the reservation. It
	// must fail (any error) rather than partially satisfy a request.
	Extend func(ctx context.Context, reservationID uuid.UUID, tiers []TierQuantity) error
	// Shrink releases Quantity units of each tier from the reservation.
	Shrink func(ctx context.Context, reservationID uuid.UUID, tiers []TierQuantity) error
}

// ReconcileStore is the query surface ReconcileLines needs.
type ReconcileStore interface {
	ListReservationGAItems(ctx context.Context, reservationID uuid.UUID) ([]gen.ReservationGAItemRow, error)
	EventStore
}

// ─────────────────────────────────────────────────────────────────────────────
// Input / output
// ─────────────────────────────────────────────────────────────────────────────

// Line is one requested cart line as the partner site reports it: an absolute
// desired quantity for a tier (CREATE_ORDER_EXT.ticketList grouped by
// categoryPriceId).
type Line struct {
	TierID   uuid.UUID
	Quantity int32
}

// LineDelta records what reconciliation did to one tier, for the audit trail
// and for the caller's own logging.
type LineDelta struct {
	TierID    uuid.UUID `json:"tier_id"`
	Held      int32     `json:"held"`
	Requested int32     `json:"requested"`
	Added     int32     `json:"added"`
	Removed   int32     `json:"removed"`
}

// ReconcileInput parameterises ReconcileLines.
type ReconcileInput struct {
	ReservationID uuid.UUID
	Lines         []Line

	// OrderID, when non-nil, gets a lines_reconciled audit event describing
	// the delta. Nil is the pre-order case (the cart is being reconciled
	// before CreateOrderFromCheckout runs).
	OrderID *uuid.UUID
	Actor   string
}

// ReconcileResult reports the deltas applied, tier-sorted for determinism.
type ReconcileResult struct {
	Deltas  []LineDelta
	Changed bool
}

// ─────────────────────────────────────────────────────────────────────────────
// ReconcileLines
// ─────────────────────────────────────────────────────────────────────────────

// ReconcileLines makes the reservation's GA lines match what the partner site
// says the cart contains (spec §7.7 step 3). For each tier:
//
//	requested > held  → extend the hold by the difference; a failure here is
//	                    ErrCapacityUnavailable (gateway result code 101)
//	requested < held  → shrink by the difference, releasing the most recently
//	                    added units
//	tier absent from Lines → removed entirely
//
// Seats are deliberately untouched: on the seated path the partner site owns
// seat selection and reconciles seats itself, so a seat the site did not
// mention is NOT evidence the buyer dropped it (spec §7.7 step 3).
//
// Duplicate lines for one tier are summed before comparison, which is what a
// site that lists the same category twice means. The whole thing runs in the
// caller's transaction: either every delta lands or none does.
func ReconcileLines(ctx context.Context, q ReconcileStore, m HoldMutators, in ReconcileInput) (ReconcileResult, error) {
	if m.Extend == nil || m.Shrink == nil {
		return ReconcileResult{}, errors.New("ordering: ReconcileLines requires both hold mutators")
	}

	held, err := q.ListReservationGAItems(ctx, in.ReservationID)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("ordering: list held GA items: %w", err)
	}

	heldByTier := make(map[uuid.UUID]int32, len(held))
	for _, h := range held {
		heldByTier[h.TierID] += h.Quantity
	}

	requestedByTier := make(map[uuid.UUID]int32, len(in.Lines))
	for _, l := range in.Lines {
		if l.Quantity < 0 {
			return ReconcileResult{}, fmt.Errorf("ordering: negative quantity for tier %s", l.TierID)
		}
		requestedByTier[l.TierID] += l.Quantity
	}

	// Union of both sides, sorted, so deltas and mutator calls are stable.
	tiers := make([]uuid.UUID, 0, len(heldByTier)+len(requestedByTier))
	seen := make(map[uuid.UUID]struct{}, len(heldByTier)+len(requestedByTier))
	for t := range heldByTier {
		tiers, seen = appendTier(tiers, seen, t)
	}
	for t := range requestedByTier {
		tiers, seen = appendTier(tiers, seen, t)
	}
	sort.Slice(tiers, func(i, j int) bool { return tiers[i].String() < tiers[j].String() })

	var (
		deltas  []LineDelta
		toAdd   []TierQuantity
		toDrop  []TierQuantity
		changed bool
	)
	for _, t := range tiers {
		h, r := heldByTier[t], requestedByTier[t]
		d := LineDelta{TierID: t, Held: h, Requested: r}
		switch {
		case r > h:
			d.Added = r - h
			toAdd = append(toAdd, TierQuantity{TierID: t, Quantity: d.Added})
			changed = true
		case r < h:
			d.Removed = h - r
			toDrop = append(toDrop, TierQuantity{TierID: t, Quantity: d.Removed})
			changed = true
		}
		deltas = append(deltas, d)
	}

	// Shrink first: releasing before acquiring keeps the peak footprint at
	// the larger of the two carts rather than their sum, so a site swapping
	// one category for another never trips capacity it already owns.
	if len(toDrop) > 0 {
		if err := m.Shrink(ctx, in.ReservationID, toDrop); err != nil {
			return ReconcileResult{}, fmt.Errorf("ordering: shrink hold: %w", err)
		}
	}
	if len(toAdd) > 0 {
		if err := m.Extend(ctx, in.ReservationID, toAdd); err != nil {
			return ReconcileResult{}, fmt.Errorf("%w: %w", ErrCapacityUnavailable, err)
		}
	}

	if changed && in.OrderID != nil {
		payload := map[string]any{"deltas": deltas}
		if _, err := q.InsertOrderEvent(ctx, *in.OrderID, EventLinesReconciled, actorOrSystem(in.Actor), marshalPayload(payload)); err != nil {
			return ReconcileResult{}, fmt.Errorf("ordering: insert lines_reconciled event: %w", err)
		}
	}

	return ReconcileResult{Deltas: deltas, Changed: changed}, nil
}

func appendTier(tiers []uuid.UUID, seen map[uuid.UUID]struct{}, t uuid.UUID) ([]uuid.UUID, map[uuid.UUID]struct{}) {
	if _, ok := seen[t]; ok {
		return tiers, seen
	}
	seen[t] = struct{}{}
	return append(tiers, t), seen
}

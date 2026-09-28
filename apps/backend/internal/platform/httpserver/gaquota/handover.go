package gaquota

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// HandOverResult is what one chain hand-over did.
type HandOverResult struct {
	SessionID uuid.UUID
	FromTier  uuid.UUID
	ToTier    uuid.UUID
	// Moved is the number of free places that changed category.
	Moved int64
	// First is true when this was the link's first hand-over: the source
	// category was closed and the target opened.
	First bool
}

// HandOver moves every free GA place of a category whose turn has ended to
// the next category of its chain (migration 0112). A turn ends when the sale
// window closes or, for a quantity step (sell_limit, migration 0114), when the
// step has no free place left.
//
// It runs under the same lock order as every other quota mutation — the
// sessions row first — so no hold can take one of those places half-way.
// Only places that are 'available' and referenced by no reservation_seats
// row move; held and sold places stay with the category that sold them.
// The session's place count is unchanged, so capacity_total and the session
// ledger row stay valid; both categories' ticket_tiers.capacity is brought
// back to the number of places they own.
//
// On the link's FIRST hand-over the source category is closed and the target
// opened. Later hand-overs (a place that came back from an expired hold)
// only move places, so a category the operator closed by hand stays closed.
// When the target is itself a quantity step, it then keeps its own limit and
// passes the rest of the hall on (RebalanceStep).
//
// The target must be a GA category of the same session; a seated target is
// refused with ErrSeatedCategory (a seat cannot become a GA place).
func HandOver(ctx context.Context, txq *gen.Queries, link gen.TierChainRow) (HandOverResult, error) {
	if txq == nil {
		return HandOverResult{}, errors.New("gaquota: nil queries")
	}
	res := HandOverResult{SessionID: link.SessionID, FromTier: link.TierID, ToTier: link.NextTierID}

	// Lock order step 1 — the sessions row, before any read.
	version, err := txq.IncrementSessionSeatStatusVersion(ctx, link.SessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return res, ErrSessionNotFound
		}
		return res, fmt.Errorf("gaquota: bump seat_status_version: %w", err)
	}

	if _, err := txq.GetTicketTierByID(ctx, link.TierID, link.SessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return res, ErrCategoryNotFound
		}
		return res, fmt.Errorf("gaquota: load source category: %w", err)
	}
	moved, err := moveFreePlaces(ctx, txq, link.SessionID, link.TierID, link.NextTierID, version, nil)
	if err != nil {
		return res, err
	}
	res.Moved = moved

	if link.HandedOverAt == nil {
		res.First = true
		if _, err := txq.SetTicketTierOpen(ctx, link.TierID, link.SessionID, false); err != nil {
			return res, fmt.Errorf("gaquota: close source category: %w", err)
		}
		if _, err := txq.SetTicketTierOpen(ctx, link.NextTierID, link.SessionID, true); err != nil {
			return res, fmt.Errorf("gaquota: open target category: %w", err)
		}
		if err := txq.StartTierSaleNow(ctx, link.NextTierID, link.SessionID); err != nil {
			return res, fmt.Errorf("gaquota: start target sale: %w", err)
		}
		if err := txq.MarkTierChainHandedOver(ctx, link.TierID); err != nil {
			return res, fmt.Errorf("gaquota: stamp hand-over: %w", err)
		}
	}

	// The category selling now may be a quantity step of its own.
	links, err := txq.ListTierChainForSession(ctx, link.SessionID)
	if err != nil {
		return res, fmt.Errorf("gaquota: read category chain: %w", err)
	}
	for _, next := range links {
		if next.TierID == link.NextTierID {
			if _, err := RebalanceStep(ctx, txq, next, version); err != nil {
				return res, err
			}
		}
	}

	if _, err := recompute(ctx, txq, link.SessionID); err != nil {
		return res, err
	}
	return res, nil
}

// RebalanceStep brings the category selling now — the source of a link that
// has not handed over yet — to own exactly its share of the hall: its
// sell_limit (migration 0114), or without one every place still waiting in
// the next category. Free places beyond the limit go to the next category,
// which is still closed; a raised or removed limit takes free places back
// from it while it is closed. Sold and held places never move, so a limit
// below them only stops further sales at this price. A link that already
// handed over is left alone. Returns the number of places moved either way.
//
// The caller MUST hold the sessions row lock and pass its seat_status_version.
func RebalanceStep(ctx context.Context, txq *gen.Queries, link gen.TierChainRow, version int64) (int64, error) {
	if link.HandedOverAt != nil {
		return 0, nil
	}
	// Without a limit the category selling now owns the rest of the hall: a
	// limit removed before its step ran out must not leave places parked in
	// the closed next category, or the step would read "sold out" while the
	// hall still has seats.
	limit := int64(math.MaxInt64)
	if link.SellLimit != nil {
		limit = int64(*link.SellLimit)
	}
	owned, free, err := txq.CountTierGAPlaces(ctx, link.SessionID, link.TierID)
	if err != nil {
		return 0, fmt.Errorf("gaquota: count step places: %w", err)
	}
	switch {
	case owned > limit:
		n := min(owned-limit, free)
		if n <= 0 {
			return 0, nil
		}
		return moveFreePlaces(ctx, txq, link.SessionID, link.TierID, link.NextTierID, version, &n)
	case owned < limit:
		next, err := txq.GetTicketTierByID(ctx, link.NextTierID, link.SessionID)
		if err != nil {
			return 0, fmt.Errorf("gaquota: load next step: %w", err)
		}
		if next.IsOpen {
			return 0, nil // the next step is selling: its places are its own
		}
		_, nextFree, err := txq.CountTierGAPlaces(ctx, link.SessionID, link.NextTierID)
		if err != nil {
			return 0, fmt.Errorf("gaquota: count next step places: %w", err)
		}
		n := min(limit-owned, nextFree)
		if n <= 0 {
			return 0, nil
		}
		return moveFreePlaces(ctx, txq, link.SessionID, link.NextTierID, link.TierID, version, &n)
	}
	return 0, nil
}

// RebalanceSession runs RebalanceStep for every chained category of a
// session that is selling now (open, not handed over) — after an import wrote
// the chain and its limits. It takes the sessions row lock itself. A chain
// without limits is untouched: its next categories own no places.
func RebalanceSession(ctx context.Context, txq *gen.Queries, sessionID uuid.UUID) (int64, error) {
	links, err := txq.ListTierChainForSession(ctx, sessionID)
	if err != nil {
		return 0, fmt.Errorf("gaquota: read category chain: %w", err)
	}
	var steps []gen.TierChainRow
	for _, l := range links {
		if l.HandedOverAt == nil {
			steps = append(steps, l)
		}
	}
	if len(steps) == 0 {
		return 0, nil
	}
	version, err := txq.IncrementSessionSeatStatusVersion(ctx, sessionID)
	if err != nil {
		return 0, fmt.Errorf("gaquota: bump seat_status_version: %w", err)
	}
	var moved int64
	for _, l := range steps {
		src, err := txq.GetTicketTierByID(ctx, l.TierID, sessionID)
		if err != nil {
			return moved, fmt.Errorf("gaquota: load step: %w", err)
		}
		if !src.IsOpen {
			continue // not selling yet: rebalanced when its turn comes
		}
		n, err := RebalanceStep(ctx, txq, l, version)
		if err != nil {
			return moved, err
		}
		moved += n
	}
	if moved > 0 {
		if _, err := recompute(ctx, txq, sessionID); err != nil {
			return moved, err
		}
	}
	return moved, nil
}

// moveFreePlaces re-tiers up to limit (nil = all) free GA places of from to
// to, re-keyed under to's unit prefix, and re-syncs both capacities. A seated
// target is refused with ErrSeatedCategory. The caller holds the sessions
// row lock.
func moveFreePlaces(ctx context.Context, txq *gen.Queries, sessionID, from, to uuid.UUID, version int64, limit *int64) (int64, error) {
	target, err := txq.GetTicketTierByID(ctx, to, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrCategoryNotFound
		}
		return 0, fmt.Errorf("gaquota: load target category: %w", err)
	}
	seats, err := txq.CountSeatRowsForTier(ctx, sessionID, to)
	if err != nil {
		return 0, fmt.Errorf("gaquota: count seats of target category: %w", err)
	}
	if seats > 0 {
		return 0, ErrSeatedCategory
	}
	seq, err := ensureUnitSeq(ctx, txq, sessionID, target)
	if err != nil {
		return 0, err
	}
	prefix := unitKeyPrefix(seq)
	start, err := txq.MaxGAUnitIndexForTierPrefix(ctx, sessionID, prefix)
	if err != nil {
		return 0, fmt.Errorf("gaquota: read highest place index: %w", err)
	}
	moved, err := txq.MoveFreeGAUnitsToTierUpTo(ctx, sessionID, from, to, prefix, start, version, limit)
	if err != nil {
		return 0, fmt.Errorf("gaquota: move free places: %w", err)
	}
	for _, id := range []uuid.UUID{from, to} {
		if err := txq.SyncTierCapacityToPlaces(ctx, id, sessionID); err != nil {
			return moved, fmt.Errorf("gaquota: write category quantity: %w", err)
		}
	}
	return moved, nil
}

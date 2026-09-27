package gaquota

import (
	"context"
	"errors"
	"fmt"

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

// HandOver moves every free GA place of a category whose sale window has
// closed to the next category of its chain (migration 0112).
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
	target, err := txq.GetTicketTierByID(ctx, link.NextTierID, link.SessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return res, ErrCategoryNotFound
		}
		return res, fmt.Errorf("gaquota: load target category: %w", err)
	}
	seats, err := txq.CountSeatRowsForTier(ctx, link.SessionID, link.NextTierID)
	if err != nil {
		return res, fmt.Errorf("gaquota: count seats of target category: %w", err)
	}
	if seats > 0 {
		return res, ErrSeatedCategory
	}

	seq, err := ensureUnitSeq(ctx, txq, link.SessionID, target)
	if err != nil {
		return res, err
	}
	prefix := unitKeyPrefix(seq)
	start, err := txq.MaxGAUnitIndexForTierPrefix(ctx, link.SessionID, prefix)
	if err != nil {
		return res, fmt.Errorf("gaquota: read highest place index: %w", err)
	}
	moved, err := txq.MoveFreeGAUnitsToTier(ctx, link.SessionID, link.TierID, link.NextTierID, prefix, start, version)
	if err != nil {
		return res, fmt.Errorf("gaquota: move free places: %w", err)
	}
	res.Moved = moved

	for _, id := range []uuid.UUID{link.TierID, link.NextTierID} {
		if err := txq.SyncTierCapacityToPlaces(ctx, id, link.SessionID); err != nil {
			return res, fmt.Errorf("gaquota: write category quantity: %w", err)
		}
	}

	if link.HandedOverAt == nil {
		res.First = true
		if _, err := txq.SetTicketTierOpen(ctx, link.TierID, link.SessionID, false); err != nil {
			return res, fmt.Errorf("gaquota: close source category: %w", err)
		}
		if _, err := txq.SetTicketTierOpen(ctx, link.NextTierID, link.SessionID, true); err != nil {
			return res, fmt.Errorf("gaquota: open target category: %w", err)
		}
		if err := txq.MarkTierChainHandedOver(ctx, link.TierID); err != nil {
			return res, fmt.Errorf("gaquota: stamp hand-over: %w", err)
		}
	}

	if _, err := recompute(ctx, txq, link.SessionID); err != nil {
		return res, err
	}
	return res, nil
}

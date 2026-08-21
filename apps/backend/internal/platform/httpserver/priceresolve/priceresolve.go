// Package priceresolve is the thin persistence adapter in front of the
// AB-48 resolver (internal/domain/pricing): it loads a tier's price
// windows and returns the effective price at a moment. Every surface
// that sells or displays a tier price — pricing quote, checkout,
// reservation creation (price lock), public feed, widget schema, Bil24
// gateway — MUST go through here so the window-selection logic is never
// reimplemented per surface.
//
// Windows only apply to pricing_mode=fixed. free tiers are always 0 and
// pwyw tiers take the buyer's chosen price; both pass through unchanged.
package priceresolve

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/pricing"
)

// WindowLister is the narrow query surface needed; *gen.Queries satisfies it.
type WindowLister interface {
	ListTierPriceWindows(ctx context.Context, tierIDs []uuid.UUID) ([]gen.TicketTierPriceRow, error)
}

// Effective is the resolved price of one tier at the requested moment.
type Effective struct {
	// Amount is the effective price in minor units (base price when no
	// window contains the moment — the documented gap policy).
	Amount int64
	// NextChangeAt is when the price next changes, if known.
	NextChangeAt *time.Time
	// Scheduled is true when a window (not the base price) produced Amount.
	Scheduled bool
}

// ForTiers resolves every given tier at `at` in one round-trip.
func ForTiers(ctx context.Context, q WindowLister, tiers []gen.TicketTierRow, at time.Time) (map[uuid.UUID]Effective, error) {
	out := make(map[uuid.UUID]Effective, len(tiers))
	ids := make([]uuid.UUID, 0, len(tiers))
	for _, t := range tiers {
		out[t.ID] = Effective{Amount: t.PriceAmount}
		if t.PricingMode == "fixed" {
			ids = append(ids, t.ID)
		}
	}
	if len(ids) == 0 || q == nil {
		return out, nil
	}
	rows, err := q.ListTierPriceWindows(ctx, ids)
	if err != nil {
		return nil, err
	}
	byTier := make(map[uuid.UUID][]pricing.Window, len(ids))
	for _, r := range rows {
		byTier[r.TierID] = append(byTier[r.TierID], pricing.Window{
			From: r.ValidFrom, To: r.ValidTo, Amount: r.PriceAmount,
		})
	}
	for _, t := range tiers {
		if t.PricingMode != "fixed" {
			continue
		}
		ws := byTier[t.ID]
		amount, next := pricing.Resolve(t.PriceAmount, ws, at)
		out[t.ID] = Effective{
			Amount:       amount,
			NextChangeAt: next,
			Scheduled:    containsAt(ws, at),
		}
	}
	return out, nil
}

// ForTier resolves a single tier at `at`.
func ForTier(ctx context.Context, q WindowLister, tier gen.TicketTierRow, at time.Time) (Effective, error) {
	m, err := ForTiers(ctx, q, []gen.TicketTierRow{tier}, at)
	if err != nil {
		return Effective{}, err
	}
	return m[tier.ID], nil
}

// containsAt reports whether any window covers the moment.
func containsAt(ws []pricing.Window, at time.Time) bool {
	for _, w := range ws {
		if !at.Before(w.From) && (w.To == nil || at.Before(*w.To)) {
			return true
		}
	}
	return false
}

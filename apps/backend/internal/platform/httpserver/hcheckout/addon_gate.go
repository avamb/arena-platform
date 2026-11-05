package hcheckout

// addon_gate.go — an add-on category (migration 0197, ticket_tiers.is_addon:
// "extra child" next to "child + adult") is sold only together with a ticket
// of a main category of the same session, in the same purchase. The widget's
// checkout start, quote and recover and the gateway's CREATE_ORDER_EXT check
// the WHOLE cart with CheckAddonCompanions; a single RESERVE of the gateway
// cart is not checked, because the site builds the cart one line at a time.

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ErrAddonWithoutMain — the cart holds add-on tickets only.
var ErrAddonWithoutMain = errors.New("hcheckout: add-on ticket without a main ticket")

// addonQuerier is the one read CheckAddonCompanions needs.
type addonQuerier interface {
	ListAddonTierIDs(ctx context.Context, tierIDs []uuid.UUID) (map[uuid.UUID]bool, error)
}

var _ addonQuerier = (*gen.Queries)(nil)

// CheckAddonCompanions refuses a cart whose categories (tierIDs, one entry
// per line or per seat, repeats allowed) are all add-ons. An empty cart is
// not this check's business.
func CheckAddonCompanions(ctx context.Context, q addonQuerier, tierIDs []uuid.UUID) error {
	if len(tierIDs) == 0 {
		return nil
	}
	if q == nil {
		return errors.New("hcheckout: CheckAddonCompanions requires queries")
	}
	addon, err := q.ListAddonTierIDs(ctx, tierIDs)
	if err != nil {
		return err
	}
	return AddonCompanionsOK(tierIDs, addon)
}

// AddonCompanionsOK is the rule of CheckAddonCompanions over flags already
// loaded: at least one category of the cart must be a main one.
func AddonCompanionsOK(tierIDs []uuid.UUID, addon map[uuid.UUID]bool) error {
	for _, id := range tierIDs {
		if !addon[id] {
			return nil
		}
	}
	if len(tierIDs) == 0 {
		return nil
	}
	return ErrAddonWithoutMain
}

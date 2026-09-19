// import_categories.go is the single place where BOTH importers — the
// Bil24-format package (import_exec.go) and the arena-native event bundle
// (import_arena.go) — turn the categories they just upserted into General
// Admission categories that own their places (plan
// 08_architecture/23 step 7, decision 7).
//
// The rule the two importers share:
//
//   - FIRST import of a category (it owns no place yet): the quantity is the
//     package's declared `availability`. A package that declares 0 means the
//     source system has sold the category out — but a GA category with a
//     quantity of 0 is illegal (decision 3), so arena creates it CLOSED with
//     a quantity of 1 and raises import.category_sold_out. An operator then
//     sets the real quantity and opens it.
//
//   - REPEAT import (the category already owns places): a Bil24-format
//     package NEVER touches the quantity. Its `availability` is the REMAINDER
//     at export time, not a quantity — re-applying it would subtract the
//     source system's sales on top of arena's. Price, sale window and sort
//     order do keep updating (upsertTiers / upsertArenaTiers).
//
//   - REPEAT import of an arena-native bundle (the site's event center) is
//     the exception: there `availability` is the quantity the operator typed
//     in the event center form, and the event center is where the operator
//     manages the event. It is applied through gaquota.SetQuantity, so it can
//     never drop below the places already held or sold; such a change is
//     skipped with import.category_quantity_below_used instead of being lost
//     silently (found in the 2026-09-19 functional run).
//
//   - A category arena knows but the package no longer mentions is CLOSED,
//     never deleted: it may still hold sold places and live tickets.
//
// A category whose places are plan seats (a placed/seated category) is left
// alone entirely: its quantity is the geometry's, read-only (decision 10).
package himports

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
)

// importedCategory is one categoryList entry after its ticket_tiers row has
// been resolved by the importer.
type importedCategory struct {
	TierID uuid.UUID
	// Availability is the package's declared availability for the category
	// (the source system's remainder), 0 when it declared none.
	Availability int32
	// Created reports that THIS import inserted the ticket_tiers row. A row
	// that already existed but owns no place is a session imported before
	// the quota model: its stored ticket_tiers.capacity is the quantity to
	// materialize, not the package's availability.
	Created bool
}

// syncImportedCategoryQuotas applies the rules above to one session.
//
// mintPlaces is false for a package that carries an svg seating plan: there
// the places of every category — seats and the geometry's own GA places —
// are materialized by importSeating under the plan's `ga|c<index>` keys, and
// minting a second set here would double the capacity. The closing sweep
// still runs, because it is about the open flag, not about places.
func syncImportedCategoryQuotas(
	ctx context.Context,
	q *gen.Queries,
	sessionID uuid.UUID,
	cats []importedCategory,
	mintPlaces bool,
	applyQuantity bool,
	warnings *warningSink,
) error {
	stats, err := gaquota.SessionStats(ctx, q, sessionID)
	if err != nil {
		return fmt.Errorf("read category place counters: %w", err)
	}

	seen := make(map[uuid.UUID]struct{}, len(cats))
	for _, c := range cats {
		if _, dup := seen[c.TierID]; dup {
			continue
		}
		seen[c.TierID] = struct{}{}
		if !mintPlaces {
			continue
		}
		if st, ok := stats[c.TierID]; ok && st.Quantity > 0 {
			// Repeat import — the quantity is arena's now, unless the event
			// center is the one managing it.
			if applyQuantity && c.Availability > 0 && c.Availability != st.Quantity {
				if err := setImportedCategoryQuantity(ctx, q, sessionID, c, warnings); err != nil {
					return err
				}
			}
			continue
		}
		kind, kErr := gaquota.CategoryKind(ctx, q, sessionID, c.TierID)
		if kErr != nil {
			if errors.Is(kErr, gaquota.ErrCategoryNotFound) {
				continue
			}
			return fmt.Errorf("classify imported category: %w", kErr)
		}
		if kind == gaquota.KindSeated {
			// Its places are the plan geometry's seats.
			continue
		}

		tier, tErr := q.GetTicketTierByID(ctx, c.TierID, sessionID)
		if tErr != nil {
			return fmt.Errorf("load imported category: %w", tErr)
		}

		quantity := c.Availability
		closed := false
		switch {
		case !c.Created && tier.Capacity != nil && *tier.Capacity > 0:
			// A session imported before the quota model: its categories
			// carry a quantity but no places. Materialize THAT quantity —
			// the package's availability is still only a remainder.
			quantity = *tier.Capacity
		case quantity <= 0:
			quantity = 1
			closed = true
			warnings.add(WarnCategorySoldOut,
				"a category was declared with availability 0 and was created closed with a "+
					"quantity of 1; set its real quantity and open it in the admin")
		}

		if err := gaquota.CreateCategory(ctx, q, sessionID, c.TierID, quantity); err != nil {
			return fmt.Errorf("materialize category places: %w", err)
		}
		if closed {
			if err := gaquota.SetOpen(ctx, q, sessionID, c.TierID, false); err != nil {
				return fmt.Errorf("close sold-out category: %w", err)
			}
		}
	}

	// A category the package no longer mentions is closed, never deleted.
	existing, err := q.ListTicketTiersBySession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("list ticket tiers: %w", err)
	}
	closedAny := false
	for _, t := range existing {
		if _, ok := seen[t.ID]; ok || !t.IsOpen {
			continue
		}
		if err := gaquota.SetOpen(ctx, q, sessionID, t.ID, false); err != nil {
			return fmt.Errorf("close category missing from the package: %w", err)
		}
		closedAny = true
	}
	if closedAny {
		warnings.add(WarnTierNotInPayload,
			"the session carries ticket tiers the package did not mention; they were closed "+
				"(never deleted) and keep every place they already sold")
	}
	return nil
}

// setImportedCategoryQuantity applies an event-center quantity change to an
// existing category inside a SAVEPOINT: gaquota.SetQuantity refuses a change
// below the places the category cannot give up only after it may already have
// deleted some of them, and a refusal must cost the operator a warning, not
// the whole bundle.
func setImportedCategoryQuantity(
	ctx context.Context,
	q *gen.Queries,
	sessionID uuid.UUID,
	c importedCategory,
	warnings *warningSink,
) error {
	beginner, ok := q.DB().(interface {
		Begin(ctx context.Context) (pgx.Tx, error)
	})
	if !ok {
		return errors.New("set category quantity: the import is not running inside a transaction")
	}
	sp, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("set category quantity: open savepoint: %w", err)
	}
	_, err = gaquota.SetQuantity(ctx, q.WithTx(sp), sessionID, c.TierID, c.Availability)
	var below *gaquota.BelowUsedError
	switch {
	case err == nil:
		if cerr := sp.Commit(ctx); cerr != nil {
			return fmt.Errorf("set category quantity: release savepoint: %w", cerr)
		}
		return nil
	case errors.As(err, &below):
		_ = sp.Rollback(ctx)
		warnings.add(WarnCategoryQuantityBelowUsed, fmt.Sprintf(
			"a category quantity of %d was not applied: %d of its places are held or sold, "+
				"so it cannot go below %d; the previous quantity of %d is kept",
			below.Requested, below.Used, below.Floor(), below.Total))
		return nil
	case errors.Is(err, gaquota.ErrSeatedCategory):
		_ = sp.Rollback(ctx)
		return nil
	default:
		_ = sp.Rollback(ctx)
		return fmt.Errorf("set category quantity: %w", err)
	}
}

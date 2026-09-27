// import_chain.go — the event-center additions to an arena-native bundle's
// categories: a chain of categories (migration 0112) and a price schedule
// per category (AB-48). The Bil24-format importer never calls this file.
//
// A chain is a list of categories that sell one hall in turn — "Early bird"
// until one date, "Friends" until the next, "Last minute" after that. Its
// rules inside an import:
//
//   - categoryList[i].nextCategoryIndex = j links i to j; -1 removes i's
//     link; absent keeps what is stored. A link to itself or a cycle is 422.
//   - A category somebody hands over TO that is not itself the head of the
//     chain starts with no places and closed: its places arrive when the
//     previous category's sale window closes (tier.chain_sweep). Declaring
//     availability 0 for it is therefore normal, not "sold out".
//   - The head's availability is the number of places of the WHOLE chain
//     (what the operator typed as "places in the hall"). A repeat import that
//     changes it applies the difference to the category selling right now —
//     re-applying it to a head whose places were already handed over would
//     grow the hall. Other members' availability is ignored on a repeat.
package himports

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
)

// maxImportPriceWindows mirrors the price-schedule endpoint's cap.
const maxImportPriceWindows = 50

// chainIndex is a session's chain links, as stored after the import wrote
// the bundle's links.
type chainIndex struct {
	next    map[uuid.UUID]gen.TierChainRow // by source tier
	targets map[uuid.UUID]struct{}
}

func (ci *chainIndex) member(id uuid.UUID) bool {
	if ci == nil {
		return false
	}
	_, src := ci.next[id]
	_, dst := ci.targets[id]
	return src || dst
}

// successor reports a category that receives places from another one and is
// not the head of its chain.
func (ci *chainIndex) successor(id uuid.UUID) bool {
	if ci == nil {
		return false
	}
	_, dst := ci.targets[id]
	return dst
}

func (ci *chainIndex) head(id uuid.UUID) bool {
	if ci == nil {
		return false
	}
	_, src := ci.next[id]
	_, dst := ci.targets[id]
	return src && !dst
}

// members walks a chain from its head.
func (ci *chainIndex) members(headID uuid.UUID) []uuid.UUID {
	out := []uuid.UUID{headID}
	seen := map[uuid.UUID]struct{}{headID: {}}
	cur := headID
	for {
		l, ok := ci.next[cur]
		if !ok {
			return out
		}
		if _, loop := seen[l.NextTierID]; loop {
			return out
		}
		seen[l.NextTierID] = struct{}{}
		out = append(out, l.NextTierID)
		cur = l.NextTierID
	}
}

// selling is the member that owns the chain's free places now: the first one
// whose link has not been handed over yet (or the last member).
func (ci *chainIndex) selling(headID uuid.UUID) uuid.UUID {
	cur := headID
	for _, id := range ci.members(headID) {
		cur = id
		l, ok := ci.next[id]
		if !ok || l.HandedOverAt == nil {
			return cur
		}
	}
	return cur
}

// applyArenaCategoryExtras writes the bundle's chain links and price
// schedules for the categories upsertArenaTiers just resolved (ordered is
// positionally aligned with the categoryList) and returns the session's
// resulting chain index.
func applyArenaCategoryExtras(
	ctx context.Context,
	q *gen.Queries,
	plan importPlan,
	sessionID uuid.UUID,
	ordered []uuid.UUID,
) (*chainIndex, error) {
	list := plan.Request.CategoryList
	for i, c := range list {
		if c.NextCategoryIndex == nil {
			continue
		}
		j := *c.NextCategoryIndex
		switch {
		case j == -1:
			if err := q.DeleteTierChain(ctx, ordered[i]); err != nil {
				return nil, fmt.Errorf("remove category chain link: %w", err)
			}
		case j < 0 || j >= len(list) || j == i || ordered[j] == ordered[i]:
			return nil, failImport(http.StatusUnprocessableEntity, "import.invalid_next_category",
				fmt.Sprintf("categoryList[%d].nextCategoryIndex %d must point at another category of the list", i, j))
		default:
			if err := q.UpsertTierChain(ctx, ordered[i], ordered[j]); err != nil {
				return nil, fmt.Errorf("write category chain link: %w", err)
			}
		}
	}

	links, err := q.ListTierChainForSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("read category chain: %w", err)
	}
	ci := &chainIndex{next: map[uuid.UUID]gen.TierChainRow{}, targets: map[uuid.UUID]struct{}{}}
	for _, l := range links {
		ci.next[l.TierID] = l
		if _, dup := ci.targets[l.NextTierID]; dup {
			return nil, failImport(http.StatusUnprocessableEntity, "import.invalid_next_category",
				"two categories hand their places to the same category; a chain is a single line")
		}
		ci.targets[l.NextTierID] = struct{}{}
	}
	for _, l := range links {
		seen := map[uuid.UUID]struct{}{l.TierID: {}}
		for cur := l.NextTierID; ; {
			if _, loop := seen[cur]; loop {
				return nil, failImport(http.StatusUnprocessableEntity, "import.category_chain_cycle",
					"the categories' nextCategoryIndex links form a cycle")
			}
			seen[cur] = struct{}{}
			nl, ok := ci.next[cur]
			if !ok {
				break
			}
			cur = nl.NextTierID
		}
	}

	for i, c := range list {
		if c.PriceSchedule == nil {
			continue
		}
		if err := replaceImportedSchedule(ctx, q, i, ordered[i], c.PriceMinorUnits(), *c.PriceSchedule); err != nil {
			return nil, err
		}
	}
	return ci, nil
}

type importWindow struct {
	from   time.Time
	to     *time.Time
	amount int64
}

func replaceImportedSchedule(
	ctx context.Context,
	q *gen.Queries,
	idx int,
	tierID uuid.UUID,
	basePrice int64,
	in []bil24compat.ImportPriceWindow,
) error {
	bad := func(msg string) error {
		return failImport(http.StatusUnprocessableEntity, "import.invalid_price_schedule",
			fmt.Sprintf("categoryList[%d].priceSchedule: %s", idx, msg))
	}
	if len(in) > maxImportPriceWindows {
		return bad("at most 50 windows")
	}
	if len(in) > 0 && basePrice <= 0 {
		return bad("only a paid (fixed-price) category carries a price schedule")
	}
	windows := make([]importWindow, 0, len(in))
	for k, w := range in {
		from, err := time.Parse(time.RFC3339, strings.TrimSpace(w.ValidFrom))
		if err != nil {
			return bad(fmt.Sprintf("[%d].validFrom must be RFC3339", k))
		}
		var to *time.Time
		if raw := strings.TrimSpace(w.ValidTo); raw != "" {
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return bad(fmt.Sprintf("[%d].validTo must be RFC3339", k))
			}
			if !t.After(from) {
				return bad(fmt.Sprintf("[%d].validTo must be after validFrom", k))
			}
			to = &t
		}
		amount := w.PriceMinorUnits()
		if amount < 0 {
			return bad(fmt.Sprintf("[%d].price must not be negative", k))
		}
		windows = append(windows, importWindow{from: from.UTC(), to: to, amount: amount})
	}
	sort.Slice(windows, func(a, b int) bool { return windows[a].from.Before(windows[b].from) })
	for k := 1; k < len(windows); k++ {
		prev := windows[k-1]
		if prev.to == nil || windows[k].from.Before(*prev.to) {
			return bad("windows overlap")
		}
	}

	if _, err := q.DeleteTierPriceWindowsByTier(ctx, tierID); err != nil {
		return fmt.Errorf("clear price schedule: %w", err)
	}
	for _, w := range windows {
		if _, err := q.InsertTierPriceWindow(ctx, tierID, w.from, w.to, w.amount); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23P01" {
				return bad("windows overlap")
			}
			return fmt.Errorf("write price window: %w", err)
		}
	}
	return nil
}

// applyChainQuantity brings a chain's total number of places to the head's
// declared availability by growing or shrinking the member that sells now.
func applyChainQuantity(
	ctx context.Context,
	q *gen.Queries,
	sessionID uuid.UUID,
	ci *chainIndex,
	head importedCategory,
	stats map[uuid.UUID]gaquota.Stats,
	warnings *warningSink,
) error {
	var total int32
	for _, id := range ci.members(head.TierID) {
		total += stats[id].Quantity
	}
	if head.Availability <= 0 || head.Availability == total {
		return nil
	}
	active := ci.selling(head.TierID)
	target := stats[active].Quantity + (head.Availability - total)
	if target < 1 {
		warnings.add(WarnCategoryQuantityBelowUsed, fmt.Sprintf(
			"the chain's number of places was not changed to %d: the category selling now "+
				"owns only %d places, and the others are held or sold", head.Availability, stats[active].Quantity))
		return nil
	}
	return setImportedCategoryQuantity(ctx, q, sessionID, importedCategory{TierID: active, Availability: target}, warnings)
}

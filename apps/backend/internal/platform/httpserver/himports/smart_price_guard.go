// smart_price_guard.go ends a smart price when an ordinary import (the
// bot's or the web's edit wizard) changes a price or a price schedule of a
// category the preset drives (owner decision В-5, spec 51 §4.2): the same
// rule as a price typed on the category card.

package himports

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// smartPriceGuard is what the preset categories of a session looked like
// before the import ran.
type smartPriceGuard struct {
	// order lists the categories of the active presets, each chain from its
	// head, so the earliest step a person changed decides the freeze.
	order   []uuid.UUID
	prices  map[uuid.UUID]int64
	windows map[uuid.UUID][]gen.TicketTierPriceRow
	session uuid.UUID
}

// loadSmartPriceGuard reads the session the bundle addresses. It answers nil
// when the import is a smart price write itself, addresses no existing
// session, or the session has no active preset.
func loadSmartPriceGuard(ctx context.Context, q *gen.Queries, plan importPlan) (*smartPriceGuard, error) {
	if plan.SmartPriceWrite || plan.Source != bil24compat.SourceArena {
		return nil, nil
	}
	sid, err := uuid.Parse(strings.TrimSpace(plan.Request.ActionEvent.ArenaSessionID))
	if err != nil {
		return nil, nil
	}
	rules, err := q.ListSmartPriceRulesBySession(ctx, sid, plan.OrgID)
	if err != nil {
		return nil, err
	}
	g := &smartPriceGuard{session: sid, prices: map[uuid.UUID]int64{}, windows: map[uuid.UUID][]gen.TicketTierPriceRow{}}
	for _, r := range rules {
		if r.Status == "active" {
			g.order = append(g.order, r.TierID)
			g.order = append(g.order, r.StepTierIDs...)
		}
	}
	if len(g.order) == 0 {
		return nil, nil
	}
	if err := g.read(ctx, q, g.prices, g.windows); err != nil {
		return nil, err
	}
	return g, nil
}

func (g *smartPriceGuard) read(ctx context.Context, q *gen.Queries, prices map[uuid.UUID]int64, windows map[uuid.UUID][]gen.TicketTierPriceRow) error {
	tiers, err := q.ListTicketTiersBySession(ctx, g.session)
	if err != nil {
		return err
	}
	for _, t := range tiers {
		prices[t.ID] = t.PriceAmount
	}
	ws, err := q.ListTierPriceWindows(ctx, g.order)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	for _, w := range ws {
		windows[w.TierID] = append(windows[w.TierID], w)
	}
	return nil
}

// apply ends the presets whose categories the import changed. A price or a
// window the import wrote by hand stays; what the preset made and the
// import sent back unchanged stops the rise.
func (g *smartPriceGuard) apply(ctx context.Context, q *gen.Queries) error {
	prices := map[uuid.UUID]int64{}
	windows := map[uuid.UUID][]gen.TicketTierPriceRow{}
	if err := g.read(ctx, q, prices, windows); err != nil {
		return err
	}
	for _, id := range g.order {
		if prices[id] == g.prices[id] && sameWindows(windows[id], g.windows[id]) {
			continue
		}
		if _, err := q.OverrideSmartPriceRulesForTier(ctx, gen.SmartOverride{
			TierID: id, ScheduleTyped: true, Before: g.windows[id], PricesBefore: g.prices,
		}); err != nil {
			return err
		}
	}
	return nil
}

// sameWindows compares two schedules by what prices a moment (ids change on
// every import: it rewrites the whole schedule).
func sameWindows(a, b []gen.TicketTierPriceRow) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		sameEnd := (x.ValidTo == nil && y.ValidTo == nil) || (x.ValidTo != nil && y.ValidTo != nil && x.ValidTo.Equal(*y.ValidTo))
		if !x.ValidFrom.Equal(y.ValidFrom) || !sameEnd || x.PriceAmount != y.PriceAmount {
			return false
		}
	}
	return true
}

// public_feed_price_steps.go — how a chain of categories reaches the buyer
// (spec 08_architecture/51_4_2_smart_price_ru.md, section 4).
//
// A chain (migration 0112/0114) sells one category at a time. Its later steps
// are closed with no free place, so the buyer page used to draw each of them
// as a "Sold out" card — false, they have not started. The feed now leaves
// out every step after the one that sells now. A chain made by a smart price
// preset shows ONE card under the first step's name (owner decision В-4): its
// past steps are left out too, and every step answers with the first one's
// name. A chain typed by hand keeps its sold-out past steps ("Early bird",
// honest) and its own names.
//
// The card of a step with a dearer next step carries price_step: the free
// places of the step (what it sells before the price rises) and the next
// price. A smart price only raises prices: an equal or lower next step (a
// stopped preset) carries nothing.
package hfeed

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/priceresolve"
)

type publicFeedPriceStep struct {
	RemainingAtPrice *int  `json:"remaining_at_price"`
	NextPrice        int64 `json:"next_price"`
	// NextTierID is the category the sale passes to after this step.
	NextTierID *string `json:"next_tier_id"`
}

// chainView is what the feed does to the chain steps of one session.
type chainView struct {
	hide map[uuid.UUID]bool
	name map[uuid.UUID]string
	next map[uuid.UUID]uuid.UUID
}

func (h *Handler) loadChainView(ctx context.Context, sessionID uuid.UUID, tiers []gen.TicketTierRow) chainView {
	links, err := h.tierQueries.ListTierChainForSession(ctx, sessionID)
	if err != nil {
		h.logger.Error("public_feed: category chain lookup failed",
			slog.String("session_id", sessionID.String()), slog.String("error", err.Error()))
		return chainView{}
	}
	if len(links) == 0 {
		return chainView{}
	}
	rules, err := h.tierQueries.ListSmartPriceRulesForSession(ctx, sessionID)
	if err != nil {
		h.logger.Error("public_feed: smart price lookup failed",
			slog.String("session_id", sessionID.String()), slog.String("error", err.Error()))
		rules = nil
	}
	smartHeads := map[uuid.UUID]bool{}
	for _, r := range rules {
		if r.Kind == "by_sales" {
			smartHeads[r.TierID] = true
		}
	}
	return buildChainView(tiers, links, smartHeads)
}

// buildChainView is the rule itself, a pure function of the session's rows.
func buildChainView(tiers []gen.TicketTierRow, links []gen.TierChainRow, smartHeads map[uuid.UUID]bool) chainView {
	v := chainView{hide: map[uuid.UUID]bool{}, name: map[uuid.UUID]string{}, next: map[uuid.UUID]uuid.UUID{}}
	byID := map[uuid.UUID]gen.TicketTierRow{}
	for _, t := range tiers {
		byID[t.ID] = t
	}
	link := map[uuid.UUID]gen.TierChainRow{}
	incoming := map[uuid.UUID]bool{}
	for _, l := range links {
		if _, ok := byID[l.TierID]; !ok {
			continue
		}
		if _, ok := byID[l.NextTierID]; !ok {
			continue
		}
		link[l.TierID] = l
		incoming[l.NextTierID] = true
	}
	for head := range link {
		if incoming[head] {
			continue
		}
		var members []uuid.UUID
		seen := map[uuid.UUID]bool{}
		for cur := head; !seen[cur]; {
			seen[cur] = true
			members = append(members, cur)
			l, ok := link[cur]
			if !ok {
				break
			}
			v.next[cur] = l.NextTierID
			cur = l.NextTierID
		}
		selling := len(members) - 1
		for i, id := range members {
			if l, ok := link[id]; !ok || l.HandedOverAt == nil {
				selling = i
				break
			}
		}
		smart := smartHeads[head]
		for i, id := range members {
			switch {
			case i > selling:
				v.hide[id] = true
			case i < selling && smart:
				v.hide[id] = true
			case smart:
				v.name[id] = byID[head].Name
			}
		}
	}
	return v
}

// priceStep is the price_step of a visible card, nil when its next step is
// not dearer (or it has none).
func (v chainView) priceStep(tierID uuid.UUID, tr publicFeedTierResponse, eff map[uuid.UUID]priceresolve.Effective, tiers []gen.TicketTierRow) *publicFeedPriceStep {
	nextID, ok := v.next[tierID]
	if !ok {
		return nil
	}
	var nextPrice int64 = -1
	for _, t := range tiers {
		if t.ID == nextID {
			nextPrice = t.PriceAmount
		}
	}
	if e, ok := eff[nextID]; ok {
		nextPrice = e.Amount
	}
	if nextPrice <= tr.CurrentPrice {
		return nil
	}
	nextTier := nextID.String()
	return &publicFeedPriceStep{RemainingAtPrice: tr.Available, NextPrice: nextPrice, NextTierID: &nextTier}
}

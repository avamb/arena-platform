// seat_categories.go - the ONE place that turns session_seats.tier_id into the
// category a buyer sees for a seat (execution spec 40, S1a; plan 26 section 1.1).
//
// A seating plan groups seats into geometry categories (index 1..N). The
// operator can repaint seats with PATCH .../seats/category, which writes a new
// session_seats.tier_id and leaves the geometry untouched. The per-seat tier is
// therefore the truth: every buyer render (widget /schema, BSS layout.svg, the
// Bil24 sbt/1.0 image) must take a seat's category and price from it, exactly
// as GET_SEAT_LIST already does. Anchoring a whole category on its FIRST seat
// (the old rule) made a repainted seat keep the old price in those renders.
//
// Model:
//
//   - Home tier of a plan category = the tier carried by most of its seats
//     (ties: the tier met first in seat_key order). It labels the category
//     entry itself, so moving a few seats, even the first one, never changes
//     what the rest of the category shows.
//   - A seat whose tier is its category's home tier keeps the plan category.
//   - A seat with another tier moves to the category that is home to that tier
//     (the lowest such index). A tier that is home to no plan category gets a
//     synthetic category appended after the last plan index, so the seat still
//     points at an entry that carries exactly its tier and price.
//   - Seats without a resolvable tier (NULL, or a soft-deleted tier) keep
//     the plan category.
package hseating

import (
	"sort"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// syntheticCategoryPalette colours the synthetic categories. A tier has no
// colour of its own until ticket_tiers.color exists (plan 26, step S1d), so a
// repainted seat gets a stable, distinct swatch instead of its old category's.
var syntheticCategoryPalette = []string{
	"#8e24aa", "#00897b", "#f4511e", "#3949ab",
	"#7cb342", "#d81b60", "#00acc1", "#6d4c41",
}

// extraCategory is a synthetic category: a tier that carries seats but is the
// home tier of no plan category.
type extraCategory struct {
	Index int
	Tier  gen.TicketTierRow
	Color string
}

// seatCategoryResolution is the per-seat category truth of one session.
type seatCategoryResolution struct {
	// Home maps a plan category index to its home tier. A category none of
	// whose seats carries a resolvable tier is absent.
	Home map[int]gen.TicketTierRow
	// Extra lists the synthetic categories, ordered by Index.
	Extra []extraCategory
	// SeatIndex maps seat_key to the effective category index, ONLY for
	// seats whose effective category differs from the plan one.
	SeatIndex map[string]int
}

// resolveSeatCategories computes the resolution from the live seats and the
// session's tiers. Pure and deterministic: tiers are visited in slice order
// and seats in slice order (ListSessionSeats returns seat_key order).
func resolveSeatCategories(
	g seating.Geometry,
	seats []gen.SessionSeatRow,
	tiers []gen.TicketTierRow,
	seatIdx map[string]seating.Seat,
) seatCategoryResolution {
	tierByID := make(map[string]gen.TicketTierRow, len(tiers))
	tierPos := make(map[string]int, len(tiers))
	for i, t := range tiers {
		tierByID[t.ID.String()] = t
		tierPos[t.ID.String()] = i
	}

	type seatTier struct {
		key  string
		cat  int
		tier string
	}
	// counts[cat][tier] and first-seen order per category (tie-break).
	counts := make(map[int]map[string]int)
	order := make(map[int][]string)
	resolved := make([]seatTier, 0, len(seats))
	for _, s := range seats {
		if s.TierID == nil {
			continue
		}
		seat, ok := seatIdx[s.SeatKey]
		if !ok {
			continue
		}
		tid := s.TierID.String()
		if _, ok := tierByID[tid]; !ok {
			continue
		}
		c := counts[seat.CategoryIndex]
		if c == nil {
			c = make(map[string]int)
			counts[seat.CategoryIndex] = c
		}
		if c[tid] == 0 {
			order[seat.CategoryIndex] = append(order[seat.CategoryIndex], tid)
		}
		c[tid]++
		resolved = append(resolved, seatTier{key: s.SeatKey, cat: seat.CategoryIndex, tier: tid})
	}

	res := seatCategoryResolution{
		Home:      make(map[int]gen.TicketTierRow, len(counts)),
		SeatIndex: make(map[string]int),
	}
	homeID := make(map[int]string, len(counts))
	for cat, c := range counts {
		best, bestN := "", 0
		for _, tid := range order[cat] {
			if c[tid] > bestN {
				best, bestN = tid, c[tid]
			}
		}
		homeID[cat] = best
		res.Home[cat] = tierByID[best]
	}

	// Representative plan index of a tier: the lowest category it is home to.
	rep := make(map[string]int)
	for cat, tid := range homeID {
		if cur, ok := rep[tid]; !ok || cat < cur {
			rep[tid] = cat
		}
	}

	// Synthetic categories for moved-to tiers that are home to no category.
	maxIdx := 0
	for _, c := range g.Categories {
		if c.Index > maxIdx {
			maxIdx = c.Index
		}
	}
	needsExtra := make(map[string]bool)
	for _, st := range resolved {
		if homeID[st.cat] == st.tier {
			continue
		}
		if _, ok := rep[st.tier]; !ok {
			needsExtra[st.tier] = true
		}
	}
	extraIDs := make([]string, 0, len(needsExtra))
	for tid := range needsExtra {
		extraIDs = append(extraIDs, tid)
	}
	sort.Slice(extraIDs, func(i, j int) bool { return tierPos[extraIDs[i]] < tierPos[extraIDs[j]] })
	for n, tid := range extraIDs {
		idx := maxIdx + 1 + n
		rep[tid] = idx
		res.Extra = append(res.Extra, extraCategory{
			Index: idx,
			Tier:  tierByID[tid],
			Color: syntheticCategoryPalette[n%len(syntheticCategoryPalette)],
		})
	}

	for _, st := range resolved {
		if homeID[st.cat] == st.tier {
			continue
		}
		res.SeatIndex[st.key] = rep[st.tier]
	}
	return res
}

// EffectiveIndex returns the category index a seat shows: the override when
// the seat was repainted, otherwise its plan category.
func (r seatCategoryResolution) EffectiveIndex(seatKey string, planIndex int) int {
	if idx, ok := r.SeatIndex[seatKey]; ok {
		return idx
	}
	return planIndex
}

// CategoryTiers maps every category index, plan or synthetic, to the tier it
// shows.
func (r seatCategoryResolution) CategoryTiers() map[int]gen.TicketTierRow {
	out := make(map[int]gen.TicketTierRow, len(r.Home)+len(r.Extra))
	for idx, t := range r.Home {
		out[idx] = t
	}
	for _, e := range r.Extra {
		out[e.Index] = e.Tier
	}
	return out
}

// Categories returns the geometry's categories followed by the synthetic ones,
// ordered by index, so a renderer can walk one list.
func (r seatCategoryResolution) Categories(g seating.Geometry) []seating.Category {
	out := append([]seating.Category(nil), g.Categories...)
	for _, e := range r.Extra {
		out = append(out, seating.Category{Index: e.Index, Name: e.Tier.Name, Color: e.Color})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

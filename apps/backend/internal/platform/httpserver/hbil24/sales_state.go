package hbil24

// sales_state.go — the organizer's sales state of a session on the gateway
// wire (migration 0183, package salesstate). One state for every channel:
// the hold gate refuses a new hold on a "soon" or "sold out" session, and
// GET_ALL_ACTIONS reports the state as salesState so a site draws "coming
// soon" or the SOLD OUT ribbon without guessing from the places.

import (
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesstate"
)

// salesStateDescKey is the bil24.* description of a sales-state refusal.
func salesStateDescKey(err error) string {
	if errors.Is(err, hcheckout.ErrSessionSoldOut) {
		return "bil24.session_sold_out"
	}
	return "bil24.sales_soon"
}

// salesStateDescFallback is the English text used without a bundle.
func salesStateDescFallback(err error) string {
	if errors.Is(err, hcheckout.ErrSessionSoldOut) {
		return "This session is sold out."
	}
	return "Ticket sales for this session have not started yet."
}

// salesStateRefusal is the 101 answer to a hold on a session the organizer
// set to "coming soon" or "sold out". salesState (merged into the envelope
// like every Data key) carries the state in the catalog's vocabulary (soon /
// soldout), so a site can switch the page
// to it at once instead of waiting for its next catalog sync.
func salesStateRefusal(command, description string, err error) bil24Response {
	resp := bil24Error(command, ResultCodeUserVisible, description)
	resp.Data = map[string]any{
		"salesState": salesstate.Wire(hcheckout.CategoryGateSalesState(err)),
	}
	return resp
}

// actionEventSalesState decides one catalog session's state from the rows the
// catalog already loaded (no query of its own), by the same rule the event
// center uses (salesstate.Effective). tiers must already carry the session's
// sales end (hcheckout.CapSaleWindow). The catalog lists only sessions that
// are on sale or about to be, so the status is "scheduled". The places of an
// add-on category (addon, migration 0197) count only when the session has no
// main category, as in gen.ListSessionSalesFacts.
func actionEventSalesState(s gen.ActionEventRow, tiers []gen.ActionEventTierRow, manual string, addon map[uuid.UUID]bool, now time.Time) string {
	f := salesstate.Facts{Status: "scheduled", Categories: len(tiers)}
	hasMain := false
	for _, t := range tiers {
		if !addon[t.Tier.ID] {
			hasMain = true
			break
		}
	}
	if s.SellEndAt != nil {
		f.SalesEnd = *s.SellEndAt
	} else {
		f.SalesEnd = s.StartAt
	}
	for _, t := range tiers {
		if hasMain && addon[t.Tier.ID] {
			continue
		}
		total := int(t.GAUnitsTotal) + int(t.SeatsTotal)
		free := max(int(t.GAUnitsAvailable)+int(t.SeatsAvailable), 0)
		f.TotalPlaces += total
		f.FreePlaces += free
		if free == 0 {
			continue
		}
		if hcheckout.CategorySellable(t.Tier, now) == nil {
			f.Selling = true
			continue
		}
		if t.Tier.SaleWindowEnd != nil && now.After(*t.Tier.SaleWindowEnd) {
			continue
		}
		if !t.Tier.IsOpen || (t.Tier.SaleWindowStart != nil && now.Before(*t.Tier.SaleWindowStart)) {
			f.OpensLater = true
		}
	}
	return salesstate.Effective(manual, f, now)
}

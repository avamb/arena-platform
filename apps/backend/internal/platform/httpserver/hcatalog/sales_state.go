package hcatalog

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// sales_state.go computes `sales_state` for the organization events list
// (EC-02, spec 35 §5.3) on the SERVER, so the Telegram bot and admin-web
// never disagree about which events are "on sale". The facts come from one
// batched query (gen.ListEventSalesFacts); the decision is the pure function
// below.

// Sales states of an event, as the list reports them.
const (
	SalesStateOnSale   = "on_sale"  // a future session is selling right now
	SalesStateUpcoming = "upcoming" // future sessions exist, none is selling yet (or any more)
	SalesStateSoldOut  = "sold_out" // future sessions exist and no free place is left in any of them
	SalesStateArchived = "archived" // every session has ended, or the event is cancelled/archived
)

// salesStateFor decides the state from the event's lifecycle status and its
// session facts. ok=false means the event has no active session at all.
//
// Precedence: a cancelled or archived event is archived whatever its
// sessions say; so is an event whose sessions have all ended. Then a
// session selling now wins; then an empty hall (no free place in any live
// category of any future session) is sold out; everything else — sales not
// yet open, a closed category that still has places, a sale that ended
// before the start — is upcoming.
func salesStateFor(eventStatus string, f gen.EventSalesFacts, ok bool) string {
	if eventStatus == "cancelled" || eventStatus == "archived" {
		return SalesStateArchived
	}
	if !ok || f.FutureSessions == 0 {
		return SalesStateArchived
	}
	switch {
	case f.Selling:
		return SalesStateOnSale
	case !f.PlacesLeft:
		return SalesStateSoldOut
	default:
		return SalesStateUpcoming
	}
}

// hydrateSalesStates fills SalesState, NextSessionAt and SessionCount on the
// given responses from one ListEventSalesFacts round trip. A failed lookup
// logs and leaves SalesState empty rather than failing the list — the
// caller can still show the events, and an empty state is visibly "unknown"
// rather than a wrong answer.
func (h *Handler) hydrateSalesStates(ctx context.Context, responses []eventResponse) {
	if h.eventQueries == nil || len(responses) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(responses))
	for _, r := range responses {
		if id, err := uuid.Parse(r.ID); err == nil {
			ids = append(ids, id)
		}
	}
	facts, err := h.eventQueries.ListEventSalesFacts(ctx, ids)
	if err != nil {
		h.logger.Warn("event: sales-state hydration failed", slog.String("error", err.Error()))
		return
	}
	for i := range responses {
		id, err := uuid.Parse(responses[i].ID)
		if err != nil {
			continue
		}
		f, ok := facts[id]
		responses[i].SalesState = salesStateFor(responses[i].Status, f, ok)
		responses[i].SessionCount = int(f.SessionCount)
		responses[i].NextSessionAt = nil
		if ok && f.NextSessionAt != nil {
			s := f.NextSessionAt.UTC().Format(time.RFC3339)
			responses[i].NextSessionAt = &s
		}
	}
}

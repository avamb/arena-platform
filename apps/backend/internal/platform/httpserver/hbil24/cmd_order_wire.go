// cmd_order_wire.go — GET_ORDER_INFO's spec §9.3 answer (feature #505,
// W1-B7b).
//
// The wire vocabulary itself lives in internal/platform/bil24wire; this file
// only assembles the EncodeContext that a pure encoder cannot know: the
// selling agent/frontend identity, the bigint catalog ids from
// compatibility_id_map, and the order's service charge as booked on the
// checkout session.
package hbil24

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/bil24wire"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/orderexport"
)

// encodeOrderHeaderForWire answers GET_ORDER_INFO with the full spec §9.3
// order object minus ticketList (§7.8). The bool reports whether the wire
// object could be built at all; false means the caller must fall back to the
// pre-#505 hand-built body, which is the honest answer for a session that has
// no issued tickets yet (a pending cart is not a Bil24 order).
func (h *Handler) encodeOrderHeaderForWire(
	ctx context.Context,
	cs gen.CheckoutSessionRow,
	channel gen.SalesChannelRow,
) (bil24wire.Order, bool) {
	if h.orderExport == nil {
		return bil24wire.Order{}, false
	}
	projected, err := h.orderExport(ctx, cs.ID)
	if err != nil {
		h.logger.Error("bil24_compat: GET_ORDER_INFO: order projection failed",
			slog.String("order_id", cs.ID.String()),
			slog.String("error", err.Error()),
		)
		return bil24wire.Order{}, false
	}
	if projected == nil || len(projected.Tickets) == 0 {
		return bil24wire.Order{}, false
	}

	return bil24wire.EncodeOrderHeader(*projected, h.buildEncodeContext(ctx, cs, channel, *projected)), true
}

// buildEncodeContext assembles everything the neutral projection cannot carry.
// Every lookup degrades to a zero value rather than failing the command: a
// missing catalog id costs the receiver an integer, a failed GET_ORDER_INFO
// costs it the order.
func (h *Handler) buildEncodeContext(
	ctx context.Context,
	cs gen.CheckoutSessionRow,
	channel gen.SalesChannelRow,
	projected orderexport.Order,
) bil24wire.EncodeContext {
	charge := checkoutCharge(cs)
	ec := bil24wire.EncodeContext{
		// The arena organization sells; the sales channel is the frontend.
		// Neither has a compatibility_id_map kind yet (spec §3.1 covers
		// action / action_event / category_price / venue / city / country),
		// so the agent id stays 0 and the frontend reports its
		// display_number — the same integer the caller authenticated with.
		Frontend: bil24wire.Frontend{ID: channel.DisplayNumber, Name: channel.Name},
		Agent:    bil24wire.Agent{Name: channel.Name},
		Email:    projected.BuyerEmail,
		Charge:   &charge,
	}
	if h.compatDB == nil {
		return ec
	}

	var sessions, events, venues, cities []uuid.UUID
	for _, t := range projected.Tickets {
		sessions = append(sessions, t.Event.SessionID)
		events = append(events, t.Event.EventID)
		venues = append(venues, t.Event.VenueID)
		if t.Event.CityID != nil {
			cities = append(cities, *t.Event.CityID)
		}
	}
	ec.ActionEventIDs = h.ensureCompatIDs(ctx, compatids.KindActionEvent, sessions)
	ec.ActionIDs = h.ensureCompatIDs(ctx, compatids.KindAction, events)
	ec.VenueIDs = h.ensureCompatIDs(ctx, compatids.KindVenue, venues)
	ec.CityIDs = h.ensureCompatIDs(ctx, compatids.KindCity, cities)
	return ec
}

// ensureCompatIDs resolves (minting on first read) the bigint wire ids of one
// kind. A failure logs and returns nil: the encoder then emits 0 for that
// kind, which is a degraded payload rather than a dropped answer.
func (h *Handler) ensureCompatIDs(ctx context.Context, kind compatids.Kind, ids []uuid.UUID) map[uuid.UUID]int64 {
	if len(ids) == 0 {
		return nil
	}
	out, err := compatids.EnsureMany(ctx, h.compatDB, kind, ids)
	if err != nil {
		h.logger.Warn("bil24_compat: GET_ORDER_INFO: compat id resolution failed",
			slog.String("kind", string(kind)),
			slog.String("error", err.Error()),
		)
		return nil
	}
	return out
}

// checkoutCharge is the order's service charge in minor units: platform fee
// plus provider fee as booked on the checkout session. The projection cannot
// derive it — its Total is the charged amount, which already includes the fee
// but does not name it.
func checkoutCharge(cs gen.CheckoutSessionRow) int64 {
	var charge int64
	if cs.PlatformFee != nil {
		charge += *cs.PlatformFee
	}
	if cs.ProviderFee != nil {
		charge += *cs.ProviderFee
	}
	return charge
}

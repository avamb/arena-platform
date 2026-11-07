package horders

// list_cards.go — the tickets of every order of a list page (EC-V3, spec 62
// §12.2): the web event center prints order cards EXPANDED in the list, so the
// row carries what the card's ticket table needs. One query for the whole
// page; a failed lookup leaves the cards without tickets and the list still
// renders (the order screen has the full picture).

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

// listTicketCards returns, per order id, the `tickets` array of a list row.
// Every order of the page has an entry (an empty slice when it has none).
func (h *Handler) listTicketCards(ctx context.Context, ids []uuid.UUID) map[uuid.UUID][]map[string]any {
	out := make(map[uuid.UUID][]map[string]any, len(ids))
	for _, id := range ids {
		out[id] = []map[string]any{}
	}
	if len(ids) == 0 {
		return out
	}
	rows, err := h.queries.ListOrdersTicketCards(ctx, ids)
	if err != nil {
		h.logger.Warn("horders: list ticket cards lookup failed", slog.Any("error", err))
		return out
	}
	for _, r := range rows {
		letter := deliveryNone
		if r.DeliveryStatus != nil && *r.DeliveryStatus != "" {
			letter = *r.DeliveryStatus
		}
		out[r.OrderID] = append(out[r.OrderID], map[string]any{
			"id":               r.TicketID.String(),
			"system_ticket_id": r.SystemTicketID,
			"status":           r.Status,
			"tier_name":        r.TierName,
			"seat_label":       seatLabel(r.SeatSector, r.SeatRow, r.SeatNumber),
			"price":            r.Price,
			"delivery_status":  letter,
			"recipient_email":  r.RecipientEmail,
		})
	}
	return out
}

var _ = ean13.PlatformPrefix // keep the import list stable if barcodes are added to the card

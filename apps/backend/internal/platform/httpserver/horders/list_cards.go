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
		letter, to := cardLetter(r.DeliveryStatus, r.RecipientEmail, r.LetterStatus, r.LetterRecipient)
		out[r.OrderID] = append(out[r.OrderID], map[string]any{
			"id":               r.TicketID.String(),
			"system_ticket_id": r.SystemTicketID,
			"status":           r.Status,
			"tier_name":        r.TierName,
			"seat_label":       seatLabel(r.SeatSector, r.SeatRow, r.SeatNumber),
			"price":            r.Price,
			"delivery_status":  letter,
			"recipient_email":  to,
		})
	}
	return out
}

// cardLetter is the state of a ticket's letter on the list card and the
// address it went to. A delivery job still waiting to go out wins (a resend
// in progress); otherwise the provider's own word about the last letter that
// carried the ticket (sent, delayed, delivered, opened, failed); otherwise
// the job's status; otherwise "none". Until 2026-11-08 only the job was read,
// so every ticket whose letter the selling site sent (a WordPress order, no
// job) showed "no letter" while the order's letter block said "opened".
func cardLetter(jobStatus, jobTo, letterStatus, letterTo *string) (string, *string) {
	job := ""
	if jobStatus != nil {
		job = *jobStatus
	}
	if job == "pending" || job == "processing" {
		return job, jobTo
	}
	if letterStatus != nil && *letterStatus != "" {
		return *letterStatus, letterTo
	}
	if job != "" {
		return job, jobTo
	}
	return deliveryNone, jobTo
}

var _ = ean13.PlatformPrefix // keep the import list stable if barcodes are added to the card

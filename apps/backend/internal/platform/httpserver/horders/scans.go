package horders

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// M2: what the order card shows about entry at the door. A ticket carries its
// last scans (any result, newest first): when, whether the gate let it in,
// who reported it (arena's own scanner or MACS), the scanner's own name and
// the gate. `used_at` stays the first admission and is not repeated here.

// ordersScansPerTicket is how many scans one ticket of the card lists.
const ordersScansPerTicket = 5

// scanEntry shapes one scan row for the card's `scans` array.
func scanEntry(r gen.TicketScanRow) map[string]any {
	return map[string]any{
		"scanned_at":    r.ScannedAt.UTC().Format(time.RFC3339),
		"result":        r.Result,
		"source":        r.Source,
		"scanner_label": r.ScannerLabel,
		"gate":          r.Gate,
	}
}

// attachScans fills the `scans` array of every ticket entry (always present,
// empty when the ticket was never scanned). A failed lookup leaves the lists
// empty and logs: the card still renders without them.
func (h *Handler) attachScans(ctx context.Context, order gen.OrderRow, tickets []map[string]any, rows []gen.OrderTicketDetailRow) {
	byTicket := map[uuid.UUID][]map[string]any{}
	if len(rows) > 0 {
		ids := make([]uuid.UUID, len(rows))
		for i, r := range rows {
			ids[i] = r.TicketID
		}
		scans, err := h.queries.ListTicketScans(ctx, order.OrgID, ids, ordersScansPerTicket)
		if err != nil {
			h.logger.Warn("horders: ticket scans lookup failed",
				slog.String("order_id", order.ID.String()), slog.Any("error", err))
		}
		for _, s := range scans {
			byTicket[s.TicketID] = append(byTicket[s.TicketID], scanEntry(s))
		}
	}
	for i, r := range rows {
		list := byTicket[r.TicketID]
		if list == nil {
			list = []map[string]any{}
		}
		tickets[i]["scans"] = list
	}
}

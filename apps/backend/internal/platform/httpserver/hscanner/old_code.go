package hscanner

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/ticketreissue"
)

// recordOldCode keeps a try to enter with the code of a replaced ticket
// (spec 63 section 4.8). Best effort: the door answer never waits on it.
func (h *Handler) recordOldCode(ctx context.Context, ticketID uuid.UUID, at time.Time, result, gate string) {
	if h.barcodeQueries == nil {
		return
	}
	if _, err := ticketreissue.RecordOldCodeAttempt(ctx, h.barcodeQueries.DB(), ticketID, at,
		ticketreissue.AttemptSourceArena, result, gate); err != nil {
		h.logger.Warn("scanner: old code attempt not recorded",
			slog.String("ticket_id", ticketID.String()), slog.String("error", err.Error()))
	}
}

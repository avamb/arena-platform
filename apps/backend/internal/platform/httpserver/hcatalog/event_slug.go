package hcatalog

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventslug"
)

// ensureEventSlug gives a published event without a public address its first
// slug (P-5). Best effort: the event is already published, so a failure is
// logged and the next publication, the backfill command or PATCH can retry.
func (h *Handler) ensureEventSlug(ctx context.Context, orgID, eventID uuid.UUID) {
	if h.eventQueries == nil {
		return
	}
	ev, err := h.eventQueries.GetEventRaw(ctx, eventID)
	if err != nil || ev.OrgID != orgID || ev.Slug != nil {
		if err != nil {
			h.logger.Error("event: slug lookup failed", slog.String("event_id", eventID.String()), slog.String("error", err.Error()))
		}
		return
	}
	if _, err := eventslug.Assign(ctx, h.eventQueries, orgID, eventID, ev.Name); err != nil {
		h.logger.Error("event: slug assignment failed", slog.String("event_id", eventID.String()), slog.String("error", err.Error()))
	}
}

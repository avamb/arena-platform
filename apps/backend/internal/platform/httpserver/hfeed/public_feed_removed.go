package hfeed

// public_feed_removed.go - the "unpublished / deleted" marker of the public
// feed (spec 62 §6.1, item P-6).
//
// GET /v1/public/feeds/{token}/events?include_removed=true adds a
// `removed_events` array next to `events`. It lists every event that left the
// token's list within the last 30 days with a terminal status and the time it
// left, so a static-site builder can tell "removed" from "the answer was
// incomplete" and delete the page. Without the parameter the answer is
// byte-for-byte what it was: the removed events are NOT mixed into `events`
// or its pagination, because widgets and plugins read that list as "on sale".
//
// Terminal statuses:
//
//	unpublished  the publication to this token was deleted (the tombstone of
//	             event_publication_removals is the only source: a draft merely
//	             attached to the token was never on the list and is not listed)
//	cancelled    the event was cancelled
//	archived     the event was archived
//	deleted      the event was soft- or hard-deleted
//
// An unpublished or deleted event carries only id, status and removed_at: its
// name, slug, display number and last session are not public any more (and
// the event may never have been public under that name).

import (
	"context"
	"log/slog"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

const (
	// removedEventsWindow is how long a removed event stays visible.
	removedEventsWindow = 30 * 24 * time.Hour
	// removedEventsLimit bounds the answer; far above what one organizer
	// removes in 30 days.
	removedEventsLimit = 500
)

// Terminal feed statuses of a removed event.
const (
	RemovedStatusUnpublished = "unpublished"
	RemovedStatusCancelled   = "cancelled"
	RemovedStatusArchived    = "archived"
	RemovedStatusDeleted     = "deleted"
)

// publicFeedRemovedEvent is one item of `removed_events`. Name, slug,
// display_number and last_session_at are null for an unpublished or deleted
// event.
type publicFeedRemovedEvent struct {
	ID            string  `json:"id"`
	DisplayNumber *int64  `json:"display_number"`
	Name          *string `json:"name"`
	Slug          *string `json:"slug"`
	Status        string  `json:"status"`
	RemovedAt     string  `json:"removed_at"`
	LastSessionAt *string `json:"last_session_at"`
}

// removedFeedStatus names why an event is not in the list any more.
func removedFeedStatus(r gen.PublicFeedRemovedEventRow) string {
	switch {
	case r.Status == nil || r.DeletedAt != nil:
		return RemovedStatusDeleted
	case *r.Status == "cancelled":
		return RemovedStatusCancelled
	case *r.Status == "archived":
		return RemovedStatusArchived
	}
	return RemovedStatusUnpublished
}

// loadRemovedFeedEvents reads the removed events of a feed token. A failure
// is reported to the caller: an incomplete "removed" list is exactly the
// silent half-answer the builder must not trust.
func (h *Handler) loadRemovedFeedEvents(ctx context.Context, feedToken string, now time.Time) ([]publicFeedRemovedEvent, error) {
	rows, err := h.publicFeedQueries.ListPublicFeedRemovedEvents(ctx, feedToken, now.Add(-removedEventsWindow), removedEventsLimit)
	if err != nil {
		h.logger.Error("public_feed: list removed events failed", slog.String("error", err.Error()))
		return nil, err
	}
	out := make([]publicFeedRemovedEvent, 0, len(rows))
	for _, r := range rows {
		status := removedFeedStatus(r)
		item := publicFeedRemovedEvent{
			ID:        r.EventID.String(),
			Status:    status,
			RemovedAt: r.RemovedAt.UTC().Format(time.RFC3339),
		}
		if status == RemovedStatusUnpublished || status == RemovedStatusDeleted {
			out = append(out, item)
			continue
		}
		item.DisplayNumber, item.Name, item.Slug = r.DisplayNumber, r.Name, r.Slug
		if r.LastSessionAt != nil {
			s := r.LastSessionAt.UTC().Format(time.RFC3339)
			item.LastSessionAt = &s
		}
		out = append(out, item)
	}
	return out, nil
}

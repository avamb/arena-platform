// catalog_fetch.go — remember when a site last fetched the catalog.
//
// The event watcher (internal/platform/eventwatch) tells the operator when a
// selling site has picked up an announced change; it can only know that from
// here. After a GET_ALL_ACTIONS that answered resultCode 0 for an
// authenticated channel, recordCatalogFetch stores the time the request
// STARTED in channel_catalog_fetches (migration 0152). The start, not the
// end: a fetch that began before a change and finished after it carries the
// old catalog and must not count as having picked the change up.
//
// Best effort: it runs on the pool, never inside a transaction, and a failure
// only logs a warning — a buyer's catalog must never fail over bookkeeping.
// The UPSERT writes only when the stored value is older by more than
// catalogFetchThrottle, so a site polling every few seconds costs one write
// every 30 s per channel, not one per request. That can only delay a
// confirmation, never fake one: the stored value is never later than a real
// fetch.
package hbil24

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// catalogFetchThrottle is how stale the stored fetch time must be before a new
// fetch overwrites it.
const catalogFetchThrottle = 30 * time.Second

const recordCatalogFetchSQL = `
	INSERT INTO channel_catalog_fetches (channel_id, last_fetched_at)
	VALUES ($1, $2)
	ON CONFLICT (channel_id) DO UPDATE
	   SET last_fetched_at = EXCLUDED.last_fetched_at
	 WHERE channel_catalog_fetches.last_fetched_at < EXCLUDED.last_fetched_at - make_interval(secs => $3)`

// recordCatalogFetch stores startedAt as the channel's latest catalog fetch.
// A nil compatDB (unit tests without a pool) makes it a no-op.
func (h *Handler) recordCatalogFetch(ctx context.Context, channelID uuid.UUID, startedAt time.Time) {
	if h.compatDB == nil || channelID == uuid.Nil {
		return
	}
	if _, err := h.compatDB.Exec(ctx, recordCatalogFetchSQL,
		channelID, startedAt.UTC(), catalogFetchThrottle.Seconds()); err != nil {
		h.logger.Warn("bil24_compat: GET_ALL_ACTIONS: record catalog fetch failed",
			slog.String("channel_id", channelID.String()),
			slog.String("error", err.Error()),
		)
	}
}

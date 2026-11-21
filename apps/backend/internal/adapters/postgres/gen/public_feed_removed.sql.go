// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: public_feed_removed.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// PublicFeedRemovedEventRow is one event that left a feed token's list:
// unpublished from the token (tombstone row of migration 0204, the only
// source of "unpublished") or still attached to it but cancelled, archived or
// soft-deleted. A draft that is merely attached to the token is never listed:
// it was never on the list. The event columns are nil when the event row no
// longer exists.
type PublicFeedRemovedEventRow struct {
	EventID       uuid.UUID
	DisplayNumber *int64
	Name          *string
	Slug          *string
	Status        *string
	DeletedAt     *time.Time
	LastSessionAt *time.Time
	RemovedAt     time.Time
}

// One row per event (DISTINCT ON keeps the latest removal), newest first.
const listPublicFeedRemovedEvents = `-- name: ListPublicFeedRemovedEvents :many
SELECT y.event_id, y.display_number, y.name, y.slug, y.status, y.deleted_at,
       y.last_session_at, y.removed_at
FROM (
    SELECT DISTINCT ON (x.event_id)
           x.event_id, e.display_number, e.name, e.slug, e.status, e.deleted_at,
           e.last_session_at, x.removed_at
    FROM (
        SELECT ep.event_id, COALESCE(ev.deleted_at, ev.updated_at) AS removed_at
        FROM event_publications ep
        JOIN agent_feed_tokens ft ON ft.id = ep.feed_token_id
        JOIN events ev ON ev.id = ep.event_id
        WHERE ft.token = $1
          AND ft.is_active = true
          AND (ev.status IN ('cancelled', 'archived') OR ev.deleted_at IS NOT NULL)
        UNION ALL
        SELECT r.event_id, r.removed_at
        FROM event_publication_removals r
        JOIN agent_feed_tokens ft ON ft.id = r.feed_token_id
        WHERE ft.token = $1
          AND ft.is_active = true
    ) x
    LEFT JOIN events e ON e.id = x.event_id
    WHERE x.removed_at > $2
    ORDER BY x.event_id, x.removed_at DESC
) y
ORDER BY y.removed_at DESC, y.event_id ASC
LIMIT $3`

// ListPublicFeedRemovedEvents lists the events that left the feed token's
// list after since, newest first (spec 62 item P-6). The caller decides how
// far back to look and how many to return.
func (q *Queries) ListPublicFeedRemovedEvents(ctx context.Context, token string, since time.Time, limit int32) ([]PublicFeedRemovedEventRow, error) {
	rows, err := q.db.Query(ctx, listPublicFeedRemovedEvents, token, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PublicFeedRemovedEventRow
	for rows.Next() {
		var r PublicFeedRemovedEventRow
		if err := rows.Scan(&r.EventID, &r.DisplayNumber, &r.Name, &r.Slug, &r.Status,
			&r.DeletedAt, &r.LastSessionAt, &r.RemovedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: event_sales_state.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const listEventSalesFacts = `-- name: ListEventSalesFacts :many
SELECT s.event_id,
       count(*)::bigint                                   AS session_count,
       count(*) FILTER (WHERE s.end_at > now())::bigint   AS future_sessions,
       min(s.start_at) FILTER (WHERE s.end_at > now())    AS next_session_at,
       COALESCE(bool_or(
           s.end_at > now() AND s.sales_end_at > now() AND EXISTS (
               SELECT 1
               FROM   session_seats ss
               JOIN   ticket_tiers  tt ON tt.id = ss.tier_id
               WHERE  ss.session_id = s.id
                 AND  ss.status     = 'available'
                 AND  tt.deleted_at IS NULL
                 AND  tt.is_open
                 AND  (tt.sale_window_start IS NULL OR tt.sale_window_start <= now())
                 AND  (tt.sale_window_end   IS NULL OR tt.sale_window_end   >= now()))
       ), false)                                          AS selling,
       COALESCE(bool_or(
           s.end_at > now() AND EXISTS (
               SELECT 1
               FROM   session_seats ss
               JOIN   ticket_tiers  tt ON tt.id = ss.tier_id
               WHERE  ss.session_id = s.id
                 AND  ss.status     = 'available'
                 AND  tt.deleted_at IS NULL)
       ), false)                                          AS places_left
FROM   sessions s
WHERE  s.event_id = ANY($1::uuid[])
  AND  s.deleted_at IS NULL
  AND  s.status <> 'cancelled'
GROUP  BY s.event_id`

// EventSalesFacts are the per-event facts the events list derives
// `sales_state` from (EC-02). See event_sales_state.sql for each field's
// exact rule. An event without an active session has no row.
type EventSalesFacts struct {
	EventID        uuid.UUID  `json:"event_id"`
	SessionCount   int64      `json:"session_count"`
	FutureSessions int64      `json:"future_sessions"`
	NextSessionAt  *time.Time `json:"next_session_at"`
	Selling        bool       `json:"selling"`
	PlacesLeft     bool       `json:"places_left"`
}

// ListEventSalesFacts returns the sales facts of the given events keyed by
// event id, in ONE round trip for the whole list.
func (q *Queries) ListEventSalesFacts(ctx context.Context, eventIDs []uuid.UUID) (map[uuid.UUID]EventSalesFacts, error) {
	rows, err := q.db.Query(ctx, listEventSalesFacts, eventIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[uuid.UUID]EventSalesFacts, len(eventIDs))
	for rows.Next() {
		var f EventSalesFacts
		if err := rows.Scan(&f.EventID, &f.SessionCount, &f.FutureSessions, &f.NextSessionAt, &f.Selling, &f.PlacesLeft); err != nil {
			return nil, err
		}
		out[f.EventID] = f
	}
	return out, rows.Err()
}

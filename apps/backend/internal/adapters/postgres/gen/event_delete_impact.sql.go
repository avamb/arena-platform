// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: event_delete_impact.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

const eventDeleteImpact = `-- name: EventDeleteImpact :one
SELECT
    (SELECT count(*) FROM sessions s
      WHERE s.event_id = $1 AND s.deleted_at IS NULL)::bigint            AS sessions,
    (SELECT count(*) FROM orders o
      WHERE o.event_id = $1
        AND o.status IN ('paid', 'partially_refunded', 'refunded'))::bigint AS paid_orders,
    (SELECT count(*) FROM tickets t
       JOIN sessions s ON s.id = t.session_id
      WHERE s.event_id = $1)::bigint                                       AS tickets`

// EventDeleteImpactRow is what deleting an event would touch: its live
// sessions, the orders that were ever paid (paid, partially refunded or
// refunded) and every ticket issued for any of its sessions.
type EventDeleteImpactRow struct {
	Sessions   int64 `json:"sessions"`
	PaidOrders int64 `json:"paid_orders"`
	Tickets    int64 `json:"tickets"`
}

// EventDeleteImpact counts, in one round trip, what an event's deletion would
// affect. An event with paid orders or issued tickets must never be deleted
// (archive instead): the caller refuses with 409 event.has_paid_orders.
func (q *Queries) EventDeleteImpact(ctx context.Context, eventID uuid.UUID) (EventDeleteImpactRow, error) {
	var r EventDeleteImpactRow
	err := q.db.QueryRow(ctx, eventDeleteImpact, eventID).Scan(&r.Sessions, &r.PaidOrders, &r.Tickets)
	return r, err
}

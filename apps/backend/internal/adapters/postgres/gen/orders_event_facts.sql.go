// Hand-maintained typed query wrapper; follows sqlc output conventions.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// OrderEventFactsRow is what the order card prints about the event.
type OrderEventFactsRow struct {
	EventName      string    `json:"event_name"`
	SessionStartAt time.Time `json:"session_start_at"`
	VenueTimezone  string    `json:"venue_timezone"`
}

const getOrderEventFacts = `-- name: GetOrderEventFacts :one
SELECT e.name AS event_name, s.start_at AS session_start_at, v.timezone AS venue_timezone
FROM   orders o
JOIN   events   e ON e.id = o.event_id
JOIN   sessions s ON s.id = o.session_id
JOIN   venues   v ON v.id = s.venue_id
WHERE  o.id = $1
  AND  o.org_id = $2`

// GetOrderEventFacts returns the event name, the session start and the
// venue's IANA zone of an order, scoped to the org. There is deliberately no
// deleted_at filter: the card of an order whose session was soft-deleted
// (cancelled) still needs them. Returns pgx.ErrNoRows for an unknown or
// foreign order.
func (q *Queries) GetOrderEventFacts(ctx context.Context, orderID, orgID uuid.UUID) (OrderEventFactsRow, error) {
	var r OrderEventFactsRow
	err := q.db.QueryRow(ctx, getOrderEventFacts, orderID, orgID).
		Scan(&r.EventName, &r.SessionStartAt, &r.VenueTimezone)
	return r, err
}

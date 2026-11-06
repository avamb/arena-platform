// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: public_order_event.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// PublicOrderEventRow is the "what is this order for" block of the public
// order status (spec 62 §7, item P-7): the event, the session start and the
// venue with its time zone.
type PublicOrderEventRow struct {
	EventID   uuid.UUID
	SessionID uuid.UUID
	Name      string
	Slug      *string
	StartAt   time.Time
	VenueName *string
	Timezone  *string
}

const getPublicOrderEvent = `-- name: GetPublicOrderEvent :one
SELECT e.id, s.id, e.name, e.slug, s.start_at, v.name, v.timezone
FROM sessions s
JOIN events e ON e.id = s.event_id
LEFT JOIN venues v ON v.id = s.venue_id
WHERE s.id = $1`

// GetPublicOrderEvent resolves the event block for a session. Unlike the
// feed it ignores publication and soft-delete state: a buyer's order must
// keep naming its event after the event is unpublished.
func (q *Queries) GetPublicOrderEvent(ctx context.Context, sessionID uuid.UUID) (PublicOrderEventRow, error) {
	var r PublicOrderEventRow
	err := q.db.QueryRow(ctx, getPublicOrderEvent, sessionID).Scan(
		&r.EventID, &r.SessionID, &r.Name, &r.Slug, &r.StartAt, &r.VenueName, &r.Timezone)
	return r, err
}

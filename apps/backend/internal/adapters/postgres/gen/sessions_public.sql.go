package gen

// sessions_public.sql.go — hand-maintained wrapper for the buyer-facing
// session list. Mirror: queries/sessions.sql (ListPublicSessionsByEvent).

import (
	"context"

	"github.com/google/uuid"
)

const listPublicSessionsByEvent = `-- name: ListPublicSessionsByEvent :many
SELECT ` + sessionColumns + `
FROM   sessions
WHERE  event_id = $1
  AND  deleted_at IS NULL
  AND  sales_mode <> 'control_only'
ORDER BY start_at ASC, id ASC`

// ListPublicSessionsByEvent is ListSessionsByEvent for buyer-facing readers
// (public feed, widget, hosted page): sessions that exist only for door
// control (sales_mode = 'control_only') are left out. The organizer, the
// publish gate and the import keep using ListSessionsByEvent.
func (q *Queries) ListPublicSessionsByEvent(ctx context.Context, eventID uuid.UUID) ([]SessionRow, error) {
	rows, err := q.db.Query(ctx, listPublicSessionsByEvent, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []SessionRow
	for rows.Next() {
		s, err := scanSessionRow(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

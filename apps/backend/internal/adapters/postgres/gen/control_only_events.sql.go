// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: control_only_events.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

const listControlOnlyEventIDs = `-- name: ListControlOnlyEventIDs :many
SELECT DISTINCT s.event_id
FROM   sessions s
WHERE  s.event_id = ANY($1::uuid[])
  AND  s.deleted_at IS NULL
  AND  s.sales_mode = 'control_only'`

// ListControlOnlyEventIDs returns, of the given events, the ids of those that
// hold a live control_only session (migration 0228, MA-40): the control
// events. The migration's triggers keep every live session of such an event
// control_only, so one live control_only session decides. One round trip for
// a whole list; the caller has already authorized the ids.
func (q *Queries) ListControlOnlyEventIDs(ctx context.Context, eventIDs []uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.db.Query(ctx, listControlOnlyEventIDs, eventIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

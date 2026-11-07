package gen

import (
	"context"

	"github.com/google/uuid"
)

const listTestOnlyEventIDsByOrg = `-- name: ListTestOnlyEventIDsByOrg :many
SELECT e.id
FROM   events e
WHERE  e.org_id     = $1
  AND  e.status     = 'published'
  AND  e.deleted_at IS NULL
  AND  EXISTS (
        SELECT 1
        FROM   event_publications ep
        JOIN   agent_feed_tokens ft ON ft.id = ep.feed_token_id
        JOIN   sales_channels sc ON sc.id = ft.sales_channel_id
        WHERE  ep.event_id = e.id
          AND  sc.deleted_at IS NULL
          AND  sc.is_test = true)
  AND  NOT EXISTS (
        SELECT 1
        FROM   event_publications ep
        JOIN   agent_feed_tokens ft ON ft.id = ep.feed_token_id
        JOIN   sales_channels sc ON sc.id = ft.sales_channel_id
        WHERE  ep.event_id = e.id
          AND  sc.deleted_at IS NULL
          AND  sc.is_test = false)
ORDER BY e.id`

// ListTestOnlyEventIDsByOrg returns the published events of orgID that are
// published only into test channels (sales_channels.is_test). See
// queries/action_events_test_channel.sql.
func (q *Queries) ListTestOnlyEventIDsByOrg(ctx context.Context, orgID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.db.Query(ctx, listTestOnlyEventIDsByOrg, orgID)
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

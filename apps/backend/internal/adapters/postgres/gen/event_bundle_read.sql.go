// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: event_bundle_read.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

const listEventPublicationChannelIDs = `-- name: ListEventPublicationChannelIDs :many
SELECT sc.id
FROM   event_publications ep
JOIN   agent_feed_tokens  aft ON aft.id = ep.feed_token_id
JOIN   sales_channels     sc  ON sc.id  = aft.sales_channel_id
WHERE  ep.event_id = $1
  AND  sc.org_id   = $2
  AND  sc.deleted_at IS NULL
  AND  aft.is_active
  AND  aft.revoked_at IS NULL
GROUP  BY sc.id
ORDER  BY MIN(ep.published_at), sc.id`

// ListEventPublicationChannelIDs returns the organization's sales channels the
// event is published into through a usable (active, not revoked) feed token,
// oldest publication first. Never nil.
func (q *Queries) ListEventPublicationChannelIDs(ctx context.Context, eventID, orgID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.db.Query(ctx, listEventPublicationChannelIDs, eventID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

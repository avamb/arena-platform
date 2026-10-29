// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: invitations.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// InsertInternalChannel
// ─────────────────────────────────────────────────────────────────────────────

const insertInternalChannel = `-- name: InsertInternalChannel :one
INSERT INTO sales_channels (org_id, name, kind, fee_percent, settings)
VALUES ($1, $2, 'internal', 0.00, '{}'::jsonb)
ON CONFLICT (org_id) WHERE kind = 'internal' DO NOTHING
RETURNING id`

// InsertInternalChannel creates the organization's hidden "Invitations"
// channel (spec 46, section 3.3). It answers pgx.ErrNoRows when the
// organization already has one (a concurrent first invitation won the
// partial unique index); read it then with GetInternalChannelID.
func (q *Queries) InsertInternalChannel(ctx context.Context, orgID uuid.UUID, name string) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.db.QueryRow(ctx, insertInternalChannel, orgID, name).Scan(&id)
	return id, err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetInternalChannelID
// ─────────────────────────────────────────────────────────────────────────────

const getInternalChannelID = `-- name: GetInternalChannelID :one
SELECT id
FROM   sales_channels
WHERE  org_id = $1
  AND  kind = 'internal'`

// GetInternalChannelID answers the id of the organization's internal
// "Invitations" channel, pgx.ErrNoRows when it does not exist yet.
func (q *Queries) GetInternalChannelID(ctx context.Context, orgID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.db.QueryRow(ctx, getInternalChannelID, orgID).Scan(&id)
	return id, err
}

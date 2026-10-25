// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: org_archived.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

const getOrganizationArchived = `-- name: GetOrganizationArchived :one
SELECT deleted_at IS NOT NULL AS archived
FROM   organizations
WHERE  id = $1`

// GetOrganizationArchived answers whether the organization was archived
// (organizations.deleted_at is set). It returns pgx.ErrNoRows when no
// organization with that id exists, archived or not.
func (q *Queries) GetOrganizationArchived(ctx context.Context, id uuid.UUID) (bool, error) {
	var archived bool
	err := q.db.QueryRow(ctx, getOrganizationArchived, id).Scan(&archived)
	return archived, err
}

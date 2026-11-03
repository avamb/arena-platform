// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: imports_bil24.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const getEventUpdatedAtForImport = `-- name: GetEventUpdatedAtForImport :one
SELECT updated_at
FROM   events
WHERE  id     = $1
  AND  org_id = $2`

// GetEventUpdatedAtForImport reads the event's updated_at inside the import
// transaction, after SetEventImportMetadata took the row lock: the value is
// the one this import wrote, never a concurrent writer's.
func (q *Queries) GetEventUpdatedAtForImport(ctx context.Context, id, orgID uuid.UUID) (time.Time, error) {
	var t time.Time
	err := q.db.QueryRow(ctx, getEventUpdatedAtForImport, id, orgID).Scan(&t)
	return t, err
}

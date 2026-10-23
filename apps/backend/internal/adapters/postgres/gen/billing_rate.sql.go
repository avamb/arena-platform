// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: billing_rate.sql
//
// Reads of the billing rating engine (billing wave B-A, task B1-2) on top of
// billing_foundation.sql.go, whose row types and scanners it reuses.

package gen

import (
	"context"

	"github.com/google/uuid"
)

const getBillingAccountByID = `-- name: GetBillingAccountByID :one
SELECT ` + billingAccountColumns + `
FROM   billing_accounts
WHERE  id = $1`

// GetBillingAccountByID returns one billing account, pgx.ErrNoRows when it
// does not exist.
func (q *Queries) GetBillingAccountByID(ctx context.Context, id uuid.UUID) (BillingAccountRow, error) {
	return scanBillingAccountRow(q.db.QueryRow(ctx, getBillingAccountByID, id))
}

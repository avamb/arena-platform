package gen

import (
	"context"

	"github.com/google/uuid"
)

const getGettingStartedFacts = `-- name: GetGettingStartedFacts :one
SELECT EXISTS (
           SELECT 1 FROM payment_provider_configs
           WHERE  org_id = $1 AND deleted_at IS NULL AND is_active
             AND  verification_status = 'ok'
       ) AS payments_connected,
       EXISTS (
           SELECT 1 FROM events
           WHERE  org_id = $1 AND deleted_at IS NULL
       ) AS has_event,
       EXISTS (
           SELECT 1 FROM sales_links
           WHERE  org_id = $1
       ) AS has_sales_link`

// GettingStartedFacts is what the "first steps" of an organization read from
// its rows (GET /v1/organizations/{org_id}/getting-started).
type GettingStartedFacts struct {
	// PaymentsConnected: an active, not deleted payment-provider config whose
	// credential the provider accepted (verification_status 'ok').
	PaymentsConnected bool
	// HasEvent: at least one not deleted event; a draft counts.
	HasEvent bool
	// HasSalesLink: at least one sales link, whatever its status.
	HasSalesLink bool
}

// GetGettingStartedFacts reads the three row facts of the organization's
// first steps in one round trip.
func (q *Queries) GetGettingStartedFacts(ctx context.Context, orgID uuid.UUID) (GettingStartedFacts, error) {
	var f GettingStartedFacts
	err := q.db.QueryRow(ctx, getGettingStartedFacts, orgID).Scan(&f.PaymentsConnected, &f.HasEvent, &f.HasSalesLink)
	return f, err
}

// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: hand-written (the SQL lives in the constants below only)
//
// Read side of the buyer's service charge at checkout (fee_mode buyer_pays):
// a cheap "does this organization let buyers pay the commission at all?"
// probe, run on every priced cart, and the country a session's sale is billed
// by, the same rule the billing.collect job applies. Nothing here writes.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const orgHasBuyerPaysTerms = `-- name: OrgHasBuyerPaysTerms :one
SELECT EXISTS (
    SELECT 1
    FROM   billing_accounts a
    JOIN   LATERAL (
        SELECT t.fee_mode
        FROM   billing_account_terms t
        WHERE  t.account_id = a.id AND t.effective_from <= $2
        ORDER BY t.effective_from DESC
        LIMIT  1
    ) t ON true
    WHERE  a.org_id = $1 AND a.account_kind = 'organizer' AND t.fee_mode = 'buyer_pays'
)`

// OrgHasBuyerPaysTerms reports whether any organizer billing account of the
// organization has terms with fee_mode buyer_pays in force at `at` (the same
// "last row with effective_from <= at" rule as GetBillingAccountTermsAt). It
// is one indexed probe, so a checkout of an organizer_pays organization (the
// default) pays for nothing more.
func (q *Queries) OrgHasBuyerPaysTerms(ctx context.Context, orgID uuid.UUID, at time.Time) (bool, error) {
	var ok bool
	err := q.db.QueryRow(ctx, orgHasBuyerPaysTerms, orgID, at).Scan(&ok)
	return ok, err
}

const getSessionBillingCountry = `-- name: GetSessionBillingCountry :one
SELECT COALESCE(v.country::text, '') AS venue_country,
       COALESCE(NULLIF(btrim(o.legal_address_country), ''), NULLIF(btrim(o.country), ''), '')::text AS org_country
FROM   sessions s
JOIN   events e ON e.id = s.event_id
JOIN   organizations o ON o.id = e.org_id
LEFT   JOIN venues v ON v.id = s.venue_id
WHERE  s.id = $1`

// GetSessionBillingCountry returns the country of the session's venue and the
// organization's fallback country (legal_address_country, else country), the
// two inputs billing.collect routes a sale by. pgx.ErrNoRows for an unknown
// session.
func (q *Queries) GetSessionBillingCountry(ctx context.Context, sessionID uuid.UUID) (venueCountry, orgCountry string, err error) {
	err = q.db.QueryRow(ctx, getSessionBillingCountry, sessionID).Scan(&venueCountry, &orgCountry)
	return venueCountry, orgCountry, err
}

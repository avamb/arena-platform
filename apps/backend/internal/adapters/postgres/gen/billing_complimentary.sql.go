// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: hand-written (the SQL lives in the constants below only)
//
// Billing of invitations (spec 46, section 3.6, task INV-07): the yearly free
// limit of complimentary tickets is counted from the charges the rate job
// already wrote, and the invitation preview needs the country of a session's
// venue to find the billing account the collector will bill it to.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const countFreeComplimentaryChargesBefore = `-- name: CountFreeComplimentaryChargesBefore :one
SELECT count(*)
FROM   billable_events e
JOIN   charges c ON c.billable_event_id = e.id AND c.reversal_of IS NULL
WHERE  e.account_id = $1
  AND  e.kind = 'ticket_complimentary'
  AND  c.reason = 'free'
  AND  e.occurred_at >= $2
  AND  (e.occurred_at, e.id) < ($3, $4)`

// CountFreeComplimentaryChargesBefore counts the complimentary tickets of an
// account that were rated FREE (direct charge with reason free) at or after
// from and strictly before (beforeAt, beforeID) in the keyset order the rate
// job uses. A complimentary ticket charged at the standard rate or over the
// limit does not use the free limit and is not counted.
func (q *Queries) CountFreeComplimentaryChargesBefore(ctx context.Context, accountID uuid.UUID, from, beforeAt time.Time, beforeID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countFreeComplimentaryChargesBefore, accountID, from, beforeAt, beforeID).Scan(&n)
	return n, err
}

const lockComplimentaryFreeLimit = `-- name: LockComplimentaryFreeLimit :exec
SELECT pg_advisory_xact_lock(hashtextextended('billing.rate.free_limit:' || $1::text, 0))`

// LockComplimentaryFreeLimit serializes, for one account, the rating of the
// complimentary tickets that count against the yearly free limit, so two
// passes never both see the last free place. Transaction-scoped: it is
// released by the commit or rollback of the caller's transaction. The rate
// job takes it AFTER its session lock, never before.
func (q *Queries) LockComplimentaryFreeLimit(ctx context.Context, accountID uuid.UUID) error {
	_, err := q.db.Exec(ctx, lockComplimentaryFreeLimit, accountID)
	return err
}

const getSessionVenueCountry = `-- name: GetSessionVenueCountry :one
SELECT v.country::text
FROM   sessions s
LEFT   JOIN venues v ON v.id = s.venue_id
WHERE  s.id = $1`

// GetSessionVenueCountry returns the country of the session's venue, nil when
// the venue has none; pgx.ErrNoRows when the session does not exist.
func (q *Queries) GetSessionVenueCountry(ctx context.Context, sessionID uuid.UUID) (*string, error) {
	var c *string
	err := q.db.QueryRow(ctx, getSessionVenueCountry, sessionID).Scan(&c)
	return c, err
}

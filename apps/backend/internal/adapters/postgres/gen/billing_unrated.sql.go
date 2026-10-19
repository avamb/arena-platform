// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: hand-written (the SQL lives in the constants below only)
//
// Reads of the billing.rate job (billing wave B-A, task B2-3): a keyset walk
// over the events that still have no direct charge, and the two reads that
// bound a session cancellation to the sales made BEFORE it. Nothing here
// writes.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const listUnratedBillableEventsAfter = `-- name: ListUnratedBillableEventsAfter :many
SELECT ` + billableEventColumnsE + `
FROM   billable_events e
WHERE  e.occurred_at >= $1
  AND  (e.occurred_at, e.id) > ($1, $2)
  AND  NOT EXISTS (SELECT 1 FROM charges c
                   WHERE  c.billable_event_id = e.id AND c.reversal_of IS NULL)
ORDER BY e.occurred_at, e.id
LIMIT  $3`

// ListUnratedBillableEventsAfter returns up to limit events with no direct
// charge, oldest first, after the key (afterAt, afterID): a page of a keyset
// walk, so events a pass skipped are not read again within it.
func (q *Queries) ListUnratedBillableEventsAfter(ctx context.Context, afterAt time.Time, afterID uuid.UUID, limit int32) ([]BillableEventRow, error) {
	rows, err := q.db.Query(ctx, listUnratedBillableEventsAfter, afterAt, afterID, limit)
	if err != nil {
		return nil, err
	}
	return collectBillableEventRows(rows)
}

const countUnratedBillableEventsForSessionUntil = `-- name: CountUnratedBillableEventsForSessionUntil :one
SELECT count(*)
FROM   billable_events e
WHERE  e.session_id = $1 AND e.kind = $2 AND e.occurred_at <= $3
  AND  NOT EXISTS (SELECT 1 FROM charges c
                   WHERE  c.billable_event_id = e.id AND c.reversal_of IS NULL)`

// CountUnratedBillableEventsForSessionUntil counts the events of one kind of
// a session that occurred at or before notAfter and still have no direct
// charge: the sales a cancellation at notAfter has to wait for. A sale after
// the cancellation is not waited for and not reversed.
func (q *Queries) CountUnratedBillableEventsForSessionUntil(ctx context.Context, sessionID uuid.UUID, kind string, notAfter time.Time) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countUnratedBillableEventsForSessionUntil, sessionID, kind, notAfter).Scan(&n)
	return n, err
}

const listReversibleChargesForSessionUntil = `-- name: ListReversibleChargesForSessionUntil :many
SELECT ` + chargeColumnsC + `
FROM   charges c
JOIN   billable_events e ON e.id = c.billable_event_id
WHERE  e.session_id = $1 AND e.kind = 'ticket_sold' AND e.occurred_at <= $2
  AND  c.reversal_of IS NULL
  AND  NOT EXISTS (SELECT 1 FROM charges r WHERE r.reversal_of = c.id)
ORDER BY e.occurred_at, c.id`

// ListReversibleChargesForSessionUntil returns the direct charges of the
// session's ticket_sold events that occurred at or before notAfter and have
// no reversal yet: what a cancellation at notAfter reverses.
func (q *Queries) ListReversibleChargesForSessionUntil(ctx context.Context, sessionID uuid.UUID, notAfter time.Time) ([]ChargeRow, error) {
	rows, err := q.db.Query(ctx, listReversibleChargesForSessionUntil, sessionID, notAfter)
	if err != nil {
		return nil, err
	}
	return collectChargeRows(rows)
}

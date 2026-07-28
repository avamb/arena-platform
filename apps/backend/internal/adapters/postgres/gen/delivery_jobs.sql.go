// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: delivery_jobs.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// DeliveryJobRow — shared result type for all delivery_jobs queries
// ─────────────────────────────────────────────────────────────────────────────

// DeliveryJobRow is the result type returned by all delivery_jobs queries.
//
// A DeliveryJobRow tracks the state of a single ticket email delivery attempt.
// Status transitions (PR-03):
//
//	pending → processing → sent      (terminal: SMTP accepted)
//	pending → processing → failed    (terminal: dead-lettered)
//	pending → disabled               (terminal: no real sender configured)
//	pending → skipped                (terminal: no recipient email available)
//
// RecipientEmail is nil when the email address was not known at enqueue time;
// the worker resolves it from ticket.holder_email at delivery time.
type DeliveryJobRow struct {
	ID             uuid.UUID  `json:"id"`
	TicketID       uuid.UUID  `json:"ticket_id"`
	RecipientEmail *string    `json:"recipient_email"`
	Status         string     `json:"status"`
	Attempts       int32      `json:"attempts"`
	LastError      *string    `json:"last_error"`
	QueuedAt       time.Time  `json:"queued_at"`
	SentAt         *time.Time `json:"sent_at"`
	ProcessingAt   *time.Time `json:"processing_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// scanDeliveryJobRow scans a single delivery_jobs row into a DeliveryJobRow.
// Column order must match all SELECT lists in this file.
func scanDeliveryJobRow(row interface {
	Scan(dest ...any) error
}) (DeliveryJobRow, error) {
	var r DeliveryJobRow
	err := row.Scan(
		&r.ID,
		&r.TicketID,
		&r.RecipientEmail,
		&r.Status,
		&r.Attempts,
		&r.LastError,
		&r.QueuedAt,
		&r.SentAt,
		&r.ProcessingAt,
		&r.CreatedAt,
		&r.UpdatedAt,
	)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertDeliveryJob
// ─────────────────────────────────────────────────────────────────────────────

const insertDeliveryJob = `-- name: InsertDeliveryJob :one
INSERT INTO delivery_jobs (ticket_id, recipient_email)
VALUES ($1, $2)
ON CONFLICT (ticket_id) DO UPDATE
    SET ticket_id = EXCLUDED.ticket_id
RETURNING id, ticket_id, recipient_email, status, attempts, last_error,
          queued_at, sent_at, processing_at, created_at, updated_at`

// InsertDeliveryJob creates a new pending delivery job for a ticket.
// recipientEmail may be nil when the email address is not yet known at
// enqueue time; the worker resolves it from ticket.holder_email at delivery time.
func (q *Queries) InsertDeliveryJob(
	ctx context.Context,
	ticketID uuid.UUID,
	recipientEmail *string,
) (DeliveryJobRow, error) {
	row := q.db.QueryRow(ctx, insertDeliveryJob, ticketID, recipientEmail)
	return scanDeliveryJobRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetDeliveryJobByTicketID
// ─────────────────────────────────────────────────────────────────────────────

const getDeliveryJobByTicketID = `-- name: GetDeliveryJobByTicketID :one
SELECT id, ticket_id, recipient_email, status, attempts, last_error,
       queued_at, sent_at, processing_at, created_at, updated_at
FROM   delivery_jobs
WHERE  ticket_id = $1
ORDER  BY created_at DESC
LIMIT  1`

// GetDeliveryJobByTicketID returns the most recent delivery job for a ticket.
// Returns pgx.ErrNoRows when no delivery job exists for the ticket.
func (q *Queries) GetDeliveryJobByTicketID(
	ctx context.Context,
	ticketID uuid.UUID,
) (DeliveryJobRow, error) {
	row := q.db.QueryRow(ctx, getDeliveryJobByTicketID, ticketID)
	return scanDeliveryJobRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// UpdateDeliveryJobStatus
// ─────────────────────────────────────────────────────────────────────────────

const updateDeliveryJobStatus = `-- name: UpdateDeliveryJobStatus :one
UPDATE delivery_jobs
SET    status     = $2,
       attempts   = attempts + 1,
       last_error = $3,
       sent_at    = CASE WHEN $2 = 'sent' THEN now() ELSE sent_at END,
       updated_at = now()
WHERE  id = $1
RETURNING id, ticket_id, recipient_email, status, attempts, last_error,
          queued_at, sent_at, processing_at, created_at, updated_at`

// UpdateDeliveryJobStatus transitions a delivery_jobs row to a new status.
// Increments attempts, stores the last error (or nil on success), and sets
// sent_at when the new status is 'sent'. Returns the updated row.
func (q *Queries) UpdateDeliveryJobStatus(
	ctx context.Context,
	id uuid.UUID,
	newStatus string,
	lastError *string,
) (DeliveryJobRow, error) {
	row := q.db.QueryRow(ctx, updateDeliveryJobStatus, id, newStatus, lastError)
	return scanDeliveryJobRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// ClaimDeliveryJobForProcessing
// ─────────────────────────────────────────────────────────────────────────────

const claimDeliveryJobForProcessing = `-- name: ClaimDeliveryJobForProcessing :one
UPDATE delivery_jobs
SET    status        = 'processing',
       processing_at = now(),
       updated_at    = now()
WHERE  id = $1
  AND  status = 'pending'
RETURNING id, ticket_id, recipient_email, status, attempts, last_error,
          queued_at, sent_at, processing_at, created_at, updated_at`

// ClaimDeliveryJobForProcessing atomically transitions a delivery_jobs row
// from 'pending' to 'processing'. Returns (row, nil) when the claim succeeds.
// Returns (zero, pgx.ErrNoRows) when the row does not exist or is not in
// 'pending' state — indicating that another worker already claimed it or it
// has already reached a terminal status. The caller should treat ErrNoRows as
// a signal to skip delivery (idempotent).
func (q *Queries) ClaimDeliveryJobForProcessing(
	ctx context.Context,
	id uuid.UUID,
) (DeliveryJobRow, error) {
	row := q.db.QueryRow(ctx, claimDeliveryJobForProcessing, id)
	return scanDeliveryJobRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// ListPendingDeliveryJobs
// ─────────────────────────────────────────────────────────────────────────────

const listPendingDeliveryJobs = `-- name: ListPendingDeliveryJobs :many
SELECT id, ticket_id, recipient_email, status, attempts, last_error,
       queued_at, sent_at, processing_at, created_at, updated_at
FROM   delivery_jobs
WHERE  status = 'pending'
ORDER  BY queued_at ASC
LIMIT  $1`

// ListPendingDeliveryJobs returns up to limit pending delivery jobs ordered by
// enqueue time (oldest first). Used by the worker for batch processing.
func (q *Queries) ListPendingDeliveryJobs(
	ctx context.Context,
	limit int32,
) ([]DeliveryJobRow, error) {
	rows, err := q.db.Query(ctx, listPendingDeliveryJobs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []DeliveryJobRow
	for rows.Next() {
		r, err := scanDeliveryJobRow(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, r)
	}
	return jobs, rows.Err()
}

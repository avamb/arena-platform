-- delivery_jobs.sql — SQL queries for ticket email delivery (feature #141).

-- name: InsertDeliveryJob :one
-- InsertDeliveryJob creates a new pending delivery job for a ticket.
-- recipient_email may be NULL when the email address is not yet known at
-- enqueue time; the worker resolves it from ticket.holder_email at delivery time.
INSERT INTO delivery_jobs (ticket_id, recipient_email)
VALUES ($1, $2)
RETURNING id, ticket_id, recipient_email, status, attempts, last_error,
          queued_at, sent_at, created_at, updated_at;

-- name: GetDeliveryJobByTicketID :one
-- GetDeliveryJobByTicketID returns the most recent delivery job for a ticket.
SELECT id, ticket_id, recipient_email, status, attempts, last_error,
       queued_at, sent_at, created_at, updated_at
FROM   delivery_jobs
WHERE  ticket_id = $1
ORDER  BY created_at DESC
LIMIT  1;

-- name: UpdateDeliveryJobStatus :one
-- UpdateDeliveryJobStatus transitions a delivery_jobs row to a new status.
-- Increments attempts, stores the last error (or NULL on success), and sets
-- sent_at when status='sent'.
UPDATE delivery_jobs
SET    status     = $2,
       attempts   = attempts + 1,
       last_error = $3,
       sent_at    = CASE WHEN $2 = 'sent' THEN now() ELSE sent_at END,
       updated_at = now()
WHERE  id = $1
RETURNING id, ticket_id, recipient_email, status, attempts, last_error,
          queued_at, sent_at, created_at, updated_at;

-- name: ListPendingDeliveryJobs :many
-- ListPendingDeliveryJobs returns up to $1 pending delivery jobs ordered by
-- enqueue time (oldest first). Used by the worker for batch processing.
SELECT id, ticket_id, recipient_email, status, attempts, last_error,
       queued_at, sent_at, created_at, updated_at
FROM   delivery_jobs
WHERE  status = 'pending'
ORDER  BY queued_at ASC
LIMIT  $1;

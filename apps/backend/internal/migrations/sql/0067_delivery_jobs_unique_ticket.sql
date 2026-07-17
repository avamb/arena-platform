-- 0067_delivery_jobs_unique_ticket.sql — enforce one delivery_job per ticket (feature #367).
--
-- Problem: IssueTicketsForCheckout called EnqueueDeliveryJobs internally AND
-- checkout.go/payment_intents.go called enqueueDelivery again after issueTickets
-- returned, producing at least two delivery_jobs rows per ticket (and thus two emails).
--
-- Fix:
--   1. Remove duplicate rows — keep the oldest delivery_job per ticket_id
--      (the first INSERT wins; subsequent ones were the duplicates).
--   2. Replace the non-unique index with a UNIQUE constraint so that any
--      future InsertDeliveryJob conflict (e.g. webhook replay) is caught at
--      the database level.
--   3. InsertDeliveryJob SQL is updated to use ON CONFLICT (ticket_id) DO UPDATE
--      so that the Go function always returns a row and remains idempotent on
--      webhook replay.

-- +goose Up

-- Step 1: Remove duplicate rows — keep the earliest delivery_job per ticket.
-- We keep the row with the smallest queued_at; for ties the smallest id wins.
-- This eliminates all duplicates regardless of the reason they were inserted.
DELETE FROM delivery_jobs
WHERE id NOT IN (
    SELECT DISTINCT ON (ticket_id) id
    FROM   delivery_jobs
    ORDER  BY ticket_id, queued_at ASC, id ASC
);

-- Step 2: Drop the old non-unique index.
DROP INDEX IF EXISTS delivery_jobs_ticket_id;

-- Step 3: Create a UNIQUE index (doubles as the fast-lookup index from before).
CREATE UNIQUE INDEX delivery_jobs_ticket_id ON delivery_jobs (ticket_id);

COMMENT ON INDEX delivery_jobs_ticket_id IS
    'Unique index: exactly one delivery_job per ticket. '
    'InsertDeliveryJob uses ON CONFLICT (ticket_id) DO UPDATE to make '
    'webhook-replay idempotent without producing duplicate email deliveries.';

-- +goose Down

DROP INDEX IF EXISTS delivery_jobs_ticket_id;

-- Restore the original non-unique index.
CREATE INDEX delivery_jobs_ticket_id ON delivery_jobs (ticket_id);

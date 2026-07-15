-- 0064_delivery_jobs_processing.sql — extend delivery_jobs state machine (PR-03).
--
-- PR-03 acceptance criteria require honest status transitions:
--   - 'processing' : job is actively being processed (SMTP in flight); the
--                    handler claims this state atomically before connecting to
--                    SMTP so that a retry sees the claim and skips the send.
--   - 'disabled'   : no real sender was configured (EMAIL_MODE ≠ smtp or
--                    sender is nil); email was never attempted.
--   - 'skipped'    : terminal skip — no recipient address was available at
--                    delivery time; no email was sent.
--
-- Previous states that remain unchanged:
--   - 'pending'    : initial state; job not yet claimed.
--   - 'sent'       : terminal success; SMTP server accepted the message.
--   - 'failed'     : terminal failure; dead-lettered after max_attempts.
--
-- State machine (updated):
--   pending  → processing  (claimed by worker before SMTP dial)
--   processing → sent      (SMTP accepted the message)
--   processing → failed    (dead-lettered; worker max_attempts exhausted)
--   pending  → disabled    (no real sender configured; terminal non-retry)
--   pending  → skipped     (no recipient address; terminal non-retry)

-- +goose Up

ALTER TABLE delivery_jobs
    DROP CONSTRAINT delivery_jobs_status_check;

ALTER TABLE delivery_jobs
    ADD CONSTRAINT delivery_jobs_status_check
    CHECK (status IN ('pending', 'processing', 'sent', 'failed', 'disabled', 'skipped'));

-- Track when a delivery job was claimed for processing.  NULL until the
-- worker atomically transitions pending → processing.
ALTER TABLE delivery_jobs
    ADD COLUMN IF NOT EXISTS processing_at timestamptz;

-- Update the pending index to also cover processing rows so a reconciliation
-- query can detect stale processing jobs efficiently.
DROP INDEX IF EXISTS delivery_jobs_status_pending;

CREATE INDEX delivery_jobs_status_pending ON delivery_jobs (queued_at)
    WHERE status = 'pending';

CREATE INDEX delivery_jobs_status_processing ON delivery_jobs (processing_at)
    WHERE status = 'processing';

COMMENT ON COLUMN delivery_jobs.processing_at IS
    'Timestamp when the delivery job was claimed by a worker (pending → processing). '
    'NULL for jobs that have not been claimed yet. Useful for reconciliation: '
    'a stale processing job (processing_at very old) may indicate a crashed worker.';

-- +goose Down

ALTER TABLE delivery_jobs
    DROP CONSTRAINT delivery_jobs_status_check;

ALTER TABLE delivery_jobs
    ADD CONSTRAINT delivery_jobs_status_check
    CHECK (status IN ('pending', 'sent', 'failed'));

ALTER TABLE delivery_jobs
    DROP COLUMN IF EXISTS processing_at;

DROP INDEX IF EXISTS delivery_jobs_status_processing;

CREATE INDEX delivery_jobs_status_pending ON delivery_jobs (queued_at)
    WHERE status = 'pending';

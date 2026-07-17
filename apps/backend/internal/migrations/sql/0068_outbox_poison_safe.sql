-- +goose Up
-- Feature #369: poison-safe outbox delivery
--
-- Adds next_attempt_at (for exponential backoff scheduling) and
-- dead_lettered_at (for permanent quarantine of poison events)
-- to the outbox_events table. Replaces the old partial index with
-- a scheduling-aware one.

ALTER TABLE outbox_events
  ADD COLUMN next_attempt_at  timestamptz,
  ADD COLUMN dead_lettered_at timestamptz;

-- Drop the old simple index.
DROP INDEX IF EXISTS outbox_events_unprocessed_idx;

-- New index: only rows eligible for dispatch.
-- Ordered by next_attempt_at NULLS FIRST so immediately-due rows come first.
CREATE INDEX outbox_events_backlog_idx
    ON outbox_events (next_attempt_at NULLS FIRST, occurred_at ASC)
    WHERE processed_at IS NULL AND dead_lettered_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS outbox_events_backlog_idx;
ALTER TABLE outbox_events
  DROP COLUMN IF EXISTS next_attempt_at,
  DROP COLUMN IF EXISTS dead_lettered_at;
CREATE INDEX outbox_events_unprocessed_idx
    ON outbox_events (occurred_at)
    WHERE processed_at IS NULL;

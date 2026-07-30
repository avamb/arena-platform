-- +goose Up
-- Restore the baseline outbox_events readiness/dispatcher index required by
-- the database contract. The scheduling-aware backlog index added in 0068 is
-- retained for retry ordering; this compatibility index preserves efficient
-- processed_at-only probes and consumers.

CREATE INDEX IF NOT EXISTS outbox_events_unprocessed_idx
    ON outbox_events (processed_at)
    WHERE processed_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS outbox_events_unprocessed_idx;

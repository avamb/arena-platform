-- +goose Up
-- =====================================================================
-- arena_new — Outbox migration (Wave 4, feature #100)
--
-- Adds the canonical outbox table used by the Writer/Dispatcher
-- boundary (internal/platform/outbox).  The earlier outbox_events
-- table (created by 0001_init.sql) is kept for backward compatibility
-- with the echo endpoint; the two tables coexist during the foundation
-- milestone and will be consolidated once the OutboxDispatcher worker
-- is wired in a later milestone.
--
-- Column notes:
--   aggregate_id  — typed uuid (not text) to match the domain model
--   dispatched_at — NULL until the dispatcher successfully delivers
--                   the event; the partial index below covers all
--                   NULL rows cheaply.
--   attempts      — incremented by the dispatcher on each delivery try
-- =====================================================================

CREATE TABLE outbox (
    id             uuid        PRIMARY KEY DEFAULT uuidv7(),
    aggregate_type text        NOT NULL,
    aggregate_id   uuid        NOT NULL,
    event_type     text        NOT NULL,
    payload        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    occurred_at    timestamptz NOT NULL DEFAULT now(),
    dispatched_at  timestamptz,
    attempts       integer     NOT NULL DEFAULT 0
);

-- Partial index: only undelivered rows participate in the scan.
-- Covering (dispatched_at, occurred_at) lets the dispatcher read the
-- next batch ordered by occurred_at without a separate sort step.
CREATE INDEX outbox_backlog_idx
    ON outbox (dispatched_at, occurred_at)
    WHERE dispatched_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS outbox_backlog_idx;
DROP TABLE IF EXISTS outbox;

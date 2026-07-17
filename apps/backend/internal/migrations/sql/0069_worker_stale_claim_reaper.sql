-- +goose Up
-- =====================================================================
-- 0069_worker_stale_claim_reaper.sql
--
-- Adds a partial index that makes the stale-claim reaper query efficient.
--
-- The reaper scans worker_jobs WHERE status='claimed' AND claimed_at <
-- now()-interval. Without an index this is a full table scan; with a
-- partial index on (claimed_at) restricted to status='claimed' rows only
-- the scan touches at most the tiny fraction of rows that are in-flight,
-- which is O(workers) not O(all jobs).
-- =====================================================================

CREATE INDEX IF NOT EXISTS worker_jobs_stale_claimed_idx
    ON worker_jobs (claimed_at)
    WHERE status = 'claimed';

-- +goose Down
DROP INDEX IF EXISTS worker_jobs_stale_claimed_idx;

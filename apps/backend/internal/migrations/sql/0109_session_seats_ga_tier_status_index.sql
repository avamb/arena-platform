-- +goose Up
-- =====================================================================
-- arena_new — one index for every per-category walk over GA places
--
-- Found by the 2026-09-22 server load test (docs/loadtest/
-- 2026-09-22_server_step2_ru.md §3.1): the cost of the gateway's
-- RESERVATION / GET_CART / CREATE_ORDER_EXT and of the widget's hold grew
-- with the number of places in the category — 443 ms per RESERVATION on a
-- 60 000-place session against 56 ms on a 2 000-place one, on the same
-- server, and every millisecond of it spent while the sessions row lock
-- was held.
--
-- AllocateGAUnitsForHold (queries/session_seats.sql) and TakeGAUnitsForTier
-- (queries/ga_quota.sql) select
--
--     WHERE session_id = $1 AND kind = 'ga_unit' AND status = $s AND tier_id = $t
--     ORDER BY seat_key LIMIT $n FOR UPDATE SKIP LOCKED
--
-- The only index that matched the predicates, session_seats_ga_alloc_idx
-- (session_id, kind, status, tier_id), does not carry seat_key, so the
-- planner walked the session's (session_id, seat_key) unique index and
-- filtered every place of the session to find the first n available ones
-- of the tier: 2 130 buffers and 150 ms for three units on a 66 000-place
-- session in the local stand.
--
-- This index leads with the equality columns and ends with seat_key, so the
-- allocation becomes a bounded index walk in seat_key order (9 buffers,
-- 0.2 ms on the same session), and its partial predicate keeps seated
-- places out of it. The same index serves CountGAUnitsForTierByStatus, the
-- ga_units_available subquery of the gateway catalogue, and the
-- DeleteAvailableGAUnitsForTier guard (backward walk on seat_key DESC).
--
-- Plain CREATE INDEX on purpose: goose runs every migration in a
-- transaction and the production table holds a few thousand rows, so the
-- lock lasts milliseconds. A deployment with hundreds of thousands of
-- places should build it CONCURRENTLY by hand first; CREATE INDEX IF NOT
-- EXISTS then makes this migration a no-op.
-- =====================================================================

CREATE INDEX IF NOT EXISTS session_seats_ga_tier_status_idx
    ON session_seats (session_id, tier_id, status, seat_key)
    WHERE kind = 'ga_unit';

-- +goose Down
DROP INDEX IF EXISTS session_seats_ga_tier_status_idx;

-- session_seat_batches.sql — sqlc-style query definitions for the seat
-- batches of a session (task S1c, migration 0158). Companion hand-written
-- gen file: apps/backend/internal/adapters/postgres/gen/session_seat_batches.sql.go
--
-- Lock order of a batch (seatlayout.ApplyBatch / Revert, AGENTS.md):
--   1. IncrementSessionSeatStatusVersion (the sessions row, one bump);
--   2. LockSessionSeatsForBatch (FOR UPDATE in seat_key order — the order
--      CreateSeatedHold locks seats in);
--   3. ApplySessionSeatBatchUpdates, InsertSessionSeatBatch.
-- A revert also locks the original batch (GetSessionSeatBatchForUpdate)
-- right after step 1.

-- name: LockSessionSeatsForBatch :many
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind
FROM   session_seats
WHERE  session_id = $1
  AND  seat_key   = ANY($2::text[])
ORDER  BY seat_key ASC, id ASC
FOR UPDATE;

-- name: ListActiveSessionTierIDs :many
SELECT id FROM ticket_tiers
WHERE  session_id = $1 AND id = ANY($2::uuid[]) AND deleted_at IS NULL;

-- name: ApplySessionSeatBatchUpdates :execrows
UPDATE session_seats ss
SET    tier_id = v.tier_id::uuid, status = v.status, status_version = $2, updated_at = now()
FROM   unnest($3::text[], $4::text[], $5::text[]) AS v(id, tier_id, status)
WHERE  ss.id = v.id::uuid
  AND  ss.session_id = $1
  AND  (ss.kind = 'seat' OR ss.tier_id IS NOT DISTINCT FROM v.tier_id::uuid)
  AND  ss.status IN ('available', 'unavailable')
  AND  v.status  IN ('available', 'unavailable');

-- name: InsertSessionSeatBatch :one
INSERT INTO session_seat_batches
    (org_id, session_id, actor_id, actor_label, kind, seat_status_version,
     reverts_batch_id, changes, skipped)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb)
RETURNING *;

-- name: GetSessionSeatBatchForUpdate :one
SELECT * FROM session_seat_batches
WHERE  id = $1 AND session_id = $2 AND org_id = $3
FOR UPDATE;

-- name: MarkSessionSeatBatchReverted :execrows
UPDATE session_seat_batches
SET    reverted_at = $3, reverted_by_batch_id = $2
WHERE  id = $1 AND reverted_at IS NULL AND kind <> 'revert';

-- name: ListSessionSeatBatches :many
SELECT * FROM session_seat_batches
WHERE  session_id = $1 AND org_id = $2
ORDER  BY applied_at DESC, id DESC
LIMIT  $3 OFFSET $4;

-- name: CountSessionSeatBatches :one
SELECT count(*) FROM session_seat_batches WHERE session_id = $1 AND org_id = $2;

-- name: ListSeatBatchHistory :many
SELECT * FROM session_seat_batches
WHERE  session_id = $1 AND org_id = $2
  AND  (changes @> $3::jsonb OR skipped @> $3::jsonb)
ORDER  BY applied_at DESC, id DESC
LIMIT  $4 OFFSET $5;

-- name: CountSeatBatchHistory :one
SELECT count(*) FROM session_seat_batches
WHERE  session_id = $1 AND org_id = $2
  AND  (changes @> $3::jsonb OR skipped @> $3::jsonb);

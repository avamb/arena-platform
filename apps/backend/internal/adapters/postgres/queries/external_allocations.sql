-- external_allocations.sql — typed sqlc queries for the external_allocations table.
-- Feature #145 — Wave 10 External Allocations.
--
-- External allocations track quota blocks reserved for partner organisations
-- (resellers, distribution agents, box offices). The inventory_ledger is
-- modified atomically alongside allocation mutations:
--   • pending → active   : ReserveCapacity called (inventory held)
--   • active  → reconciled: ConfirmCapacity(consumed) + ReleaseCapacity(remainder)
--   • active  → disputed  : no inventory change (still held)
--   • disputed→ reconciled: ConfirmCapacity(consumed) + ReleaseCapacity(remainder)

-- name: InsertExternalAllocation :one
-- Creates a new external allocation in 'pending' status.
-- The caller is responsible for calling ReserveCapacity on the inventory_ledger
-- when transitioning to 'active'.
INSERT INTO external_allocations (
    session_id, partner_org_id, tier_id, quota_qty, status, notes
)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, session_id, partner_org_id, tier_id, quota_qty, quota_consumed,
          status, notes, created_at, updated_at;

-- name: GetExternalAllocationByID :one
-- Fetches a single external allocation by its UUID.
-- Returns pgx.ErrNoRows when not found.
SELECT id, session_id, partner_org_id, tier_id, quota_qty, quota_consumed,
       status, notes, created_at, updated_at
FROM   external_allocations
WHERE  id = $1;

-- name: ListExternalAllocationsBySession :many
-- Lists all external allocations for a session, ordered by creation time.
SELECT id, session_id, partner_org_id, tier_id, quota_qty, quota_consumed,
       status, notes, created_at, updated_at
FROM   external_allocations
WHERE  session_id = $1
ORDER BY created_at ASC;

-- name: ListExternalAllocationsByOrg :many
-- Lists all external allocations where the partner_org_id matches.
-- Optional status filter: pass NULL to list all statuses.
SELECT id, session_id, partner_org_id, tier_id, quota_qty, quota_consumed,
       status, notes, created_at, updated_at
FROM   external_allocations
WHERE  partner_org_id = $1
  AND  ($2::text IS NULL OR status = $2::text)
ORDER BY created_at DESC;

-- name: UpdateExternalAllocationStatus :one
-- Transitions the allocation to a new status.
-- Returns pgx.ErrNoRows when the allocation does not exist.
UPDATE external_allocations
SET    status     = $2,
       updated_at = now()
WHERE  id = $1
RETURNING id, session_id, partner_org_id, tier_id, quota_qty, quota_consumed,
          status, notes, created_at, updated_at;

-- name: ReportAllocationConsumption :one
-- Updates quota_consumed and optionally transitions to 'reconciled'.
-- Fails if new_consumed > quota_qty (DB CHECK constraint).
-- Returns pgx.ErrNoRows when the allocation does not exist.
UPDATE external_allocations
SET    quota_consumed = $2,
       status         = $3,
       updated_at     = now()
WHERE  id = $1
RETURNING id, session_id, partner_org_id, tier_id, quota_qty, quota_consumed,
          status, notes, created_at, updated_at;

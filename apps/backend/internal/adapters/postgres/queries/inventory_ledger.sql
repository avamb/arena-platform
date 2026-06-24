-- inventory_ledger.sql — typed sqlc queries for the inventory_ledger table.
-- Feature #130 — Wave 5 Inventory & Reservations.
--
-- All write operations (ReserveCapacity, ReleaseCapacity, ConfirmCapacity)
-- use SELECT ... FOR UPDATE inside a CTE to serialise concurrent access to
-- the same ledger row.  The conditional UPDATE returns the updated row on
-- success and zero rows when the capacity invariant would be violated
-- (application layer surfaces 409 Conflict).
--
-- tier_id is nullable: NULL represents the session-level aggregate entry.
-- The NULL-safe equality expression
--     (tier_id = $2::uuid OR (tier_id IS NULL AND $2::uuid IS NULL))
-- is used throughout so both session-level and tier-level rows can be
-- addressed with a single parameter set.

-- name: InsertInventoryLedger :one
-- Creates a new inventory ledger row for a session or a specific tier.
-- Pass tier_id = NULL for the session-level aggregate row.
-- capacity_total = NULL means unlimited availability.
INSERT INTO inventory_ledger (session_id, tier_id, capacity_total)
VALUES ($1, $2, $3)
RETURNING id, session_id, tier_id, capacity_total, capacity_held, capacity_sold,
          version, created_at, updated_at;

-- name: GetInventoryLedger :one
-- Fetches the ledger row for a (session, tier) pair.
-- Pass tier_id = NULL to get the session-level aggregate row.
-- Returns pgx.ErrNoRows when no matching row exists.
SELECT id, session_id, tier_id, capacity_total, capacity_held, capacity_sold,
       version, created_at, updated_at
FROM   inventory_ledger
WHERE  session_id = $1
  AND  (tier_id = $2::uuid OR (tier_id IS NULL AND $2::uuid IS NULL));

-- name: ListInventoryLedgersBySession :many
-- Lists all ledger rows for a session (aggregate row first, then tier rows).
SELECT id, session_id, tier_id, capacity_total, capacity_held, capacity_sold,
       version, created_at, updated_at
FROM   inventory_ledger
WHERE  session_id = $1
ORDER BY tier_id NULLS FIRST, id ASC;

-- name: ReserveCapacity :one
-- Atomically reserves `amount` units of capacity.
--
-- Locking: SELECT FOR UPDATE in the CTE pins the row before the conditional
-- UPDATE executes, preventing lost-update anomalies under concurrent load.
--
-- Returns the updated row on success.
-- Returns pgx.ErrNoRows when:
--   • the row does not exist, OR
--   • capacity_held + capacity_sold + amount > capacity_total (over-capacity).
WITH locked AS (
    SELECT id, capacity_total, capacity_held, capacity_sold
    FROM   inventory_ledger
    WHERE  session_id = $1
      AND  (tier_id = $2::uuid OR (tier_id IS NULL AND $2::uuid IS NULL))
    FOR UPDATE
)
UPDATE inventory_ledger il
SET    capacity_held = il.capacity_held + $3::integer,
       version       = il.version + 1,
       updated_at    = now()
FROM   locked
WHERE  il.id = locked.id
  AND  (locked.capacity_total IS NULL
        OR locked.capacity_held + locked.capacity_sold + $3::integer <= locked.capacity_total)
RETURNING il.id, il.session_id, il.tier_id, il.capacity_total,
          il.capacity_held, il.capacity_sold, il.version, il.created_at, il.updated_at;

-- name: ReleaseCapacity :one
-- Releases `amount` units back from held to available.
-- Fails (ErrNoRows) when held < amount (cannot release more than held).
WITH locked AS (
    SELECT id, capacity_held
    FROM   inventory_ledger
    WHERE  session_id = $1
      AND  (tier_id = $2::uuid OR (tier_id IS NULL AND $2::uuid IS NULL))
    FOR UPDATE
)
UPDATE inventory_ledger il
SET    capacity_held = il.capacity_held - $3::integer,
       version       = il.version + 1,
       updated_at    = now()
FROM   locked
WHERE  il.id = locked.id
  AND  locked.capacity_held >= $3::integer
RETURNING il.id, il.session_id, il.tier_id, il.capacity_total,
          il.capacity_held, il.capacity_sold, il.version, il.created_at, il.updated_at;

-- name: ConfirmCapacity :one
-- Moves `amount` units from held to sold (purchase confirmed).
-- Fails (ErrNoRows) when held < amount.
WITH locked AS (
    SELECT id, capacity_held
    FROM   inventory_ledger
    WHERE  session_id = $1
      AND  (tier_id = $2::uuid OR (tier_id IS NULL AND $2::uuid IS NULL))
    FOR UPDATE
)
UPDATE inventory_ledger il
SET    capacity_held = il.capacity_held - $3::integer,
       capacity_sold = il.capacity_sold + $3::integer,
       version       = il.version + 1,
       updated_at    = now()
FROM   locked
WHERE  il.id = locked.id
  AND  locked.capacity_held >= $3::integer
RETURNING il.id, il.session_id, il.tier_id, il.capacity_total,
          il.capacity_held, il.capacity_sold, il.version, il.created_at, il.updated_at;

-- name: RestoreSoldCapacity :one
-- Decrements capacity_sold by `amount`, moving units back to available.
-- This is the inverse of ConfirmCapacity and is used when a complimentary
-- issuance is revoked to restore the inventory capacity.
-- Fails (ErrNoRows) when:
--   • the row does not exist, OR
--   • capacity_sold < amount (cannot restore more than was sold).
WITH locked AS (
    SELECT id, capacity_sold
    FROM   inventory_ledger
    WHERE  session_id = $1
      AND  (tier_id = $2::uuid OR (tier_id IS NULL AND $2::uuid IS NULL))
    FOR UPDATE
)
UPDATE inventory_ledger il
SET    capacity_sold = il.capacity_sold - $3::integer,
       version       = il.version + 1,
       updated_at    = now()
FROM   locked
WHERE  il.id = locked.id
  AND  locked.capacity_sold >= $3::integer
RETURNING il.id, il.session_id, il.tier_id, il.capacity_total,
          il.capacity_held, il.capacity_sold, il.version, il.created_at, il.updated_at;

-- name: UpdateCapacityTotal :one
-- Propagates a capacity_total change from the session or tier.
-- Fails (ErrNoRows) when:
--   • no matching row exists, OR
--   • new_total < capacity_held + capacity_sold (invariant would be broken).
-- Pass NULL to remove the capacity ceiling (unlimited).
UPDATE inventory_ledger
SET    capacity_total = $3::integer,
       version        = version + 1,
       updated_at     = now()
WHERE  session_id = $1
  AND  (tier_id = $2::uuid OR (tier_id IS NULL AND $2::uuid IS NULL))
  AND  ($3::integer IS NULL OR $3::integer >= capacity_held + capacity_sold)
RETURNING id, session_id, tier_id, capacity_total, capacity_held, capacity_sold,
          version, created_at, updated_at;

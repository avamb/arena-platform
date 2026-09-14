-- ga_quota.sql — queries behind the single GA category quota mechanism
-- (plan 08_architecture/23 step 3, package
-- internal/platform/httpserver/gaquota). Companion hand-written gen file:
--   apps/backend/internal/adapters/postgres/gen/ga_quota.sql.go
--
-- Model (migration 0101): a General Admission category OWNS its places.
-- ticket_tiers.capacity is the quantity, the category's places are the
-- session_seats rows of kind='ga_unit' carrying its tier_id under the
-- 'ga|t<unit_seq>|<n>' key prefix, and the session capacity is always the
-- sum. Every mutation runs in the caller's transaction under the
-- platform-wide hold-mutation lock order: sessions row
-- (IncrementSessionSeatStatusVersion) FIRST, then inventory_ledger, then
-- the GA unit rows — and never a kind='seat' row after the ledger.

-- name: MaxTicketTierUnitSeq :one
-- Highest category number ever handed out on this session, INCLUDING
-- soft-deleted categories, so a number is never reused and a deleted
-- category's old seat keys can never be minted again. 0 when the session
-- has no numbered category yet.
SELECT COALESCE(MAX(unit_seq), 0)::int AS max_unit_seq
FROM   ticket_tiers
WHERE  session_id = $1;

-- name: AssignTicketTierUnitSeq :one
-- Assigns the category number, but only when it has none yet, so a retry
-- or a concurrent caller cannot renumber a category that already owns
-- places. Returns pgx.ErrNoRows when the row already has a number.
UPDATE ticket_tiers
SET    unit_seq   = $3::int,
       updated_at = now()
WHERE  id         = $1
  AND  session_id = $2
  AND  unit_seq IS NULL
RETURNING unit_seq;

-- name: SetTicketTierOpen :one
-- Opens or closes an active category. A closed category accepts no NEW
-- holds; places already held or sold are untouched.
UPDATE ticket_tiers
SET    is_open    = $3::boolean,
       updated_at = now()
WHERE  id         = $1
  AND  session_id = $2
  AND  deleted_at IS NULL
RETURNING is_open;

-- name: SetTicketTierCapacity :one
-- Writes the category quantity. The quota mechanism keeps this equal to
-- the number of places the category owns.
UPDATE ticket_tiers
SET    capacity   = $3::int,
       updated_at = now()
WHERE  id         = $1
  AND  session_id = $2
  AND  deleted_at IS NULL
RETURNING capacity;

-- name: MaxGAUnitIndexForTierPrefix :one
-- Highest <n> already used under a category's own key prefix
-- ('ga|t<unit_seq>'), so newly minted places continue the sequence
-- instead of colliding on UNIQUE (session_id, seat_key). 0 when none.
SELECT COALESCE(MAX(split_part(seat_key, '|', 3)::int), 0)::int AS max_index
FROM   session_seats
WHERE  session_id = $1
  AND  kind       = 'ga_unit'
  AND  seat_key LIKE $2 || '|%';

-- name: InsertGAUnitsForTier :execrows
-- Materializes quantity places for one category under its own key prefix
-- starting at start_index+1, stamped with the caller's freshly bumped
-- sessions.seat_status_version so seat-status delta readers see them.
INSERT INTO session_seats
    (session_id, seat_key, sector_name, row_name, seat_number,
     tier_id, status, kind, status_version)
SELECT $1,
       $2 || '|' || lpad((gs + $3)::text, 6, '0'),
       '', '', '',
       $4::uuid,
       'available',
       'ga_unit',
       $6::bigint
FROM generate_series(1, $5::int) gs;

-- name: CountDeletableGAUnitsForTier :one
-- How many of a category's places could actually be removed: available,
-- and referenced by neither reservation_seats nor order_items. Both FKs
-- point at session_seats WITHOUT a cascade (migrations 0058 / 0092) and a
-- converted reservation keeps its join rows, so a DELETE of a referenced
-- row fails with 23503 — the shrink path must know this number up front.
SELECT COUNT(*)::bigint AS count
FROM   session_seats ss
WHERE  ss.session_id = $1
  AND  ss.kind       = 'ga_unit'
  AND  ss.tier_id    = $2
  AND  ss.status     = 'available'
  AND  NOT EXISTS (SELECT 1 FROM reservation_seats rs
                    WHERE rs.session_seat_id = ss.id)
  AND  NOT EXISTS (SELECT 1 FROM order_items oi
                    WHERE oi.session_seat_id = ss.id);

-- name: DeleteAvailableGAUnitsForTier :execrows
-- Shrinks a category by removing its highest-keyed AVAILABLE places that
-- nothing references. Held and sold places are never touched. Returns the
-- number actually removed; the caller compares it with what it asked for.
DELETE FROM session_seats
WHERE id IN (
    SELECT ss.id
    FROM   session_seats ss
    WHERE  ss.session_id = $1
      AND  ss.kind       = 'ga_unit'
      AND  ss.tier_id    = $2
      AND  ss.status     = 'available'
      AND  NOT EXISTS (SELECT 1 FROM reservation_seats rs
                        WHERE rs.session_seat_id = ss.id)
      AND  NOT EXISTS (SELECT 1 FROM order_items oi
                        WHERE oi.session_seat_id = ss.id)
    ORDER  BY ss.seat_key DESC
    LIMIT  $3
);

-- name: ListGAUnitStatsBySession :many
-- Per-category place counters for one session: quantity owned, held,
-- sold and available. Feeds the quota mechanism's own guards and the
-- admin category table.
SELECT ss.tier_id,
       COUNT(*)::bigint                                              AS total,
       COUNT(*) FILTER (WHERE ss.status = 'held')::bigint            AS held,
       COUNT(*) FILTER (WHERE ss.status = 'sold')::bigint            AS sold,
       COUNT(*) FILTER (WHERE ss.status = 'available')::bigint       AS available
FROM   session_seats ss
WHERE  ss.session_id = $1
  AND  ss.kind       = 'ga_unit'
  AND  ss.tier_id IS NOT NULL
GROUP  BY ss.tier_id;

-- name: CountSeatRowsForTier :one
-- Number of coordinate-bearing seats (kind='seat') carrying this
-- category. Non-zero means a SEATED category: its quantity comes from the
-- plan geometry and the quota mechanism refuses to change or delete it.
SELECT COUNT(*)::bigint AS count
FROM   session_seats
WHERE  session_id = $1
  AND  tier_id    = $2
  AND  kind       = 'seat';

-- name: CountSessionPlaces :one
-- Every sellable place of a session: plan seats plus GA places. This is
-- what sessions.capacity_total and the session-level inventory_ledger row
-- are recomputed from after any quota mutation.
SELECT COUNT(*)::bigint AS count
FROM   session_seats
WHERE  session_id = $1
  AND  kind IN ('seat', 'ga_unit');

-- name: CountActiveTicketsForTier :one
-- Active tickets issued against a category — the delete guard: a category
-- that ever sold a ticket may only be closed, never removed.
SELECT COUNT(*)::bigint AS count
FROM   tickets
WHERE  session_id = $1
  AND  tier_id    = $2
  AND  status     = 'active';

-- name: SetSessionCapacityTotal :exec
-- Writes the recomputed session capacity. Unlike UpdateSession this is
-- keyed on the session alone (the quota mechanism never holds an
-- event_id) and touches nothing else.
UPDATE sessions
SET    capacity_total = $2::int,
       updated_at     = now()
WHERE  id             = $1
  AND  deleted_at     IS NULL;

-- name: SetSessionAdmissionMode :exec
-- Flips a session's admission mode without touching its plan binding.
-- Used by the quota mechanism for the one legal transition it performs:
-- assigned_seats -> hybrid when the first GA category is added to a
-- seated session (plan 08_architecture/23, decision 10). The reverse
-- transition is deliberately not performed anywhere.
UPDATE sessions
SET    admission_mode = $2::text,
       updated_at     = now()
WHERE  id             = $1
  AND  deleted_at     IS NULL;

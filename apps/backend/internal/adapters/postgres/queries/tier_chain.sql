-- tier_chain.sql — a category hands its free places to the next one when its
-- sale window closes (migration 0112, internal/platform/tierchain).

-- name: UpsertTierChain :exec
-- Sets (or replaces) the category a tier hands its places to. Changing the
-- target resets handed_over_at so the new target is opened on its first
-- hand-over.
INSERT INTO ticket_tier_chain (tier_id, next_tier_id)
VALUES ($1, $2)
ON CONFLICT (tier_id) DO UPDATE
SET    next_tier_id   = EXCLUDED.next_tier_id,
       handed_over_at = CASE WHEN ticket_tier_chain.next_tier_id = EXCLUDED.next_tier_id
                             THEN ticket_tier_chain.handed_over_at END,
       updated_at     = now();

-- name: DeleteTierChain :exec
DELETE FROM ticket_tier_chain WHERE tier_id = $1;

-- name: ListTierChainForSession :many
-- Every chain link of one session's live categories.
SELECT c.tier_id, c.next_tier_id, c.handed_over_at
FROM   ticket_tier_chain c
JOIN   ticket_tiers t ON t.id = c.tier_id
WHERE  t.session_id = $1
  AND  t.deleted_at IS NULL;

-- name: ListDueTierHandOvers :many
-- Links whose source category's sale window has closed and that still own a
-- free GA place nothing references through a live-or-converted hold — the
-- work of one sweep. Ordered by window end so a chain whose several windows
-- already passed cascades in one sweep (A->B is handed over before B->C).
SELECT t.session_id, c.tier_id, c.next_tier_id, c.handed_over_at
FROM   ticket_tier_chain c
JOIN   ticket_tiers t  ON t.id  = c.tier_id      AND t.deleted_at  IS NULL
JOIN   ticket_tiers nt ON nt.id = c.next_tier_id AND nt.deleted_at IS NULL
                      AND nt.session_id = t.session_id
WHERE  t.sale_window_end IS NOT NULL
  AND  t.sale_window_end <= now()
  AND  EXISTS (
         SELECT 1 FROM session_seats ss
         WHERE  ss.session_id = t.session_id
           AND  ss.tier_id    = t.id
           AND  ss.kind       = 'ga_unit'
           AND  ss.status     = 'available'
           AND  NOT EXISTS (SELECT 1 FROM reservation_seats rs WHERE rs.session_seat_id = ss.id))
ORDER  BY t.sale_window_end, t.sort_order
LIMIT  $1;

-- name: MoveFreeGAUnitsToTier :execrows
-- Re-assigns every free GA place of one category to another and re-keys it
-- under the target's prefix, numbering after start_index. The rows are kept
-- (not deleted and re-minted): order_items of an expired order may still
-- point at a free place, and DeleteAvailableGAUnitsForTier would have to
-- leave such a place behind for good. A place referenced by reservation_seats
-- (a live cart or a converted hold) never moves.
-- The caller holds the sessions row lock (the first statement of every hold
-- mutation), so no hold can take one of these places concurrently.
WITH free AS (
    SELECT ss.id, ss.seat_key
    FROM   session_seats ss
    WHERE  ss.session_id = $1
      AND  ss.tier_id    = $2
      AND  ss.kind       = 'ga_unit'
      AND  ss.status     = 'available'
      AND  NOT EXISTS (SELECT 1 FROM reservation_seats rs WHERE rs.session_seat_id = ss.id)
    FOR UPDATE
), moving AS (
    SELECT id, row_number() OVER (ORDER BY seat_key) AS rn FROM free
)
UPDATE session_seats ss
SET    tier_id        = $3,
       seat_key       = $4 || '|' || lpad((m.rn + $5)::text, 6, '0'),
       status_version = $6,
       updated_at     = now()
FROM   moving m
WHERE  ss.id = m.id;

-- name: SyncTierCapacityToPlaces :exec
-- ticket_tiers.capacity = the number of places the category owns (the quota
-- invariant). A category left with none gets NULL: the CHECK refuses 0 and
-- such a category is closed by the hand-over anyway.
UPDATE ticket_tiers t
SET    capacity   = NULLIF((SELECT COUNT(*) FROM session_seats ss
                             WHERE ss.session_id = t.session_id AND ss.tier_id = t.id
                               AND ss.kind IN ('seat', 'ga_unit')), 0),
       updated_at = now()
WHERE  t.id = $1 AND t.session_id = $2;

-- name: MarkTierChainHandedOver :exec
UPDATE ticket_tier_chain
SET    handed_over_at = now(), updated_at = now()
WHERE  tier_id = $1 AND handed_over_at IS NULL;

-- reservation_ga_items.sql — sqlc-style query definitions for the
-- reservation_ga_items table (migration 0063). Companion hand-written
-- gen file:
--   apps/backend/internal/adapters/postgres/gen/reservation_ga_items.sql.go
--
-- The table persists the general-admission portion of a reservation as
-- one row per tier so mixed (seats + GA) and multi-tier GA holds keep a
-- queryable breakdown past the hold transaction. Rows are written inside
-- the same transaction as the hold (public checkout start, WID-0c
-- recovery, Bil24 RESERVATION) and read by the anonymous order-status
-- endpoint (WID-0b) and the recovery / UN_RESERVE release paths.

-- name: InsertReservationGAItem :exec
-- Records one GA line (tier, quantity, unit-price snapshot) for a
-- reservation. Callers aggregate quantities per tier before the insert;
-- the composite PRIMARY KEY (reservation_id, tier_id) raises 23505 on a
-- duplicate tier, which the caller MUST treat as a programming error in
-- the aggregation step.
INSERT INTO reservation_ga_items (reservation_id, tier_id, quantity, unit_price)
VALUES ($1, $2, $3, $4);

-- name: ListReservationGAItems :many
-- Returns every GA line of a reservation joined with the owning
-- ticket_tiers row (display name + currency). Ordered by tier name then
-- tier id for deterministic status responses.
SELECT gi.reservation_id, gi.tier_id, gi.quantity, gi.unit_price,
       t.name AS tier_name, t.currency
FROM   reservation_ga_items gi
JOIN   ticket_tiers t ON t.id = gi.tier_id
WHERE  gi.reservation_id = $1
ORDER  BY t.name ASC, gi.tier_id ASC;

-- name: DeleteReservationGAItems :exec
-- Removes every GA line for a reservation. The FK already cascades on
-- reservation deletion; this exists for explicit cleanup paths that keep
-- the reservation row (none today — provided for symmetry with
-- DeleteReservationSeats).
DELETE FROM reservation_ga_items
WHERE  reservation_id = $1;

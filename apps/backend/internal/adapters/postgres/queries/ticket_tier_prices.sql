-- ticket_tier_prices.sql — AB-48 scheduled pricing windows.
-- Companion hand-written gen file: ../gen/ticket_tier_prices.sql.go
--
-- Window-overlap safety lives in the DB (GiST exclusion, 0087); the
-- resolution logic lives in internal/domain/pricing (the ONE resolver).

-- name: ListTierPriceWindows :many
-- Returns every price window for the given tiers, ordered by tier and
-- start. Surfaces feed the rows into pricing.Resolve.
SELECT id, tier_id, valid_from, valid_to, price_amount, created_at, updated_at
FROM   ticket_tier_prices
WHERE  tier_id = ANY($1::uuid[])
ORDER  BY tier_id, valid_from ASC;

-- name: InsertTierPriceWindow :one
-- Inserts one window. Overlaps with an existing window of the same tier
-- raise SQLSTATE 23P01 (exclusion_violation) — handlers map it to 422.
INSERT INTO ticket_tier_prices (tier_id, valid_from, valid_to, price_amount)
VALUES ($1, $2, $3, $4)
RETURNING id, tier_id, valid_from, valid_to, price_amount, created_at, updated_at;

-- name: DeleteTierPriceWindowsByTier :execrows
-- Wipes a tier's schedule; used by the replace-all PUT inside its
-- transaction before re-inserting the new set.
DELETE FROM ticket_tier_prices
WHERE  tier_id = $1;

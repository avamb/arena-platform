-- promo_codes.sql — promo code CRUD + validation queries (feature #128)

-- name: InsertPromoCode :one
INSERT INTO promo_codes (org_id, code, discount_type, discount_value, applies_to_tier_ids,
    max_uses, max_uses_per_customer, valid_from, valid_until, min_order_amount, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, COALESCE(NULLIF($11, ''), 'active'))
RETURNING id, org_id, code, discount_type, discount_value, applies_to_tier_ids,
          max_uses, max_uses_per_customer, valid_from, valid_until, min_order_amount,
          status, created_at, updated_at, deleted_at;

-- name: GetPromoCodeByID :one
SELECT id, org_id, code, discount_type, discount_value, applies_to_tier_ids,
       max_uses, max_uses_per_customer, valid_from, valid_until, min_order_amount,
       status, created_at, updated_at, deleted_at
FROM promo_codes
WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL;

-- name: GetPromoCodeByCode :one
-- Fetch by org_id + code string for validation.
SELECT id, org_id, code, discount_type, discount_value, applies_to_tier_ids,
       max_uses, max_uses_per_customer, valid_from, valid_until, min_order_amount,
       status, created_at, updated_at, deleted_at
FROM promo_codes
WHERE org_id = $1 AND code = $2 AND deleted_at IS NULL;

-- name: ListPromoCodesByOrg :many
SELECT id, org_id, code, discount_type, discount_value, applies_to_tier_ids,
       max_uses, max_uses_per_customer, valid_from, valid_until, min_order_amount,
       status, created_at, updated_at, deleted_at
FROM promo_codes
WHERE org_id = $1 AND deleted_at IS NULL
ORDER BY created_at DESC;

-- name: UpdatePromoCode :one
UPDATE promo_codes
SET discount_type         = COALESCE(NULLIF($3, ''), discount_type),
    discount_value        = CASE WHEN $4::bigint IS NOT NULL THEN $4::bigint ELSE discount_value END,
    applies_to_tier_ids   = CASE WHEN $5::uuid[] IS NOT NULL THEN $5::uuid[] ELSE applies_to_tier_ids END,
    max_uses              = CASE WHEN $6::integer IS NOT NULL THEN $6::integer ELSE max_uses END,
    max_uses_per_customer = CASE WHEN $7::integer IS NOT NULL THEN $7::integer ELSE max_uses_per_customer END,
    valid_from            = CASE WHEN $8::timestamptz IS NOT NULL THEN $8::timestamptz ELSE valid_from END,
    valid_until           = CASE WHEN $9::timestamptz IS NOT NULL THEN $9::timestamptz ELSE valid_until END,
    min_order_amount      = CASE WHEN $10::bigint IS NOT NULL THEN $10::bigint ELSE min_order_amount END,
    status                = COALESCE(NULLIF($11, ''), status),
    updated_at            = now()
WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL
RETURNING id, org_id, code, discount_type, discount_value, applies_to_tier_ids,
          max_uses, max_uses_per_customer, valid_from, valid_until, min_order_amount,
          status, created_at, updated_at, deleted_at;

-- name: SoftDeletePromoCode :one
UPDATE promo_codes
SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL
RETURNING id, org_id, code, discount_type, discount_value, applies_to_tier_ids,
          max_uses, max_uses_per_customer, valid_from, valid_until, min_order_amount,
          status, created_at, updated_at, deleted_at;

-- name: CountPromoCodeRedemptions :one
-- Count total redemptions for a promo code (for max_uses check).
SELECT COUNT(*)::int FROM promo_code_redemptions WHERE promo_code_id = $1;

-- name: CountUserRedemptions :one
-- Count redemptions for a specific user (for max_uses_per_customer check).
SELECT COUNT(*)::int FROM promo_code_redemptions WHERE promo_code_id = $1 AND user_id = $2;

-- name: InsertPromoCodeRedemption :one
INSERT INTO promo_code_redemptions (promo_code_id, user_id, reservation_id, discount_amount, order_amount)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, promo_code_id, user_id, reservation_id, redeemed_at, discount_amount, order_amount;

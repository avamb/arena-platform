-- promo_codes.sql — promo code CRUD + validation queries (feature #128;
-- session scope, currency and per-order usage since migration 0108)

-- name: InsertPromoCode :one
INSERT INTO promo_codes (org_id, code, discount_type, discount_value, applies_to_tier_ids,
    applies_to_session_ids, currency,
    max_uses, max_uses_per_customer, valid_from, valid_until, min_order_amount, status)
VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, $9, $10, $11, $12, COALESCE(NULLIF($13, ''), 'active'))
RETURNING id, org_id, code, discount_type, discount_value, applies_to_tier_ids,
          applies_to_session_ids, currency,
          max_uses, max_uses_per_customer, valid_from, valid_until, min_order_amount,
          status, created_at, updated_at, deleted_at;

-- name: GetPromoCodeByID :one
SELECT id, org_id, code, discount_type, discount_value, applies_to_tier_ids,
       applies_to_session_ids, currency,
       max_uses, max_uses_per_customer, valid_from, valid_until, min_order_amount,
       status, created_at, updated_at, deleted_at
FROM promo_codes
WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL;

-- name: GetPromoCodeByCode :one
-- Fetch by org_id + code string for validation.
SELECT id, org_id, code, discount_type, discount_value, applies_to_tier_ids,
       applies_to_session_ids, currency,
       max_uses, max_uses_per_customer, valid_from, valid_until, min_order_amount,
       status, created_at, updated_at, deleted_at
FROM promo_codes
WHERE org_id = $1 AND code = $2 AND deleted_at IS NULL;

-- name: GetPromoCodeByCodeCI :one
-- Fetch by org_id + code matched case-insensitively (Bil24 gateway, spec §7.6:
-- buyers type the code by hand in the WordPress checkout).
SELECT id, org_id, code, discount_type, discount_value, applies_to_tier_ids,
       applies_to_session_ids, currency,
       max_uses, max_uses_per_customer, valid_from, valid_until, min_order_amount,
       status, created_at, updated_at, deleted_at
FROM promo_codes
WHERE org_id = $1 AND lower(code) = lower($2) AND deleted_at IS NULL
ORDER BY created_at
LIMIT 1;

-- name: ListPromoCodesByOrg :many
SELECT id, org_id, code, discount_type, discount_value, applies_to_tier_ids,
       applies_to_session_ids, currency,
       max_uses, max_uses_per_customer, valid_from, valid_until, min_order_amount,
       status, created_at, updated_at, deleted_at
FROM promo_codes
WHERE org_id = $1 AND deleted_at IS NULL
ORDER BY created_at DESC;

-- name: UpdatePromoCode :one
-- currency: NULL keeps the stored value, '' clears it (any currency), a code sets it.
UPDATE promo_codes
SET discount_type          = COALESCE(NULLIF($3, ''), discount_type),
    discount_value         = CASE WHEN $4::bigint IS NOT NULL THEN $4::bigint ELSE discount_value END,
    applies_to_tier_ids    = CASE WHEN $5::uuid[] IS NOT NULL THEN $5::uuid[] ELSE applies_to_tier_ids END,
    applies_to_session_ids = CASE WHEN $6::uuid[] IS NOT NULL THEN $6::uuid[] ELSE applies_to_session_ids END,
    currency               = CASE WHEN $7::text IS NOT NULL THEN NULLIF($7::text, '') ELSE currency END,
    max_uses               = CASE WHEN $8::integer IS NOT NULL THEN $8::integer ELSE max_uses END,
    max_uses_per_customer  = CASE WHEN $9::integer IS NOT NULL THEN $9::integer ELSE max_uses_per_customer END,
    valid_from             = CASE WHEN $10::timestamptz IS NOT NULL THEN $10::timestamptz ELSE valid_from END,
    valid_until            = CASE WHEN $11::timestamptz IS NOT NULL THEN $11::timestamptz ELSE valid_until END,
    min_order_amount       = CASE WHEN $12::bigint IS NOT NULL THEN $12::bigint ELSE min_order_amount END,
    status                 = COALESCE(NULLIF($13, ''), status),
    updated_at             = now()
WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL
RETURNING id, org_id, code, discount_type, discount_value, applies_to_tier_ids,
          applies_to_session_ids, currency,
          max_uses, max_uses_per_customer, valid_from, valid_until, min_order_amount,
          status, created_at, updated_at, deleted_at;

-- name: SoftDeletePromoCode :one
UPDATE promo_codes
SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL
RETURNING id, org_id, code, discount_type, discount_value, applies_to_tier_ids,
          applies_to_session_ids, currency,
          max_uses, max_uses_per_customer, valid_from, valid_until, min_order_amount,
          status, created_at, updated_at, deleted_at;

-- name: CountPromoCodeRedemptions :one
-- Count total redemptions for a promo code (for max_uses check).
SELECT COUNT(*)::int FROM promo_code_redemptions WHERE promo_code_id = $1;

-- name: CountUserRedemptions :one
-- Count redemptions for a specific platform user (legacy REST checkout).
SELECT COUNT(*)::int FROM promo_code_redemptions WHERE promo_code_id = $1 AND user_id = $2;

-- name: CountPromoRedemptionsByCustomer :one
-- Count redemptions of one customer (max_uses_per_customer on the gateway,
-- where the buyer is known before the order exists).
SELECT COUNT(*)::int FROM promo_code_redemptions WHERE promo_code_id = $1 AND customer_id = $2;

-- name: CountPromoRedemptionsByBuyerEmail :one
-- Count redemptions whose order was bought under this e-mail
-- (max_uses_per_customer on the widget, where only the e-mail is known yet).
SELECT COUNT(*)::int
FROM promo_code_redemptions r
JOIN orders o ON o.id = r.order_id
WHERE r.promo_code_id = $1 AND lower(o.buyer_email) = lower($2);

-- name: InsertPromoCodeRedemption :exec
-- One redemption per order: a replayed PAY_ORDER or payment webhook is a no-op.
INSERT INTO promo_code_redemptions (promo_code_id, user_id, reservation_id, discount_amount, order_amount,
                                    order_id, customer_id, channel_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (order_id) WHERE order_id IS NOT NULL DO NOTHING;

-- name: GetPromoCodeByIDForUpdate :one
-- Lock the promo code row exclusively so concurrent checkout completions
-- serialise their redemption count-check and insert (feature #368 — PR2-12).
-- Must be called inside an explicit transaction; the lock is released at COMMIT/ROLLBACK.
SELECT id, org_id, code, discount_type, discount_value, applies_to_tier_ids,
       applies_to_session_ids, currency,
       max_uses, max_uses_per_customer, valid_from, valid_until, min_order_amount,
       status, created_at, updated_at, deleted_at
FROM promo_codes
WHERE id = $1 AND deleted_at IS NULL
FOR UPDATE;

-- name: ListPromoCodeUsageByOrg :many
-- Per-code usage for the organizer's list: how many times, how much, when last.
SELECT p.id                                   AS promo_code_id,
       count(r.id)::int                       AS uses,
       COALESCE(sum(r.discount_amount), 0)::bigint AS discount_total,
       max(r.redeemed_at)                     AS last_used_at
FROM promo_codes p
LEFT JOIN promo_code_redemptions r ON r.promo_code_id = p.id
WHERE p.org_id = $1 AND p.deleted_at IS NULL
GROUP BY p.id;

-- name: ListPromoCodeRedemptionsByOrg :many
-- The usage report: every redemption of the organization's codes, newest
-- first, with the order it paid for. $2 narrows it to one code; NULL means all.
SELECT r.id, r.promo_code_id, p.code, r.redeemed_at, r.discount_amount, r.order_amount,
       r.order_id, o.system_id AS order_number, o.status AS order_status, o.currency,
       o.buyer_email, o.session_id, r.channel_id, c.name AS channel_name
FROM promo_code_redemptions r
JOIN promo_codes p ON p.id = r.promo_code_id
LEFT JOIN orders o         ON o.id = r.order_id
LEFT JOIN sales_channels c ON c.id = r.channel_id
WHERE p.org_id = $1
  AND ($2::uuid IS NULL OR r.promo_code_id = $2::uuid)
ORDER BY r.redeemed_at DESC;

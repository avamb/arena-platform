-- referral.sql — "Refer a friend" (spec 52, migration 0195). The Go wrappers
-- live in gen/referral.sql.go (hand-maintained); the payout and sweep SQL is
-- in internal/platform/referral.

-- name: GetReferralProgramByID :one
SELECT * FROM referral_programs WHERE id = $1;

-- name: GetActiveReferralProgramForEvent :one
SELECT * FROM referral_programs
WHERE scope = 'event' AND event_id = $1 AND status = 'active'
  AND (starts_at IS NULL OR starts_at <= $2)
  AND (ends_at IS NULL OR ends_at > $2);

-- name: GetReferralCodeByReferrerOrder :one
SELECT pc.id, pc.code, pc.referral_program_id, pc.discount_value, p.cashback_pct, pc.status
FROM promo_codes pc JOIN referral_programs p ON p.id = pc.referral_program_id
WHERE pc.referrer_order_id = $1;

-- name: InsertReferralPromoCode :one
INSERT INTO promo_codes (org_id, code, discount_type, discount_value, applies_to_session_ids,
    max_uses, max_uses_per_customer, valid_until, status, origin, referral_program_id, referrer_order_id)
VALUES ($1, $2, 'percent', $3, $4, $5, 1, $6, 'active', 'referral', $7, $8)
ON CONFLICT DO NOTHING
RETURNING id;

-- name: InsertReferralReward :one
INSERT INTO referral_rewards (org_id, program_id, promo_code_id, referrer_order_id, friend_order_id,
    amount_minor, currency, cashback_pct, cap_pct, state, reason)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, ''))
ON CONFLICT (friend_order_id) DO NOTHING
RETURNING id;

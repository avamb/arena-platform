-- order_referral.sql — what the order card says about "Refer a friend"
-- (spec 52): the cashback a referrer's order earned, and the personal code a
-- friend's order used. Read-only; carries order numbers and amounts, no buyer
-- data. Every query is scoped to the organization.

-- name: ListOrderReferralCashback :many
-- One row per friend order that paid off on this (the referrer's) order.
-- paid_at is the latest succeeded cashback refund of the reward (its own
-- refund, or any refund of its batch); NULL until the money went back.
SELECT rr.id, rr.friend_order_id, fo.system_id AS friend_order_number,
       rr.state, rr.amount_minor, rr.currency, rr.friend_reversal,
       (SELECT max(r.succeeded_at)
          FROM refunds r
         WHERE r.origin = 'referral_cashback'
           AND r.state  = 'succeeded'
           AND (r.id = rr.refund_id
                OR (rr.refund_batch_id IS NOT NULL AND r.batch_id = rr.refund_batch_id))) AS paid_at,
       rr.created_at
FROM      referral_rewards rr
LEFT JOIN orders fo ON fo.id = rr.friend_order_id
WHERE  rr.referrer_order_id = $1
  AND  rr.org_id            = $2
ORDER  BY rr.created_at, rr.id;

-- name: GetOrderReferralCode :one
-- The personal "Refer a friend" code this order used, with the referrer's
-- order it belongs to. pgx.ErrNoRows when the order used none.
SELECT p.code, p.referrer_order_id, ro.system_id AS referrer_order_number
FROM      promo_codes p
JOIN      orders o  ON o.promo_code_id = p.id
LEFT JOIN orders ro ON ro.id = p.referrer_order_id
WHERE  o.id     = $1
  AND  o.org_id = $2
  AND  p.org_id = $2
  AND  p.origin = 'referral';

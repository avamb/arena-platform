// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: promo_codes.sql (LockPromoCodeUses, CountPromoCodeUsesInFlight)

package gen

import (
	"context"

	"github.com/google/uuid"
)

const lockPromoCodeUses = `-- name: LockPromoCodeUses :exec
SELECT pg_advisory_xact_lock(hashtextextended('promo_code_uses:' || $1::text, 0))`

// LockPromoCodeUses serializes every reservation of one code's max_uses and
// max_uses_per_customer (P0-1 defect D-02). The lock is held until the
// transaction ends, so a second checkout counts the first one's order once
// it has committed.
func (q *Queries) LockPromoCodeUses(ctx context.Context, promoCodeID uuid.UUID) error {
	_, err := q.db.Exec(ctx, lockPromoCodeUses, promoCodeID)
	return err
}

const countPromoCodeUsesInFlight = `-- name: CountPromoCodeUsesInFlight :one
WITH pc AS (
    SELECT id, org_id, max_uses, max_uses_per_customer
    FROM   promo_codes
    WHERE  id = $1 AND org_id = $2
), taken AS (
    SELECT o.customer_id, o.buyer_email
    FROM   orders o, pc
    WHERE  o.org_id = pc.org_id AND o.promo_code_id = pc.id
      AND  NOT EXISTS (SELECT 1 FROM promo_code_redemptions r WHERE r.order_id = o.id)
      AND  (o.status IN ('paid', 'partially_refunded', 'refunded', 'manual_review')
            OR (o.status = 'pending_payment' AND o.expires_at > now()
                AND o.reservation_id IS DISTINCT FROM $3::uuid
                AND NOT (o.session_id IS NOT DISTINCT FROM $4::uuid
                         AND (COALESCE(o.customer_id = $5::uuid, false)
                              OR COALESCE($6::text <> '' AND lower(o.buyer_email) = lower($6::text), false)))))
), redeemed AS (
    SELECT r.customer_id AS redemption_customer_id, o.customer_id, o.buyer_email
    FROM   promo_code_redemptions r
    JOIN   pc ON pc.id = r.promo_code_id
    LEFT JOIN orders o ON o.id = r.order_id
)
SELECT pc.max_uses, pc.max_uses_per_customer,
       ((SELECT COUNT(*) FROM redeemed) + (SELECT COUNT(*) FROM taken))::int AS used,
       ((SELECT COUNT(*) FROM redeemed x
          WHERE COALESCE(x.redemption_customer_id = $5::uuid OR x.customer_id = $5::uuid, false)
             OR COALESCE($6::text <> '' AND lower(x.buyer_email) = lower($6::text), false))
        + (SELECT COUNT(*) FROM taken x
            WHERE COALESCE(x.customer_id = $5::uuid, false)
               OR COALESCE($6::text <> '' AND lower(x.buyer_email) = lower($6::text), false))
       )::int AS mine
FROM pc`

// PromoCodeUsesInFlight is one code's limits and the uses already taken.
type PromoCodeUsesInFlight struct {
	MaxUses            *int32 // nil = unlimited
	MaxUsesPerCustomer *int32 // nil = unlimited
	Used               int32  // every buyer
	Mine               int32  // this buyer (customer or e-mail)
}

// CountPromoCodeUsesInFlight returns the code's limits and the uses already
// taken: recorded redemptions plus orders that carry the code without a
// redemption and were paid or still hold their places. Used counts every
// buyer, Mine the buyer named by customerID or buyerEmail (either matches).
// A live unpaid order of reservationID, and one of the same buyer on
// sessionID, are not counted. pgx.ErrNoRows when the code is not the
// organization's.
func (q *Queries) CountPromoCodeUsesInFlight(
	ctx context.Context,
	promoCodeID, orgID uuid.UUID,
	reservationID, sessionID, customerID *uuid.UUID,
	buyerEmail string,
) (PromoCodeUsesInFlight, error) {
	var out PromoCodeUsesInFlight
	row := q.db.QueryRow(ctx, countPromoCodeUsesInFlight,
		promoCodeID, orgID, reservationID, sessionID, customerID, buyerEmail)
	err := row.Scan(&out.MaxUses, &out.MaxUsesPerCustomer, &out.Used, &out.Mine)
	return out, err
}

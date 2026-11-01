// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: promo_codes.sql (LockPromoCodeUses, CountPromoCodeUsesInFlight)

package gen

import (
	"context"

	"github.com/google/uuid"
)

const lockPromoCodeUses = `-- name: LockPromoCodeUses :exec
SELECT pg_advisory_xact_lock(hashtextextended('promo_code_uses:' || $1::text, 0))`

// LockPromoCodeUses serializes every reservation of one code's max_uses
// (P0-1 defect D-02). The lock is held until the transaction ends, so a
// second checkout counts the first one's order once it has committed.
func (q *Queries) LockPromoCodeUses(ctx context.Context, promoCodeID uuid.UUID) error {
	_, err := q.db.Exec(ctx, lockPromoCodeUses, promoCodeID)
	return err
}

const countPromoCodeUsesInFlight = `-- name: CountPromoCodeUsesInFlight :one
SELECT pc.max_uses,
       ((SELECT COUNT(*) FROM promo_code_redemptions r WHERE r.promo_code_id = pc.id)
        + (SELECT COUNT(*) FROM orders o
            WHERE o.org_id = pc.org_id AND o.promo_code_id = pc.id
              AND NOT EXISTS (SELECT 1 FROM promo_code_redemptions r WHERE r.order_id = o.id)
              AND (o.status IN ('paid', 'partially_refunded', 'refunded', 'manual_review')
                   OR (o.status = 'pending_payment' AND o.expires_at > now()
                       AND o.reservation_id IS DISTINCT FROM $3::uuid
                       AND NOT (($4::uuid IS NOT NULL AND o.customer_id = $4::uuid)
                                OR ($5::text <> '' AND lower(o.buyer_email) = lower($5::text))))))
       )::int AS used
FROM promo_codes pc
WHERE pc.id = $1 AND pc.org_id = $2`

// CountPromoCodeUsesInFlight returns max_uses of the code (nil = unlimited)
// and the uses already taken: recorded redemptions plus orders that carry
// the code without a redemption and were paid or still hold their places.
// A live unpaid order of reservationID, customerID or buyerEmail is not
// counted. pgx.ErrNoRows when the code is not the organization's.
func (q *Queries) CountPromoCodeUsesInFlight(
	ctx context.Context,
	promoCodeID, orgID uuid.UUID,
	reservationID, customerID *uuid.UUID,
	buyerEmail string,
) (maxUses *int32, used int32, err error) {
	row := q.db.QueryRow(ctx, countPromoCodeUsesInFlight, promoCodeID, orgID, reservationID, customerID, buyerEmail)
	err = row.Scan(&maxUses, &used)
	return maxUses, used, err
}

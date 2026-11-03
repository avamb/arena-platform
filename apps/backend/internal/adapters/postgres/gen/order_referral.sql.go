// Hand-maintained typed query wrapper; follows sqlc output conventions.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const listOrderReferralCashback = `-- name: ListOrderReferralCashback :many
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
ORDER  BY rr.created_at, rr.id`

// OrderReferralCashbackRow is one friend order's cashback on a referrer's
// order. AmountMinor is in minor units of Currency; PaidAt is set once a
// cashback refund of the reward succeeded.
type OrderReferralCashbackRow struct {
	ID                uuid.UUID  `json:"id"`
	FriendOrderID     *uuid.UUID `json:"friend_order_id"`
	FriendOrderNumber *int64     `json:"friend_order_number"`
	State             string     `json:"state"`
	AmountMinor       int64      `json:"amount_minor"`
	Currency          string     `json:"currency"`
	FriendReversal    *string    `json:"friend_reversal"`
	PaidAt            *time.Time `json:"paid_at"`
	CreatedAt         time.Time  `json:"created_at"`
}

// ListOrderReferralCashback returns the "Refer a friend" rewards a referrer's
// order earned, oldest first, scoped to the organization.
func (q *Queries) ListOrderReferralCashback(ctx context.Context, orderID, orgID uuid.UUID) ([]OrderReferralCashbackRow, error) {
	rows, err := q.db.Query(ctx, listOrderReferralCashback, orderID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []OrderReferralCashbackRow
	for rows.Next() {
		var i OrderReferralCashbackRow
		if err := rows.Scan(&i.ID, &i.FriendOrderID, &i.FriendOrderNumber, &i.State, &i.AmountMinor,
			&i.Currency, &i.FriendReversal, &i.PaidAt, &i.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

const getOrderReferralCode = `-- name: GetOrderReferralCode :one
SELECT p.code, p.referrer_order_id, ro.system_id AS referrer_order_number
FROM      promo_codes p
JOIN      orders o  ON o.promo_code_id = p.id
LEFT JOIN orders ro ON ro.id = p.referrer_order_id
WHERE  o.id     = $1
  AND  o.org_id = $2
  AND  p.org_id = $2
  AND  p.origin = 'referral'`

// OrderReferralCodeRow is the personal code a friend's order used and the
// referrer's order behind it.
type OrderReferralCodeRow struct {
	Code                string     `json:"code"`
	ReferrerOrderID     *uuid.UUID `json:"referrer_order_id"`
	ReferrerOrderNumber *int64     `json:"referrer_order_number"`
}

// GetOrderReferralCode returns the personal "Refer a friend" code an order
// used; pgx.ErrNoRows when it used none.
func (q *Queries) GetOrderReferralCode(ctx context.Context, orderID, orgID uuid.UUID) (OrderReferralCodeRow, error) {
	var i OrderReferralCodeRow
	err := q.db.QueryRow(ctx, getOrderReferralCode, orderID, orgID).Scan(&i.Code, &i.ReferrerOrderID, &i.ReferrerOrderNumber)
	return i, err
}

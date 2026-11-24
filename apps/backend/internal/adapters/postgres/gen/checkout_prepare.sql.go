// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: checkout_prepare.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

const getCheckoutFeedContext = `-- name: GetCheckoutFeedContext :one
SELECT ft.token, o.event_id
FROM   checkout_sessions cs
JOIN   orders o              ON o.checkout_session_id = cs.id
JOIN   agent_feed_tokens ft  ON ft.sales_channel_id = cs.channel_id
JOIN   event_publications ep ON ep.feed_token_id = ft.id AND ep.event_id = o.event_id
WHERE  cs.id = $1
  AND  ft.is_active = true
ORDER  BY ft.created_at, ft.id
LIMIT  1`

// CheckoutFeedContextRow is an active feed token of a checkout's channel that
// publishes the order's event, and that event.
type CheckoutFeedContextRow struct {
	FeedToken string    `json:"feed_token"`
	EventID   uuid.UUID `json:"event_id"`
}

// GetCheckoutFeedContext names an active feed token of the checkout's sales
// channel that publishes the order's event (spec 65 prepared order: the attach
// route reads the buyer-field flags and consent texts through it). Returns
// pgx.ErrNoRows when there is none.
func (q *Queries) GetCheckoutFeedContext(ctx context.Context, checkoutSessionID uuid.UUID) (CheckoutFeedContextRow, error) {
	var r CheckoutFeedContextRow
	err := q.db.QueryRow(ctx, getCheckoutFeedContext, checkoutSessionID).Scan(&r.FeedToken, &r.EventID)
	return r, err
}

const attachOrderBuyer = `-- name: AttachOrderBuyer :execrows
UPDATE orders
SET    buyer_email = $2,
       buyer_name  = COALESCE($3, buyer_name),
       buyer_phone = COALESCE($4, buyer_phone),
       customer_id = COALESCE($5, customer_id),
       updated_at  = now()
WHERE  id = $1
  AND  buyer_email IS NULL`

// AttachOrderBuyer writes the buyer onto a prepared order that has none yet.
// A nil name, phone or customer keeps what the order holds. Reports whether a
// row changed (false: the order already has a buyer).
func (q *Queries) AttachOrderBuyer(ctx context.Context, orderID uuid.UUID, email string, name, phone *string, customerID *uuid.UUID) (bool, error) {
	tag, err := q.db.Exec(ctx, attachOrderBuyer, orderID, email, name, phone, customerID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

const setCheckoutBuyerLocale = `-- name: SetCheckoutBuyerLocale :exec
UPDATE checkout_sessions
SET    buyer_locale = $2
WHERE  id = $1`

// SetCheckoutBuyerLocale records the language the buyer attached in.
func (q *Queries) SetCheckoutBuyerLocale(ctx context.Context, checkoutSessionID uuid.UUID, locale string) error {
	_, err := q.db.Exec(ctx, setCheckoutBuyerLocale, checkoutSessionID, locale)
	return err
}

const fillOrderBuyerEmail = `-- name: FillOrderBuyerEmail :execrows
UPDATE orders
SET    buyer_email = $2,
       updated_at  = now()
WHERE  checkout_session_id = $1
  AND  buyer_email IS NULL`

// FillOrderBuyerEmail gives the order of a paid checkout the e-mail address
// the payment provider collected when the order has none. Reports whether a
// row changed.
func (q *Queries) FillOrderBuyerEmail(ctx context.Context, checkoutSessionID uuid.UUID, email string) (bool, error) {
	tag, err := q.db.Exec(ctx, fillOrderBuyerEmail, checkoutSessionID, email)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

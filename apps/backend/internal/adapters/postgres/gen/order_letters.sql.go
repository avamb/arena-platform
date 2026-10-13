package gen

// order_letters.sql.go — the letters an order sent to its buyer, as the
// event center shows them: the address each ticket letter went to, the
// refund letter of the order's refund operations, and the folded ticket
// letter state of a page of orders (the list's warning mark).

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const listOrderDeliveryRecipients = `-- name: ListOrderDeliveryRecipients :many
SELECT dj.ticket_id, dj.recipient_email
FROM   delivery_jobs dj
JOIN   order_items oi ON oi.ticket_id = dj.ticket_id
WHERE  oi.order_id = $1`

// OrderDeliveryRecipientRow is the address one ticket's letter goes to.
type OrderDeliveryRecipientRow struct {
	TicketID       uuid.UUID `json:"ticket_id"`
	RecipientEmail *string   `json:"recipient_email"`
}

// ListOrderDeliveryRecipients lists the address of every ticket letter of
// an order (a resend to another address changes it).
func (q *Queries) ListOrderDeliveryRecipients(ctx context.Context, orderID uuid.UUID) ([]OrderDeliveryRecipientRow, error) {
	rows, err := q.db.Query(ctx, listOrderDeliveryRecipients, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrderDeliveryRecipientRow
	for rows.Next() {
		var r OrderDeliveryRecipientRow
		if err := rows.Scan(&r.TicketID, &r.RecipientEmail); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const getOrderRefundLetter = `-- name: GetOrderRefundLetter :one
SELECT count(*)                                                                   AS refunds,
       count(*) FILTER (WHERE b.notify_buyer)                                     AS notify,
       count(*) FILTER (WHERE b.notify_buyer AND r.buyer_notified_at IS NOT NULL) AS notified,
       count(*) FILTER (WHERE b.notify_buyer AND r.buyer_notice_given_up_at IS NOT NULL
                          AND r.buyer_notified_at IS NULL)                        AS given_up,
       max(r.buyer_notified_at) FILTER (WHERE b.notify_buyer)                     AS last_notified,
       max(r.buyer_notice_given_up_at) FILTER (WHERE b.notify_buyer)              AS last_given_up
FROM   refunds r
JOIN   refund_batches b ON b.id = r.batch_id
WHERE  b.order_id = $1
  AND  r.state NOT IN ('failed', 'rejected')`

// OrderRefundLetterRow counts the order's live refunds of refund operations
// (refund_batches) by what their buyer letter came to.
type OrderRefundLetterRow struct {
	Refunds      int64      `json:"refunds"`
	Notify       int64      `json:"notify"`
	Notified     int64      `json:"notified"`
	GivenUp      int64      `json:"given_up"`
	LastNotified *time.Time `json:"last_notified"`
	LastGivenUp  *time.Time `json:"last_given_up"`
}

// GetOrderRefundLetter folds the refund letter of an order. A refund that
// failed or was rejected moved no money and is never mailed, so it is left
// out; a refund without a batch (the flat routes) never sends a letter.
func (q *Queries) GetOrderRefundLetter(ctx context.Context, orderID uuid.UUID) (OrderRefundLetterRow, error) {
	var r OrderRefundLetterRow
	err := q.db.QueryRow(ctx, getOrderRefundLetter, orderID).Scan(
		&r.Refunds, &r.Notify, &r.Notified, &r.GivenUp, &r.LastNotified, &r.LastGivenUp)
	return r, err
}

const listOrdersDeliveryCounts = `-- name: ListOrdersDeliveryCounts :many
SELECT oi.order_id,
       count(*)                                                       AS tickets,
       count(*) FILTER (WHERE dj.status = 'sent')                     AS sent,
       count(*) FILTER (WHERE dj.status IN ('pending', 'processing')) AS pending,
       count(*) FILTER (WHERE dj.status = 'failed')                   AS failed
FROM   order_items oi
JOIN   tickets t ON t.id = oi.ticket_id
LEFT JOIN delivery_jobs dj ON dj.ticket_id = t.id
WHERE  oi.order_id = ANY($1::uuid[])
GROUP  BY oi.order_id`

// OrderDeliveryCountsRow counts one order's issued tickets by the state of
// their letter.
type OrderDeliveryCountsRow struct {
	OrderID uuid.UUID `json:"order_id"`
	Tickets int64     `json:"tickets"`
	Sent    int64     `json:"sent"`
	Pending int64     `json:"pending"`
	Failed  int64     `json:"failed"`
}

// ListOrdersDeliveryCounts counts the ticket letters of a page of orders in
// one round trip.
func (q *Queries) ListOrdersDeliveryCounts(ctx context.Context, orderIDs []uuid.UUID) ([]OrderDeliveryCountsRow, error) {
	rows, err := q.db.Query(ctx, listOrdersDeliveryCounts, orderIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrderDeliveryCountsRow
	for rows.Next() {
		var r OrderDeliveryCountsRow
		if err := rows.Scan(&r.OrderID, &r.Tickets, &r.Sent, &r.Pending, &r.Failed); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

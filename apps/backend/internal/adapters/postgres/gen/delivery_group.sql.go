// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: delivery_jobs.sql (ClaimPendingDeliveryJobsForOrder,
// GetDeliveryPaymentSummaryByTicketID)

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// ClaimPendingDeliveryJobsForOrder
// ─────────────────────────────────────────────────────────────────────────────

const claimPendingDeliveryJobsForOrder = `-- name: ClaimPendingDeliveryJobsForOrder :many
UPDATE delivery_jobs dj
SET    status        = 'processing',
       processing_at = now(),
       updated_at    = now()
FROM   delivery_jobs own
LEFT JOIN tickets own_t ON own_t.id = own.ticket_id
WHERE  own.ticket_id = $1
  AND  own.status = 'pending'
  AND  dj.status  = 'pending'
  AND  ( dj.ticket_id = own.ticket_id
         OR ( own.recipient_email IS NOT NULL
              AND dj.recipient_email = own.recipient_email
              AND own_t.order_id IS NOT NULL
              AND dj.ticket_id IN (SELECT id FROM tickets WHERE order_id = own_t.order_id) ) )
RETURNING dj.id, dj.ticket_id, dj.recipient_email, dj.status, dj.attempts, dj.last_error,
          dj.queued_at, dj.sent_at, dj.processing_at, dj.created_at, dj.updated_at`

// ClaimPendingDeliveryJobsForOrder atomically moves the delivery job of
// ticketID AND every other pending job of the same order and the same
// recipient from 'pending' to 'processing', so one e-mail can carry all the
// tickets of an order. The whole claim is ONE statement: of several worker
// jobs that start together for the tickets of one order, exactly one gets
// the rows and the others get none, which the caller treats as "already
// taken" — the same idempotent skip ClaimDeliveryJobForProcessing gives.
//
// It returns nothing when ticketID's own job is not pending, so a retry or a
// duplicate never claims siblings on behalf of a job somebody else owns. A
// ticket without an order, or a job without a recipient, claims only itself.
func (q *Queries) ClaimPendingDeliveryJobsForOrder(
	ctx context.Context,
	ticketID uuid.UUID,
) ([]DeliveryJobRow, error) {
	rows, err := q.db.Query(ctx, claimPendingDeliveryJobsForOrder, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []DeliveryJobRow
	for rows.Next() {
		r, err := scanDeliveryJobRow(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, r)
	}
	return jobs, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// GetDeliveryPaymentSummaryByTicketID
// ─────────────────────────────────────────────────────────────────────────────

// DeliveryPaymentSummary is what the ticket e-mail's payment block says about
// the purchase a ticket belongs to. All amounts are minor units in Currency.
// The order columns are nil when the checkout has no order row.
type DeliveryPaymentSummary struct {
	OrderNumber     *int64
	OrderStatus     *string
	OrderSource     *string
	PaidAt          *time.Time
	Currency        string
	Subtotal        int64
	Discount        int64
	PlatformFee     int64
	Total           int64
	PaymentProvider *string
	// TicketCount is how many tickets the checkout issued, whatever their
	// status. The payment block is printed only when the e-mail carries all of
	// them, so a resend of one ticket never shows the whole order's money.
	TicketCount int64
}

const getDeliveryPaymentSummaryByTicketID = `-- name: GetDeliveryPaymentSummaryByTicketID :one
SELECT o.system_id, o.status, o.source, o.paid_at,
       COALESCE(cs.currency, ''), COALESCE(cs.subtotal, 0), COALESCE(cs.discount, 0),
       COALESCE(cs.platform_fee, 0), COALESCE(cs.total, 0), cs.payment_provider,
       (SELECT count(*) FROM tickets x WHERE x.checkout_session_id = t.checkout_session_id)
FROM   tickets t
JOIN   checkout_sessions cs ON cs.id = t.checkout_session_id
LEFT JOIN orders o ON o.checkout_session_id = cs.id
WHERE  t.id = $1
LIMIT  1`

// GetDeliveryPaymentSummaryByTicketID reads the money of the purchase a ticket
// was issued for, for the "Payment" block of the ticket e-mail. Returns
// pgx.ErrNoRows when the ticket has no checkout session.
func (q *Queries) GetDeliveryPaymentSummaryByTicketID(
	ctx context.Context,
	ticketID uuid.UUID,
) (DeliveryPaymentSummary, error) {
	var r DeliveryPaymentSummary
	err := q.db.QueryRow(ctx, getDeliveryPaymentSummaryByTicketID, ticketID).Scan(
		&r.OrderNumber,
		&r.OrderStatus,
		&r.OrderSource,
		&r.PaidAt,
		&r.Currency,
		&r.Subtotal,
		&r.Discount,
		&r.PlatformFee,
		&r.Total,
		&r.PaymentProvider,
		&r.TicketCount,
	)
	return r, err
}

package refunds

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// TicketOption is one ticket of an order as a refund screen needs it.
type TicketOption struct {
	TicketID uuid.UUID
	Status   string
	// Paid is what the buyer paid for this ticket (order_items.total), nil
	// when the order item carries no price.
	Paid *int64
	// Refunded counts the live refunds against the ticket (provider_pending
	// included, failed excluded), Refundable is what is left to return.
	Refunded   int64
	Refundable int64
	// InProgress: a live refund that cancels this ticket already exists.
	InProgress bool
	// TicketNumber is the number printed on the ticket (system_ticket_id) and
	// SeatLabel the place ("A / 3 / 12"), nil for general admission.
	TicketNumber *int64
	SeatLabel    *string
	// NotRefundableReason says why Refundable is 0 for an active ticket that
	// still has money left ("review_hold": a refund made outside arena put
	// the order's tickets under review). Empty otherwise.
	NotRefundableReason string
}

// OrderOptions is the answer of GET .../orders/{order_id}/refund-options.
type OrderOptions struct {
	OrderID  uuid.UUID
	Route    Route
	Provider string
	Currency string
	// PartialRefund: the provider can return less than the whole payment.
	PartialRefund bool
	// PaymentLeft is what is left of the whole payment. Each ticket's
	// Refundable is capped by it on its own, so the tickets together may
	// offer more than this; no refund operation can return more.
	PaymentLeft int64
	// Refusal explains a route the engine cannot drive (nil when it can).
	Refusal *Error
	Tickets []TicketOption
	Active  []Refund
}

// RouteNone is reported for an order arena took no money for.
const RouteNone Route = "none"

// Options reads what can be refunded on an order (spec 36 section 8). It
// writes nothing and takes no lock: the answer is advice, CreateBatch
// re-checks every rule under the payment lock.
func (e *Engine) Options(ctx context.Context, orgID, orderID uuid.UUID) (OrderOptions, error) {
	out := OrderOptions{OrderID: orderID}
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		ord, pay, err := loadOrderPayment(ctx, tx, orgID, orderID)
		var refErr *Error
		if errors.As(err, &refErr) {
			switch refErr.Code {
			case CodeSellerSiteOrder:
				out.Route, out.Refusal = RouteSellerSite, refErr
				return nil
			case CodeNothingToRefund:
				out.Route, out.Refusal = RouteNone, refErr
				return nil
			}
		}
		if err != nil {
			return err
		}
		out.Provider = payments.NormalizeProviderName(pay.Provider)
		out.Currency = pay.Currency
		out.Route = e.RouteFor(out.Provider, ord.Source)
		out.Refusal = RouteRefusal(out.Route, out.Provider)
		if desc, ok := e.modules.Descriptor(out.Provider); ok {
			out.PartialRefund = desc.Capabilities.PartialRefund
		}

		// What is left of the payment: a refund that names no ticket (made in
		// the provider dashboard, or a flat refund of the payment) reduces it
		// without reducing any one ticket, and no ticket may offer more.
		var taken int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(amount), 0)::bigint FROM refunds
			WHERE payment_intent_id = $1 AND state NOT IN ('failed', 'rejected') AND `+countsAgainstBudgetSQL,
			pay.ID).Scan(&taken); err != nil {
			return err
		}
		paymentLeft := max(pay.Amount-taken, 0)
		out.PaymentLeft = paymentLeft

		rows, err := tx.Query(ctx, `
			SELECT t.id, t.status, oi.total,
			       COALESCE((SELECT SUM(r.amount) FROM refunds r
			                  WHERE r.ticket_id = t.id AND r.state NOT IN ('failed', 'rejected')
			                  AND `+countsAgainstBudgetSQL+`), 0)::bigint,
			       EXISTS (SELECT 1 FROM refunds r
			                WHERE r.ticket_id = t.id AND r.cancel_ticket AND r.state NOT IN ('failed', 'rejected')),
			       t.review_hold, t.system_ticket_id,
			       NULLIF(concat_ws(' / ', NULLIF(btrim(t.seat_sector), ''), NULLIF(btrim(t.seat_row), ''), NULLIF(btrim(t.seat_number), '')), '')
			FROM   order_items oi
			JOIN   tickets t ON t.id = oi.ticket_id
			WHERE  oi.order_id = $1
			ORDER  BY t.system_ticket_id NULLS LAST, t.id`, ord.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t TicketOption
			var onHold bool
			if err := rows.Scan(&t.TicketID, &t.Status, &t.Paid, &t.Refunded, &t.InProgress, &onHold, &t.TicketNumber, &t.SeatLabel); err != nil {
				return err
			}
			if t.Paid != nil && t.Status == "active" {
				if left := min(*t.Paid-t.Refunded, paymentLeft); left > 0 {
					if onHold {
						t.NotRefundableReason = "review_hold"
					} else {
						t.Refundable = left
					}
				}
			}
			out.Tickets = append(out.Tickets, t)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()

		arows, err := tx.Query(ctx, `SELECT `+refundColumns+` FROM refunds
			WHERE order_id = $1 AND org_id = $2 AND state IN ('requested', 'approved', 'provider_pending', 'manual_review')
			ORDER BY created_at, id`, ord.ID, orgID)
		if err != nil {
			return err
		}
		out.Active, err = scanRefunds(arows)
		return err
	})
	if err != nil {
		return OrderOptions{}, err
	}
	return out, nil
}

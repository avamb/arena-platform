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
}

// OrderOptions is the answer of GET .../orders/{order_id}/refund-options.
type OrderOptions struct {
	OrderID  uuid.UUID
	Route    Route
	Provider string
	Currency string
	// PartialRefund: the provider can return less than the whole payment.
	PartialRefund bool
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
		paymentLeft := pay.Amount - taken

		rows, err := tx.Query(ctx, `
			SELECT t.id, t.status, oi.total,
			       COALESCE((SELECT SUM(r.amount) FROM refunds r
			                  WHERE r.ticket_id = t.id AND r.state NOT IN ('failed', 'rejected')
			                  AND `+countsAgainstBudgetSQL+`), 0)::bigint,
			       EXISTS (SELECT 1 FROM refunds r
			                WHERE r.ticket_id = t.id AND r.cancel_ticket AND r.state NOT IN ('failed', 'rejected'))
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
			if err := rows.Scan(&t.TicketID, &t.Status, &t.Paid, &t.Refunded, &t.InProgress); err != nil {
				return err
			}
			if t.Paid != nil && t.Status == "active" {
				if left := min(*t.Paid-t.Refunded, paymentLeft); left > 0 {
					t.Refundable = left
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

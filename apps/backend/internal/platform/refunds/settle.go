// settle.go — what follows a refund the provider ACCEPTED: the ticket is
// cancelled (never before, owner decision 2026-09-20), the order moves to
// refunded / partially_refunded, and v1.ticket.refunded goes out once the
// money actually settled. Every step is idempotent and runs after the
// outcome is committed, so a crash in between only delays it: refund.sweep's
// repair pass finishes an accepted refund whose ticket is still active.
package refunds

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// settle runs the after-acceptance steps. project is true on the FIRST
// acceptance (the order event is written once); a later pending→succeeded
// only publishes.
func (e *Engine) settle(ctx context.Context, r Refund, pay Payment) {
	if !r.Accepted() {
		return
	}
	cancelled := e.cancelTickets(ctx, r, pay)
	e.projectOrder(ctx, r, pay)
	if r.State == StateSucceeded {
		e.publish(ctx, r, pay, cancelled)
	}
}

// cancelTickets cancels what the refund pays back and returns the ids of
// the tickets it covers. A ticket-level refund cancels its own ticket when
// the operation asked for it; an order-level refund (no ticket, the flat
// POST /v1/refunds) of the WHOLE payment cancels every active ticket of the
// order, unless a ticket already carries this refund (the flat cancel route
// links its ticket before the money moves).
func (e *Engine) cancelTickets(ctx context.Context, r Refund, pay Payment) []string {
	if r.TicketID != nil {
		if r.CancelTicket {
			e.cancelOne(ctx, CancelRequest{TicketID: *r.TicketID, RefundID: r.ID, Amount: r.Amount, Reason: deref(r.Reason)})
		}
		return []string{r.TicketID.String()}
	}
	if pay.CheckoutSessionID == nil || r.Amount < pay.Amount {
		return nil
	}
	type target struct {
		id    uuid.UUID
		price int64
	}
	var (
		targets    []target
		attributed bool
	)
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		if attributed, err = ticketsLinkedTo(ctx, tx, r.ID); err != nil || attributed {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT t.id, COALESCE(oi.total, 0)::bigint FROM tickets t
			LEFT JOIN order_items oi ON oi.ticket_id = t.id
			WHERE t.checkout_session_id = $1 AND t.status = 'active' ORDER BY t.ordinal, t.id`, *pay.CheckoutSessionID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t target
			if err := rows.Scan(&t.id, &t.price); err != nil {
				return err
			}
			targets = append(targets, t)
		}
		return rows.Err()
	})
	if err != nil {
		e.logger.Error("refunds: listing the order's tickets failed", "refund_id", r.ID.String(), "error", err.Error())
		return nil
	}
	ids := make([]string, 0, len(targets))
	for _, t := range targets {
		e.cancelOne(ctx, CancelRequest{TicketID: t.id, RefundID: r.ID, Amount: t.price, Reason: deref(r.Reason)})
		ids = append(ids, t.id.String())
	}
	return ids
}

func (e *Engine) cancelOne(ctx context.Context, req CancelRequest) {
	if e.cancelTicket == nil {
		e.logger.Warn("refunds: no ticket canceller wired; the sweep repair will cancel the ticket",
			"refund_id", req.RefundID.String(), "ticket_id", req.TicketID.String())
		return
	}
	if err := e.cancelTicket(ctx, req); err != nil && !errors.Is(err, ErrTicketNotActive) {
		e.logger.Error("refunds: ticket cancellation after an accepted refund failed; the sweep repair retries it",
			"refund_id", req.RefundID.String(), "ticket_id", req.TicketID.String(), "error", err.Error())
	}
}

// projectOrder moves the order to refunded (no active ticket left) or
// partially_refunded, and appends one order_events row per refund. A flat
// refund names no order; its payment's checkout session does.
func (e *Engine) projectOrder(ctx context.Context, r Refund, pay Payment) {
	if r.OrderID == nil && pay.CheckoutSessionID == nil {
		return
	}
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		orderID := r.OrderID
		if orderID == nil {
			var id uuid.UUID
			err := tx.QueryRow(ctx, `SELECT id FROM orders WHERE checkout_session_id = $1`, *pay.CheckoutSessionID).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			orderID = &id
		}
		var already bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM order_events
			WHERE order_id = $1 AND type IN ('ticket_refunded', 'refunded') AND payload->>'refund_id' = $2)`,
			*orderID, r.ID.String()).Scan(&already); err != nil {
			return err
		}
		if already {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE orders o
			SET status = CASE WHEN EXISTS (SELECT 1 FROM tickets t
			                                WHERE t.checkout_session_id = o.checkout_session_id AND t.status = 'active')
			                  THEN 'partially_refunded' ELSE 'refunded' END,
			    updated_at = now()
			WHERE o.id = $1 AND o.status IN ('paid', 'partially_refunded')`, *orderID); err != nil {
			return err
		}
		kind := "refunded"
		payload := map[string]any{"refund_id": r.ID.String(), "amount": r.Amount, "currency": r.Currency,
			"provider_refund_id": deref(r.ProviderRefundID), "refund_mode": "automatic"}
		if r.TicketID != nil {
			kind = "ticket_refunded"
			payload["ticket_id"] = r.TicketID.String()
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO order_events (order_id, type, actor, payload) VALUES ($1, $2, 'system', $3)`,
			*orderID, kind, raw)
		return err
	})
	if err != nil {
		e.logger.Error("refunds: order projection failed", "refund_id", r.ID.String(), "error", err.Error())
	}
}

func (e *Engine) publish(ctx context.Context, r Refund, pay Payment, ticketIDs []string) {
	if e.publishRefunded == nil || len(ticketIDs) == 0 {
		return
	}
	cs := ""
	if pay.CheckoutSessionID != nil {
		cs = pay.CheckoutSessionID.String()
	}
	e.publishRefunded(ctx, ticketIDs, cs, r.ID.String(), r.Currency, r.Amount)
}

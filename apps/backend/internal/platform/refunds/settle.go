// settle.go — what follows a refund the provider ACCEPTED: the ticket is
// cancelled (never before, owner decision 2026-09-20), the order moves to
// refunded / partially_refunded, and v1.ticket.refunded goes out once the
// money actually settled. Every step is idempotent and runs after the
// outcome is committed, so a crash in between only delays it: when every
// step is done the refund is stamped settled_at, and refund.sweep's repair
// pass finishes any accepted refund that is not.
package refunds

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// settle runs the after-acceptance steps.
func (e *Engine) settle(ctx context.Context, r Refund, pay Payment) {
	e.finish(ctx, r, pay)
}

// Settlement timings (fourth review, M-a).
const (
	// ticketCancelTimeout bounds ONE ticket cancellation, on a context of
	// its own: a slow or expiring caller context must not cut a
	// cancellation off half-way (third review, H1). The loop itself stops
	// starting new cancellations once the caller's context is done; the
	// sweep's repair finishes the rest.
	ticketCancelTimeout = 10 * time.Second
	// settleWriteTimeout bounds the order projection, the publish and the
	// settled_at stamp together, on a context of their own: they must not
	// fail because the cancellations used up the caller's deadline.
	settleWriteTimeout = 10 * time.Second
)

// finish runs every after-acceptance step of r and reports whether all of
// them are done; only then is the refund stamped settled_at, so that the
// sweep's repair (repairWhere) picks up whatever is left — a ticket still
// active, an order not projected, a publish not claimed.
func (e *Engine) finish(ctx context.Context, r Refund, pay Payment) bool {
	if !r.Accepted() {
		return true
	}
	cancelled, ok := e.cancelTickets(ctx, r, pay)
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleWriteTimeout)
	defer cancel()
	if !e.projectOrder(wctx, r, pay) {
		ok = false
	}
	// v1.ticket.refunded only once every due cancellation went through:
	// otherwise the repair publishes it when it finishes the cancellation,
	// so a site never hears "refunded" for a ticket that still admits, and
	// never hears it twice.
	if ok && r.State == StateSucceeded && !e.publish(wctx, r, pay, cancelled) {
		ok = false
	}
	if ok {
		ok = e.markSettled(wctx, r)
	}
	return ok
}

// markSettled stamps settled_at — only while the row is still in the state
// finish acted on: a pending refund the lookup moved to succeeded meanwhile
// (which clears settled_at) still owes its publish.
func (e *Engine) markSettled(ctx context.Context, r Refund) bool {
	if err := e.inTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE refunds SET settled_at = now()
			WHERE id = $1 AND state = $2 AND settled_at IS NULL`, r.ID, r.State)
		return err
	}); err != nil {
		e.logger.Error("refunds: marking the refund settled failed; the sweep repair retries it", "refund_id", r.ID.String(), "error", err.Error())
		return false
	}
	return true
}

// scopeTicket is one ticket a refund covers.
type scopeTicket struct {
	id     uuid.UUID
	price  int64
	active bool
	// cancel: this refund is the one that cancels the ticket.
	cancel bool
}

// refundScope lists the tickets a refund covers (third review, H1/M4;
// fourth review, H-1). It is the ONE place that decides it: the sweep's
// repair selects by settled_at, never by its own copy of these rules.
//   - a ticket-level refund covers its ticket; it cancels it when
//     cancel_ticket is set. A money-only refund of a ticket whose
//     cancellation ANOTHER live refund owns covers nothing (that refund
//     speaks for the ticket);
//   - a refund written by the ticket-cancel route (fromTicketCancel) covers
//     the ticket(s) linked to it through tickets.refund_id and cancels
//     nothing: the operator cancelled its ticket before the refund existed,
//     and its amount — even the whole payment — is that ticket's money;
//   - an order-level refund (no ticket, the flat POST /v1/refunds) of the
//     WHOLE payment covers every ticket of the order that it cancelled
//     already (tickets.refund_id) plus every active ticket no other live
//     refund owns — the set stays the same however many times it is
//     settled, so a cancellation that failed half-way is finished later;
//   - a partial order-level refund covers nothing (its tickets were put on
//     review hold when the provider accepted it).
func (e *Engine) refundScope(ctx context.Context, r Refund, pay Payment) ([]scopeTicket, error) {
	var out []scopeTicket
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		var (
			rows pgx.Rows
			err  error
		)
		switch {
		case r.TicketID != nil:
			rows, err = tx.Query(ctx, `SELECT t.id, COALESCE(oi.total, 0)::bigint, t.status = 'active', $2::boolean
				FROM tickets t LEFT JOIN order_items oi ON oi.ticket_id = t.id
				WHERE t.id = $1 AND ($2::boolean OR NOT EXISTS (
					SELECT 1 FROM refunds o WHERE o.ticket_id = t.id AND o.id <> $3
					  AND o.cancel_ticket AND o.state NOT IN ('failed', 'rejected')))`,
				*r.TicketID, r.CancelTicket, r.ID)
		case r.fromTicketCancel():
			rows, err = tx.Query(ctx, `SELECT t.id, COALESCE(oi.total, 0)::bigint, t.status = 'active', false
				FROM tickets t LEFT JOIN order_items oi ON oi.ticket_id = t.id
				WHERE t.refund_id = $1
				ORDER BY t.ordinal, t.id`, r.ID)
		case pay.CheckoutSessionID != nil && r.Amount >= pay.Amount:
			rows, err = tx.Query(ctx, `SELECT t.id, COALESCE(oi.total, 0)::bigint, t.status = 'active', true
				FROM tickets t LEFT JOIN order_items oi ON oi.ticket_id = t.id
				WHERE t.checkout_session_id = $1
				  AND (t.refund_id = $2 OR (t.status = 'active' AND t.refund_id IS NULL AND NOT EXISTS (
				       SELECT 1 FROM refunds o WHERE o.ticket_id = t.id AND o.id <> $2
				         AND o.cancel_ticket AND o.state NOT IN ('failed', 'rejected'))))
				ORDER BY t.ordinal, t.id`, *pay.CheckoutSessionID, r.ID)
		default:
			return nil
		}
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t scopeTicket
			if err := rows.Scan(&t.id, &t.price, &t.active, &t.cancel); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// cancelTickets cancels every active ticket of the refund's scope that it
// is due to cancel, each on its own deadline, and returns the ids of the
// whole scope. It starts no new cancellation once ctx is done (a big order
// must not hold the caller far past its deadline, fourth review M-a). ok is
// false when a due cancellation failed or was not started; the refund then
// stays unsettled and the sweep's repair finishes it.
func (e *Engine) cancelTickets(ctx context.Context, r Refund, pay Payment) (ids []string, ok bool) {
	scope, err := e.refundScope(ctx, r, pay)
	if err != nil {
		e.logger.Error("refunds: listing the refund's tickets failed", "refund_id", r.ID.String(), "error", err.Error())
		return nil, false
	}
	ids = make([]string, 0, len(scope))
	ok = true
	for _, t := range scope {
		ids = append(ids, t.id.String())
		if !t.cancel || !t.active {
			continue
		}
		if ctx.Err() != nil {
			ok = false
			continue
		}
		amount := t.price
		if r.TicketID != nil {
			amount = r.Amount
		}
		tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ticketCancelTimeout)
		done := e.cancelOne(tctx, CancelRequest{TicketID: t.id, RefundID: r.ID, Amount: amount, Reason: deref(r.Reason)})
		cancel()
		if !done {
			ok = false
		}
	}
	return ids, ok
}

// cancelOne reports whether the ticket is cancelled now (already cancelled
// counts).
func (e *Engine) cancelOne(ctx context.Context, req CancelRequest) bool {
	if e.cancelTicket == nil {
		e.logger.Warn("refunds: no ticket canceller wired; the sweep repair will cancel the ticket",
			"refund_id", req.RefundID.String(), "ticket_id", req.TicketID.String())
		return false
	}
	if err := e.cancelTicket(ctx, req); err != nil && !errors.Is(err, ErrTicketNotActive) {
		e.logger.Error("refunds: ticket cancellation after an accepted refund failed; the sweep repair retries it",
			"refund_id", req.RefundID.String(), "ticket_id", req.TicketID.String(), "error", err.Error())
		return false
	}
	return true
}

// projectOrder moves the order to refunded (no active ticket left) or
// partially_refunded, and appends one order_events row per refund. A flat
// refund names no order; its payment's checkout session does. It reports
// whether the projection is in place (true when there is no order).
func (e *Engine) projectOrder(ctx context.Context, r Refund, pay Payment) bool {
	if r.OrderID == nil && pay.CheckoutSessionID == nil {
		return true
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
		// The status is recomputed on EVERY settle, even when this refund's
		// event is already written: a settle cut short by its deadline
		// projects while a ticket of its scope is still active, and only a
		// later repair cancels it (fifth review, M-3). Only the event is
		// written once; the order row lock serializes two settles of the
		// same refund (a request and a repair) between check and insert.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM orders WHERE id = $1 FOR UPDATE`, *orderID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE orders o
			SET status = CASE WHEN EXISTS (SELECT 1 FROM tickets t
			                                WHERE t.checkout_session_id = o.checkout_session_id AND t.status = 'active')
			                  THEN 'partially_refunded' ELSE 'refunded' END,
			    updated_at = now()
			WHERE o.id = $1 AND o.status IN ('paid', 'partially_refunded')`, *orderID); err != nil {
			return err
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
		e.logger.Error("refunds: order projection failed; the sweep repair retries it", "refund_id", r.ID.String(), "error", err.Error())
		return false
	}
	return true
}

// publish emits v1.ticket.refunded for the refund AT MOST ONCE: it first
// claims refunds.refunded_published_at, so two settles (a request and a
// sweep repair, or two sweep chains) can never both publish. The claim is
// committed before the outbox write; a crash between the two loses that one
// publish (spec 36 §14) rather than ever sending it twice. It reports
// whether the publish is done (claimed now or before, or nothing to send).
func (e *Engine) publish(ctx context.Context, r Refund, pay Payment, ticketIDs []string) bool {
	if e.publishRefunded == nil || len(ticketIDs) == 0 {
		return true
	}
	claimed := false
	if err := e.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE refunds SET refunded_published_at = now()
			WHERE id = $1 AND refunded_published_at IS NULL`, r.ID)
		claimed = err == nil && tag.RowsAffected() == 1
		return err
	}); err != nil {
		e.logger.Error("refunds: claiming the refunded publish failed; the sweep repair retries it", "refund_id", r.ID.String(), "error", err.Error())
		return false
	}
	if !claimed {
		return true
	}
	cs := ""
	if pay.CheckoutSessionID != nil {
		cs = pay.CheckoutSessionID.String()
	}
	e.publishRefunded(ctx, ticketIDs, cs, r.ID.String(), r.Currency, r.Amount)
	return true
}

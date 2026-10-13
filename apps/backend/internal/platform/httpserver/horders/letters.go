package horders

// letters.go — what the order's letters to its buyer came to, for the event
// center (owner decision 2026-10-13: the organizer must see on the order card
// whether the tickets reached the buyer, and notice a failed letter in the
// orders list without opening every order).
//
//   - delivery[].recipient_email: the address each ticket letter goes to;
//   - refund_letter: the letter of the order's refund operations, or null;
//   - delivery_state on every row of the orders list, folded like the card's.

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Refund letter states (refund_letter.state).
const (
	refundLetterOff     = "off"     // the operation asked for no letter
	refundLetterQueued  = "queued"  // the refund has not succeeded yet, or the letter is on its way
	refundLetterSent    = "sent"    // every refund that asked for a letter is covered by one
	refundLetterFailed  = "failed"  // the worker gave up on a letter
	refundLetterPartial = "partial" // some refunds are covered, others still wait
)

// addRecipients writes each ticket letter's address into its delivery entry.
// A failed lookup leaves the entries without one (the card then names the
// buyer's address of the order).
func (h *Handler) addRecipients(ctx context.Context, orderID uuid.UUID, delivery []map[string]any) {
	rows, err := h.queries.ListOrderDeliveryRecipients(ctx, orderID)
	if err != nil {
		h.logger.Warn("horders: delivery recipient lookup failed", slog.String("order_id", orderID.String()), slog.Any("error", err))
		return
	}
	byTicket := make(map[string]*string, len(rows))
	for _, r := range rows {
		byTicket[r.TicketID.String()] = r.RecipientEmail
	}
	for _, d := range delivery {
		id, _ := d["ticket_id"].(string)
		d["recipient_email"] = byTicket[id]
	}
}

// refundLetter is the refund_letter block of the card, or nil when the order
// has no refund operation that could write to the buyer.
func (h *Handler) refundLetter(ctx context.Context, orderID uuid.UUID) map[string]any {
	r, err := h.queries.GetOrderRefundLetter(ctx, orderID)
	if err != nil {
		h.logger.Warn("horders: refund letter lookup failed", slog.String("order_id", orderID.String()), slog.Any("error", err))
		return nil
	}
	return refundLetterEntry(r)
}

func refundLetterEntry(r gen.OrderRefundLetterRow) map[string]any {
	if r.Refunds == 0 {
		return nil
	}
	m := map[string]any{"refunds": r.Notify, "sent": r.Notified, "at": nil}
	switch {
	case r.Notify == 0:
		m["state"] = refundLetterOff
		m["refunds"] = r.Refunds
	case r.GivenUp > 0:
		m["state"] = refundLetterFailed
		m["at"] = timePtr(r.LastGivenUp)
	case r.Notified >= r.Notify:
		m["state"] = refundLetterSent
		m["at"] = timePtr(r.LastNotified)
	case r.Notified > 0:
		m["state"] = refundLetterPartial
		m["at"] = timePtr(r.LastNotified)
	default:
		m["state"] = refundLetterQueued
	}
	return m
}

// listDeliveryStates folds the ticket letters of a page of orders, keyed by
// order id. A failed lookup answers an empty map: the list then carries
// `none` and still renders.
func (h *Handler) listDeliveryStates(ctx context.Context, ids []uuid.UUID) map[uuid.UUID]string {
	out := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return out
	}
	rows, err := h.queries.ListOrdersDeliveryCounts(ctx, ids)
	if err != nil {
		h.logger.Warn("horders: list delivery lookup failed", slog.Any("error", err))
		return out
	}
	for _, r := range rows {
		out[r.OrderID] = foldDeliveryCounts(r)
	}
	return out
}

// foldDeliveryCounts is deliveryState over counts: sent when every ticket's
// letter went out, pending while any is queued, failed when one failed and
// none is pending, none otherwise.
func foldDeliveryCounts(r gen.OrderDeliveryCountsRow) string {
	switch {
	case r.Tickets > 0 && r.Sent == r.Tickets:
		return deliverySent
	case r.Pending > 0:
		return deliveryPending
	case r.Failed > 0:
		return deliveryFailed
	}
	return deliveryNone
}

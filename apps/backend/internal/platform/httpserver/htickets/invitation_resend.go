// invitation_resend.go — the staff-invitation resend of spec 46, section 3.4.
//
// POST .../invitations/{order_id}/resend lives in hinvitations; the delivery
// machinery it needs (the requeue, the per-ticket worker jobs with the
// invitation template, the order-history row and the audit event) is the one
// the order resend already uses, so it is exposed here instead of copied.
package htickets

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ErrNoActiveTickets is returned by ResendInvitationOrder when every ticket of
// the order is already cancelled or refunded.
var ErrNoActiveTickets = errors.New("htickets: the order has no active tickets to send")

// ResendInvitationOrder queues the active tickets of a complimentary order for
// delivery to recipient again, in the invitation template, as ONE e-mail (the
// worker folds the requeued jobs of one order and recipient into one letter).
// oneTime marks recipient as a one-time address that expires after
// OneTimeAddressTTL and never becomes the order's address. It returns the
// number of tickets queued.
func (h *Handler) ResendInvitationOrder(ctx context.Context, r *http.Request, order gen.OrderRow, recipient string, oneTime bool) (int, error) {
	if h.deliveryJobQueries == nil || h.ticketQueries == nil || h.pool == nil {
		return 0, errors.New("htickets: delivery store is not available")
	}
	rows, err := h.ticketQueries.ListOrderTicketDetails(ctx, order.ID)
	if err != nil {
		return 0, err
	}
	var active []gen.OrderTicketDetailRow
	for _, t := range rows {
		if t.Status == "active" {
			active = append(active, t)
		}
	}
	if len(active) == 0 {
		return 0, ErrNoActiveTickets
	}
	var expires *time.Time
	if oneTime {
		e := time.Now().UTC().Add(OneTimeAddressTTL)
		expires = &e
	}
	return h.queueOrderResend(ctx, r, order, active, recipient, expires, oneTime)
}

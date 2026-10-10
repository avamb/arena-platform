// refund_canceller.go — how the refund engine (internal/platform/refunds,
// PAY-03) cancels a ticket once the payment provider ACCEPTED its refund:
// the very same AB-49 cancellation transaction an operator runs, with
// refund_mode=automatic and the refund id linked on the ticket.
package htickets

import (
	"context"
	"errors"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// refundEngineActor names the principal in the cancellation audit row.
const refundEngineActor = "refund_engine"

// RefundCanceller returns the refunds.CancelTicketFunc backed by
// CancelTicketTx. A ticket that is no longer active answers
// refunds.ErrTicketNotActive, which the engine treats as done.
func (h *Handler) RefundCanceller() refunds.CancelTicketFunc {
	return func(ctx context.Context, req refunds.CancelRequest) error {
		refundID := req.RefundID
		amount := req.Amount
		reason := req.Reason
		if reason == "" {
			reason = "refunded"
		}
		_, cErr := h.CancelTicketTx(ctx, CancelTicketParams{
			TicketID:     req.TicketID,
			Reason:       reason,
			RefundMode:   RefundModeAutomatic,
			RefundAmount: &amount,
			ActorType:    "system",
			ActorLabel:   refundEngineActor,
			RefundID:     &refundID,
		})
		if cErr == nil {
			return nil
		}
		if cErr.Code == "ticket.not_active" {
			return errors.Join(refunds.ErrTicketNotActive, cErr)
		}
		return cErr
	}
}

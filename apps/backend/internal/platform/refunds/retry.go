// retry.go — "try this refund again" (spec 36 §8). A retry never revives the
// old row: it starts a NEW one-ticket operation, so the provider sees a new
// idempotency key (the new refund's id) and the engine's rule that nothing
// outside it sets a refund back from failed stays true.
//
// Only a refund that provably moved no money can be retried: one the
// provider refused outright (failed), or one parked before any provider call
// (manual_review with no attempt). A refund whose call ended without an
// answer, or that the provider accepted, is never retried — a second refund
// could return the money twice — it is looked up by refund.sweep or closed by
// an operator who checked the provider dashboard.
package refunds

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// CodeNotRetryable: the refund cannot be sent again safely.
const CodeNotRetryable = "refund.not_retryable"

// RetryInput names the refund to send again.
type RetryInput struct {
	OrgID    uuid.UUID
	RefundID uuid.UUID
	Actor    Actor
	Via      string
}

// Retryable reports whether r may be sent again.
func Retryable(r Refund) bool {
	if r.Settlement != "provider" || r.Provider == nil || r.TicketID == nil || r.OrderID == nil || r.ProviderRefundID != nil {
		return false
	}
	switch r.State {
	case StateFailed:
		return true
	case StateManualReview:
		return r.ProviderAttempts == 0
	}
	return false
}

// Retry starts a new one-ticket refund operation from a failed or never-sent
// refund and drives it at once. Replaying the same retry answers the first
// result (the idempotency key is derived from the refund id).
func (e *Engine) Retry(ctx context.Context, in RetryInput) (BatchResult, error) {
	var (
		cur                   Refund
		notify, cancelTickets bool
	)
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		r, err := getRefund(ctx, tx, in.RefundID, false)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && r.OrgID != in.OrgID) {
			return refusal(http.StatusNotFound, CodeRefundNotFound, "refund not found", nil)
		}
		if err != nil {
			return err
		}
		cur = r
		notify, cancelTickets = true, r.CancelTicket
		if r.BatchID != nil {
			if err := tx.QueryRow(ctx, `SELECT notify_buyer, cancel_tickets FROM refund_batches WHERE id = $1`,
				*r.BatchID).Scan(&notify, &cancelTickets); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return BatchResult{}, err
	}
	if !Retryable(cur) {
		return BatchResult{}, refusal(http.StatusConflict, CodeNotRetryable,
			"this refund cannot be sent again: only a refund the provider refused, or one that was never sent, can be retried",
			map[string]any{"state": cur.State})
	}
	reason := "retry of refund " + cur.ID.String()
	if cur.Reason != nil && *cur.Reason != "" {
		reason = *cur.Reason
	}
	amount := cur.Amount
	res, err := e.CreateBatch(ctx, BatchInput{
		OrgID: cur.OrgID, OrderID: *cur.OrderID,
		Items:          []Item{{TicketID: *cur.TicketID, Amount: &amount}},
		Reason:         reason,
		CancelTickets:  cancelTickets,
		NotifyBuyer:    notify,
		IdempotencyKey: "retry-" + cur.ID.String(),
		Actor:          in.Actor, Via: in.Via, Approved: true,
	})
	if err != nil {
		return BatchResult{}, err
	}
	if !res.Replayed && len(res.Refunds) > 0 {
		_ = e.inTx(ctx, func(tx pgx.Tx) error {
			return e.writeAudit(ctx, tx, audit.Event{ActorType: in.Actor.Type, ActorID: in.Actor.ID,
				Action: "v1.refund.retry", ResourceType: "refund", ResourceID: cur.ID.String(),
				Metadata: map[string]any{"replacement_refund_id": res.Refunds[0].ID.String(), "via": in.Via}})
		})
	}
	return res, nil
}

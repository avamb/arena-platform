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

// CodeAlreadyReplaced: the refund was already retried; the replacement is the
// one to look at (or retry).
const CodeAlreadyReplaced = "refund.already_replaced"

// failureReplaced marks a never-sent refund that a retry took the place of.
const failureReplaced = "replaced_by_retry"

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
	// A refund is retried once: the replacement is what to look at next (and
	// to retry again if it fails too). Repeating the call while the
	// replacement is still alive answers it, a failed replacement is named.
	key := "retry-" + cur.ID.String()
	var prior *Refund
	if err := e.inTx(ctx, func(tx pgx.Tx) error {
		b, found, ferr := findBatch(ctx, tx, in.OrgID, key)
		if ferr != nil || !found || len(b.Refunds) == 0 {
			return ferr
		}
		first := b.Refunds[0]
		prior = &first
		return nil
	}); err != nil {
		return BatchResult{}, err
	}
	if prior != nil {
		if cur2, gerr := e.GetRefund(ctx, prior.ID); gerr == nil && (cur2.State == StateFailed || cur2.State == "rejected") {
			return BatchResult{}, refusal(http.StatusConflict, CodeAlreadyReplaced,
				"this refund was already retried and the retry failed too: retry the newer refund",
				map[string]any{"replacement_refund_id": cur2.ID.String()})
		}
	}
	reason := "retry of refund " + cur.ID.String()
	if cur.Reason != nil && *cur.Reason != "" {
		reason = *cur.Reason
	}
	// A refund parked before any call still holds its ticket's "live
	// cancellation" slot, which would refuse the replacement: it is closed as
	// replaced first (no money moved, nothing was sent). If the replacement
	// cannot be created the row goes back to review.
	prevCode, prevReason := cur.FailureCode, cur.FailureReason
	replaced := false
	if cur.State == StateManualReview && prior == nil {
		ok, rerr := e.markReplaced(ctx, cur)
		if rerr != nil {
			return BatchResult{}, rerr
		}
		replaced = ok
	}
	amount := cur.Amount
	res, err := e.CreateBatch(ctx, BatchInput{
		OrgID: cur.OrgID, OrderID: *cur.OrderID,
		Items:          []Item{{TicketID: *cur.TicketID, Amount: &amount}},
		Reason:         reason,
		CancelTickets:  cancelTickets,
		NotifyBuyer:    notify,
		IdempotencyKey: key,
		Actor:          in.Actor, Via: in.Via, Approved: true,
	})
	if err != nil {
		if replaced {
			e.unmarkReplaced(ctx, cur.ID, prevCode, prevReason)
		}
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

// markReplaced closes a never-sent manual_review refund as failed with the
// replaced_by_retry code, under the payment lock. Only a row that is still
// parked, never attempted and without a provider id moves.
func (e *Engine) markReplaced(ctx context.Context, cur Refund) (bool, error) {
	moved := false
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		if err := LockPayment(ctx, tx, cur.PaymentIntentID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE refunds SET state = 'failed', failed_at = now(), failure_code = $2,
			failure_reason = 'replaced by a retry; nothing had been sent to the provider', alert_due_at = NULL,
			updated_at = now()
			WHERE id = $1 AND state = 'manual_review' AND provider_attempts = 0 AND provider_refund_id IS NULL`,
			cur.ID, failureReplaced)
		moved = err == nil && tag.RowsAffected() == 1
		return err
	})
	return moved, err
}

// unmarkReplaced puts a row marked replaced back into review when the
// replacement could not be created.
func (e *Engine) unmarkReplaced(ctx context.Context, id uuid.UUID, code, reason *string) {
	wctx := context.WithoutCancel(ctx)
	err := e.inTx(wctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(wctx, `UPDATE refunds SET state = 'manual_review', failed_at = NULL, failure_code = $2,
			failure_reason = $3, updated_at = now()
			WHERE id = $1 AND state = 'failed' AND failure_code = $4`, id, code, reason, failureReplaced)
		return err
	})
	if err != nil {
		e.logger.Error("refunds: putting a replaced refund back into review failed", "refund_id", id.String(), "error", err.Error())
	}
}

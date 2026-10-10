// heldback.go — what a person is told when a refund is held back for its
// budget (fourth review M-b, fifth review M-1, sixth review HIGH-1 and
// MEDIUM-1, seventh review).
//
// Owner decision (sixth review): money first, never cancel a ticket on a
// guess. Whatever the budget re-check holds back, no ticket is cancelled
// automatically. The texts a person reads must not assert anything that can
// be false and never tell them to cancel unconditionally (seventh review):
// a stored reason is written once and the ticket may change afterwards, so
// it speaks conditionally; the ops alert is built when it is sent and reads
// the ticket's status then (alertText). An earlier version handed the
// held-back refund's cancellation to "the accepted refund of the same
// ticket", which could be a money-only partial refund that never returned
// the ticket's price.
package refunds

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Budget park codes.
const (
	failureBudgetTaken           = "budget_taken_by_another_refund"
	failureBudgetAfterUnanswered = "budget_exceeded_after_unanswered_call"
	failureLateOverBudget        = "late_acceptance_over_budget"
)

// ifTicketStillActive is the only instruction a stored reason gives about the
// ticket: conditional on what the person finds, because another refund may
// have cancelled it, or only part of its money may be back.
const ifTicketStillActive = "Check the provider dashboard first. Only if the ticket is still active AND the buyer's money for it is back, cancel it without a refund (POST /v1/tickets/{ticket_id}/cancel with refund_mode=none). If another refund already cancelled it, the buyer may have been refunded twice: recover the excess at the provider."

const (
	reasonBudgetTakenNeverSent  = "this refund was never sent to the provider, so it moved no money: another refund of this payment or ticket was accepted by the provider after this one was created (a refund marked failed was accepted late), and sending this one could have refunded the buyer twice. The buyer may still be owed part of the money. " + ifTicketStillActive
	reasonBudgetAfterUnanswered = "another refund of this payment or ticket was accepted by the provider after this one was created, and an earlier call of this refund had no answer, so it may have reached the provider too: the buyer may have been refunded twice. It is not sent again and still counts against the payment. " + ifTicketStillActive
	// reasonLateOverBudget: the provider CONFIRMED the late refund.
	reasonLateOverBudget = "the provider confirmed this refund after it had been marked failed, and another refund has since been created for its amount. If that one also reached the provider, the buyer was refunded twice; if it was not sent yet, arena holds it back. " + ifTicketStillActive
	// reasonLateOverBudgetPending: the provider only accepted it as pending.
	// A parked refund is never read back by the sweep's lookup.
	reasonLateOverBudgetPending = "the provider accepted this refund as PENDING after it had been marked failed and has not confirmed it yet; arena does not read a parked refund back, so check in the provider dashboard whether it completed. Another refund has since been created for its amount; if that one also reached the provider, the buyer may be refunded twice. " + ifTicketStillActive
	// Both refunds named, and the CONFIRMED refunds of the ticket cover its
	// price. %[1]s = the late refund, %[2]s = the held-back one, %[3]d its
	// amount, %[4]d the late refund's amount, %[5]d the confirmed refunds
	// of the ticket, %[6]d the ticket's price.
	reasonHeldBackCovered = "this refund (%[3]d) was never sent to the provider: refund %[1]s of the same ticket was confirmed by the provider late (%[4]d), after it had been marked failed, and the confirmed refunds of this ticket (%[5]d) cover its price (%[6]d). Nothing was sent twice. " + ifTicketStillActive
	reasonLateCovered     = "the provider confirmed this refund %[1]s (%[4]d) after it had been marked failed; its replacement %[2]s (%[3]d) was held back and never sent, and the confirmed refunds of this ticket (%[5]d) cover its price (%[6]d). Nothing was sent twice. " + ifTicketStillActive
	// Not covered (a partial late refund, or one not confirmed yet).
	reasonHeldBackNotCovered = "this refund (%[3]d) was never sent to the provider, so it moved no money. Refund %[1]s of the same ticket was accepted late (%[4]d), but the confirmed refunds of this ticket (%[5]d) do NOT cover its price (%[6]d): the buyer may still be owed money. Do not cancel the ticket without a refund on the strength of this; check the provider dashboard and decide whether to refund the rest."
)

// noteHeldBackReplacement runs in the transaction that parked held (never
// sent) for its budget, under the payment's lock. When the budget was taken
// by a late-accepted refund of the SAME ticket and payment, parked as
// late_acceptance_over_budget, the reasons name each other — and say the
// ticket's money is back ONLY when that refund was confirmed (not pending),
// is at least as large as held, and the confirmed refunds of the ticket
// cover its price (order_items.total). Otherwise only held's reason changes,
// to say the ticket is NOT covered. Neither row's cancel_ticket or state
// changes.
func noteHeldBackReplacement(ctx context.Context, tx pgx.Tx, held *Refund, pay Payment) error {
	if held.TicketID == nil {
		return nil
	}
	var (
		lateID        string
		lateAmount    int64
		lateStatus    *string
		confirmed     int64
		price         *int64
		lateConfirmed bool
	)
	err := tx.QueryRow(ctx, `SELECT r.id::text, r.amount, r.provider_status,
		       (SELECT COALESCE(SUM(c.amount), 0)::bigint FROM refunds c
		         WHERE c.ticket_id = r.ticket_id AND c.state NOT IN ('failed', 'rejected')
		           AND c.provider_refund_id IS NOT NULL AND c.provider_status = 'succeeded'),
		       (SELECT oi.total FROM order_items oi WHERE oi.ticket_id = r.ticket_id LIMIT 1)
		FROM refunds r
		WHERE r.ticket_id = $1 AND r.id <> $2 AND r.payment_intent_id = $3 AND r.state = 'manual_review'
		  AND r.failure_code = $4 AND r.provider_refund_id IS NOT NULL
		ORDER BY r.updated_at DESC, r.id LIMIT 1 FOR UPDATE OF r`,
		*held.TicketID, held.ID, pay.ID, failureLateOverBudget).Scan(&lateID, &lateAmount, &lateStatus, &confirmed, &price)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	lateConfirmed = deref(lateStatus) == "succeeded"
	var ticketPrice int64
	if price != nil {
		ticketPrice = *price
	}
	covered := lateConfirmed && lateAmount >= held.Amount && price != nil && confirmed >= ticketPrice
	args := []any{lateID, held.ID, held.Amount, lateAmount, confirmed, ticketPrice}
	reason := fmt.Sprintf(reasonHeldBackNotCovered, args...)
	if covered {
		reason = fmt.Sprintf(reasonHeldBackCovered, args...)
		if _, err := tx.Exec(ctx, `UPDATE refunds SET failure_reason = $2, updated_at = now() WHERE id = $1`,
			lateID, fmt.Sprintf(reasonLateCovered, args...)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE refunds SET failure_reason = $2 WHERE id = $1`, held.ID, reason); err != nil {
		return err
	}
	held.FailureReason = &reason
	return nil
}

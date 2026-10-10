// heldback.go — what a person is told when a refund is held back for its
// budget (fourth review M-b, fifth review M-1, sixth review HIGH-1 and
// MEDIUM-1).
//
// Owner decision (sixth review): money first, never cancel a ticket on a
// guess. Whatever the budget re-check holds back, no ticket is cancelled
// automatically: the reasons and the ops alert say plainly whether the
// ticket is still valid and how a person cancels it. An earlier version
// handed the held-back refund's cancellation to "the accepted refund of the
// same ticket", which could be a money-only partial refund that never
// returned the ticket's price.
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

// howToCancel is the one way a person cancels a ticket whose money already
// went back, without sending more money.
const howToCancel = "cancel it without a refund: POST /v1/tickets/{ticket_id}/cancel with refund_mode=none"

const (
	reasonBudgetTakenNeverSent  = "this refund was never sent to the provider, so it moved no money: another refund of this payment or ticket was accepted by the provider after this one was created (a refund marked failed was accepted late), and sending this one could have refunded the buyer twice. Nothing was sent twice. The ticket is STILL VALID; check the provider dashboard and, if the buyer's money for it is back, " + howToCancel
	reasonBudgetAfterUnanswered = "another refund of this payment or ticket was accepted by the provider after this one was created, and an earlier call of this refund had no answer, so it may have reached the provider too: the buyer may have been refunded twice. It is not sent again and still counts against the payment. The ticket is STILL VALID; check the provider dashboard and, if the buyer's money for it is back, " + howToCancel
	reasonLateOverBudget        = "the provider accepted this refund after it had been marked failed, and another refund has since been created for its amount. If that one also reached the provider, the buyer was refunded twice; if it was not sent yet, arena holds it back. The ticket is STILL VALID; check the provider dashboard and, if the buyer's money for it is back, " + howToCancel
	// %[1]s = the late-accepted refund, %[2]s = the held-back one.
	reasonHeldBackReplacement = "this refund was never sent to the provider: the money for this ticket already went back through refund %[1]s (accepted by the provider late, after it had been marked failed). Nothing was sent twice. The ticket is STILL VALID: " + howToCancel
	reasonLateWithHeldBack    = "the provider accepted this refund after it had been marked failed; its replacement %[2]s was held back and never sent, so the money for this ticket went back once, through this refund %[1]s. Nothing was sent twice. The ticket is STILL VALID: " + howToCancel
)

// noteHeldBackReplacement runs in the transaction that parked held (never
// sent) for its budget, under the payment's lock. When the budget was taken
// by a late-accepted refund of the SAME ticket and payment, parked as
// late_acceptance_over_budget, both reasons are rewritten to name each other:
// the money for the ticket went back once, through that refund, nothing was
// sent twice, the ticket is still valid and a person cancels it. Neither
// row's cancel_ticket or state changes.
func noteHeldBackReplacement(ctx context.Context, tx pgx.Tx, held *Refund, pay Payment) error {
	if held.TicketID == nil {
		return nil
	}
	var lateID string
	err := tx.QueryRow(ctx, `SELECT id::text FROM refunds
		WHERE ticket_id = $1 AND id <> $2 AND payment_intent_id = $3 AND state = 'manual_review'
		  AND failure_code = $4 AND provider_refund_id IS NOT NULL
		ORDER BY updated_at DESC, id LIMIT 1 FOR UPDATE`, *held.TicketID, held.ID, pay.ID, failureLateOverBudget).Scan(&lateID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE refunds SET failure_reason = $2, updated_at = now() WHERE id = $1`,
		lateID, fmt.Sprintf(reasonLateWithHeldBack, lateID, held.ID)); err != nil {
		return err
	}
	reason := fmt.Sprintf(reasonHeldBackReplacement, lateID, held.ID)
	if _, err := tx.Exec(ctx, `UPDATE refunds SET failure_reason = $2 WHERE id = $1`, held.ID, reason); err != nil {
		return err
	}
	held.FailureReason = &reason
	return nil
}

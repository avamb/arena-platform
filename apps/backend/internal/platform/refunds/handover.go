// handover.go — who cancels the ticket once a replacement refund is held
// back for its budget (PAY-03 fifth review, M-2).
//
// The story: refund A of a ticket is marked failed while its provider call
// is in flight, an operator creates replacement B, and then A's provider
// accepts after all. A is parked over budget (recordOverBudget) and gives up
// its cancel_ticket because B holds it. When B is about to be sent, the
// budget re-check (parkOverBudget) holds B back. If B was NEVER sent, A is
// the one refund whose money went back: B's cancellation must go back to A,
// or the ticket keeps admitting for money already returned and B's live
// cancel_ticket blocks every new refund of it.
package refunds

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

const reasonBudgetTakenHandedOver = "this refund was never sent to the provider: refund %s of the same ticket, marked failed and then accepted by the provider late, already returned the money and now cancels the ticket. Nothing went out twice unless the provider dashboard shows otherwise"

// handOverCancellation runs under the payment's advisory lock, in the same
// transaction that parked `parked` (never sent; its cancel_ticket already
// cleared). It gives the ticket's cancellation to the accepted refund of the
// same ticket and payment that does not hold it, when no other live refund
// of the ticket does:
//   - a refund parked as late_acceptance_over_budget is brought back to its
//     accepted state (succeeded, or provider_pending with its provider id)
//     when, with `parked` no longer counting, it fits the budget; it owes an
//     ops alert saying it was accepted late. Still over budget, it stays for
//     a person and the ticket stays valid;
//   - an accepted refund gets cancel_ticket and loses settled_at, so its
//     settlement (or the sweep's repair) cancels the ticket and publishes.
//
// It returns the refund that now owns the cancellation, to be settled after
// the commit, or nil when nothing was handed over.
func (e *Engine) handOverCancellation(ctx context.Context, tx pgx.Tx, parked Refund, pay Payment) (*Refund, error) {
	if parked.TicketID == nil || parked.ProviderAttempts > 0 {
		return nil, nil
	}
	sib, err := scanRefund(tx.QueryRow(ctx, `SELECT `+refundColumns+` FROM refunds
		WHERE ticket_id = $1 AND id <> $2 AND payment_intent_id = $3 AND settlement = 'provider'
		  AND provider_refund_id IS NOT NULL AND NOT cancel_ticket
		  AND (state IN ('succeeded', 'provider_pending')
		       OR (state = 'manual_review' AND failure_code = 'late_acceptance_over_budget'))
		ORDER BY updated_at DESC, id LIMIT 1 FOR UPDATE`, *parked.TicketID, parked.ID, pay.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var owned bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM refunds WHERE ticket_id = $1 AND cancel_ticket
		AND state NOT IN ('failed', 'rejected'))`, *parked.TicketID).Scan(&owned); err != nil {
		return nil, err
	}
	if owned {
		return nil, nil // another live refund speaks for the ticket
	}
	restored := false
	if sib.State == StateManualReview {
		over, err := overBudget(ctx, tx, sib, pay)
		if err != nil || over {
			return nil, err
		}
		sib, err = scanRefund(tx.QueryRow(ctx, `UPDATE refunds
			SET state = CASE WHEN provider_status = 'succeeded' THEN 'succeeded' ELSE 'provider_pending' END,
			    succeeded_at = CASE WHEN provider_status = 'succeeded' THEN COALESCE(succeeded_at, now()) ELSE succeeded_at END,
			    cancel_ticket = true, failure_code = NULL, failure_reason = NULL, settled_at = NULL,
			    alert_due_at = now(), alert_attempts = 0, review_alerted_at = NULL, updated_at = now()
			WHERE id = $1 RETURNING `+refundColumns, sib.ID))
		if err != nil {
			return nil, err
		}
		restored = true
	} else {
		sib, err = scanRefund(tx.QueryRow(ctx, `UPDATE refunds SET cancel_ticket = true, settled_at = NULL, updated_at = now()
			WHERE id = $1 RETURNING `+refundColumns, sib.ID))
		if err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE refunds SET failure_reason = format($2::text, $3::text) WHERE id = $1`,
		parked.ID, reasonBudgetTakenHandedOver, sib.ID.String()); err != nil {
		return nil, err
	}
	if err := e.writeAudit(ctx, tx, audit.Event{ActorType: "system", Action: "v1.refund.cancellation_handed_over",
		ResourceType: "refund", ResourceID: sib.ID.String(),
		Metadata: map[string]any{"from_refund_id": parked.ID.String(), "ticket_id": parked.TicketID.String(),
			"order_id": uuidString(sib.OrderID), "state": sib.State, "restored_from_review": restored}}); err != nil {
		return nil, err
	}
	return &sib, nil
}

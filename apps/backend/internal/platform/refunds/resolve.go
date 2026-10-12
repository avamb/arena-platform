// resolve.go — an operator closes a refund parked in manual_review after
// checking the provider's dashboard (spec 36 §8). It lives in the engine
// because nothing outside it may set an engine refund to failed or
// succeeded: the operator states what they SAW, and the engine applies the
// matching transition under the payment's lock.
package refunds

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// Resolution outcomes: what the operator found at the provider.
const (
	ResolveRefunded    = "refunded"
	ResolveNotRefunded = "not_refunded"
	// ResolveRefundedBeyondPayment: the money of this refund really went back
	// although the payment's refunds already cover what was paid (the buyer
	// was refunded twice). The refund is recorded as succeeded so the books
	// show what happened; nothing is cancelled because of it.
	ResolveRefundedBeyondPayment = "refunded_beyond_payment"
)

// CodeNotInReview: only a refund in manual_review can be resolved.
const CodeNotInReview = "refund.not_in_review"

// CodeNeverSent: the refund never reached the provider, so no money can have
// gone back through it.
const CodeNeverSent = "refund.never_sent"

// CodeOverBudget: settling the refund would put the payment's refunds above
// what was paid.
const CodeOverBudget = "refund.over_budget"

// ResolveInput is one manual closure.
type ResolveInput struct {
	OrgID    uuid.UUID
	RefundID uuid.UUID
	Outcome  string
	Note     string
	Actor    Actor
	Via      string
}

const maxResolveNote = 1000

// Resolve closes a manual_review refund. refunded: the money went back at
// the provider, so the refund becomes succeeded and settles like any accepted
// refund (its ticket is cancelled only if the refund was meant to cancel it).
// refunded_beyond_payment: the money went back although the payment was
// already covered; recorded as succeeded, nothing is cancelled. not_refunded:
// the provider shows this refund did not go through; it becomes failed and
// its ticket can be refunded again, which sends a NEW refund.
func (e *Engine) Resolve(ctx context.Context, in ResolveInput) (Refund, error) {
	bad := func(msg string) error { return refusal(http.StatusBadRequest, CodeInvalidRequest, msg, nil) }
	note := strings.TrimSpace(in.Note)
	switch {
	case in.Outcome != ResolveRefunded && in.Outcome != ResolveNotRefunded && in.Outcome != ResolveRefundedBeyondPayment:
		return Refund{}, bad("outcome must be refunded, not_refunded or refunded_beyond_payment")
	case note == "":
		return Refund{}, bad("a note saying what was checked is required")
	case len([]rune(note)) > maxResolveNote:
		return Refund{}, bad("note is too long")
	}
	var (
		out Refund
		pay Payment
	)
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		var piID *uuid.UUID
		err := tx.QueryRow(ctx, `SELECT payment_intent_id FROM refunds WHERE id = $1 AND org_id = $2`,
			in.RefundID, in.OrgID).Scan(&piID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && piID == nil) {
			return refusal(http.StatusNotFound, CodeRefundNotFound, "refund not found", nil)
		}
		if err != nil {
			return err
		}
		if err := LockPayment(ctx, tx, *piID); err != nil {
			return err
		}
		cur, err := getRefund(ctx, tx, in.RefundID, true)
		if err != nil {
			return err
		}
		if cur.Provider == nil || cur.State != StateManualReview {
			return refusal(http.StatusConflict, CodeNotInReview, "only a refund waiting for manual review can be resolved",
				map[string]any{"state": cur.State})
		}
		if pay, err = getPayment(ctx, tx, *piID); err != nil {
			return err
		}
		if in.Outcome == ResolveRefunded || in.Outcome == ResolveRefundedBeyondPayment {
			// Nothing was sent: the money cannot have gone back through this
			// row. (If the buyer was refunded by hand at the provider, that
			// refund is its own row - the dashboard webhook records it.)
			if cur.ProviderAttempts == 0 && cur.ProviderRefundID == nil {
				return refusal(http.StatusConflict, CodeNeverSent,
					"this refund never reached the provider, so it cannot be closed as refunded; retry it or close it as not refunded",
					map[string]any{"state": cur.State})
			}
		}
		if in.Outcome == ResolveRefundedBeyondPayment {
			// Only for a refund that really is beyond the payment: anything
			// else is closed as refunded.
			var live int64
			if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(amount), 0)::bigint FROM refunds
				WHERE payment_intent_id = $1 AND state NOT IN ('failed', 'rejected') AND `+countsAgainstBudgetSQL, *piID).Scan(&live); err != nil {
				return err
			}
			if live <= pay.Amount {
				return refusal(http.StatusConflict, CodeInvalidRequest,
					"this refund fits the payment: close it as refunded",
					map[string]any{"refunds_total": live, "paid": pay.Amount})
			}
		}
		if in.Outcome == ResolveRefunded {
			// Settling puts the amount into the payment's live sum: under the
			// lock, it must still fit what was paid.
			var live int64
			if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(amount), 0)::bigint FROM refunds
				WHERE payment_intent_id = $1 AND state NOT IN ('failed', 'rejected') AND `+countsAgainstBudgetSQL, *piID).Scan(&live); err != nil {
				return err
			}
			if live > pay.Amount {
				return refusal(http.StatusConflict, CodeOverBudget,
					"the refunds of this payment already add up to more than was paid; check the provider dashboard before closing this one",
					map[string]any{"refunds_total": live, "paid": pay.Amount})
			}
		}
		// The operator's note goes to the audit trail only. The row keeps
		// the reason it was parked with, except that a refund the operator
		// found refunded carries no failure at all.
		state := StateFailed
		clearFailure := false
		if in.Outcome == ResolveRefunded || in.Outcome == ResolveRefundedBeyondPayment {
			state, clearFailure = StateSucceeded, true
		}
		out, err = scanRefund(tx.QueryRow(ctx, `UPDATE refunds SET state = $2,
			failure_code = CASE WHEN $3 THEN NULL ELSE failure_code END,
			failure_reason = CASE WHEN $3 THEN NULL ELSE failure_reason END,
			provider_status = CASE WHEN $3 THEN 'succeeded' ELSE provider_status END,
			succeeded_at = CASE WHEN $3 THEN now() ELSE succeeded_at END,
			failed_at = CASE WHEN $3 THEN NULL ELSE now() END,
			settled_at = CASE WHEN $3 THEN NULL ELSE settled_at END,
			cancel_ticket = CASE WHEN $4 THEN false ELSE cancel_ticket END,
			alert_due_at = NULL, updated_at = now()
			WHERE id = $1 AND state = 'manual_review' RETURNING `+refundColumns, in.RefundID, state, clearFailure,
			in.Outcome == ResolveRefundedBeyondPayment))
		if err != nil {
			return err
		}
		return e.writeAudit(ctx, tx, audit.Event{ActorType: in.Actor.Type, ActorID: in.Actor.ID,
			Action: "v1.refund.resolve", ResourceType: "refund", ResourceID: out.ID.String(),
			Metadata: map[string]any{"outcome": in.Outcome, "state": state, "via": in.Via, "note": note,
				"previous_failure_code": deref(cur.FailureCode), "previous_failure_reason": deref(cur.FailureReason)}})
	})
	if err != nil {
		return Refund{}, err
	}
	if out.State == StateSucceeded {
		e.settle(ctx, out, pay)
	}
	return out, nil
}

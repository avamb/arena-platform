// drive.go — spec 36 §7 steps 3–4: claim, call the provider OUTSIDE any
// transaction, record the answer, and only then cancel the ticket.
package refunds

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// Call outcomes (the RefundProviderCallsTotal label set — fixed, bounded).
const (
	outcomeSucceeded = "succeeded"
	outcomePending   = "pending"
	outcomeDeclined  = "declined"
	outcomeUnknown   = "unknown"
)

// callOutcome is what one provider call ended in.
type callOutcome struct {
	kind   string // outcome* constant
	result payments.RefundResult
	// code/message explain a declined or unknown outcome.
	code    string
	message string
	// chargeRef is a reference the module resolved and arena must store.
	chargeRef string
	// review: a refusal that itself says the money may already be back
	// (payments.RefundDeclinedError.NeedsReview) — manual_review, never
	// failed.
	review bool
}

// hadUnknownOutcome reports whether an EARLIER call of this refund ended
// without an answer. Its request may have reached the provider and returned
// the money, so a later refusal (a config error, a decline, "already
// refunded") proves nothing about the money: such a refund goes to
// manual_review, never to failed — failed would free the ticket for a
// second refund (PAY-03 review H1). r is the row as claimed for the current
// call, so the current attempt is already counted.
func hadUnknownOutcome(r Refund) bool {
	return r.ProviderAttempts > 1 || deref(r.ProviderStatus) == providerStatusUnknown
}

const providerStatusUnknown = "unknown_outcome"

// reviewAlertSQL is the alert bookkeeping of every move INTO manual_review:
// an ops alert is owed from now (refund.sweep's alert pass sends it once).
const reviewAlertSQL = `alert_due_at = now(), review_alerted_at = NULL, alert_attempts = 0`

// ErrNotClaimable: the refund is not waiting for a provider call (already
// answered, not approved yet, not an engine refund) or a call is in flight.
var ErrNotClaimable = errors.New("refunds: refund is not waiting for a provider call")

// Drive sends one provider_pending refund to its provider and applies the
// answer. It is safe to call concurrently and repeatedly: only one caller
// claims the call, the idempotency key is the refund id, and a refund that
// is not waiting for a call is returned unchanged.
func (e *Engine) Drive(ctx context.Context, id uuid.UUID) (Refund, error) {
	r, _, err := e.drive(ctx, id)
	return r, err
}

// drive is Drive that also reports whether a provider call was made: a
// refund that was not claimable (in flight elsewhere, already answered) is
// "skipped", never an unanswered call (third review, L1).
func (e *Engine) drive(ctx context.Context, id uuid.UUID) (Refund, bool, error) {
	r, pay, err := e.claim(ctx, id)
	if errors.Is(err, ErrNotClaimable) {
		cur, gerr := e.GetRefund(ctx, id)
		if gerr != nil {
			return Refund{}, false, gerr
		}
		return cur, false, nil
	}
	if err != nil {
		return Refund{}, false, err
	}
	out := e.call(ctx, r, pay)
	// The provider's answer is recorded even when the caller's context ended
	// meanwhile (a closed HTTP request, the sweep's pass deadline): losing
	// an acceptance would leave a ticket valid for money already returned.
	applyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.callTimeout)
	defer cancel()
	updated, err := e.apply(applyCtx, ctx, r, pay, out)
	return updated, true, err
}

// claim stamps the call marker under the payment's lock. A refund is
// claimable when it is an engine refund in provider_pending with no
// provider refund id, no call of it started within CallStaleAfter, no
// OTHER refund of the same payment is mid-call (one call per payment at a
// time, spec §7 step 3), and its FIRST provider attempt (or, never
// attempted, its approval) is less than StuckAfter (23 hours) old: Stripe
// keeps an idempotency key for 24 hours from the first request, so a later
// re-POST with the same key could create a SECOND refund; the hour of
// margin covers a POST that lands a call timeout after the check. Such a
// row is only ever parked (second review item 2, third review M2).
// Before claiming, the budget is checked again (parkOverBudget, fourth
// review M-b): a refund that would refund more than was paid is parked, not
// sent.
func (e *Engine) claim(ctx context.Context, id uuid.UUID) (Refund, Payment, error) {
	var (
		r      Refund
		pay    Payment
		parked bool
	)
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		var piID *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT payment_intent_id FROM refunds WHERE id = $1`, id).Scan(&piID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotClaimable
			}
			return err
		}
		if piID == nil {
			return ErrNotClaimable
		}
		if err := LockPayment(ctx, tx, *piID); err != nil {
			return err
		}
		var busy bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM refunds
			WHERE  payment_intent_id = $1 AND id <> $2 AND state = 'provider_pending'
			  AND  provider IS NOT NULL AND provider_refund_id IS NULL
			  AND  provider_attempted_at > now() - $3::interval)`,
			*piID, id, intervalText(CallStaleAfter)).Scan(&busy); err != nil {
			return err
		}
		if busy {
			return ErrNotClaimable
		}
		var err error
		if pay, err = getPayment(ctx, tx, *piID); err != nil {
			return err
		}
		if p, err := e.parkOverBudget(ctx, tx, id, pay); err != nil || p != nil {
			if p != nil {
				r, parked = *p, true
			}
			return err // the park commits
		}
		claimed, err := scanRefund(tx.QueryRow(ctx, `
			UPDATE refunds
			SET    provider_attempts = provider_attempts + 1, provider_attempted_at = now(),
			       first_attempted_at = COALESCE(first_attempted_at, now()), updated_at = now()
			WHERE  id = $1 AND settlement = 'provider' AND state = 'provider_pending'
			  AND  provider IS NOT NULL AND provider_refund_id IS NULL
			  AND  (provider_attempted_at IS NULL OR provider_attempted_at <= now() - $2::interval)
			  AND  COALESCE(first_attempted_at, approved_at, created_at) > now() - $3::interval
			RETURNING `+refundColumns, id, intervalText(CallStaleAfter), intervalText(StuckAfter)))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotClaimable
		}
		if err != nil {
			return err
		}
		r = claimed
		return nil
	})
	if err == nil && parked {
		e.logReview(r)
		return r, pay, ErrNotClaimable
	}
	return r, pay, err
}

// parkOverBudget is the claim's budget re-check (fourth review, M-b). The
// budget is checked when a refund is created, but a LATE acceptance of a
// refund that had been marked failed brings that refund's money back into
// the payment's live sum after its replacement was created: the replacement
// must then never reach the provider, or the buyer is refunded twice. Under
// the payment's lock the caller holds, a waiting refund (provider_pending,
// no provider refund id, no call in flight) that would push the live
// refunds of its payment or ticket past what was paid is parked in
// manual_review with an alert instead of being sent. It returns the parked
// row, or nil when the refund is within budget or not waiting. Nothing here
// cancels a ticket (owner decision, sixth review: money first, never
// cancel on a guess) — the reasons and the alert tell a person to.
func (e *Engine) parkOverBudget(ctx context.Context, tx pgx.Tx, id uuid.UUID, pay Payment) (*Refund, error) {
	cur, err := getRefund(ctx, tx, id, true)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if cur.State != StateProviderPending || cur.ProviderRefundID != nil || cur.Provider == nil {
		return nil, nil
	}
	over, err := overBudget(ctx, tx, cur, pay)
	if err != nil || !over {
		return nil, err
	}
	// A refund that was never sent cannot have moved any money: it stops
	// counting against the budget. One whose earlier call ended without an
	// answer may have created the refund at the provider under its
	// idempotency key: it keeps counting, under a code of its own (fifth
	// review, M-1). A never-sent refund also gives its cancel_ticket up: it
	// will never return money, so a dead flag must not block the ticket.
	code, reason := failureBudgetTaken, reasonBudgetTakenNeverSent
	if cur.ProviderAttempts > 0 {
		code, reason = failureBudgetAfterUnanswered, reasonBudgetAfterUnanswered
	}
	parked, err := scanRefund(tx.QueryRow(ctx, `UPDATE refunds SET state = 'manual_review',
		failure_code = $3, failure_reason = $4,
		cancel_ticket = CASE WHEN provider_attempts = 0 THEN false ELSE cancel_ticket END,
		`+reviewAlertSQL+`, updated_at = now()
		WHERE id = $1 AND settlement = 'provider' AND state = 'provider_pending' AND provider_refund_id IS NULL
		  AND (provider_attempted_at IS NULL OR provider_attempted_at <= now() - $2::interval)
		RETURNING `+refundColumns, id, intervalText(CallStaleAfter), code, reason))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // a call is in flight; the claim below refuses it too
	}
	if err != nil {
		return nil, err
	}
	if err := e.writeAudit(ctx, tx, audit.Event{ActorType: "system", Action: "v1.refund.manual_review",
		ResourceType: "refund", ResourceID: parked.ID.String(),
		Metadata: map[string]any{"reason": code, "order_id": uuidString(parked.OrderID),
			"amount": parked.Amount, "currency": parked.Currency, "provider": deref(parked.Provider),
			"provider_attempts": parked.ProviderAttempts}}); err != nil {
		return nil, err
	}
	if parked.ProviderAttempts == 0 {
		if err := noteHeldBackReplacement(ctx, tx, &parked, pay); err != nil {
			return nil, err
		}
	}
	return &parked, nil
}

// countsAgainstBudgetSQL is true for a live refund row that counts against
// its payment's and ticket's budget: every row except one the engine parked
// BEFORE any provider call — held back for its budget (fifth review, M-1)
// or stuck unsent for 23 hours (sixth review, LOW-3) — because it moved no
// money; an attempted row may have moved money and keeps counting. The
// exclusion holds only while the row is still parked: once an operator
// resolves it (to succeeded, say) it counts again.
// gen.SumNonFailedRefundsByIntent, the flat routes' sum, carries the same
// rule (fifth review, LOW d). Unqualified: use it where the refunds row is
// the innermost table.
const countsAgainstBudgetSQL = `NOT (state = 'manual_review' AND provider_attempts = 0 AND provider_refund_id IS NULL
	AND COALESCE(failure_code, '') IN ('` + failureBudgetTaken + `', 'stuck_provider_pending'))`

// deterministicModuleError reports an error that repeating the call can
// never change and that proves the provider was NOT reached: the module is
// unknown or only declared, or the module refused the request before any
// network call (amount, missing charge reference). PAY-03 second review,
// item 6: such a refund goes to manual_review with an alert at once instead
// of being retried every minute for a day.
func deterministicModuleError(err error) bool {
	return errors.Is(err, payments.ErrUnknownProvider) ||
		errors.Is(err, payments.ErrModuleNotImplemented) ||
		errors.Is(err, payments.ErrRefundAmountInvalid) ||
		errors.Is(err, payments.ErrRefundExceedsPayment) ||
		errors.Is(err, payments.ErrRefundChargeRefMissing)
}

// call talks to the provider. It never holds a transaction. ONE timeout
// (callTimeout) bounds everything it does: building the module (which reads
// the organization's configuration), resolving the charge reference and the
// refund itself — a hung database or provider must not hold a claim, a
// request or a sweep pass open beyond it.
func (e *Engine) call(ctx context.Context, r Refund, pay Payment) callOutcome {
	callCtx, cancel := context.WithTimeout(ctx, e.callTimeout)
	defer cancel()

	provider := deref(r.Provider)
	module, err := e.modules.Build(callCtx, r.OrgID, provider)
	if err != nil {
		var cfg *ConfigError
		switch {
		case errors.As(err, &cfg):
			// No usable configuration is not a provider verdict: a human
			// fixes the organization's setup (third review, L3).
			return callOutcome{kind: outcomeDeclined, review: true, code: cfg.Code, message: cfg.Message}
		case deterministicModuleError(err):
			return callOutcome{kind: outcomeDeclined, review: true, code: "payment_module_unavailable", message: err.Error()}
		}
		return callOutcome{kind: outcomeUnknown, code: "module_unavailable", message: err.Error()}
	}
	refunder, ok := module.(payments.Refunder)
	if !ok {
		return callOutcome{kind: outcomeDeclined, review: true, code: failureCodeUnknownProvider, message: "the payment module cannot refund"}
	}

	var out callOutcome
	chargeRef := deref(pay.ProviderChargeRef)
	if chargeRef == "" {
		// Spec §3/§7: a payment made before migration 0103 (or whose webhook
		// carried no payment_intent) only knows its cs_…; the module reads
		// the pi_… back and arena stores it before refunding.
		if resolver, ok := module.(payments.ChargeRefResolver); ok && deref(pay.ProviderPaymentID) != "" {
			resolved, rerr := resolver.ResolveChargeRef(callCtx, deref(pay.ProviderPaymentID))
			if rerr != nil {
				if code, msg, declined := payments.RefundDeclined(rerr); declined {
					// The charge reference could not be found: the refund
					// never reached the refund endpoint (third review, L3).
					return callOutcome{kind: outcomeDeclined, code: code, message: msg, review: true}
				}
				return callOutcome{kind: outcomeUnknown, code: "charge_ref_lookup_failed", message: rerr.Error()}
			}
			chargeRef = resolved
			out.chargeRef = resolved
		}
	}

	res, err := refunder.Refund(callCtx, payments.RefundRequest{
		ProviderChargeRef:  chargeRef,
		AmountMinor:        r.Amount,
		PaymentAmountMinor: pay.Amount,
		Currency:           r.Currency,
		Reason:             deref(r.Reason),
		IdempotencyKey:     r.ID.String(),
		Metadata:           payments.RefundMetadata{RefundID: r.ID.String(), OrderID: uuidString(r.OrderID)},
	})
	switch {
	case err != nil:
		if code, msg, declined := payments.RefundDeclined(err); declined {
			out.kind, out.code, out.message = outcomeDeclined, code, msg
			// A refusal the module raised BEFORE any network call (amount,
			// charge reference) is a configuration or data problem a human
			// must look at, not a provider verdict (second review, item 6).
			out.review = payments.RefundDeclineNeedsReview(err) || deterministicModuleError(err)
		} else {
			out.kind, out.code, out.message = outcomeUnknown, "provider_unavailable", err.Error()
		}
	case res.Status == payments.RefundSucceeded:
		out.kind, out.result = outcomeSucceeded, res
	case res.Status == payments.RefundFailed:
		out.kind, out.result, out.code, out.message = outcomeDeclined, res, res.FailureCode, res.FailureMessage
		if out.code == "" {
			out.code = "failed"
		}
	default:
		out.kind, out.result = outcomePending, res
	}
	if (out.kind == outcomeSucceeded || out.kind == outcomePending) && res.ProviderRefundID == "" {
		// An acceptance without an id cannot be tracked or looked up; treat
		// it as unknown so the sweep asks again with the same key.
		out.kind, out.code, out.message = outcomeUnknown, "provider_refund_id_missing", "the provider accepted the refund without an id"
	}
	return out
}

// apply records the outcome, then — strictly after the commit, and only
// when the provider accepted — settles the tickets.
func (e *Engine) apply(ctx, stop context.Context, claimed Refund, pay Payment, out callOutcome) (Refund, error) {
	e.observe(out.kind)
	var (
		updated Refund
		changed bool
		revived bool
	)
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		if err := LockPayment(ctx, tx, pay.ID); err != nil {
			return err
		}
		if out.chargeRef != "" {
			if _, err := tx.Exec(ctx, `UPDATE payment_intents SET provider_charge_ref = $2, updated_at = now()
				WHERE id = $1 AND provider_charge_ref IS NULL`, pay.ID, out.chargeRef); err != nil {
				return err
			}
		}
		cur, err := getRefund(ctx, tx, claimed.ID, true)
		if err != nil {
			return err
		}
		late := false
		accepted := out.kind == outcomeSucceeded || out.kind == outcomePending
		switch {
		case cur.State == StateProviderPending && cur.ProviderRefundID == nil:
			// Judged on the LOCKED row, never on the claim snapshot: another
			// attempt may have recorded an unknown outcome while this call
			// was in flight (second review, item 3).
			updated, err = recordOutcome(ctx, tx, cur, out, pay, false)
		case cur.ProviderRefundID == nil && accepted && (cur.State == StateManualReview || cur.State == StateFailed):
			// The row left provider_pending while this call was in flight:
			// the sweep or an operator parked it, or another attempt's
			// refusal failed it. The provider's ACCEPTANCE is the truth
			// about the money and must never be lost: record it and settle,
			// so the ticket is cancelled (review H2). Reviving a FAILED row
			// also owes an ops alert — the failure may already have let a
			// second refund of the same ticket through.
			late, revived = true, cur.State == StateFailed
			over := false
			if revived {
				if over, err = overBudget(ctx, tx, cur, pay); err != nil {
					return err
				}
			}
			if over {
				// The failure let another refund take this money's place:
				// counting this one as an ordinary success would hide a
				// double refund. Park it with its provider id (third
				// review, M3); it does not cancel or publish anything.
				updated, err = recordOverBudget(ctx, tx, cur, out)
			} else {
				updated, err = recordOutcome(ctx, tx, cur, out, pay, revived)
			}
		default:
			// Someone else (a webhook, a concurrent drive) recorded an
			// answer first, or the row left provider_pending and this
			// answer is not an acceptance: keep the row, audit the answer.
			updated = cur
			return e.writeAudit(ctx, tx, audit.Event{
				ActorType: "system", Action: "v1.refund.provider_late_answer",
				ResourceType: "refund", ResourceID: cur.ID.String(),
				Metadata: map[string]any{"outcome": out.kind, "state": cur.State, "failure_code": out.code,
					"provider_refund_id": out.result.ProviderRefundID, "order_id": uuidString(cur.OrderID)},
			})
		}
		if err != nil {
			return err
		}
		changed = true
		return e.writeAudit(ctx, tx, audit.Event{
			ActorType: "system", Action: "v1.refund.provider_result",
			ResourceType: "refund", ResourceID: cur.ID.String(),
			Metadata: map[string]any{
				"order_id": uuidString(cur.OrderID), "ticket_id": uuidString(cur.TicketID),
				"payment_intent_id": pay.ID.String(), "provider": deref(cur.Provider),
				"outcome": out.kind, "state": updated.State, "amount": cur.Amount, "currency": cur.Currency,
				"provider_refund_id": deref(updated.ProviderRefundID), "failure_code": out.code,
				"attempt": claimed.ProviderAttempts, "late_answer": late,
			},
		})
	})
	if err != nil {
		return Refund{}, err
	}
	if changed {
		e.logger.Info("refunds: provider answered",
			"refund_id", updated.ID.String(), "outcome", out.kind, "state", updated.State)
		if updated.State == StateManualReview {
			e.logReview(updated)
		}
		if revived && updated.State != StateManualReview {
			e.logger.Error("refunds: REFUND NEEDS MANUAL REVIEW",
				"event", "refund_manual_review", "refund_id", updated.ID.String(), "order_id", uuidString(updated.OrderID),
				"amount", updated.Amount, "currency", updated.Currency,
				"failure_code", "accepted_after_failure")
		}
		e.settleUntil(ctx, stop, updated, pay)
	}
	return updated, nil
}

// logReview is THE log line an operator searches for: every refund the
// engine moves to manual_review writes it, in whichever process moved it.
// refund.sweep also sends one ops alert per such row (review_alerted_at).
func (e *Engine) logReview(r Refund) {
	e.logger.Error("refunds: REFUND NEEDS MANUAL REVIEW",
		"event", "refund_manual_review", "refund_id", r.ID.String(), "order_id", uuidString(r.OrderID),
		"amount", r.Amount, "currency", r.Currency, "failure_code", deref(r.FailureCode))
}

// acceptAlertSQL is the alert bookkeeping of an ACCEPTED outcome: leaving
// manual_review clears the last alert, so a later return to manual_review
// is announced again (second review, item 4). An alert is owed when a
// failed row is revived ($4), and when an operator was ALREADY told about
// this refund (review_alerted_at, read before the update): the follow-up
// says it was accepted late (third review, M5).
const acceptAlertSQL = `alert_due_at = CASE WHEN $4::boolean OR review_alerted_at IS NOT NULL THEN now() END,
	alert_attempts = CASE WHEN $4::boolean OR review_alerted_at IS NOT NULL THEN 0 ELSE alert_attempts END,
	review_alerted_at = NULL`

// overBudget reports whether recording cur as accepted would push the live
// refunds of its payment, or of its ticket, past what was paid. Read under
// the payment's advisory lock the caller holds.
func overBudget(ctx context.Context, tx pgx.Tx, cur Refund, pay Payment) (bool, error) {
	var payUsed, ticketUsed int64
	var ticketPrice *int64
	err := tx.QueryRow(ctx, `SELECT
		(SELECT COALESCE(SUM(amount), 0)::bigint FROM refunds
		  WHERE payment_intent_id = $1 AND id <> $2 AND state NOT IN ('failed', 'rejected')
		    AND `+countsAgainstBudgetSQL+`),
		(SELECT COALESCE(SUM(amount), 0)::bigint FROM refunds
		  WHERE $3::uuid IS NOT NULL AND ticket_id = $3 AND id <> $2 AND state NOT IN ('failed', 'rejected')
		    AND `+countsAgainstBudgetSQL+`),
		(SELECT oi.total FROM order_items oi WHERE $3::uuid IS NOT NULL AND oi.ticket_id = $3 LIMIT 1)`,
		pay.ID, cur.ID, cur.TicketID).Scan(&payUsed, &ticketUsed, &ticketPrice)
	if err != nil {
		return false, err
	}
	if payUsed+cur.Amount > pay.Amount {
		return true, nil
	}
	return ticketPrice != nil && ticketUsed+cur.Amount > *ticketPrice, nil
}

// recordOverBudget parks a late acceptance that would exceed the payment or
// the ticket: the provider id is kept and an alert is owed. cancel_ticket is
// dropped because the refund that took its place holds it, and nothing ever
// gives it back: a person cancels the ticket (sixth review — money first,
// never cancel a ticket on a guess).
func recordOverBudget(ctx context.Context, tx pgx.Tx, cur Refund, out callOutcome) (Refund, error) {
	return scanRefund(tx.QueryRow(ctx, `UPDATE refunds SET state = 'manual_review', failed_at = NULL,
		provider_refund_id = $2, provider_status = $3, cancel_ticket = false,
		failure_code = 'late_acceptance_over_budget', failure_reason = $4,
		`+reviewAlertSQL+`, updated_at = now() WHERE id = $1 RETURNING `+refundColumns,
		cur.ID, out.result.ProviderRefundID, string(out.result.Status), reasonLateOverBudget))
}

// reviveCancelSQL keeps cancel_ticket only when no other live cancelling
// refund of the ticket exists: a failed row revived by a late acceptance
// must not collide with the refund that replaced it.
const reviveCancelSQL = `cancel_ticket = cancel_ticket AND NOT EXISTS (
	SELECT 1 FROM refunds o WHERE o.ticket_id = refunds.ticket_id AND o.id <> refunds.id
	  AND o.cancel_ticket AND o.state NOT IN ('failed', 'rejected'))`

// recordOutcome writes one outcome onto the locked current row cur
// (provider_pending, or a parked/failed row a late acceptance revives).
func recordOutcome(ctx context.Context, tx pgx.Tx, cur Refund, out callOutcome, pay Payment, revivedFromFailed bool) (Refund, error) {
	var (
		sql  string
		args []any
	)
	switch out.kind {
	case outcomeSucceeded:
		sql = `UPDATE refunds SET state = 'succeeded', succeeded_at = now(), failed_at = NULL, provider_refund_id = $2,
		       provider_status = $3, failure_code = NULL, failure_reason = NULL, settled_at = NULL, ` + acceptAlertSQL + `, ` + reviveCancelSQL + `,
		       updated_at = now() WHERE id = $1 RETURNING ` + refundColumns
		args = []any{cur.ID, out.result.ProviderRefundID, string(out.result.Status), revivedFromFailed}
	case outcomePending:
		// state is set too: a late acceptance brings a parked row back.
		sql = `UPDATE refunds SET state = 'provider_pending', failed_at = NULL, provider_refund_id = $2, provider_status = $3,
		       failure_code = NULL, failure_reason = NULL, settled_at = NULL, ` + acceptAlertSQL + `, ` + reviveCancelSQL + `,
		       updated_at = now() WHERE id = $1 RETURNING ` + refundColumns
		args = []any{cur.ID, out.result.ProviderRefundID, string(out.result.Status), revivedFromFailed}
	case outcomeDeclined:
		if out.review || hadUnknownOutcome(cur) {
			// The money's whereabouts are not known (an earlier call had no
			// answer, or the provider says it was refunded already): never
			// failed — that would free the ticket for a second refund.
			reason := "the provider refused this attempt, but an earlier attempt had no answer and may have returned the money; check the provider dashboard: " + out.message
			if out.review {
				reason = "the refund needs a human; check the provider dashboard: " + out.message
			}
			sql = `UPDATE refunds SET state = 'manual_review', provider_refund_id = $2, provider_status = 'declined',
			       failure_code = $3, failure_reason = $4, ` + reviewAlertSQL + `, updated_at = now()
			       WHERE id = $1 RETURNING ` + refundColumns
			args = []any{cur.ID, strPtr(out.result.ProviderRefundID), truncate(out.code, 100), truncate(reason, 500)}
			break
		}
		sql = `UPDATE refunds SET state = 'failed', failed_at = now(), provider_refund_id = $2,
		       provider_status = 'failed', failure_code = $3, failure_reason = $4, updated_at = now()
		       WHERE id = $1 RETURNING ` + refundColumns
		args = []any{cur.ID, strPtr(out.result.ProviderRefundID), truncate(out.code, 100), truncate(out.message, 500)}
	default:
		// Unknown: nothing is known about the money. Stay provider_pending
		// without a provider refund id; refund.sweep calls again with the
		// same idempotency key once the claim goes stale.
		sql = `UPDATE refunds SET provider_status = '` + providerStatusUnknown + `', failure_code = $2, failure_reason = $3,
		       updated_at = now() WHERE id = $1 RETURNING ` + refundColumns
		args = []any{cur.ID, truncate(out.code, 100), truncate(out.message, 500)}
	}
	updated, err := scanRefund(tx.QueryRow(ctx, sql, args...))
	if err != nil {
		return Refund{}, err
	}
	// A ticket-less (order-level) refund the provider took for LESS than
	// the payment cannot be attributed to tickets by arena: flag every
	// active ticket of the order for review (AB-49 policy, the same as an
	// inbound partial refund). Written with the outcome, atomically.
	// A refund of the ticket-cancel route names its ticket (fourth review,
	// H-1), even when the route could not link it.
	if updated.Accepted() && updated.TicketID == nil && !updated.fromTicketCancel() &&
		updated.Amount < pay.Amount && pay.CheckoutSessionID != nil {
		attributed, err := ticketsLinkedTo(ctx, tx, updated.ID)
		if err != nil {
			return Refund{}, err
		}
		if !attributed {
			reason := "partial refund " + updated.ID.String() + " (" + strconv.FormatInt(updated.Amount, 10) + " " +
				updated.Currency + ") — attribute it to specific tickets"
			if _, err := tx.Exec(ctx, `UPDATE tickets SET review_hold = true, review_hold_reason = $2, updated_at = now()
				WHERE checkout_session_id = $1 AND status = 'active' AND review_hold = false`,
				*pay.CheckoutSessionID, reason); err != nil {
				return Refund{}, err
			}
		}
	}
	return updated, nil
}

func ticketsLinkedTo(ctx context.Context, tx pgx.Tx, refundID uuid.UUID) (bool, error) {
	var linked bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tickets WHERE refund_id = $1)`, refundID).Scan(&linked)
	return linked, err
}

func uuidString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func intervalText(d interface{ Seconds() float64 }) string {
	return fmt.Sprintf("%d seconds", int64(d.Seconds()))
}

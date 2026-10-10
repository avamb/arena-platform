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

// ErrNotClaimable: the refund is not waiting for a provider call (already
// answered, not approved yet, not an engine refund) or a call is in flight.
var ErrNotClaimable = errors.New("refunds: refund is not waiting for a provider call")

// Drive sends one provider_pending refund to its provider and applies the
// answer. It is safe to call concurrently and repeatedly: only one caller
// claims the call, the idempotency key is the refund id, and a refund that
// is not waiting for a call is returned unchanged.
func (e *Engine) Drive(ctx context.Context, id uuid.UUID) (Refund, error) {
	r, pay, err := e.claim(ctx, id)
	if errors.Is(err, ErrNotClaimable) {
		cur, gerr := e.GetRefund(ctx, id)
		if gerr != nil {
			return Refund{}, gerr
		}
		return cur, nil
	}
	if err != nil {
		return Refund{}, err
	}
	out := e.call(ctx, r, pay)
	// The provider's answer is recorded even when the caller's context ended
	// meanwhile (a closed HTTP request, the sweep's pass deadline): losing
	// an acceptance would leave a ticket valid for money already returned.
	applyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.callTimeout)
	defer cancel()
	return e.apply(applyCtx, r, pay, out)
}

// claim stamps the call marker under the payment's lock. A refund is
// claimable when it is an engine refund in provider_pending with no
// provider refund id, no call of it started within CallStaleAfter, and no
// OTHER refund of the same payment is mid-call (one call per payment at a
// time, spec §7 step 3).
func (e *Engine) claim(ctx context.Context, id uuid.UUID) (Refund, Payment, error) {
	var (
		r   Refund
		pay Payment
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
		claimed, err := scanRefund(tx.QueryRow(ctx, `
			UPDATE refunds
			SET    provider_attempts = provider_attempts + 1, provider_attempted_at = now(), updated_at = now()
			WHERE  id = $1 AND settlement = 'provider' AND state = 'provider_pending'
			  AND  provider IS NOT NULL AND provider_refund_id IS NULL
			  AND  (provider_attempted_at IS NULL OR provider_attempted_at <= now() - $2::interval)
			RETURNING `+refundColumns, id, intervalText(CallStaleAfter)))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotClaimable
		}
		if err != nil {
			return err
		}
		r = claimed
		pay, err = getPayment(ctx, tx, *piID)
		return err
	})
	return r, pay, err
}

// call talks to the provider. It never holds a transaction.
func (e *Engine) call(ctx context.Context, r Refund, pay Payment) callOutcome {
	provider := deref(r.Provider)
	module, err := e.modules.Build(ctx, r.OrgID, provider)
	if err != nil {
		var cfg *ConfigError
		if errors.As(err, &cfg) {
			return callOutcome{kind: outcomeDeclined, code: cfg.Code, message: cfg.Message}
		}
		return callOutcome{kind: outcomeUnknown, code: "module_unavailable", message: err.Error()}
	}
	refunder, ok := module.(payments.Refunder)
	if !ok {
		return callOutcome{kind: outcomeDeclined, code: failureCodeUnknownProvider, message: "the payment module cannot refund"}
	}

	callCtx, cancel := context.WithTimeout(ctx, e.callTimeout)
	defer cancel()

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
					return callOutcome{kind: outcomeDeclined, code: code, message: msg, review: payments.RefundDeclineNeedsReview(rerr)}
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
			out.review = payments.RefundDeclineNeedsReview(err)
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
func (e *Engine) apply(ctx context.Context, claimed Refund, pay Payment, out callOutcome) (Refund, error) {
	e.observe(out.kind)
	var (
		updated Refund
		changed bool
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
		switch {
		case cur.State == StateProviderPending && cur.ProviderRefundID == nil:
			updated, err = recordOutcome(ctx, tx, claimed, out, pay)
		case cur.ProviderRefundID == nil && cur.State == StateManualReview && (out.kind == outcomeSucceeded || out.kind == outcomePending):
			// The sweep parked the row while this call was in flight (or an
			// operator did). The provider's ACCEPTANCE is the truth about
			// the money and must never be lost: record it and settle, so
			// the ticket is cancelled (PAY-03 review H2).
			late = true
			updated, err = recordOutcome(ctx, tx, claimed, out, pay)
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
		e.settle(ctx, updated, pay)
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

// recordOutcome writes one outcome onto a provider_pending row.
func recordOutcome(ctx context.Context, tx pgx.Tx, cur Refund, out callOutcome, pay Payment) (Refund, error) {
	var (
		sql  string
		args []any
	)
	switch out.kind {
	case outcomeSucceeded:
		sql = `UPDATE refunds SET state = 'succeeded', succeeded_at = now(), provider_refund_id = $2,
		       provider_status = $3, failure_code = NULL, failure_reason = NULL, updated_at = now()
		       WHERE id = $1 RETURNING ` + refundColumns
		args = []any{cur.ID, out.result.ProviderRefundID, string(out.result.Status)}
	case outcomePending:
		// state is set too: a late acceptance brings a parked row back.
		sql = `UPDATE refunds SET state = 'provider_pending', provider_refund_id = $2, provider_status = $3,
		       failure_code = NULL, failure_reason = NULL, updated_at = now() WHERE id = $1 RETURNING ` + refundColumns
		args = []any{cur.ID, out.result.ProviderRefundID, string(out.result.Status)}
	case outcomeDeclined:
		if out.review || hadUnknownOutcome(cur) {
			// The money's whereabouts are not known (an earlier call had no
			// answer, or the provider says it was refunded already): never
			// failed — that would free the ticket for a second refund.
			reason := "the provider refused this attempt, but an earlier attempt had no answer and may have returned the money; check the provider dashboard: " + out.message
			if out.review {
				reason = "the provider says the money may already be back; check the provider dashboard: " + out.message
			}
			sql = `UPDATE refunds SET state = 'manual_review', provider_refund_id = $2, provider_status = 'declined',
			       failure_code = $3, failure_reason = $4, updated_at = now() WHERE id = $1 RETURNING ` + refundColumns
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
	if updated.Accepted() && updated.TicketID == nil && updated.Amount < pay.Amount && pay.CheckoutSessionID != nil {
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

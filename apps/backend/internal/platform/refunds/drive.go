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
}

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
	return e.apply(ctx, r, pay, out)
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
					return callOutcome{kind: outcomeDeclined, code: code, message: msg}
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
		if cur.State != StateProviderPending || cur.ProviderRefundID != nil {
			// Someone else (a webhook, a concurrent sweep) got there first.
			updated = cur
			return nil
		}
		updated, err = recordOutcome(ctx, tx, cur, out, pay)
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
				"attempt": cur.ProviderAttempts,
			},
		})
	})
	if err != nil {
		return Refund{}, err
	}
	if changed {
		e.logger.Info("refunds: provider answered",
			"refund_id", updated.ID.String(), "outcome", out.kind, "state", updated.State)
		e.settle(ctx, updated, pay)
	}
	return updated, nil
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
		sql = `UPDATE refunds SET provider_refund_id = $2, provider_status = $3, failure_code = NULL,
		       failure_reason = NULL, updated_at = now() WHERE id = $1 RETURNING ` + refundColumns
		args = []any{cur.ID, out.result.ProviderRefundID, string(out.result.Status)}
	case outcomeDeclined:
		sql = `UPDATE refunds SET state = 'failed', failed_at = now(), provider_refund_id = $2,
		       provider_status = 'failed', failure_code = $3, failure_reason = $4, updated_at = now()
		       WHERE id = $1 RETURNING ` + refundColumns
		args = []any{cur.ID, strPtr(out.result.ProviderRefundID), truncate(out.code, 100), truncate(out.message, 500)}
	default:
		// Unknown: nothing is known about the money. Stay provider_pending
		// without a provider refund id; refund.sweep calls again with the
		// same idempotency key once the claim goes stale.
		sql = `UPDATE refunds SET provider_status = 'unknown_outcome', failure_code = $2, failure_reason = $3,
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

// dashboard.go — a refund the organizer made in the provider's own dashboard
// (spec 36 section 7, PAY-05). The provider tells arena through the
// per-config webhook route; no refunds row of arena carries its id, so the
// engine writes one with origin 'provider_dashboard'. It is already
// succeeded and already settled: the money is back, and which tickets it
// covers is a human's call (the caller applies the AB-49 policy), so the
// engine's own settlement and repair passes must never touch it.
package refunds

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// ErrCannotConfirm: the provider's refund could not be read back, so
// nothing was written. The caller answers 500 and the provider redelivers.
var ErrCannotConfirm = errors.New("refunds: the provider's refund could not be confirmed")

// DashboardRefund is what RecordDashboardRefund decided.
type DashboardRefund struct {
	// Found: the refund belongs to a payment of this organization.
	Found bool
	// Created: a new row was written (false on a replayed delivery).
	Created bool
	// MaybeOurs: the provider's refund carries an arena refund id (it was
	// created by arena) that no row has recorded yet. Nothing is written:
	// the sweep reads the refund back when its call's outcome is known.
	MaybeOurs bool
	Refund    Refund
	Payment   Payment
	// Full: the live refunds of the payment now cover all of it.
	Full bool
	// Capped: the provider's amount exceeded what was still refundable and
	// the row was written with the remainder (an alert is owed).
	Capped bool
	// Unconfirmed: the refund belongs to a payment of this organization but
	// could not be read back from the provider after MaxDashboardConfirmAttempts
	// deliveries. Nothing is written; the operator is alerted (log) and the
	// delivery is acknowledged so the provider stops retrying.
	Unconfirmed bool
}

// MaxDashboardConfirmAttempts is how many deliveries of one dashboard refund
// may fail to be read back before arena stops asking the provider to retry.
const MaxDashboardConfirmAttempts = 8

// DashboardApply runs INSIDE the transaction that wrote the row, so the
// row and its consequences (ticket cancellation, order status) commit
// together or not at all. An error rolls everything back: the caller
// answers 500 and the provider redelivers.
type DashboardApply func(ctx context.Context, tx pgx.Tx, res DashboardRefund) error

// RecordDashboardRefund writes the row for a SUCCEEDED refund event of the
// provider that no arena refund carries. The payment is found by the charge
// reference the event names, inside the organization whose config verified
// the delivery. Before anything is written the refund is read back from the
// provider (RefundLookup): the event only says where to look, the amount,
// currency and payment come from the provider's own answer. A pending or
// failed refund writes nothing; the provider sends another event when it
// settles.
func (e *Engine) RecordDashboardRefund(ctx context.Context, orgID uuid.UUID, provider string, ev payments.NormalizedEvent, apply DashboardApply) (DashboardRefund, error) {
	var out DashboardRefund
	chargeRef := strings.TrimSpace(ev.ProviderChargeRef)
	refundID := strings.TrimSpace(ev.ProviderRefundID)
	if provider == "" || refundID == "" || ev.RefundStatus != payments.RefundSucceeded {
		return out, nil
	}
	if arenaID := strings.TrimSpace(ev.ArenaRefundID); arenaID != "" {
		// Created by arena. If this organization has that refund the engine
		// owns the outcome; if not, it belongs to another arena environment
		// sharing the provider account and is not ours.
		var exists bool
		if err := e.inTx(ctx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM refunds WHERE id::text = $1 AND org_id = $2)`,
				arenaID, orgID).Scan(&exists)
		}); err != nil {
			return out, err
		}
		if exists {
			out.MaybeOurs = true
			e.logger.Error("refunds: REFUND EVENT FOR AN ARENA REFUND THAT HAS NO PROVIDER ID RECORDED YET; the sweep reads it back",
				"event", "refund_event_before_record", "refund_id", arenaID, "provider_refund_id", refundID, "provider", provider)
		}
		return out, nil
	}
	// Look for the payment the event names BEFORE asking the provider: the
	// organizer's account also refunds payments of their other sites, and
	// those must cost neither a provider call nor a log line.
	if chargeRef != "" {
		known, err := e.knownPayment(ctx, orgID, provider, chargeRef)
		if err != nil {
			return out, err
		}
		if !known {
			return out, nil
		}
	}
	confirmed, ok, err := e.confirmProviderRefund(ctx, orgID, provider, refundID)
	if err != nil {
		if errors.Is(err, ErrCannotConfirm) && chargeRef != "" {
			attempts, cerr := e.noteUnconfirmed(ctx, orgID, provider, refundID, err)
			if cerr != nil {
				return out, errors.Join(err, cerr)
			}
			if attempts >= MaxDashboardConfirmAttempts {
				out.Unconfirmed = true
				e.logger.Error("refunds: A REFUND MADE AT THE PROVIDER COULD NOT BE CONFIRMED; nothing recorded, check the provider dashboard",
					"event", "dashboard_refund_unconfirmed", "provider", provider, "provider_refund_id", refundID,
					"charge_ref", chargeRef, "attempts", attempts, "error", err.Error())
				return out, nil
			}
		}
		return out, err
	}
	if !ok {
		return out, nil
	}
	if confirmed.ProviderChargeRef != "" {
		chargeRef = confirmed.ProviderChargeRef
	}
	if chargeRef == "" || confirmed.AmountMinor <= 0 {
		return out, nil
	}
	err = e.inTx(ctx, func(tx pgx.Tx) error {
		pay, err := scanPayment(tx.QueryRow(ctx, `SELECT `+paymentColumns+` FROM payment_intents
			WHERE org_id = $1 AND provider = $2 AND (provider_charge_ref = $3 OR provider_payment_id = $3)
			ORDER BY created_at DESC LIMIT 1`, orgID, provider, chargeRef))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // not a payment of this organization
		}
		if err != nil {
			return err
		}
		if err := LockPayment(ctx, tx, pay.ID); err != nil {
			return err
		}
		out.Found, out.Payment = true, pay
		if cur := strings.ToUpper(strings.TrimSpace(confirmed.Currency)); cur != "" && cur != strings.ToUpper(pay.Currency) {
			out.Found = false // not this payment's money
			return nil
		}
		// What is still refundable: the payment minus every refund that
		// counts against its budget (arena's own, earlier dashboard ones).
		var taken int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(amount), 0)::bigint FROM refunds
			WHERE payment_intent_id = $1 AND state <> 'failed' AND `+countsAgainstBudgetSQL, pay.ID).Scan(&taken); err != nil {
			return err
		}
		amount := confirmed.AmountMinor
		if left := pay.Amount - taken; amount > left {
			out.Capped = true
			amount = left
		}
		if amount <= 0 {
			// The payment is already fully refunded, yet the provider says
			// more money went back. A row would break the budget; nothing
			// is written and the log is the alert.
			out.Capped = true
			e.logger.Error("refunds: A REFUND MADE AT THE PROVIDER EXCEEDS WHAT WAS LEFT OF THE PAYMENT; nothing recorded, check the provider dashboard",
				"event", "dashboard_refund_over_payment", "provider", provider, "provider_refund_id", refundID,
				"payment_intent_id", pay.ID.String(), "provider_amount", confirmed.AmountMinor)
			return nil
		}
		var orderID *uuid.UUID
		if pay.CheckoutSessionID != nil {
			if err := tx.QueryRow(ctx, `SELECT id FROM orders WHERE checkout_session_id = $1 AND org_id = $2`,
				*pay.CheckoutSessionID, orgID).Scan(&orderID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		// An alert is owed when the row is capped, or when it is smaller
		// than the payment (tickets go under review hold).
		r, err := scanRefund(tx.QueryRow(ctx, `
			INSERT INTO refunds (payment_intent_id, org_id, order_id, amount, currency, reason, requested_by,
			                     state, settlement, provider, provider_refund_id, provider_status, origin,
			                     cancel_ticket, succeeded_at, settled_at)
			VALUES ($1, $2, $3, $4, $5, 'refund made in the provider dashboard', 'provider_dashboard',
			        'succeeded', 'provider', $6, $7, 'succeeded', 'provider_dashboard', false, now(), now())
			ON CONFLICT (provider, provider_refund_id) WHERE provider IS NOT NULL AND provider_refund_id IS NOT NULL DO NOTHING
			RETURNING `+refundColumns, pay.ID, orgID, orderID, amount, pay.Currency, provider, refundID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // a replayed delivery: already recorded
		}
		if err != nil {
			return err
		}
		out.Created, out.Refund = true, r
		var total int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(amount), 0)::bigint FROM refunds
			WHERE payment_intent_id = $1 AND state = 'succeeded'`, pay.ID).Scan(&total); err != nil {
			return err
		}
		out.Full = total >= pay.Amount
		if out.Capped || !out.Full {
			if _, err := tx.Exec(ctx, `UPDATE refunds SET alert_due_at = now() WHERE id = $1`, r.ID); err != nil {
				return err
			}
		}
		if err := e.writeAudit(ctx, tx, audit.Event{ActorType: "system", Action: "v1.refund.dashboard",
			ResourceType: "refund", ResourceID: r.ID.String(),
			Metadata: map[string]any{"provider": provider, "amount": r.Amount, "provider_amount": confirmed.AmountMinor,
				"currency": r.Currency, "order_id": uuidString(r.OrderID), "full": out.Full, "capped": out.Capped}}); err != nil {
			return err
		}
		if apply != nil {
			return apply(ctx, tx, out)
		}
		return nil
	})
	return out, err
}

// knownPayment reports whether chargeRef names a payment of this organization.
func (e *Engine) knownPayment(ctx context.Context, orgID uuid.UUID, provider, chargeRef string) (bool, error) {
	var known bool
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM payment_intents
			WHERE org_id = $1 AND provider = $2 AND (provider_charge_ref = $3 OR provider_payment_id = $3))`,
			orgID, provider, chargeRef).Scan(&known)
	})
	return known, err
}

// noteUnconfirmed records one delivery whose refund could not be read back
// and returns how many such deliveries this refund has had.
func (e *Engine) noteUnconfirmed(ctx context.Context, orgID uuid.UUID, provider, refundID string, cause error) (int, error) {
	var n int
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO audit_events (occurred_at, actor_type, action, resource_type, resource_id, metadata)
			VALUES (now(), 'system', 'v1.refund.dashboard_unconfirmed', 'provider_refund', $1,
			        jsonb_build_object('org_id', $2::text, 'provider', $3::text, 'error', $4::text))`,
			refundID, orgID, provider, truncate(cause.Error(), 300)); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_events
			WHERE action = 'v1.refund.dashboard_unconfirmed' AND resource_type = 'provider_refund'
			  AND resource_id = $1 AND metadata->>'org_id' = $2::text`, refundID, orgID).Scan(&n)
	})
	return n, err
}

// confirmProviderRefund reads the refund back from the provider. ok is false
// when it is not a succeeded refund of a provider that can be asked (nothing
// to record); a read that fails is ErrCannotConfirm so the delivery is
// retried rather than trusted.
func (e *Engine) confirmProviderRefund(ctx context.Context, orgID uuid.UUID, provider, refundID string) (payments.RefundResult, bool, error) {
	desc, hasModule := e.modules.Descriptor(provider)
	if !hasModule || !desc.Capabilities.RefundLookup {
		return payments.RefundResult{}, false, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, e.callTimeout)
	defer cancel()
	module, err := e.modules.Build(callCtx, orgID, provider)
	if err != nil {
		return payments.RefundResult{}, false, errors.Join(ErrCannotConfirm, err)
	}
	looker, ok := module.(payments.RefundLookup)
	if !ok {
		return payments.RefundResult{}, false, nil
	}
	res, err := looker.RefundStatus(callCtx, refundID)
	if errors.Is(err, payments.ErrRefundNotFound) {
		// The provider says there is no such refund: nothing to record, and
		// redelivering cannot change that.
		return payments.RefundResult{}, false, nil
	}
	if err != nil {
		return payments.RefundResult{}, false, errors.Join(ErrCannotConfirm, err)
	}
	if res.Status != payments.RefundSucceeded {
		return payments.RefundResult{}, false, nil
	}
	return res, true, nil
}

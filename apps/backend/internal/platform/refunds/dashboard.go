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

// ErrRefundInFlight: an arena refund of the same payment is mid-call or
// waiting for the answer of an unknown outcome, so the provider's refund may
// be that very refund. The caller answers 500 and the provider redelivers
// once the engine has recorded its own answer.
var ErrRefundInFlight = errors.New("refunds: an arena refund of this payment has not been answered yet")

// DashboardRefund is what RecordDashboardRefund decided.
type DashboardRefund struct {
	// Found: the refund belongs to a payment of this organization.
	Found bool
	// Created: a new row was written (false on a replayed delivery).
	Created bool
	Refund  Refund
	Payment Payment
	// Full: the live refunds of the payment now cover all of it.
	Full bool
}

// RecordDashboardRefund writes the row for a SUCCEEDED refund event of the
// provider that no arena refund carries. The payment is found by the charge
// reference (or payment id) the event names, inside the organization whose
// config verified the delivery. A pending or failed event writes nothing:
// the provider sends another event when it settles.
func (e *Engine) RecordDashboardRefund(ctx context.Context, orgID uuid.UUID, provider string, ev payments.NormalizedEvent) (DashboardRefund, error) {
	var out DashboardRefund
	chargeRef := strings.TrimSpace(ev.ProviderChargeRef)
	refundID := strings.TrimSpace(ev.ProviderRefundID)
	if provider == "" || refundID == "" || chargeRef == "" || ev.RefundStatus != payments.RefundSucceeded || ev.AmountMinor <= 0 {
		return out, nil
	}
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		pay, err := scanPayment(tx.QueryRow(ctx, `SELECT `+paymentColumns+` FROM payment_intents
			WHERE org_id = $1 AND provider = $2 AND (provider_charge_ref = $3 OR provider_payment_id = $3)
			ORDER BY created_at DESC LIMIT 1`, orgID, provider, chargeRef))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := LockPayment(ctx, tx, pay.ID); err != nil {
			return err
		}
		out.Found, out.Payment = true, pay
		if cur := strings.ToUpper(strings.TrimSpace(ev.Currency)); cur != "" && cur != strings.ToUpper(pay.Currency) {
			out.Found = false // not this payment's money
			return nil
		}
		var inflight bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM refunds WHERE payment_intent_id = $1
			AND provider IS NOT NULL AND state = 'provider_pending' AND provider_refund_id IS NULL)`, pay.ID).Scan(&inflight); err != nil {
			return err
		}
		if inflight {
			return ErrRefundInFlight
		}
		var orderID *uuid.UUID
		if pay.CheckoutSessionID != nil {
			if err := tx.QueryRow(ctx, `SELECT id FROM orders WHERE checkout_session_id = $1 AND org_id = $2`,
				*pay.CheckoutSessionID, orgID).Scan(&orderID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		r, err := scanRefund(tx.QueryRow(ctx, `
			INSERT INTO refunds (payment_intent_id, org_id, order_id, amount, currency, reason, requested_by,
			                     state, settlement, provider, provider_refund_id, provider_status, origin,
			                     cancel_ticket, succeeded_at, settled_at)
			VALUES ($1, $2, $3, $4, $5, 'refund made in the provider dashboard', 'provider_dashboard',
			        'succeeded', 'provider', $6, $7, 'succeeded', 'provider_dashboard', false, now(), now())
			ON CONFLICT (provider, provider_refund_id) WHERE provider IS NOT NULL AND provider_refund_id IS NOT NULL DO NOTHING
			RETURNING `+refundColumns, pay.ID, orgID, orderID, ev.AmountMinor, pay.Currency, provider, refundID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // a replayed delivery: already recorded
		}
		if err != nil {
			return err
		}
		out.Created, out.Refund = true, r
		var total int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(amount), 0) FROM refunds
			WHERE payment_intent_id = $1 AND state = 'succeeded'`, pay.ID).Scan(&total); err != nil {
			return err
		}
		out.Full = total >= pay.Amount
		return e.writeAudit(ctx, tx, audit.Event{ActorType: "system", Action: "v1.refund.dashboard",
			ResourceType: "refund", ResourceID: r.ID.String(),
			Metadata: map[string]any{"provider": provider, "amount": r.Amount, "currency": r.Currency,
				"order_id": uuidString(r.OrderID), "full": out.Full}})
	})
	return out, err
}

package refunds

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// ApplyProviderRefund records a provider's refund webhook on the refund it
// belongs to (spec 36 section 7, PAY-05). The refund is found by
// (provider, provider_refund_id) INSIDE the organization whose config
// verified the delivery, so one organizer's signature can never move another's
// refund. found is false when no refund of this organization carries that
// provider id: the caller acknowledges the event and does nothing else.
//
// The outcome goes through the same locked path as the sweep's read-back
// (applyStatus): only a provider_pending row moves, under the payment lock,
// and a repeated or reordered delivery changes nothing.
func (e *Engine) ApplyProviderRefund(ctx context.Context, orgID uuid.UUID, provider, providerRefundID string, res payments.RefundResult) (found bool, err error) {
	providerRefundID = strings.TrimSpace(providerRefundID)
	if providerRefundID == "" || provider == "" {
		return false, nil
	}
	var id uuid.UUID
	err = e.inTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id FROM refunds
			WHERE org_id = $1 AND provider = $2 AND provider_refund_id = $3`,
			orgID, provider, providerRefundID).Scan(&id)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	r, pay, err := e.refundAndPayment(ctx, id)
	if err != nil {
		return true, err
	}
	// The event body is only a hint: the refund is read back from the provider
	// before anything is closed or parked. A read that fails answers an error,
	// so the provider redelivers the event later.
	read, ok, err := e.readBack(ctx, r)
	if err != nil {
		return true, err
	}
	if ok {
		res = read
	}
	if r.State == StateSucceeded && res.Status == payments.RefundFailed {
		return true, e.reverseSucceeded(ctx, id, pay, res)
	}
	return true, e.applyStatus(ctx, id, pay, res)
}

// readBack asks the provider for the refund's current status. ok is false
// when the provider offers no read-back (the event is then all there is).
func (e *Engine) readBack(ctx context.Context, r Refund) (payments.RefundResult, bool, error) {
	if r.ProviderRefundID == nil {
		return payments.RefundResult{}, false, nil
	}
	desc, hasModule := e.modules.Descriptor(deref(r.Provider))
	if !hasModule || !desc.Capabilities.RefundLookup {
		return payments.RefundResult{}, false, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, e.callTimeout)
	defer cancel()
	module, err := e.modules.Build(callCtx, r.OrgID, deref(r.Provider))
	if err != nil {
		return payments.RefundResult{}, false, fmt.Errorf("refunds: build %s to read refund back: %w", deref(r.Provider), err)
	}
	looker, ok := module.(payments.RefundLookup)
	if !ok {
		return payments.RefundResult{}, false, nil
	}
	res, err := looker.RefundStatus(callCtx, *r.ProviderRefundID)
	if err != nil {
		return payments.RefundResult{}, false, fmt.Errorf("refunds: read refund back: %w", err)
	}
	return res, true, nil
}

// reverseSucceeded parks a SUCCEEDED refund the provider now reports failed or
// canceled: the tickets were cancelled and the money did not go back after
// all. Nothing is undone automatically; an operator is alerted and decides.
func (e *Engine) reverseSucceeded(ctx context.Context, id uuid.UUID, pay Payment, res payments.RefundResult) error {
	var (
		updated Refund
		changed bool
	)
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		if err := LockPayment(ctx, tx, pay.ID); err != nil {
			return err
		}
		cur, err := getRefund(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if cur.State != StateSucceeded {
			return nil // another delivery got here first
		}
		code := res.FailureCode
		if code == "" {
			code = "failed_after_success"
		}
		reason := "the provider reports this refund failed or was canceled after it had succeeded; " +
			"its tickets were cancelled. Check the provider dashboard before acting: " + res.FailureMessage
		if updated, err = scanRefund(tx.QueryRow(ctx, `UPDATE refunds SET state = 'manual_review', provider_status = $2,
			failure_code = $3, failure_reason = $4, `+reviewAlertSQL+`, updated_at = now()
			WHERE id = $1 RETURNING `+refundColumns,
			id, string(res.Status), truncate(code, 100), truncate(reason, 500))); err != nil {
			return err
		}
		changed = true
		return e.writeAudit(ctx, tx, audit.Event{ActorType: "system", Action: "v1.refund.provider_reversed",
			ResourceType: "refund", ResourceID: id.String(),
			Metadata: map[string]any{"state": updated.State, "provider_status": string(res.Status),
				"order_id": uuidString(updated.OrderID), "amount": updated.Amount, "currency": updated.Currency}})
	})
	if err != nil {
		return err
	}
	if changed {
		e.logReview(updated)
	}
	return nil
}

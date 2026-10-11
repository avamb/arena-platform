package refunds

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
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
	_, pay, err := e.refundAndPayment(ctx, id)
	if err != nil {
		return true, err
	}
	return true, e.applyStatus(ctx, id, pay, res)
}

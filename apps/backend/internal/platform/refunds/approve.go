// approve.go — the flat POST /v1/refunds/{id}/approve route keeps its own
// checks and transaction (hcheckout), and hands the engine the two things
// only the engine may do: mark the refund as an engine refund waiting for
// its provider call (inside the route's transaction), then drive it after
// the commit. Before PAY-03 that route only pretended: it set
// provider_pending and nothing ever called the provider.
package refunds

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// MarkApprovedTx moves a requested refund to provider_pending as an engine
// refund of provider, inside the caller's transaction (which must hold
// LockPayment). The caller commits and then calls Drive.
func MarkApprovedTx(ctx context.Context, tx pgx.Tx, refundID uuid.UUID, provider string) (Refund, error) {
	return scanRefund(tx.QueryRow(ctx, `
		UPDATE refunds
		SET    state = 'provider_pending', approved_at = COALESCE(approved_at, now()),
		       provider = $2, updated_at = now()
		WHERE  id = $1 AND state = 'requested' AND settlement = 'provider'
		RETURNING `+refundColumns, refundID, payments.NormalizeProviderName(provider)))
}

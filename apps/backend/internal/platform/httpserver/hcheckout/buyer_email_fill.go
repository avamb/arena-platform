// buyer_email_fill.go — the webhook half of the prepared widget order
// (spec 65 early order, risk 4): the order, the hold and the embedded Stripe
// session are created before the buyer is known, and the buyer is attached by
// POST /v1/public/checkout/{token}/buyer before the payment is confirmed. A
// client that skipped the attach still pays with an e-mail address, because
// Stripe refuses to confirm without one; the paid order takes that address,
// so the tickets reach the person who paid.
package hcheckout

import (
	"context"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailaddr"
)

// fillMissingBuyerEmailTx writes email onto the checkout's order when the
// order has no buyer e-mail. An empty or malformed address, or an order that
// already has one, writes nothing. It runs inside the money transaction
// behind a SAVEPOINT (AGENTS.md): a failure is logged and discarded, never
// the payment.
func (h *Handler) fillMissingBuyerEmailTx(ctx context.Context, tx pgx.Tx, checkoutSessionID uuid.UUID, email string) {
	email = strings.TrimSpace(email)
	if email == "" || !emailaddr.Valid(email) {
		return
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		h.logger.Warn("webhook: could not open a savepoint for the buyer e-mail",
			slog.String("checkout_session_id", checkoutSessionID.String()),
			slog.String("error", err.Error()))
		return
	}
	filled, err := gen.New(sp).FillOrderBuyerEmail(ctx, checkoutSessionID, email)
	if err != nil {
		_ = sp.Rollback(ctx)
		h.logger.Warn("webhook: filling the buyer e-mail from the provider failed (non-fatal)",
			slog.String("checkout_session_id", checkoutSessionID.String()),
			slog.String("error", err.Error()))
		return
	}
	if err := sp.Commit(ctx); err != nil {
		_ = sp.Rollback(ctx)
		h.logger.Warn("webhook: releasing the buyer e-mail savepoint failed (non-fatal)",
			slog.String("checkout_session_id", checkoutSessionID.String()),
			slog.String("error", err.Error()))
		return
	}
	if filled {
		// The address itself is not logged.
		h.logger.Info("webhook: the order had no buyer e-mail; it took the one the provider collected",
			slog.String("checkout_session_id", checkoutSessionID.String()))
	}
}

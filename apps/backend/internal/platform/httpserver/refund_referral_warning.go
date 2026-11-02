package httpserver

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// referralCashbackWarning is the "referral_cashback_paid" block of
// GET .../orders/{order_id}/refund-options (spec 52 section 5.2): when the
// order is a friend's and the referrer has been given (or is being given)
// cashback for it, a refund to the friend does not take that money back, and
// the screen says so before the organizer presses. nil when there is nothing to
// warn about; a failed read is logged and leaves the block out (it is advice,
// the refund itself is not blocked).
func (s *Server) referralCashbackWarning(ctx context.Context, orgID, orderID uuid.UUID) map[string]any {
	if s.pgxPool == nil {
		return nil
	}
	w, err := gen.New(s.pgxPool).GetReferralCashbackForFriendOrder(ctx, orgID, orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		s.logger.Warn("refund options: referral cashback lookup failed",
			slog.String("order_id", orderID.String()), slog.String("error", err.Error()))
		return nil
	}
	return map[string]any{"amount_minor": w.AmountMinor, "currency": w.Currency, "code": w.Code}
}

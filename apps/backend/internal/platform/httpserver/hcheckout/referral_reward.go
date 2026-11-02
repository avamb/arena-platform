// referral_reward.go — the "Refer a friend" reward of a paid order that used a
// personal code (spec 52). It runs inside the payment's transaction, but under
// a SAVEPOINT: a failure here is logged and rolled back alone, and the friend's
// paid order commits regardless (AGENTS.md: best-effort writes inside a money
// transaction). The cashback itself is paid later by the referral.payout job,
// which the reward enqueues in this same transaction.
package hcheckout

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/referral"
)

func recordReferralRewardTx(ctx context.Context, tx pgx.Tx, ord gen.OrderRow) {
	if ord.PromoCodeID == nil {
		return
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		slog.Warn("referral: could not open a savepoint, no reward written", "order_id", ord.ID.String(), "error", err.Error())
		return
	}
	if err := referral.RecordFriendOrderTx(ctx, sp, ord); err != nil {
		_ = sp.Rollback(ctx)
		slog.Error("referral: reward not written", "order_id", ord.ID.String(), "error", err.Error())
		return
	}
	if err := sp.Commit(ctx); err != nil {
		slog.Error("referral: reward savepoint release failed", "order_id", ord.ID.String(), "error", err.Error())
	}
}

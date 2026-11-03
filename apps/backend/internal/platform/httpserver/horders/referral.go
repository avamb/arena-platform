package horders

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// referral.go — what the order card says about "Refer a friend" (spec 52):
// the cashback this order earned as a referrer's order, and the personal code
// it used as a friend's order. Best effort: a failed lookup leaves the block
// out (null) with a log line, the card still renders.

// orderReferralCashback is one friend order that paid off on this order.
type orderReferralCashback struct {
	FriendOrderID     *string `json:"friend_order_id"`
	FriendOrderNumber *int64  `json:"friend_order_number"`
	State             string  `json:"state"`
	Amount            int64   `json:"amount"`
	Currency          string  `json:"currency"`
	FriendReversal    *string `json:"friend_reversal"`
	PaidAt            *string `json:"paid_at"`
}

// orderReferralCode is the referrer's personal code a friend's order used.
type orderReferralCode struct {
	Code                string  `json:"code"`
	ReferrerOrderID     *string `json:"referrer_order_id"`
	ReferrerOrderNumber *int64  `json:"referrer_order_number"`
}

// orderReferral is the card's "Refer a friend" block; nil when the order
// has no part in the programme.
type orderReferral struct {
	Cashback []orderReferralCashback `json:"cashback"`
	UsedCode *orderReferralCode      `json:"used_code"`
}

func (h *Handler) loadOrderReferral(ctx context.Context, order gen.OrderRow) *orderReferral {
	out := &orderReferral{Cashback: []orderReferralCashback{}}
	rows, err := h.queries.ListOrderReferralCashback(ctx, order.ID, order.OrgID)
	if err != nil {
		h.logger.Warn("horders: referral cashback lookup failed",
			slog.String("order_id", order.ID.String()), slog.Any("error", err))
		return nil
	}
	for _, r := range rows {
		c := orderReferralCashback{
			FriendOrderNumber: r.FriendOrderNumber,
			State:             r.State,
			Amount:            r.AmountMinor,
			Currency:          r.Currency,
			FriendReversal:    r.FriendReversal,
		}
		if r.FriendOrderID != nil {
			s := r.FriendOrderID.String()
			c.FriendOrderID = &s
		}
		if r.PaidAt != nil {
			s := r.PaidAt.UTC().Format(time.RFC3339)
			c.PaidAt = &s
		}
		out.Cashback = append(out.Cashback, c)
	}
	code, err := h.queries.GetOrderReferralCode(ctx, order.ID, order.OrgID)
	switch {
	case err == nil:
		used := &orderReferralCode{Code: code.Code, ReferrerOrderNumber: code.ReferrerOrderNumber}
		if code.ReferrerOrderID != nil {
			s := code.ReferrerOrderID.String()
			used.ReferrerOrderID = &s
		}
		out.UsedCode = used
	case errors.Is(err, pgx.ErrNoRows):
	default:
		h.logger.Warn("horders: referral code lookup failed",
			slog.String("order_id", order.ID.String()), slog.Any("error", err))
		return nil
	}
	if len(out.Cashback) == 0 && out.UsedCode == nil {
		return nil
	}
	return out
}

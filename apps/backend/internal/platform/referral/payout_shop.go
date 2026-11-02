package referral

// payout_shop.go — paying the cashback of a shop order (spec 52 section 4).
//
// The cashback is a partial refund of the referrer's own SHOP payment, written
// the way shop.Service.Refund writes one (a refunds row on the order's payment
// intent, tied to the order by shop_order_id and to the reward by the
// Idempotency-Key "referral-<reward id>"), but with origin 'referral_cashback'.
// The refund engine drives it to the provider. A cashback is not the return of
// goods: the engine does not project it onto the order (refunds/settle.go) and
// the shop's own sum of accepted refunds leaves it out, so the referrer's order
// keeps its status and its refunded amount.

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// payoutShop pays one 'due' reward whose referrer and friend are shop orders.
// Like Payout it ends the reward on a decision made without a refund, retries
// transient failures, and is a no-op for a reward that is not due.
func (s *Service) payoutShop(ctx context.Context, rw gen.ReferralRewardRow) error {
	q := gen.New(s.pool)
	key := "referral-" + rw.ID.String()

	// A refund with the key already exists: an earlier run got as far as
	// writing it. Finish from it.
	if prior, err := q.GetShopRefundByKey(ctx, rw.OrgID, key); err == nil {
		return s.settleShopRefund(ctx, rw, prior.ID, []string{StateDue})
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return s.retryLater(ctx, rw.ID, err)
	}

	friend, err := q.GetReferralShopOrderFacts(ctx, *rw.FriendShopOrderID)
	if err != nil {
		return s.retryLater(ctx, rw.ID, fmt.Errorf("referral: friend shop order: %w", err))
	}
	if oc := friendShopOrderOutcome(friend.Status); oc != nil {
		return s.applyOutcome(ctx, rw.ID, oc)
	}
	refundID, err := s.writeShopCashback(ctx, rw, key, friend.Number)
	if err != nil {
		var oc *outcome
		if errors.As(err, &oc) {
			return s.applyOutcome(ctx, rw.ID, oc)
		}
		return s.retryLater(ctx, rw.ID, err)
	}
	return s.settleShopRefund(ctx, rw, refundID, []string{StateDue})
}

// friendShopOrderOutcome is what the friend's shop order status means for a
// reward not yet paid: nil when the order stands (paid, or being shipped); a
// cancellation when the friend got the money back or the order never became
// paid; a retry while it is parked.
func friendShopOrderOutcome(status string) *outcome {
	switch status {
	case "paid", "shipped":
		return nil
	case "refunded", "partially_refunded":
		return &outcome{state: StateCancelled, reason: "friend_refunded"}
	case "pending_payment", "manual_review":
		return &outcome{retry: true, reason: "friend_order_" + status}
	default:
		return &outcome{state: StateCancelled, reason: "friend_not_paid"}
	}
}

// writeShopCashback decides the amount under the payment's advisory lock and
// writes the approved refund in one transaction. The decision is taken on what
// the payment's refunds say right now. A decision that ends the reward without
// money is returned as an *outcome (the transaction rolls back).
func (s *Service) writeShopCashback(ctx context.Context, rw gen.ReferralRewardRow, key string, friendNumber int64) (uuid.UUID, error) {
	q := gen.New(s.pool)
	referrer, err := q.GetReferralShopOrderFacts(ctx, *rw.ReferrerShopOrderID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("referral: referrer shop order: %w", err)
	}
	if referrer.PaymentIntentID == nil {
		return uuid.Nil, &outcome{state: StateNotPossible, reason: "no_payment"}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The payment's lock first (the order every refund writer follows), then
	// the reward's row.
	if err := refunds.LockPayment(ctx, tx, *referrer.PaymentIntentID); err != nil {
		return uuid.Nil, err
	}
	qt := q.WithTx(tx)
	cur, err := qt.GetReferralRewardByIDForUpdate(ctx, rw.ID)
	if err != nil {
		return uuid.Nil, err
	}
	if cur.State != StateDue {
		return uuid.Nil, &outcome{reason: "already_handled"}
	}
	// A repeat that raced a run which already wrote the refund finds it here.
	if prior, err := qt.GetShopRefundByKey(ctx, rw.OrgID, key); err == nil {
		return prior.ID, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}
	var friendStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM shop_orders WHERE id = $1`, *cur.FriendShopOrderID).Scan(&friendStatus); err != nil {
		return uuid.Nil, err
	}
	if oc := friendShopOrderOutcome(friendStatus); oc != nil {
		return uuid.Nil, oc
	}
	pi, err := qt.GetPaymentIntentByID(ctx, *referrer.PaymentIntentID)
	if err != nil {
		return uuid.Nil, err
	}
	if pi.State != "succeeded" {
		return uuid.Nil, &outcome{state: StateNotPossible, reason: "payment_not_succeeded"}
	}
	if pi.Currency != cur.Currency {
		return uuid.Nil, &outcome{state: StateNotPossible, reason: "currency_mismatch"}
	}
	if reason := shopRefundRefusal(s.engine, pi.Provider); reason != "" {
		return uuid.Nil, &outcome{state: StateNotPossible, reason: reason}
	}

	var cashback, other int64
	if err := tx.QueryRow(ctx, `SELECT
			COALESCE(SUM(amount) FILTER (WHERE origin = 'referral_cashback'), 0)::bigint,
			COALESCE(SUM(amount) FILTER (WHERE origin <> 'referral_cashback'), 0)::bigint
		FROM refunds WHERE payment_intent_id = $1 AND state NOT IN ('failed', 'rejected')`,
		pi.ID).Scan(&cashback, &other); err != nil {
		return uuid.Nil, err
	}
	paid := min(referrer.Total, pi.Amount)
	amount, _ := ApplyCap(cur.AmountMinor, paid, cashback, other, int(cur.CapPct))
	// The payment can never give back more than it took, cashback included.
	amount = min(amount, paid-cashback-other)
	if amount <= 0 {
		return uuid.Nil, &outcome{state: StateCapped, reason: "cap_reached"}
	}

	reason := fmt.Sprintf("Referral cashback for friend order %d", friendNumber)
	by := "referral"
	row, err := qt.InsertRefund(ctx, pi.ID, rw.OrgID, amount, pi.Currency, &reason, &by)
	if err != nil {
		return uuid.Nil, err
	}
	if err := qt.SetShopRefundKey(ctx, row.ID, referrer.ID, key); err != nil {
		return uuid.Nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE refunds SET origin = $2 WHERE id = $1`, row.ID, refunds.OriginReferralCashback); err != nil {
		return uuid.Nil, err
	}
	if _, err := refunds.MarkApprovedTx(ctx, tx, row.ID, pi.Provider); err != nil {
		return uuid.Nil, fmt.Errorf("referral: approve cashback refund: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return row.ID, nil
}

// shopRefundRefusal is why a shop payment cannot give a part of its money back
// ("" = it can): the payment module has no refund, or no partial refund. It
// reads static descriptors only.
func shopRefundRefusal(engine *refunds.Engine, provider string) string {
	if engine != nil && engine.RouteFor(provider, "web") != refunds.RouteArenaProvider {
		return "provider_not_supported"
	}
	entry, ok := paymodules.Registry().Get(payments.NormalizeProviderName(provider))
	if !ok || !entry.Descriptor.Capabilities.PartialRefund {
		return "partial_not_supported"
	}
	return ""
}

// settleShopRefund drives the refund (a no-op for one the provider answered)
// and writes the reward's state from it.
func (s *Service) settleShopRefund(ctx context.Context, rw gen.ReferralRewardRow, refundID uuid.UUID, from []string) error {
	r, derr := s.engine.Drive(ctx, refundID)
	if derr != nil {
		s.logger.Warn("referral: drive failed, the refund sweep retries it", "refund_id", refundID.String(), "error", derr.Error())
		var gerr error
		if r, gerr = s.engine.GetRefund(ctx, refundID); gerr != nil {
			return gerr
		}
	}
	return s.applyShopRefund(ctx, rw, r, from)
}

// applyShopRefund writes the reward's state from its one refund.
func (s *Service) applyShopRefund(ctx context.Context, rw gen.ReferralRewardRow, r refunds.Refund, from []string) error {
	view := refundView{State: r.State, Amount: r.Amount, HasProviderRef: r.ProviderRefundID != nil}
	if r.FailureCode != nil {
		view.FailureCode = *r.FailureCode
	}
	state, reason, amount := resultOf([]refundView{view})
	if state == rw.State && (reason == "" || (rw.Reason != nil && *rw.Reason == reason)) && rw.RefundID != nil {
		return nil // nothing new
	}
	c := stateChange{State: state, Reason: reason, Amount: &amount, RefundID: &r.ID}
	if state == StateNotPossible {
		// Nothing was returned, but the cashback stays owed on the row (see
		// applyRefunds).
		c.Amount = nil
	}
	_, err := s.setState(ctx, rw.ID, from, c)
	return err
}

package referral

// notices.go — what the organizer hears about cashback payouts (owner decision
// 03.11.2026, spec 52 section 6.2). Each notice goes once: a message in the
// organization's sales chats (PayoutNotifier, through referralnotify) and a
// letter to its owners (referral.organizer_email, queued in the transaction
// that marks the rewards as told). Repeated attempts never repeat a notice.
//
//   - balance: rewards wait for the organizer's Stripe balance
//     (waiting_balance): "N cashbacks need X on your Stripe balance". One
//     notice per organization and currency whenever a reward starts to wait;
//     it states every reward waiting at that moment.
//   - pending: Stripe accepted a cashback refund as pending (it waits for the
//     balance; Stripe cancels it after 45 days) and has not finished it within
//     pendingNoticeAfter.
//   - unpaid: a cashback ended not_possible after it was owed (the provider
//     refused it, or the balance never covered it within 30 days): it is not
//     paid, the referrer was told to contact the organizer, and the organizer
//     settles with them directly (for example with a promo code of their own).
//     Arena issues nothing in its place.
//
// The notices carry the code, the friend's order number, counts and amounts,
// never a buyer's e-mail, name or phone. Only ticket rewards are told: the
// shop's payout is unchanged.

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// JobTypeOrganizerEmail is the letter to an organization's owners about
// cashback payouts; its payload is PayoutNotice.
const JobTypeOrganizerEmail = "referral.organizer_email"

// Notice kinds (PayoutNotice.Kind).
const (
	NoticeBalance = "balance"
	NoticePending = "pending"
	NoticeUnpaid  = "unpaid"
)

// pendingNoticeAfter: a cashback refund still pending at the provider this
// long after it was sent is not a card refund on its normal way.
const pendingNoticeAfter = "1 hour"

// unpaidNoticeWindow bounds how far back an unpaid reward is still told.
const unpaidNoticeWindow = "14 days"

// PayoutNotice is one notice to an organization (and the payload of
// JobTypeOrganizerEmail).
type PayoutNotice struct {
	Kind  string    `json:"kind"`
	OrgID uuid.UUID `json:"org_id"`
	// Currency, Count and AmountMinor: the rewards the notice speaks of and
	// their sum (unpaid: one reward).
	Currency    string `json:"currency"`
	Count       int    `json:"count"`
	AmountMinor int64  `json:"amount_minor"`
	// RewardID, Code, FriendOrderNumber and Reason: the unpaid reward.
	RewardID          string `json:"reward_id,omitempty"`
	Code              string `json:"code,omitempty"`
	FriendOrderNumber int64  `json:"friend_order_number,omitempty"`
	Reason            string `json:"reason,omitempty"`
}

// PayoutNotifier posts a PayoutNotice to the organization's sales chats. A
// Notifier that also implements it is used for both.
type PayoutNotifier interface {
	PayoutNotice(ctx context.Context, n PayoutNotice) error
}

type nopPayoutNotifier struct{}

func (nopPayoutNotifier) PayoutNotice(context.Context, PayoutNotice) error { return ErrNotWired }

// cancelDueReversed cancels the due ticket rewards whose friend already got the
// money back or opened a dispute, without waiting for the payout's own check
// after the event: the organizer's screens then say so at once. A reward that
// already has a refund batch is left to the payout (its money may be moving).
func (s *Service) cancelDueReversed(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE referral_rewards r
		SET state = 'cancelled', reason = 'friend_refunded', wait_reason = NULL, next_attempt_at = NULL, updated_at = now()
		FROM orders o
		WHERE o.id = r.friend_order_id AND r.state = 'due' AND r.refund_batch_id IS NULL
		  AND o.status IN ('refunded', 'partially_refunded')
		  AND NOT EXISTS (SELECT 1 FROM refund_batches b
		                   WHERE b.org_id = r.org_id AND b.idempotency_key = 'referral-' || r.id::text)`)
	if err != nil {
		return 0, err
	}
	n := int(tag.RowsAffected())
	ok, err := disputesTable(ctx, s.pool)
	if err != nil || !ok {
		return n, err
	}
	tag, err = s.pool.Exec(ctx, `UPDATE referral_rewards r
		SET state = 'cancelled', reason = 'friend_disputed', wait_reason = NULL, next_attempt_at = NULL, updated_at = now()
		WHERE r.state = 'due' AND r.refund_batch_id IS NULL AND r.friend_order_id IS NOT NULL
		  AND EXISTS (SELECT 1 FROM payment_disputes d WHERE d.order_id = r.friend_order_id AND NOT d.is_inquiry)
		  AND NOT EXISTS (SELECT 1 FROM refund_batches b
		                   WHERE b.org_id = r.org_id AND b.idempotency_key = 'referral-' || r.id::text)`)
	if err != nil {
		s.logger.Warn("referral: dispute scan of due rewards failed", "error", err.Error())
		return n, nil
	}
	return n + int(tag.RowsAffected()), nil
}

// notifyBalanceWaits tells each organization with rewards newly waiting for
// its balance how many wait and how much they need.
func (s *Service) notifyBalanceWaits(ctx context.Context) (int, error) {
	return s.notifyGroups(ctx, NoticeBalance, `
		WITH fresh AS (
			UPDATE referral_rewards SET balance_notice_at = now()
			WHERE  state = 'due' AND wait_reason = 'waiting_balance' AND balance_notice_at IS NULL
			  AND  friend_order_id IS NOT NULL
			RETURNING org_id, currency)
		SELECT f.org_id, f.currency, w.n, w.amount
		FROM   (SELECT DISTINCT org_id, currency FROM fresh) f
		CROSS  JOIN LATERAL (
			SELECT count(*)::int AS n, COALESCE(sum(r.amount_minor), 0)::bigint AS amount
			FROM   referral_rewards r
			WHERE  r.org_id = f.org_id AND r.currency = f.currency AND r.state = 'due'
			  AND  r.wait_reason = 'waiting_balance' AND r.friend_order_id IS NOT NULL) w`)
}

// notifyProviderPending tells each organization whose cashback refunds the
// provider holds pending (sent, not finished within pendingNoticeAfter).
func (s *Service) notifyProviderPending(ctx context.Context) (int, error) {
	return s.notifyGroups(ctx, NoticePending, `
		WITH fresh AS (
			UPDATE referral_rewards r SET pending_notice_at = now()
			WHERE  r.state = 'sent' AND r.pending_notice_at IS NULL AND r.friend_order_id IS NOT NULL
			  AND  EXISTS (SELECT 1 FROM refunds f
			               WHERE f.batch_id = r.refund_batch_id AND f.state = 'provider_pending'
			                 AND f.provider_refund_id IS NOT NULL
			                 AND COALESCE(f.approved_at, f.created_at) <= now() - interval '`+pendingNoticeAfter+`')
			RETURNING r.org_id, r.currency, r.amount_minor)
		SELECT org_id, currency, count(*)::int, sum(amount_minor)::bigint FROM fresh GROUP BY org_id, currency`)
}

// notifyGroups marks rewards as told and queues the owners' letter for each
// (organization, currency) the query returns, in one transaction; then it
// posts the chat notices. The marks commit before any chat is told, so a
// failure afterwards loses one chat message rather than repeating it.
func (s *Service) notifyGroups(ctx context.Context, kind, sql string) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, sql)
	if err != nil {
		return 0, err
	}
	var list []PayoutNotice
	for rows.Next() {
		n := PayoutNotice{Kind: kind}
		if err := rows.Scan(&n.OrgID, &n.Currency, &n.Count, &n.AmountMinor); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := queueOrganizerLetters(ctx, tx, list); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	s.announce(ctx, list)
	return len(list), nil
}

// notifyUnpaid tells the organization about each ticket reward that ended
// unpaid although it was owed (the cases that also write to the referrer).
func (s *Service) notifyUnpaid(ctx context.Context) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		UPDATE referral_rewards r SET unpaid_notice_at = now()
		WHERE  r.id IN (
			SELECT id FROM referral_rewards
			WHERE  state = 'not_possible' AND unpaid_notice_at IS NULL AND friend_order_id IS NOT NULL
			  AND  updated_at > now() - interval '`+unpaidNoticeWindow+`'
			  AND  (reason IN ($1, $2) OR (provider_code IS NOT NULL AND reason = provider_code))
			ORDER  BY updated_at LIMIT $3)
		RETURNING r.id, r.org_id, r.currency, r.amount_minor, COALESCE(r.reason, ''),
		          COALESCE((SELECT pc.code FROM promo_codes pc WHERE pc.id = r.promo_code_id), ''),
		          COALESCE((SELECT o.system_id FROM orders o WHERE o.id = r.friend_order_id), 0)`,
		ReasonBalanceTimeout, ReasonProviderDeclined, sweepBatch)
	if err != nil {
		return 0, err
	}
	var list []PayoutNotice
	for rows.Next() {
		n := PayoutNotice{Kind: NoticeUnpaid, Count: 1}
		var id uuid.UUID
		if err := rows.Scan(&id, &n.OrgID, &n.Currency, &n.AmountMinor, &n.Reason, &n.Code, &n.FriendOrderNumber); err != nil {
			rows.Close()
			return 0, err
		}
		n.RewardID = id.String()
		list = append(list, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := queueOrganizerLetters(ctx, tx, list); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	s.announce(ctx, list)
	return len(list), nil
}

func queueOrganizerLetters(ctx context.Context, tx pgx.Tx, list []PayoutNotice) error {
	for _, n := range list {
		if _, err := worker.EnqueueInTx(ctx, tx, JobTypeOrganizerEmail, n, letterMaxAttempts); err != nil {
			return fmt.Errorf("referral: queue organizer letter: %w", err)
		}
	}
	return nil
}

// announce posts the chat notices; a failure is logged, never retried (the
// letter carries the same news).
func (s *Service) announce(ctx context.Context, list []PayoutNotice) {
	for _, n := range list {
		if err := s.payoutNotifier.PayoutNotice(ctx, n); err != nil && !errors.Is(err, ErrNotWired) {
			s.logger.Warn("referral: payout notice not posted to the sales chats",
				"org_id", n.OrgID.String(), "kind", n.Kind, "error", err.Error())
		}
	}
}

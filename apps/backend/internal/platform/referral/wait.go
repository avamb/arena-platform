package referral

// wait.go — when a ticket cashback may leave (owner decision 03.11.2026, spec
// 52 R-RF2 and section 5.1). It replaced "pay right after the friend pays":
// a referrer who kept their ticket while the friend got the money back cost
// the organizer the cashback.
//
//  1. After the event. The reward is written 'due' in the friend's payment
//     transaction, but it is paid only once the friend's session has ended,
//     plus PayoutGrace (the latest session when the order spans several). A
//     friend who gets the money back or opens a dispute before that cancels it
//     (friend_refunded, friend_disputed). A scanned ticket changes nothing. A
//     session moved to another date moves the payout with it (the end is read
//     again at every attempt). A cancelled session pays nothing while the
//     friend has not been refunded: the reward waits, and a friend refunded
//     meanwhile cancels it; if nobody refunds them within
//     cancelledSessionGiveUp the reward ends cancelled (session_cancelled).
//  2. The organizer's balance. A refund leaves the organizer's own Stripe
//     balance; with too little on it Stripe holds the refund pending and
//     cancels it after 45 days. So the balance is read first (Stripe only:
//     AllPay and PayPlus have no such call). Too little keeps the reward due
//     (waiting_balance), checked again every balanceRetryEvery, and the
//     organizer is told once (sweep, notices.go). A balance that cannot be
//     read is "not known", tried again after balanceUnknownRetry, never a
//     refusal. After BalanceWaitLimit of waiting the reward ends not_possible
//     (balance_timeout): the referrer gets the "could not be returned"
//     letter, the organizer a notice to settle with them directly. Arena
//     issues no promo code in its place (owner decision 03.11).
//
// The wait reasons live in referral_rewards.wait_reason (migration 0196), not
// in reason: a reward paid later keeps its reason (cap_reduced).

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Timings of the wait.
const (
	// PayoutGrace is how long after the friend's session ends the cashback
	// waits: the evening of the event and the next day, when most "I could
	// not come" refunds are asked for.
	PayoutGrace = 24 * time.Hour
	// cancelledSessionGiveUp: a cancelled session whose friend nobody
	// refunded for this long after its planned end pays nothing.
	cancelledSessionGiveUp = 60 * 24 * time.Hour
	// cancelledRecheck is how often a reward of a cancelled session looks
	// again for the friend's refund.
	cancelledRecheck = 6 * time.Hour
	// balanceRetryEvery is how often a reward waiting for the balance reads
	// it again.
	balanceRetryEvery = 6 * time.Hour
	// balanceUnknownRetry is how soon a balance that could not be read is
	// read again.
	balanceUnknownRetry = time.Hour
	// BalanceWaitLimit ends a reward that has waited this long for the
	// organizer's balance.
	BalanceWaitLimit = 30 * 24 * time.Hour
)

// Wait reasons (referral_rewards.wait_reason, migration 0196).
const (
	WaitEvent            = "waiting_event"
	WaitSessionCancelled = "session_cancelled"
	WaitBalance          = "waiting_balance"
	WaitBalanceUnknown   = "balance_unknown"
)

// Reasons a reward ends with because of the wait.
const (
	// ReasonBalanceTimeout: the organizer's balance never covered the
	// cashback within BalanceWaitLimit. The referrer gets the "could not be
	// returned" letter.
	ReasonBalanceTimeout = "balance_timeout"
	// ReasonSessionCancelled: the friend's session was cancelled and the
	// friend was not refunded within cancelledSessionGiveUp.
	ReasonSessionCancelled = "session_cancelled"
)

// SessionEnds is what the friend order's sessions say about the payout:
// LiveEnd is the latest end of a session that still takes place (nil when
// all are cancelled), AnyEnd the latest planned end of any of them (nil when
// the order has no session at all).
type SessionEnds struct {
	LiveEnd *time.Time
	AnyEnd  *time.Time
}

// EventGate is the event's verdict on a reward (pure): pay now, wait until
// Until (WaitReason), or end it cancelled (Cancel).
type EventGate struct {
	Pay        bool
	Until      time.Time
	WaitReason string
	Cancel     bool
}

// GateByEvent decides from the friend order's sessions whether its cashback
// may be paid at now.
func GateByEvent(now time.Time, ends SessionEnds) EventGate {
	switch {
	case ends.LiveEnd != nil:
		due := ends.LiveEnd.Add(PayoutGrace)
		if now.Before(due) {
			return EventGate{Until: due, WaitReason: WaitEvent}
		}
		return EventGate{Pay: true}
	case ends.AnyEnd != nil:
		// Every session is cancelled: no payout while the friend has not
		// got the money back (the refund itself cancels the reward).
		if now.Before(ends.AnyEnd.Add(cancelledSessionGiveUp)) {
			return EventGate{Until: now.Add(cancelledRecheck), WaitReason: WaitSessionCancelled}
		}
		return EventGate{Cancel: true}
	default:
		// An order with no session row at all (never seen): nothing to
		// wait for.
		return EventGate{Pay: true}
	}
}

// friendSessionEnds reads the ends of every session of the friend's order:
// the order's own and those of its tickets. A soft-deleted session counts as
// cancelled.
func friendSessionEnds(ctx context.Context, db gen.DBTX, friendOrderID uuid.UUID) (SessionEnds, error) {
	var ends SessionEnds
	err := db.QueryRow(ctx, `
		SELECT max(s.end_at) FILTER (WHERE s.status <> 'cancelled' AND s.deleted_at IS NULL), max(s.end_at)
		FROM   sessions s
		WHERE  s.id IN (SELECT o.session_id FROM orders o WHERE o.id = $1
		                UNION
		                SELECT t.session_id FROM order_items oi JOIN tickets t ON t.id = oi.ticket_id
		                WHERE  oi.order_id = $1)`, friendOrderID).Scan(&ends.LiveEnd, &ends.AnyEnd)
	return ends, err
}

// BalanceVerdict is the balance's verdict on a reward (pure).
type BalanceVerdict struct {
	Pay        bool
	WaitReason string
	Retry      time.Duration
	// GiveUp: the reward has waited BalanceWaitLimit; it ends not_possible.
	GiveUp bool
}

// GateByBalance decides from the organizer's available balance (known false
// when it could not be read) whether amount may be paid now. waitingSince is
// when the reward first waited for the balance (nil: never), now the clock.
func GateByBalance(now time.Time, waitingSince *time.Time, known bool, available, amount int64) BalanceVerdict {
	if known && available >= amount {
		return BalanceVerdict{Pay: true}
	}
	if waitingSince != nil && now.Sub(*waitingSince) >= BalanceWaitLimit {
		return BalanceVerdict{GiveUp: true}
	}
	if !known {
		return BalanceVerdict{WaitReason: WaitBalanceUnknown, Retry: balanceUnknownRetry}
	}
	return BalanceVerdict{WaitReason: WaitBalance, Retry: balanceRetryEvery}
}

// postpone keeps a due reward waiting until at, for reason, without counting
// an attempt (waiting is not a failure). startBalanceClock also starts the
// BalanceWaitLimit clock if it is not running yet.
func (s *Service) postpone(ctx context.Context, id uuid.UUID, at time.Time, reason string, startBalanceClock bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE referral_rewards
		SET wait_reason = $3, next_attempt_at = $2, updated_at = now(),
		    balance_wait_since = CASE WHEN $4 THEN COALESCE(balance_wait_since, $5) ELSE balance_wait_since END
		WHERE id = $1 AND state = 'due'`, id, at, reason, startBalanceClock, s.now())
	return err
}

// clearWait drops the wait reason of a reward about to be paid.
func (s *Service) clearWait(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE referral_rewards SET wait_reason = NULL
		WHERE id = $1 AND state = 'due' AND wait_reason IS NOT NULL`, id)
	return err
}

// balanceWaitSince reads when the reward started to wait for the balance.
func (s *Service) balanceWaitSince(ctx context.Context, id uuid.UUID) (*time.Time, error) {
	var since *time.Time
	err := s.pool.QueryRow(ctx, `SELECT balance_wait_since FROM referral_rewards WHERE id = $1`, id).Scan(&since)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return since, err
}

// gateEvent applies the event's verdict: true when the reward may be paid
// now; otherwise it was postponed or ended and the caller stops.
func (s *Service) gateEvent(ctx context.Context, rw gen.ReferralRewardRow, friendOrderID uuid.UUID) (bool, error) {
	ends, err := friendSessionEnds(ctx, s.pool, friendOrderID)
	if err != nil {
		return false, s.retryLater(ctx, rw.ID, err)
	}
	g := GateByEvent(s.now(), ends)
	switch {
	case g.Pay:
		return true, nil
	case g.Cancel:
		return false, s.end(ctx, rw.ID, StateCancelled, ReasonSessionCancelled)
	default:
		return false, s.postpone(ctx, rw.ID, g.Until, g.WaitReason, false)
	}
}

// gateBalance reads the organizer's balance for the referrer's payment and
// applies the verdict: true when the reward may be paid now.
func (s *Service) gateBalance(ctx context.Context, rw gen.ReferralRewardRow, referrerOrderID uuid.UUID) (bool, error) {
	provider, found, err := providerOfOrder(ctx, s.pool, referrerOrderID)
	if err != nil {
		return false, s.retryLater(ctx, rw.ID, err)
	}
	if !found {
		return true, nil // the engine says what is wrong with the payment
	}
	available, supported, berr := s.engine.AvailableBalance(ctx, rw.OrgID, provider, rw.Currency)
	if !supported {
		return true, nil
	}
	if berr != nil {
		s.logger.Warn("referral: balance not read, the payout waits", "reward_id", rw.ID.String(), "error", berr.Error())
	}
	since, err := s.balanceWaitSince(ctx, rw.ID)
	if err != nil {
		return false, s.retryLater(ctx, rw.ID, err)
	}
	now := s.now()
	v := GateByBalance(now, since, berr == nil, available, rw.AmountMinor)
	switch {
	case v.Pay:
		return true, nil
	case v.GiveUp:
		return false, s.end(ctx, rw.ID, StateNotPossible, ReasonBalanceTimeout)
	default:
		return false, s.postpone(ctx, rw.ID, now.Add(v.Retry), v.WaitReason, true)
	}
}

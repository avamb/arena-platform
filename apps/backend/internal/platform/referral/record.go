package referral

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// Reward states (referral_rewards.state).
const (
	StateDue         = "due"
	StateSent        = "sent"
	StatePaid        = "paid"
	StateCapped      = "capped"
	StateNotPossible = "not_possible"
	StateFailed      = "failed"
	StateCancelled   = "cancelled"
)

// JobTypePayout is the worker job that pays one reward; its payload is
// PayoutPayload.
const JobTypePayout = "referral.payout"

// PayoutPayload is the payload of JobTypePayout.
type PayoutPayload struct {
	RewardID string `json:"reward_id"`
}

const payoutMaxAttempts = 5

// IsSelfReferral reports whether the friend order and the referrer order are
// the same person: the same customer, or any e-mail address (lower-cased,
// trimmed), phone number (digits only, six or more) or Telegram identity in
// common. Each side's identities are the order's own e-mail and phone plus
// every e-mail, phone and Telegram identity of its customer, so a buyer who
// paid once as a guest and once signed in is still the same person. A device
// is not a person and does not count.
func IsSelfReferral(ctx context.Context, db gen.DBTX, referrerOrderID, friendOrderID uuid.UUID) (bool, error) {
	var same bool
	err := db.QueryRow(ctx, `
		WITH a AS (
			SELECT customer_id, lower(btrim(COALESCE(buyer_email, ''))) AS em,
			       regexp_replace(COALESCE(buyer_phone, ''), '\D', '', 'g') AS ph
			FROM orders WHERE id = $1),
		b AS (
			SELECT customer_id, lower(btrim(COALESCE(buyer_email, ''))) AS em,
			       regexp_replace(COALESCE(buyer_phone, ''), '\D', '', 'g') AS ph
			FROM orders WHERE id = $2),
		ia AS (
			SELECT i.kind, CASE WHEN i.kind = 'phone' THEN regexp_replace(i.value_normalized, '\D', '', 'g')
			                    ELSE lower(btrim(i.value_normalized)) END AS v
			FROM customer_identities i JOIN a ON i.customer_id = a.customer_id
			WHERE i.kind IN ('email', 'phone', 'telegram')
			UNION SELECT 'email', em FROM a WHERE em <> ''
			UNION SELECT 'phone', ph FROM a WHERE length(ph) >= 6),
		ib AS (
			SELECT i.kind, CASE WHEN i.kind = 'phone' THEN regexp_replace(i.value_normalized, '\D', '', 'g')
			                    ELSE lower(btrim(i.value_normalized)) END AS v
			FROM customer_identities i JOIN b ON i.customer_id = b.customer_id
			WHERE i.kind IN ('email', 'phone', 'telegram')
			UNION SELECT 'email', em FROM b WHERE em <> ''
			UNION SELECT 'phone', ph FROM b WHERE length(ph) >= 6)
		SELECT EXISTS (SELECT 1 FROM a JOIN b ON a.customer_id IS NOT NULL AND a.customer_id = b.customer_id)
		    OR EXISTS (SELECT 1 FROM ia JOIN ib ON ib.kind = ia.kind AND ib.v = ia.v
		               WHERE ia.v <> '' AND (ia.kind <> 'phone' OR length(ia.v) >= 6))`,
		referrerOrderID, friendOrderID).Scan(&same)
	return same, err
}

// RecordFriendOrderTx writes the reward of a paid friend order that used a
// personal code, on the caller's transaction (the payment's). It does nothing
// for an order without a code or with an ordinary one. The reward is
// idempotent per friend order (a replayed payment is a no-op). A reward that is
// due also enqueues the referral.payout job in the same transaction, so the
// money moves only after the payment commits.
//
// The caller must run it under a SAVEPOINT: a failure here must never cost the
// friend their paid order.
//
// The programme's status does not matter: the friend got the discount at the
// price quoted, so the cashback follows (spec 52 section 3.1).
func RecordFriendOrderTx(ctx context.Context, tx pgx.Tx, ord gen.OrderRow) error {
	if ord.PromoCodeID == nil {
		return nil
	}
	q := gen.New(tx)
	meta, err := q.GetReferralPromoMeta(ctx, *ord.PromoCodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("referral: promo lookup: %w", err)
	}
	// A code of a shop order is for the shop step; nothing to write here yet.
	if meta.Origin != "referral" || meta.ProgramID == nil || meta.ReferrerOrderID == nil {
		return nil
	}
	program, err := q.GetReferralProgramByID(ctx, *meta.ProgramID)
	if err != nil {
		return fmt.Errorf("referral: programme lookup: %w", err)
	}
	referrer, err := q.GetReferralOrderFacts(ctx, *meta.ReferrerOrderID)
	if err != nil {
		return fmt.Errorf("referral: referrer order lookup: %w", err)
	}
	// What the code promised when it was issued, not what the programme says
	// today (a code written before the snapshot existed falls back to it).
	cashbackPct, capPct := codePercents(meta, program)
	p := gen.InsertReferralRewardParams{
		OrgID: ord.OrgID, ProgramID: program.ID, PromoCodeID: *ord.PromoCodeID,
		ReferrerOrderID: referrer.ID, FriendOrderID: ord.ID,
		Currency: referrer.Currency, CashbackPct: cashbackPct, CapPct: capPct,
	}

	// The code's row is locked first (the payment already locked it for the
	// redemption, this keeps the order the same on every path), then the
	// friends it has earned are counted: two payments racing for its last
	// place cannot both win.
	friends, err := lockedFriendCount(ctx, tx, *ord.PromoCodeID)
	if err != nil {
		return fmt.Errorf("referral: friend count: %w", err)
	}
	self, err := IsSelfReferral(ctx, tx, referrer.ID, ord.ID)
	if err != nil {
		return fmt.Errorf("referral: self-referral check: %w", err)
	}
	provider, found, err := providerOfOrder(ctx, tx, referrer.ID)
	if err != nil {
		return fmt.Errorf("referral: referrer payment: %w", err)
	}
	unsupported := found && !CanPartialRefund(paymodules.Registry(), provider)
	switch {
	case program.EventID != nil && *program.EventID != ord.EventID:
		// The code belongs to one event's programme; a cart of another event
		// of the same organization must not earn its cashback.
		p.State, p.Reason = StateCancelled, "other_event"
	case self:
		p.State, p.Reason = StateCancelled, "self_referral"
	case meta.MaxUses != nil && friends >= int64(*meta.MaxUses):
		p.State, p.Reason = StateCapped, "max_friends"
	case ord.Currency != referrer.Currency:
		p.State, p.Reason = StateNotPossible, "currency_mismatch"
	case unsupported:
		// The referrer's payment cannot return a part of itself (a code issued
		// before the rule, or a module that changed). No letter: nothing was
		// ever promised that could be paid.
		p.State, p.Reason = StateNotPossible, ReasonProviderUnsupported
	default:
		amount := CashbackAmount(ItemsBase(ord.Subtotal, ord.Discount), int(cashbackPct))
		cashback, err := q.SumReferrerLiveRewards(ctx, referrer.ID)
		if err != nil {
			return fmt.Errorf("referral: cashback sum: %w", err)
		}
		refunded, err := q.SumOrderLiveOtherRefunds(ctx, referrer.ID)
		if err != nil {
			return fmt.Errorf("referral: refund sum: %w", err)
		}
		pay, capped := ApplyCap(amount, ItemsBase(referrer.Subtotal, referrer.Discount), cashback, refunded, int(capPct))
		switch {
		case amount == 0:
			p.State, p.Reason = StateCapped, "zero_amount"
		case pay == 0:
			p.State, p.Reason, p.AmountMinor = StateCapped, "cap_reached", amount
		case capped:
			p.State, p.Reason, p.AmountMinor = StateDue, "cap_reduced", pay
		default:
			p.State, p.AmountMinor = StateDue, pay
		}
	}

	id, inserted, err := q.InsertReferralReward(ctx, p)
	if err != nil {
		return fmt.Errorf("referral: insert reward: %w", err)
	}
	if !inserted || p.State != StateDue {
		return nil
	}
	if _, err := worker.EnqueueInTx(ctx, tx, JobTypePayout, PayoutPayload{RewardID: id.String()}, payoutMaxAttempts); err != nil {
		return fmt.Errorf("referral: enqueue payout: %w", err)
	}
	return nil
}

// codePercents are the cashback and cap percentages a personal code carries.
// A code issued before the snapshot columns existed has none and follows the
// programme.
func codePercents(meta gen.ReferralPromoMeta, program gen.ReferralProgramRow) (cashbackPct, capPct int16) {
	cashbackPct, capPct = program.CashbackPct, program.CashbackCapPct
	if meta.CashbackPct != nil {
		cashbackPct = *meta.CashbackPct
	}
	if meta.CapPct != nil {
		capPct = *meta.CapPct
	}
	return cashbackPct, capPct
}

// lockedFriendCount locks the promo code's row (FOR NO KEY UPDATE: it
// serializes the writers of one code's rewards without blocking the foreign-key
// checks of orders that merely point at it) and counts the friends the code
// has already earned a reward for. A reward that was cancelled does not use a
// place.
func lockedFriendCount(ctx context.Context, tx pgx.Tx, promoCodeID uuid.UUID) (int64, error) {
	var locked uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM promo_codes WHERE id = $1 FOR NO KEY UPDATE`, promoCodeID).Scan(&locked); err != nil {
		return 0, err
	}
	var n int64
	err := tx.QueryRow(ctx, `SELECT count(*) FROM referral_rewards WHERE promo_code_id = $1 AND state <> 'cancelled'`,
		promoCodeID).Scan(&n)
	return n, err
}

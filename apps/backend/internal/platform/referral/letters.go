package referral

// letters.go — the letters and notices of "Refer a friend" (spec 52 sections
// 5.2, 6.2 and 7.3).
//
//   - referral.cashback_email {reward_id, kind} writes to the referrer. It is
//     queued, in the transaction that changes the reward's state, when the
//     cashback is paid ("sent") or when it was owed and the provider refused to
//     return it to the card ("unavailable"). The handler lives in package
//     delivery and sends once per reward (referral_rewards.letter_sent_at). A
//     reward that was announced as paid and later turns into a final refusal
//     gets ONE more "unavailable" letter, a correction (Correction in the
//     payload, referral_rewards.correction_sent_at).
//   - referral.reversal_email {reward_id} writes to the organization's owners
//     when a friend took the money back after the cashback was paid. It is
//     queued by the OrganizerNotifier (referralnotify/notify.go).

import (
	"context"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Worker job types of the letters.
const (
	JobTypeCashbackEmail = "referral.cashback_email"
	JobTypeReversalEmail = "referral.reversal_email"
)

// Letter kinds (CashbackEmailPayload.Kind).
const (
	// LetterSent: the cashback went back to the referrer's card.
	LetterSent = "sent"
	// LetterUnavailable: the cashback was owed but could not be returned to
	// the referrer's card.
	LetterUnavailable = "unavailable"
)

// CashbackEmailPayload is the payload of JobTypeCashbackEmail.
type CashbackEmailPayload struct {
	RewardID string `json:"reward_id"`
	Kind     string `json:"kind"`
	// Correction: the referrer was already told the money went back; this
	// letter takes that back. Sent once (referral_rewards.correction_sent_at).
	Correction bool `json:"correction,omitempty"`
}

// ReversalEmailPayload is the payload of JobTypeReversalEmail.
type ReversalEmailPayload struct {
	RewardID string `json:"reward_id"`
}

const letterMaxAttempts = 5

// ReasonProviderDeclined is the reward reason when a refund the provider
// really received failed without a code of its own.
const ReasonProviderDeclined = "provider_declined"

// LetterKindFor says which letter a reward that reaches state with reason gets,
// if any. Only 'paid' (the money went back, fully or in part) and one kind of
// 'not_possible' write to the referrer.
//
// The "could not be returned" letter is sent from an explicit rule, never "every
// reason except these": it goes out only when the refund really reached the
// provider and the provider refused it. That is, the reason is
// ReasonProviderDeclined, or it is the provider's own failure code
// (providerCode, which resultOf sets to the reason in that case). Every reason
// the platform itself decided without asking the provider gets NO letter: no
// ticket to take the money from, nothing to refund, the payment not succeeded,
// no payment, the order not found, an unsupported provider, another currency,
// a refund waiting for a person (manual_review), another event, the friends
// limit, a self-referral, the refund engine's own refusals (refund.*), and
// any reason added later.
func LetterKindFor(state, reason, providerCode string) (string, bool) {
	switch state {
	case StatePaid:
		return LetterSent, true
	case StateNotPossible:
		if reason == ReasonProviderDeclined || (providerCode != "" && reason == providerCode) {
			return LetterUnavailable, true
		}
	}
	return "", false
}

// ReversalFacts is what the organizer notices say about one reward whose
// friend took the money back.
type ReversalFacts struct {
	OrgName           string
	OrgLocale         string
	Code              string
	CodeStatus        string
	ReversalsOnCode   int
	FriendOrderNumber int64
}

// CodePaused reports whether the code is on pause: it is, or the sweep is
// about to pause it (pauseAfterReversals friends under one code).
func (f ReversalFacts) CodePaused() bool {
	return f.CodeStatus == "paused" || f.ReversalsOnCode >= pauseAfterReversals
}

// LoadReversalFacts reads the facts for rw (a ticket order's or a shop order's
// number). ok is false when the reward has no friend order at all.
func LoadReversalFacts(ctx context.Context, db gen.DBTX, rw gen.ReferralRewardRow) (f ReversalFacts, ok bool, err error) {
	if rw.FriendShopOrderID != nil {
		err = db.QueryRow(ctx, `
			SELECT o.name, COALESCE(o.default_locale, ''), pc.code, pc.status,
			       (SELECT count(*) FROM referral_rewards r2
			         WHERE r2.promo_code_id = pc.id AND r2.friend_reversal IS NOT NULL)::int,
			       COALESCE(fo.number, 0)
			FROM   organizations o, promo_codes pc, shop_orders fo
			WHERE  o.id = $1 AND pc.id = $2 AND fo.id = $3`,
			rw.OrgID, rw.PromoCodeID, *rw.FriendShopOrderID).
			Scan(&f.OrgName, &f.OrgLocale, &f.Code, &f.CodeStatus, &f.ReversalsOnCode, &f.FriendOrderNumber)
		if err != nil {
			return f, false, err
		}
		return f, true, nil
	}
	if rw.FriendOrderID == nil {
		return f, false, nil
	}
	err = db.QueryRow(ctx, `
		SELECT o.name, COALESCE(o.default_locale, ''), pc.code, pc.status,
		       (SELECT count(*) FROM referral_rewards r2
		         WHERE r2.promo_code_id = pc.id AND r2.friend_reversal IS NOT NULL)::int,
		       COALESCE(fo.system_id, 0)
		FROM   organizations o, promo_codes pc, orders fo
		WHERE  o.id = $1 AND pc.id = $2 AND fo.id = $3`,
		rw.OrgID, rw.PromoCodeID, *rw.FriendOrderID).
		Scan(&f.OrgName, &f.OrgLocale, &f.Code, &f.CodeStatus, &f.ReversalsOnCode, &f.FriendOrderNumber)
	if err != nil {
		return f, false, err
	}
	return f, true, nil
}

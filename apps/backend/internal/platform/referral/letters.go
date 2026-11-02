package referral

// letters.go — the letters and notices of "Refer a friend" (spec 52 sections
// 5.2, 6.2 and 7.3).
//
//   - referral.cashback_email {reward_id, kind} writes to the referrer. It is
//     queued, in the transaction that changes the reward's state, when the
//     cashback is paid ("sent") or when it was owed and could not be returned
//     to the card ("unavailable"). The handler lives in package delivery and
//     sends once per reward (referral_rewards.letter_sent_at).
//   - referral.reversal_email {reward_id} writes to the organization's owners
//     when a friend took the money back after the cashback was paid. It is
//     queued by the OrganizerNotifier (notify.go).

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
}

// ReversalEmailPayload is the payload of JobTypeReversalEmail.
type ReversalEmailPayload struct {
	RewardID string `json:"reward_id"`
}

const letterMaxAttempts = 5

// noLetterReasons are the reasons of a 'not_possible' reward that get NO
// letter to the referrer:
//
//   - currency_mismatch: the friend paid in another currency than the referrer,
//     the cashback was never calculated for them;
//   - shop_not_supported: the reward belongs to the shop step, not wired yet;
//   - manual_review: not final, the sweep reads the refund back and the reward
//     may still become paid.
//
// Every other reason of 'not_possible' means a cashback that WAS owed and that
// the provider, the card or a refusal of the refund engine kept from the
// referrer: provider_declined and the provider's own failure codes,
// provider_not_supported, seller_site_order, partial_not_supported,
// no_refundable_ticket, and the other refund.* codes.
var noLetterReasons = map[string]bool{
	"currency_mismatch":  true,
	"shop_not_supported": true,
	"manual_review":      true,
}

// LetterKindFor says which letter a reward that reaches state with reason
// gets, if any. Only 'paid' (the money went back, fully or in part) and
// 'not_possible' (see noLetterReasons) write to the referrer.
func LetterKindFor(state, reason string) (string, bool) {
	switch state {
	case StatePaid:
		return LetterSent, true
	case StateNotPossible:
		if noLetterReasons[reason] {
			return "", false
		}
		return LetterUnavailable, true
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

// LoadReversalFacts reads the facts for rw. ok is false when the reward has
// no friend order of tickets (a shop reward): there is nothing to say yet.
func LoadReversalFacts(ctx context.Context, db gen.DBTX, rw gen.ReferralRewardRow) (f ReversalFacts, ok bool, err error) {
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

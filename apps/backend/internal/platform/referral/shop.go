package referral

// shop.go — "Refer a friend" for the merchandise shop (spec 52 sections 0
// R-RF6, 3.5 and 4). A shop has its own programme (scope 'shop', its own
// percentages); a paid shop order gets a personal code; a friend's shop order
// that used the code earns the referrer a cashback, paid as a partial refund of
// the referrer's own shop payment (payout_shop.go).

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// shopOrderEligible: a shop order that took money and still stands (paid, being
// shipped, or partly refunded). A refunded, cancelled or parked order cannot
// carry a personal code.
func shopOrderEligible(o gen.ReferralShopOrderFacts) bool {
	switch o.Status {
	case "paid", "shipped", "partially_refunded":
		return o.Total > 0
	}
	return false
}

// IssueShopCode returns the personal code of a paid shop order, creating it on
// the first call. created tells whether this call made it. It is idempotent: a
// repeat (or a race) returns the code the order already has. The order's
// organization needs an active shop programme in its period, else ErrNoProgram;
// a code issued earlier is returned whatever the programme's status is now.
//
// db is a pool or a transaction. Issuing is not part of the payment's
// transaction: the buyer's page asks for the code after the order is paid.
func IssueShopCode(ctx context.Context, db gen.DBTX, shopOrderID uuid.UUID) (Code, bool, error) {
	return issueShopCode(ctx, db, shopOrderID, time.Now(), rand.Reader)
}

func issueShopCode(ctx context.Context, db gen.DBTX, shopOrderID uuid.UUID, now time.Time, rnd io.Reader) (Code, bool, error) {
	q := gen.New(db)
	if existing, err := q.GetReferralCodeByReferrerShopOrder(ctx, shopOrderID); err == nil {
		return codeFromRow(existing), false, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Code{}, false, err
	}
	ord, err := q.GetReferralShopOrderFacts(ctx, shopOrderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Code{}, false, ErrOrderNotFound
	}
	if err != nil {
		return Code{}, false, err
	}
	if !shopOrderEligible(ord) {
		return Code{}, false, ErrNotEligible
	}
	program, err := q.GetActiveReferralProgramForShop(ctx, ord.OrgID, now)
	if errors.Is(err, pgx.ErrNoRows) {
		return Code{}, false, ErrNoProgram
	}
	if err != nil {
		return Code{}, false, err
	}
	// The cashback is a partial refund of this order's payment: a provider that
	// cannot do it cannot carry a code.
	if can, err := shopOrderCanCarryCode(ctx, db, ord.PaymentIntentID); err != nil {
		return Code{}, false, err
	} else if !can {
		return Code{}, false, ErrNotEligible
	}
	var maxUses *int32
	if program.MaxFriends != nil {
		v := int32(*program.MaxFriends)
		maxUses = &v
	}
	for range maxCodeAttempts {
		text, err := generateCode(ord.BuyerName, rnd)
		if err != nil {
			return Code{}, false, err
		}
		id, inserted, err := q.InsertReferralShopPromoCode(ctx, gen.InsertReferralShopPromoCodeParams{
			OrgID: ord.OrgID, Code: text, DiscountPct: int64(program.FriendDiscountPct), MaxUses: maxUses,
			ValidUntil: program.EndsAt, ProgramID: program.ID, ReferrerShopOrderID: shopOrderID,
			CashbackPct: program.CashbackPct, CapPct: program.CashbackCapPct,
		})
		if err != nil {
			return Code{}, false, err
		}
		if inserted {
			return Code{
				PromoCodeID: id, ProgramID: program.ID, Code: text, Status: "active",
				FriendDiscountPct: int(program.FriendDiscountPct), CashbackPct: int(program.CashbackPct),
			}, true, nil
		}
		// Not written: the code text collided, or a parallel call issued the
		// order's code first. The order's own code tells which.
		if existing, err := q.GetReferralCodeByReferrerShopOrder(ctx, shopOrderID); err == nil {
			return codeFromRow(existing), false, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return Code{}, false, err
		}
	}
	return Code{}, false, fmt.Errorf("referral: no free code after %d attempts", maxCodeAttempts)
}

// ShopDiscount is the discount a personal code takes off a shop cart: the
// programme's percent of the goods subtotal, rounded down (the delivery fee is
// never discounted).
func ShopDiscount(subtotal int64, pct int64) int64 {
	if subtotal <= 0 || pct <= 0 {
		return 0
	}
	if pct > 100 {
		pct = 100
	}
	return subtotal * pct / 100
}

// Refusals of a personal code at the shop checkout; the values are the stable
// 422 codes of the answer.
const (
	// ShopPromoNotApplicable: no such code in this shop (also: any code that is
	// not a personal code of the shop's programme).
	ShopPromoNotApplicable = "promo.not_applicable"
	// ShopPromoOwn: the code belongs to the person who is buying.
	ShopPromoOwn = "promo.own_referral_code"
	// ShopPromoNotActive: the programme or the code is paused, ended or outside
	// its period.
	ShopPromoNotActive = "promo.not_active"
	// ShopPromoExhausted: the code's limit of friends, or this buyer's own
	// single use, is used up.
	ShopPromoExhausted = "promo.exhausted"
)

// ShopPromo is a personal code accepted for a shop cart.
type ShopPromo struct {
	PromoCodeID uuid.UUID
	Discount    int64
	Pct         int64
}

// CheckShopPromo judges the code a buyer typed at the shop checkout. subtotal
// is the goods total of the cart (delivery excluded), email and phone are the
// buyer's. It returns a stable refusal code, "" when the code applies. q must
// run on the checkout's transaction: the code's limits are counted under its
// advisory lock (taken here), so they cannot be raced.
func CheckShopPromo(ctx context.Context, q *gen.Queries, orgID uuid.UUID, code string, subtotal int64, email, phone string, now time.Time) (ShopPromo, string, error) {
	p, err := q.GetReferralShopPromoByCode(ctx, orgID, code)
	if errors.Is(err, pgx.ErrNoRows) {
		return ShopPromo{}, ShopPromoNotApplicable, nil
	}
	if err != nil {
		return ShopPromo{}, "", err
	}
	if p.ProgramStatus != "active" || p.Status != "active" ||
		(p.ProgramStartsAt != nil && now.Before(*p.ProgramStartsAt)) || (p.ProgramEndsAt != nil && !now.Before(*p.ProgramEndsAt)) ||
		(p.ValidFrom != nil && now.Before(*p.ValidFrom)) || (p.ValidUntil != nil && !now.Before(*p.ValidUntil)) {
		return ShopPromo{}, ShopPromoNotActive, nil
	}
	own, err := IsSelfReferralShopBuyer(ctx, q.DB(), p.ReferrerShopOrderID, email, phone)
	if err != nil {
		return ShopPromo{}, "", err
	}
	if own {
		return ShopPromo{}, ShopPromoOwn, nil
	}
	if p.MaxUses != nil || p.MaxUsesPerCustomer != nil {
		if err := q.LockShopPromo(ctx, p.ID); err != nil {
			return ShopPromo{}, "", err
		}
	}
	if p.MaxUses != nil {
		n, err := q.CountShopPromoUses(ctx, p.ID)
		if err != nil {
			return ShopPromo{}, "", err
		}
		if int32(n) >= *p.MaxUses { //nolint:gosec // a count of rows
			return ShopPromo{}, ShopPromoExhausted, nil
		}
	}
	if p.MaxUsesPerCustomer != nil {
		n, err := q.CountShopPromoUsesByBuyer(ctx, p.ID, email)
		if err != nil {
			return ShopPromo{}, "", err
		}
		if int32(n) >= *p.MaxUsesPerCustomer { //nolint:gosec // a count of rows
			return ShopPromo{}, ShopPromoExhausted, nil
		}
	}
	return ShopPromo{PromoCodeID: p.ID, Discount: ShopDiscount(subtotal, p.DiscountPct), Pct: p.DiscountPct}, "", nil
}

// RecordFriendShopOrderTx writes the reward of a paid shop order that used a
// personal code, on the caller's transaction (the payment's). It does nothing
// for an order without a code. The reward is idempotent per friend order (a
// replayed payment is a no-op). A reward that is due also enqueues the
// referral.payout job in the same transaction, so the money moves only after the
// payment commits.
//
// The caller must run it under a SAVEPOINT: a failure here must never cost the
// friend their paid order.
//
// The cashback base is what the friend paid for the goods: the subtotal minus
// the discount, never the delivery fee. The programme's status does not matter:
// the friend got the discount at the price quoted, so the cashback follows
// (spec 52 section 3.1).
func RecordFriendShopOrderTx(ctx context.Context, tx pgx.Tx, ord gen.ShopOrderRow) error {
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
	if meta.Origin != "referral" || meta.ProgramID == nil || meta.ReferrerShopOrderID == nil {
		return nil
	}
	program, err := q.GetReferralProgramByID(ctx, *meta.ProgramID)
	if err != nil {
		return fmt.Errorf("referral: programme lookup: %w", err)
	}
	referrer, err := q.GetReferralShopOrderFacts(ctx, *meta.ReferrerShopOrderID)
	if err != nil {
		return fmt.Errorf("referral: referrer order lookup: %w", err)
	}
	cashbackPct, capPct := codePercents(meta, program)
	p := gen.InsertReferralShopRewardParams{
		OrgID: ord.OrgID, ProgramID: program.ID, PromoCodeID: *ord.PromoCodeID,
		ReferrerShopOrderID: referrer.ID, FriendShopOrderID: ord.ID,
		Currency: referrer.Currency, CashbackPct: cashbackPct, CapPct: capPct,
	}

	friends, err := lockedFriendCount(ctx, tx, *ord.PromoCodeID)
	if err != nil {
		return fmt.Errorf("referral: friend count: %w", err)
	}
	self, err := IsSelfReferralShop(ctx, tx, referrer.ID, ord.ID)
	if err != nil {
		return fmt.Errorf("referral: self-referral check: %w", err)
	}
	// A referrer whose payment cannot return a part of itself is owed nothing
	// that can be paid (a code issued before the rule, or a changed module).
	provider, found, err := providerOfShopPayment(ctx, tx, referrer.PaymentIntentID)
	if err != nil {
		return fmt.Errorf("referral: referrer payment: %w", err)
	}
	unsupported := found && !CanPartialRefund(paymodules.Registry(), provider)
	switch {
	case self:
		p.State, p.Reason = StateCancelled, "self_referral"
	case meta.MaxUses != nil && friends >= int64(*meta.MaxUses):
		p.State, p.Reason = StateCapped, "max_friends"
	case ord.Currency != referrer.Currency:
		p.State, p.Reason = StateNotPossible, "currency_mismatch"
	case unsupported:
		p.State, p.Reason = StateNotPossible, ReasonProviderUnsupported
	default:
		amount := CashbackAmount(ItemsBase(ord.Subtotal, ord.Discount), int(cashbackPct))
		cashback, err := q.SumReferrerShopLiveRewards(ctx, referrer.ID)
		if err != nil {
			return fmt.Errorf("referral: cashback sum: %w", err)
		}
		var refunded int64
		if referrer.PaymentIntentID != nil {
			if refunded, err = q.SumPaymentLiveOtherRefunds(ctx, *referrer.PaymentIntentID); err != nil {
				return fmt.Errorf("referral: refund sum: %w", err)
			}
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

	id, inserted, err := q.InsertReferralShopReward(ctx, p)
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

// IsSelfReferralShop reports whether two shop orders are the same person: the
// same customer, or any e-mail address (lower-cased, trimmed), phone number
// (digits only, six or more) or Telegram identity in common. Each side's
// identities are the order's own e-mail and phone plus every e-mail, phone and
// Telegram identity of its customer.
func IsSelfReferralShop(ctx context.Context, db gen.DBTX, referrerShopOrderID, friendShopOrderID uuid.UUID) (bool, error) {
	var same bool
	err := db.QueryRow(ctx, `
		WITH a AS (
			SELECT customer_id, lower(btrim(COALESCE(buyer_email, ''))) AS em,
			       regexp_replace(COALESCE(buyer_phone, ''), '\D', '', 'g') AS ph
			FROM shop_orders WHERE id = $1),
		b AS (
			SELECT customer_id, lower(btrim(COALESCE(buyer_email, ''))) AS em,
			       regexp_replace(COALESCE(buyer_phone, ''), '\D', '', 'g') AS ph
			FROM shop_orders WHERE id = $2),
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
		referrerShopOrderID, friendShopOrderID).Scan(&same)
	return same, err
}

// IsSelfReferralShopBuyer is IsSelfReferralShop for a buyer who has no order
// yet: it reports whether the e-mail address or phone number a cart names
// belongs to the referrer of referrerShopOrderID (the order's own e-mail and
// phone, or any e-mail or phone identity of its customer). It is the early,
// polite half of the rule; the paid order is checked again when the reward is
// written, which is the half that pays out.
func IsSelfReferralShopBuyer(ctx context.Context, db gen.DBTX, referrerShopOrderID uuid.UUID, email, phone string) (bool, error) {
	var same bool
	err := db.QueryRow(ctx, `
		WITH a AS (
			SELECT customer_id, lower(btrim(COALESCE(buyer_email, ''))) AS em,
			       regexp_replace(COALESCE(buyer_phone, ''), '\D', '', 'g') AS ph
			FROM shop_orders WHERE id = $1),
		ia AS (
			SELECT i.kind, CASE WHEN i.kind = 'phone' THEN regexp_replace(i.value_normalized, '\D', '', 'g')
			                    ELSE lower(btrim(i.value_normalized)) END AS v
			FROM customer_identities i JOIN a ON i.customer_id = a.customer_id
			WHERE i.kind IN ('email', 'phone')
			UNION SELECT 'email', em FROM a WHERE em <> ''
			UNION SELECT 'phone', ph FROM a WHERE length(ph) >= 6),
		b AS (
			SELECT lower(btrim($2::text)) AS em, regexp_replace($3::text, '\D', '', 'g') AS ph)
		SELECT EXISTS (SELECT 1 FROM ia JOIN b ON ia.kind = 'email' AND b.em <> '' AND ia.v = b.em)
		    OR EXISTS (SELECT 1 FROM ia JOIN b ON ia.kind = 'phone' AND length(b.ph) >= 6 AND ia.v = b.ph)`,
		referrerShopOrderID, email, phone).Scan(&same)
	return same, err
}

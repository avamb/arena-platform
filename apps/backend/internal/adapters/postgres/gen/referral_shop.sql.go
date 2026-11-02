// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: referral_shop.sql
//
// "Refer a friend" for the merchandise shop (spec 52 sections 0 R-RF6, 3.5 and
// 4; migration 0195). A shop programme is referral_programs.scope = 'shop', one
// live per organization. A personal code of a shop order carries
// promo_codes.referrer_shop_order_id; the friend's shop order carries
// shop_orders.promo_code_id and its discount in shop_orders.discount.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// The shop's programme
// ─────────────────────────────────────────────────────────────────────────────

const getLatestReferralProgramForShop = `-- name: GetLatestReferralProgramForShop :one
SELECT ` + referralProgramColumns + `
FROM referral_programs
WHERE scope = 'shop' AND org_id = $1
ORDER BY (status = 'ended'), created_at DESC, id DESC
LIMIT 1`

// GetLatestReferralProgramForShop returns the organization's live (active or
// paused) shop programme, else its most recent ended one. pgx.ErrNoRows when
// it never had one.
func (q *Queries) GetLatestReferralProgramForShop(ctx context.Context, orgID uuid.UUID) (ReferralProgramRow, error) {
	return scanReferralProgramRow(q.db.QueryRow(ctx, getLatestReferralProgramForShop, orgID))
}

const getActiveReferralProgramForShop = `-- name: GetActiveReferralProgramForShop :one
SELECT ` + referralProgramColumns + `
FROM referral_programs
WHERE scope = 'shop' AND org_id = $1 AND status = 'active'
  AND (starts_at IS NULL OR starts_at <= $2)
  AND (ends_at IS NULL OR ends_at > $2)`

// GetActiveReferralProgramForShop returns the organization's shop programme
// while it is active and inside its period at now. pgx.ErrNoRows otherwise.
func (q *Queries) GetActiveReferralProgramForShop(ctx context.Context, orgID uuid.UUID, now time.Time) (ReferralProgramRow, error) {
	return scanReferralProgramRow(q.db.QueryRow(ctx, getActiveReferralProgramForShop, orgID, now))
}

// LockReferralProgramShop serializes writers of one organization's shop
// programme (the partial unique index allows one live programme per shop). A
// transaction-scoped advisory lock: call it first inside the writer's
// transaction.
func (q *Queries) LockReferralProgramShop(ctx context.Context, orgID uuid.UUID) error {
	_, err := q.db.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('arena.referral.shop_program:' || $1::text, 0))`, orgID)
	return err
}

// InsertReferralShopProgramParams are the fields of a new shop programme.
type InsertReferralShopProgramParams struct {
	OrgID             uuid.UUID
	FriendDiscountPct int16
	CashbackPct       int16
	CashbackCapPct    int16
	MaxFriends        *int16
	EndsAt            *time.Time
	CreatedBy         string
	CreatedVia        string
}

const insertReferralShopProgram = `-- name: InsertReferralShopProgram :one
INSERT INTO referral_programs (org_id, scope, event_id, status, friend_discount_pct, cashback_pct,
    cashback_cap_pct, max_friends, ends_at, terms_accepted_at, created_by, created_via)
VALUES ($1, 'shop', NULL, 'active', $2, $3, $4, $5, $6, now(), NULLIF($7, ''), NULLIF($8, ''))
RETURNING ` + referralProgramColumns

// InsertReferralShopProgram starts the organization's shop programme. The terms
// are accepted at this moment (the caller checked the person ticked them).
func (q *Queries) InsertReferralShopProgram(ctx context.Context, p InsertReferralShopProgramParams) (ReferralProgramRow, error) {
	return scanReferralProgramRow(q.db.QueryRow(ctx, insertReferralShopProgram,
		p.OrgID, p.FriendDiscountPct, p.CashbackPct, p.CashbackCapPct, p.MaxFriends, p.EndsAt, p.CreatedBy, p.CreatedVia))
}

// ─────────────────────────────────────────────────────────────────────────────
// A shop order and its personal code
// ─────────────────────────────────────────────────────────────────────────────

// ReferralShopOrderFacts is the slice of a shop order the programme needs.
type ReferralShopOrderFacts struct {
	ID              uuid.UUID  `json:"id"`
	OrgID           uuid.UUID  `json:"org_id"`
	Number          int64      `json:"number"`
	CustomerID      *uuid.UUID `json:"customer_id"`
	Status          string     `json:"status"`
	Currency        string     `json:"currency"`
	Subtotal        int64      `json:"subtotal"`
	Discount        int64      `json:"discount"`
	ShippingFee     int64      `json:"shipping_fee"`
	Total           int64      `json:"total"`
	BuyerName       string     `json:"buyer_name"`
	BuyerEmail      string     `json:"buyer_email"`
	BuyerPhone      string     `json:"buyer_phone"`
	BuyerLocale     string     `json:"buyer_locale"`
	PaymentIntentID *uuid.UUID `json:"payment_intent_id"`
	PromoCodeID     *uuid.UUID `json:"promo_code_id"`
}

const getReferralShopOrderFacts = `-- name: GetReferralShopOrderFacts :one
SELECT id, org_id, number, customer_id, status, currency, subtotal, discount, shipping_fee, total,
       buyer_name, buyer_email, buyer_phone, COALESCE(buyer_locale, ''), payment_intent_id, promo_code_id
FROM shop_orders WHERE id = $1`

// GetReferralShopOrderFacts reads one shop order. pgx.ErrNoRows when it does
// not exist.
func (q *Queries) GetReferralShopOrderFacts(ctx context.Context, id uuid.UUID) (ReferralShopOrderFacts, error) {
	var o ReferralShopOrderFacts
	err := q.db.QueryRow(ctx, getReferralShopOrderFacts, id).Scan(
		&o.ID, &o.OrgID, &o.Number, &o.CustomerID, &o.Status, &o.Currency, &o.Subtotal, &o.Discount, &o.ShippingFee,
		&o.Total, &o.BuyerName, &o.BuyerEmail, &o.BuyerPhone, &o.BuyerLocale, &o.PaymentIntentID, &o.PromoCodeID)
	return o, err
}

const getReferralShopOrderFactsByToken = `-- name: GetReferralShopOrderFactsByToken :one
SELECT id, org_id, number, customer_id, status, currency, subtotal, discount, shipping_fee, total,
       buyer_name, buyer_email, buyer_phone, COALESCE(buyer_locale, ''), payment_intent_id, promo_code_id
FROM shop_orders WHERE order_token = $1`

// GetReferralShopOrderFactsByToken reads the shop order a buyer holds the token
// of. pgx.ErrNoRows when the token names nothing.
func (q *Queries) GetReferralShopOrderFactsByToken(ctx context.Context, token string) (ReferralShopOrderFacts, error) {
	var o ReferralShopOrderFacts
	err := q.db.QueryRow(ctx, getReferralShopOrderFactsByToken, token).Scan(
		&o.ID, &o.OrgID, &o.Number, &o.CustomerID, &o.Status, &o.Currency, &o.Subtotal, &o.Discount, &o.ShippingFee,
		&o.Total, &o.BuyerName, &o.BuyerEmail, &o.BuyerPhone, &o.BuyerLocale, &o.PaymentIntentID, &o.PromoCodeID)
	return o, err
}

const getReferralCodeByReferrerShopOrder = `-- name: GetReferralCodeByReferrerShopOrder :one
SELECT pc.id, pc.code, pc.referral_program_id, pc.discount_value::smallint, p.cashback_pct, pc.status
FROM promo_codes pc
JOIN referral_programs p ON p.id = pc.referral_program_id
WHERE pc.referrer_shop_order_id = $1`

// GetReferralCodeByReferrerShopOrder returns the personal code issued for a
// shop order, pgx.ErrNoRows when none was issued yet.
func (q *Queries) GetReferralCodeByReferrerShopOrder(ctx context.Context, shopOrderID uuid.UUID) (ReferralCodeRow, error) {
	var r ReferralCodeRow
	err := q.db.QueryRow(ctx, getReferralCodeByReferrerShopOrder, shopOrderID).Scan(
		&r.PromoCodeID, &r.Code, &r.ProgramID, &r.FriendDiscountPct, &r.CashbackPct, &r.Status)
	return r, err
}

// NoSessionID is the uuid a shop code stores in applies_to_session_ids: no
// session has it, so the code can never be taken for a club code (an empty
// list) by the ticket checkout, the Bil24 gateway or the organization's
// promo-validate route.
const NoSessionID = "00000000-0000-0000-0000-000000000000"

const insertReferralShopPromoCode = `-- name: InsertReferralShopPromoCode :one
INSERT INTO promo_codes (org_id, code, discount_type, discount_value, applies_to_session_ids,
    max_uses, max_uses_per_customer, valid_until, status, origin, referral_program_id, referrer_shop_order_id)
VALUES ($1, $2, 'percent', $3, ARRAY['` + NoSessionID + `']::uuid[], $4, 1, $5, 'active', 'referral', $6, $7)
ON CONFLICT DO NOTHING
RETURNING id`

// InsertReferralShopPromoCodeParams are the fields of a shop order's personal
// code.
type InsertReferralShopPromoCodeParams struct {
	OrgID               uuid.UUID
	Code                string
	DiscountPct         int64
	MaxUses             *int32
	ValidUntil          *time.Time
	ProgramID           uuid.UUID
	ReferrerShopOrderID uuid.UUID
}

// InsertReferralShopPromoCode creates the personal code of a shop order. It
// returns inserted=false (and no error) when the row was not written: either
// the code text is taken in the organization or the order already has a code.
func (q *Queries) InsertReferralShopPromoCode(ctx context.Context, p InsertReferralShopPromoCodeParams) (uuid.UUID, bool, error) {
	rows, err := q.db.Query(ctx, insertReferralShopPromoCode,
		p.OrgID, p.Code, p.DiscountPct, p.MaxUses, p.ValidUntil, p.ProgramID, p.ReferrerShopOrderID)
	if err != nil {
		return uuid.Nil, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return uuid.Nil, false, rows.Err()
	}
	var id uuid.UUID
	if err := rows.Scan(&id); err != nil {
		return uuid.Nil, false, err
	}
	rows.Close()
	return id, true, rows.Err()
}

// ReferralShopPromo is a personal code as the shop checkout judges it, with the
// programme it belongs to.
type ReferralShopPromo struct {
	ID                  uuid.UUID  `json:"id"`
	Code                string     `json:"code"`
	Status              string     `json:"status"`
	DiscountPct         int64      `json:"discount_pct"`
	MaxUses             *int32     `json:"max_uses"`
	MaxUsesPerCustomer  *int32     `json:"max_uses_per_customer"`
	ValidFrom           *time.Time `json:"valid_from"`
	ValidUntil          *time.Time `json:"valid_until"`
	ProgramID           uuid.UUID  `json:"program_id"`
	ProgramStatus       string     `json:"program_status"`
	ProgramStartsAt     *time.Time `json:"program_starts_at"`
	ProgramEndsAt       *time.Time `json:"program_ends_at"`
	ReferrerShopOrderID uuid.UUID  `json:"referrer_shop_order_id"`
}

const getReferralShopPromoByCode = `-- name: GetReferralShopPromoByCode :one
SELECT pc.id, pc.code, pc.status, pc.discount_value, pc.max_uses, pc.max_uses_per_customer,
       pc.valid_from, pc.valid_until, p.id, p.status, p.starts_at, p.ends_at, pc.referrer_shop_order_id
FROM   promo_codes pc
JOIN   referral_programs p ON p.id = pc.referral_program_id
WHERE  pc.org_id = $1 AND lower(pc.code) = lower($2) AND pc.deleted_at IS NULL
  AND  pc.origin = 'referral' AND p.scope = 'shop' AND p.org_id = $1
  AND  pc.discount_type = 'percent' AND pc.referrer_shop_order_id IS NOT NULL
ORDER  BY (pc.code = $2) DESC, pc.created_at, pc.id
LIMIT  1`

// GetReferralShopPromoByCode finds a personal code of the organization's shop
// programme by its text, ignoring case. Any other promo code (an ordinary one,
// or a code of an event programme) is not found: a shop takes no other codes.
// pgx.ErrNoRows when nothing matches.
func (q *Queries) GetReferralShopPromoByCode(ctx context.Context, orgID uuid.UUID, code string) (ReferralShopPromo, error) {
	var c ReferralShopPromo
	err := q.db.QueryRow(ctx, getReferralShopPromoByCode, orgID, code).Scan(
		&c.ID, &c.Code, &c.Status, &c.DiscountPct, &c.MaxUses, &c.MaxUsesPerCustomer, &c.ValidFrom, &c.ValidUntil,
		&c.ProgramID, &c.ProgramStatus, &c.ProgramStartsAt, &c.ProgramEndsAt, &c.ReferrerShopOrderID)
	return c, err
}

// shopPromoUseCondition says which shop orders use up a code: orders that are
// paid or may be (money taken and parked), and orders still holding their goods
// for a payment in flight.
const shopPromoUseCondition = `promo_code_id = $1 AND (
        status IN ('paid', 'shipped', 'partially_refunded', 'refunded', 'manual_review')
        OR (status = 'pending_payment' AND expires_at > now()))`

const countShopPromoUses = `-- name: CountShopPromoUses :one
SELECT count(*)::int FROM shop_orders WHERE ` + shopPromoUseCondition

// CountShopPromoUses counts the shop orders a code was used on: paid ones and
// those still waiting for their payment inside the hold. A shop has no
// promo_code_redemptions, so the orders themselves are the count.
func (q *Queries) CountShopPromoUses(ctx context.Context, promoID uuid.UUID) (int, error) {
	var n int
	err := q.db.QueryRow(ctx, countShopPromoUses, promoID).Scan(&n)
	return n, err
}

const countShopPromoUsesByBuyer = `-- name: CountShopPromoUsesByBuyer :one
SELECT count(*)::int FROM shop_orders
WHERE ` + shopPromoUseCondition + ` AND lower(btrim(buyer_email)) = lower(btrim($2))`

// CountShopPromoUsesByBuyer is CountShopPromoUses for one buyer e-mail.
func (q *Queries) CountShopPromoUsesByBuyer(ctx context.Context, promoID uuid.UUID, email string) (int, error) {
	var n int
	err := q.db.QueryRow(ctx, countShopPromoUsesByBuyer, promoID, email).Scan(&n)
	return n, err
}

// LockShopPromo serializes the checkouts that use one code for the rest of the
// transaction, so its limits cannot be raced.
func (q *Queries) LockShopPromo(ctx context.Context, promoID uuid.UUID) error {
	_, err := q.db.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('arena.referral.shop_promo:' || $1::text, 0))`, promoID)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// Rewards of shop orders
// ─────────────────────────────────────────────────────────────────────────────

// InsertReferralShopRewardParams are the fields of a new reward of a shop
// order.
type InsertReferralShopRewardParams struct {
	OrgID               uuid.UUID
	ProgramID           uuid.UUID
	PromoCodeID         uuid.UUID
	ReferrerShopOrderID uuid.UUID
	FriendShopOrderID   uuid.UUID
	AmountMinor         int64
	Currency            string
	CashbackPct         int16
	CapPct              int16
	State               string
	Reason              string
}

const insertReferralShopReward = `-- name: InsertReferralShopReward :one
INSERT INTO referral_rewards (org_id, program_id, promo_code_id, referrer_shop_order_id, friend_shop_order_id,
    amount_minor, currency, cashback_pct, cap_pct, state, reason)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, ''))
ON CONFLICT (friend_shop_order_id) DO NOTHING
RETURNING id`

// InsertReferralShopReward records the reward of a friend's shop order.
// inserted=false when the friend order already has one (a replayed payment).
func (q *Queries) InsertReferralShopReward(ctx context.Context, p InsertReferralShopRewardParams) (uuid.UUID, bool, error) {
	rows, err := q.db.Query(ctx, insertReferralShopReward,
		p.OrgID, p.ProgramID, p.PromoCodeID, p.ReferrerShopOrderID, p.FriendShopOrderID,
		p.AmountMinor, p.Currency, p.CashbackPct, p.CapPct, p.State, p.Reason)
	if err != nil {
		return uuid.Nil, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return uuid.Nil, false, rows.Err()
	}
	var id uuid.UUID
	if err := rows.Scan(&id); err != nil {
		return uuid.Nil, false, err
	}
	rows.Close()
	return id, true, rows.Err()
}

const sumReferrerShopLiveRewards = `-- name: SumReferrerShopLiveRewards :one
SELECT COALESCE(SUM(amount_minor), 0)::bigint FROM referral_rewards
WHERE referrer_shop_order_id = $1 AND state IN ('due', 'sent', 'paid')`

// SumReferrerShopLiveRewards is what the referrer's shop order has been
// promised or paid as cashback: due, sent and paid rewards.
func (q *Queries) SumReferrerShopLiveRewards(ctx context.Context, shopOrderID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, sumReferrerShopLiveRewards, shopOrderID).Scan(&n)
	return n, err
}

const sumPaymentLiveOtherRefunds = `-- name: SumPaymentLiveOtherRefunds :one
SELECT COALESCE(SUM(amount), 0)::bigint FROM refunds
WHERE payment_intent_id = $1 AND origin <> 'referral_cashback' AND state NOT IN ('failed', 'rejected')`

// SumPaymentLiveOtherRefunds is the money other refunds (not cashback) have
// returned or are returning on a payment.
func (q *Queries) SumPaymentLiveOtherRefunds(ctx context.Context, paymentIntentID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, sumPaymentLiveOtherRefunds, paymentIntentID).Scan(&n)
	return n, err
}

// ─────────────────────────────────────────────────────────────────────────────
// The organization's routes for the shop programme
// ─────────────────────────────────────────────────────────────────────────────

const listReferralRewardsForShop = `-- name: ListReferralRewardsForShop :many
SELECT rw.id, pc.code, fo.number, fo.total, fo.currency, ro.number,
       rw.amount_minor, rw.currency, rw.cashback_pct, rw.state, rw.reason, rw.friend_reversal,
       rw.created_at, rw.updated_at
FROM   referral_rewards  rw
JOIN   referral_programs p  ON p.id  = rw.program_id
JOIN   promo_codes       pc ON pc.id = rw.promo_code_id
LEFT   JOIN shop_orders  fo ON fo.id = rw.friend_shop_order_id
LEFT   JOIN shop_orders  ro ON ro.id = rw.referrer_shop_order_id
WHERE  rw.org_id = $1 AND p.scope = 'shop' AND p.org_id = $1
  AND  ($2::text = '' OR rw.state = $2::text)
  AND  ($3::timestamptz IS NULL OR (rw.created_at, rw.id) < ($3::timestamptz, $4::uuid))
ORDER  BY rw.created_at DESC, rw.id DESC
LIMIT  $5`

// ListReferralRewardsForShop pages the rewards of the organization's shop
// programmes, newest first (the same shape as ListReferralRewardsForEvent: the
// order numbers are the shop orders' numbers).
func (q *Queries) ListReferralRewardsForShop(ctx context.Context, orgID uuid.UUID, state string, before *time.Time, beforeID uuid.UUID, limit int32) ([]ReferralRewardListRow, error) {
	rows, err := q.db.Query(ctx, listReferralRewardsForShop, orgID, state, before, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReferralRewardListRow{}
	for rows.Next() {
		var r ReferralRewardListRow
		if err := rows.Scan(&r.ID, &r.Code, &r.FriendOrderNumber, &r.FriendAmountMinor, &r.FriendCurrency,
			&r.ReferrerOrderNumber, &r.AmountMinor, &r.Currency, &r.CashbackPct, &r.State, &r.Reason,
			&r.FriendReversal, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const getReferralSummaryCountsForShop = `-- name: GetReferralSummaryCountsForShop :one
SELECT
  (SELECT count(*) FROM promo_codes pc JOIN referral_programs p ON p.id = pc.referral_program_id
    WHERE p.org_id = $1 AND p.scope = 'shop'),
  (SELECT count(*) FROM referral_rewards rw JOIN referral_programs p ON p.id = rw.program_id
    WHERE p.org_id = $1 AND p.scope = 'shop'
      AND NOT (rw.state = 'cancelled' AND rw.reason = 'self_referral')),
  (SELECT count(*) FROM referral_rewards rw JOIN referral_programs p ON p.id = rw.program_id
    WHERE p.org_id = $1 AND p.scope = 'shop' AND rw.friend_reversal IS NOT NULL)`

// GetReferralSummaryCountsForShop counts the personal codes issued, the friends
// who used one and the friends who later got their money back or opened a
// dispute, over the organization's shop programmes.
func (q *Queries) GetReferralSummaryCountsForShop(ctx context.Context, orgID uuid.UUID) (ReferralSummaryCounts, error) {
	var c ReferralSummaryCounts
	err := q.db.QueryRow(ctx, getReferralSummaryCountsForShop, orgID).Scan(&c.CodesIssued, &c.Friends, &c.FriendReversals)
	return c, err
}

const listReferralDiscountTotalsForShop = `-- name: ListReferralDiscountTotalsForShop :many
SELECT fo.currency, '' AS state, COALESCE(sum(fo.discount), 0)::bigint, count(*)
FROM   referral_rewards rw
JOIN   referral_programs p  ON p.id  = rw.program_id
JOIN   shop_orders       fo ON fo.id = rw.friend_shop_order_id
WHERE  p.org_id = $1 AND p.scope = 'shop'
  AND  NOT (rw.state = 'cancelled' AND rw.reason = 'self_referral')
GROUP  BY fo.currency
ORDER  BY fo.currency`

// ListReferralDiscountTotalsForShop sums the discount friends got in the shop,
// per currency.
func (q *Queries) ListReferralDiscountTotalsForShop(ctx context.Context, orgID uuid.UUID) ([]ReferralMoney, error) {
	return q.referralShopMoney(ctx, listReferralDiscountTotalsForShop, orgID)
}

const listReferralCashbackTotalsForShop = `-- name: ListReferralCashbackTotalsForShop :many
SELECT rw.currency, rw.state, COALESCE(sum(rw.amount_minor), 0)::bigint, count(*)
FROM   referral_rewards rw
JOIN   referral_programs p ON p.id = rw.program_id
WHERE  p.org_id = $1 AND p.scope = 'shop'
GROUP  BY rw.currency, rw.state
ORDER  BY rw.currency, rw.state`

// ListReferralCashbackTotalsForShop sums the shop cashback per currency and
// reward state.
func (q *Queries) ListReferralCashbackTotalsForShop(ctx context.Context, orgID uuid.UUID) ([]ReferralMoney, error) {
	return q.referralShopMoney(ctx, listReferralCashbackTotalsForShop, orgID)
}

func (q *Queries) referralShopMoney(ctx context.Context, sql string, orgID uuid.UUID) ([]ReferralMoney, error) {
	rows, err := q.db.Query(ctx, sql, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReferralMoney{}
	for rows.Next() {
		var m ReferralMoney
		if err := rows.Scan(&m.Currency, &m.State, &m.AmountMinor, &m.Count); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReferralShopChannelFacts is where a shop's payments go: the sales channel
// the shop settings name, if any.
const getReferralShopSalesChannel = `-- name: GetReferralShopSalesChannel :one
SELECT sc.id, COALESCE(sc.provider, ''),
       (sc.settings #>> '{gateway,token_hash}' IS NOT NULL OR sc.settings ->> 'gateway_token_hash' IS NOT NULL)
FROM   shop_settings s
JOIN   sales_channels sc ON sc.id = s.sales_channel_id AND sc.org_id = s.org_id AND sc.deleted_at IS NULL
WHERE  s.org_id = $1`

// GetReferralShopSalesChannel returns the sales channel the organization's shop
// settings name, pgx.ErrNoRows when they name none (or a deleted one).
func (q *Queries) GetReferralShopSalesChannel(ctx context.Context, orgID uuid.UUID) (ReferralChannel, error) {
	var c ReferralChannel
	err := q.db.QueryRow(ctx, getReferralShopSalesChannel, orgID).Scan(&c.ID, &c.Provider, &c.IsSite)
	return c, err
}

// ReferralShopShareFacts are the facts a shop share link is built from.
type ReferralShopShareFacts struct {
	OrgSlug    string `json:"org_slug"`
	ShopDomain string `json:"shop_domain"`
}

const getReferralShopShareFacts = `-- name: GetReferralShopShareFacts :one
SELECT COALESCE(slug, ''), COALESCE(shop_domain, '') FROM organizations WHERE id = $1`

// GetReferralShopShareFacts reads the organization's slug and its own shop
// domain; pgx.ErrNoRows for an unknown organization.
func (q *Queries) GetReferralShopShareFacts(ctx context.Context, orgID uuid.UUID) (ReferralShopShareFacts, error) {
	var f ReferralShopShareFacts
	err := q.db.QueryRow(ctx, getReferralShopShareFacts, orgID).Scan(&f.OrgSlug, &f.ShopDomain)
	return f, err
}

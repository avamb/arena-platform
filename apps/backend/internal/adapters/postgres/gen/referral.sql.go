// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: referral.sql
//
// "Refer a friend" (spec 52, migration 0191). The shared PromoCodeRow is NOT
// widened with origin / referral_* (a dozen queries feed its scanner):
// a promo code's referral facts are read by their own query below.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ReferralProgramRow mirrors one referral_programs row.
type ReferralProgramRow struct {
	ID                uuid.UUID  `json:"id"`
	OrgID             uuid.UUID  `json:"org_id"`
	Scope             string     `json:"scope"`
	EventID           *uuid.UUID `json:"event_id"`
	Status            string     `json:"status"`
	FriendDiscountPct int16      `json:"friend_discount_pct"`
	CashbackPct       int16      `json:"cashback_pct"`
	CashbackCapPct    int16      `json:"cashback_cap_pct"`
	MaxFriends        *int16     `json:"max_friends"`
	StartsAt          *time.Time `json:"starts_at"`
	EndsAt            *time.Time `json:"ends_at"`
	TermsAcceptedAt   time.Time  `json:"terms_accepted_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

const referralProgramColumns = `id, org_id, scope, event_id, status, friend_discount_pct, cashback_pct,
       cashback_cap_pct, max_friends, starts_at, ends_at, terms_accepted_at, created_at, updated_at`

func scanReferralProgramRow(row interface {
	Scan(dest ...any) error
}) (ReferralProgramRow, error) {
	var r ReferralProgramRow
	err := row.Scan(&r.ID, &r.OrgID, &r.Scope, &r.EventID, &r.Status, &r.FriendDiscountPct, &r.CashbackPct,
		&r.CashbackCapPct, &r.MaxFriends, &r.StartsAt, &r.EndsAt, &r.TermsAcceptedAt, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

const getReferralProgramByID = `-- name: GetReferralProgramByID :one
SELECT ` + referralProgramColumns + `
FROM referral_programs WHERE id = $1`

// GetReferralProgramByID returns a programme whatever its status.
func (q *Queries) GetReferralProgramByID(ctx context.Context, id uuid.UUID) (ReferralProgramRow, error) {
	return scanReferralProgramRow(q.db.QueryRow(ctx, getReferralProgramByID, id))
}

const getActiveReferralProgramForEvent = `-- name: GetActiveReferralProgramForEvent :one
SELECT ` + referralProgramColumns + `
FROM referral_programs
WHERE scope = 'event' AND event_id = $1 AND status = 'active'
  AND (starts_at IS NULL OR starts_at <= $2)
  AND (ends_at IS NULL OR ends_at > $2)`

// GetActiveReferralProgramForEvent returns the event's programme while it is
// active and inside its period at now. pgx.ErrNoRows otherwise.
func (q *Queries) GetActiveReferralProgramForEvent(ctx context.Context, eventID uuid.UUID, now time.Time) (ReferralProgramRow, error) {
	return scanReferralProgramRow(q.db.QueryRow(ctx, getActiveReferralProgramForEvent, eventID, now))
}

// ReferralOrderFacts is the slice of an order the programme needs.
type ReferralOrderFacts struct {
	ID         uuid.UUID  `json:"id"`
	SystemID   int64      `json:"system_id"`
	OrgID      uuid.UUID  `json:"org_id"`
	EventID    uuid.UUID  `json:"event_id"`
	CustomerID *uuid.UUID `json:"customer_id"`
	Status     string     `json:"status"`
	Source     string     `json:"source"`
	Currency   string     `json:"currency"`
	Subtotal   int64      `json:"subtotal"`
	Discount   int64      `json:"discount"`
	Total      int64      `json:"total"`
	BuyerName  string     `json:"buyer_name"`
}

const getReferralOrderFacts = `-- name: GetReferralOrderFacts :one
SELECT id, system_id, org_id, event_id, customer_id, status, source, currency::text,
       subtotal, discount, total, COALESCE(buyer_name, '')
FROM orders WHERE id = $1`

// GetReferralOrderFacts reads one order. pgx.ErrNoRows when it does not exist.
func (q *Queries) GetReferralOrderFacts(ctx context.Context, orderID uuid.UUID) (ReferralOrderFacts, error) {
	var o ReferralOrderFacts
	err := q.db.QueryRow(ctx, getReferralOrderFacts, orderID).Scan(
		&o.ID, &o.SystemID, &o.OrgID, &o.EventID, &o.CustomerID, &o.Status, &o.Source, &o.Currency, &o.Subtotal, &o.Discount, &o.Total, &o.BuyerName)
	return o, err
}

// ReferralCodeRow is a personal code with the percentages it promises.
type ReferralCodeRow struct {
	PromoCodeID       uuid.UUID `json:"promo_code_id"`
	Code              string    `json:"code"`
	ProgramID         uuid.UUID `json:"program_id"`
	FriendDiscountPct int16     `json:"friend_discount_pct"`
	CashbackPct       int16     `json:"cashback_pct"`
	Status            string    `json:"status"`
}

const getReferralCodeByReferrerOrder = `-- name: GetReferralCodeByReferrerOrder :one
SELECT pc.id, pc.code, pc.referral_program_id, pc.discount_value::smallint,
       COALESCE(pc.referral_cashback_pct, p.cashback_pct), pc.status
FROM promo_codes pc
JOIN referral_programs p ON p.id = pc.referral_program_id
WHERE pc.referrer_order_id = $1`

// GetReferralCodeByReferrerOrder returns the personal code issued for an
// order, pgx.ErrNoRows when none was issued yet.
func (q *Queries) GetReferralCodeByReferrerOrder(ctx context.Context, orderID uuid.UUID) (ReferralCodeRow, error) {
	var r ReferralCodeRow
	err := q.db.QueryRow(ctx, getReferralCodeByReferrerOrder, orderID).Scan(
		&r.PromoCodeID, &r.Code, &r.ProgramID, &r.FriendDiscountPct, &r.CashbackPct, &r.Status)
	return r, err
}

const listLiveSessionIDsForEvent = `-- name: ListLiveSessionIDsForEvent :many
SELECT id::text FROM sessions
WHERE event_id = $1 AND deleted_at IS NULL AND status <> 'cancelled'
ORDER BY start_at, id`

// ListLiveSessionIDsForEvent returns the event's sessions a code can be used
// on: not deleted and not cancelled.
func (q *Queries) ListLiveSessionIDsForEvent(ctx context.Context, eventID uuid.UUID) ([]string, error) {
	rows, err := q.db.Query(ctx, listLiveSessionIDsForEvent, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

const insertReferralPromoCode = `-- name: InsertReferralPromoCode :one
INSERT INTO promo_codes (org_id, code, discount_type, discount_value, applies_to_session_ids,
    max_uses, max_uses_per_customer, valid_until, status, origin, referral_program_id, referrer_order_id,
    referral_cashback_pct, referral_cap_pct)
VALUES ($1, $2, 'percent', $3, $4, $5, 1, $6, 'active', 'referral', $7, $8, $9, $10)
ON CONFLICT DO NOTHING
RETURNING id`

// InsertReferralPromoCodeParams are the fields of a personal code.
type InsertReferralPromoCodeParams struct {
	OrgID           uuid.UUID
	Code            string
	DiscountPct     int64
	SessionIDs      []string
	MaxUses         *int32
	ValidUntil      *time.Time
	ProgramID       uuid.UUID
	ReferrerOrderID uuid.UUID
	// CashbackPct and CapPct are the programme's percentages at issue time;
	// the code keeps them, whatever the programme becomes.
	CashbackPct int16
	CapPct      int16
}

// InsertReferralPromoCode creates the personal code of an order. It returns
// inserted=false (and no error) when the row was not written: either the code
// text is taken in the organization or the order already has a code. The
// caller tells the two apart by looking the order's code up.
func (q *Queries) InsertReferralPromoCode(ctx context.Context, p InsertReferralPromoCodeParams) (uuid.UUID, bool, error) {
	if p.SessionIDs == nil {
		p.SessionIDs = []string{}
	}
	var id uuid.UUID
	rows, err := q.db.Query(ctx, insertReferralPromoCode,
		p.OrgID, p.Code, p.DiscountPct, p.SessionIDs, p.MaxUses, p.ValidUntil, p.ProgramID, p.ReferrerOrderID,
		p.CashbackPct, p.CapPct)
	if err != nil {
		return uuid.Nil, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return uuid.Nil, false, rows.Err()
	}
	if err := rows.Scan(&id); err != nil {
		return uuid.Nil, false, err
	}
	rows.Close()
	return id, true, rows.Err()
}

// ReferralPromoMeta is what a promo code says about the programme.
type ReferralPromoMeta struct {
	Origin              string     `json:"origin"`
	ProgramID           *uuid.UUID `json:"referral_program_id"`
	ReferrerOrderID     *uuid.UUID `json:"referrer_order_id"`
	ReferrerShopOrderID *uuid.UUID `json:"referrer_shop_order_id"`
	// CashbackPct and CapPct are what the code promised when it was issued
	// (nil for a code written before the snapshot existed); MaxUses is the
	// code's own limit of friends.
	CashbackPct *int16 `json:"referral_cashback_pct"`
	CapPct      *int16 `json:"referral_cap_pct"`
	MaxUses     *int32 `json:"max_uses"`
}

const getReferralPromoMeta = `-- name: GetReferralPromoMeta :one
SELECT origin, referral_program_id, referrer_order_id, referrer_shop_order_id,
       referral_cashback_pct, referral_cap_pct, max_uses
FROM promo_codes WHERE id = $1`

// GetReferralPromoMeta reads a promo code's origin and referral links, deleted
// codes included (the discount was honoured, the cashback follows).
func (q *Queries) GetReferralPromoMeta(ctx context.Context, promoID uuid.UUID) (ReferralPromoMeta, error) {
	var m ReferralPromoMeta
	err := q.db.QueryRow(ctx, getReferralPromoMeta, promoID).Scan(
		&m.Origin, &m.ProgramID, &m.ReferrerOrderID, &m.ReferrerShopOrderID,
		&m.CashbackPct, &m.CapPct, &m.MaxUses)
	return m, err
}

// ReferralRewardRow mirrors one referral_rewards row.
type ReferralRewardRow struct {
	ID                  uuid.UUID  `json:"id"`
	OrgID               uuid.UUID  `json:"org_id"`
	ProgramID           uuid.UUID  `json:"program_id"`
	PromoCodeID         uuid.UUID  `json:"promo_code_id"`
	ReferrerOrderID     *uuid.UUID `json:"referrer_order_id"`
	ReferrerShopOrderID *uuid.UUID `json:"referrer_shop_order_id"`
	FriendOrderID       *uuid.UUID `json:"friend_order_id"`
	FriendShopOrderID   *uuid.UUID `json:"friend_shop_order_id"`
	AmountMinor         int64      `json:"amount_minor"`
	Currency            string     `json:"currency"`
	CashbackPct         int16      `json:"cashback_pct"`
	CapPct              int16      `json:"cap_pct"`
	State               string     `json:"state"`
	Reason              *string    `json:"reason"`
	ProviderCode        *string    `json:"provider_code"`
	FriendReversal      *string    `json:"friend_reversal"`
	ReversalNotifiedAt  *time.Time `json:"reversal_notified_at"`
	LetterSentAt        *time.Time `json:"letter_sent_at"`
	// CorrectionSentAt: the referrer was told the money went back and later
	// got the one correction letter (migration 0191).
	CorrectionSentAt *time.Time `json:"correction_sent_at"`
	Attempts         int32      `json:"attempts"`
	NextAttemptAt    *time.Time `json:"next_attempt_at"`
	RefundID         *uuid.UUID `json:"refund_id"`
	RefundBatchID    *uuid.UUID `json:"refund_batch_id"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// ReferralRewardColumns is the SELECT list ScanReferralRewardRow reads.
const ReferralRewardColumns = `id, org_id, program_id, promo_code_id, referrer_order_id, referrer_shop_order_id,
       friend_order_id, friend_shop_order_id, amount_minor, currency, cashback_pct, cap_pct, state, reason,
       provider_code, friend_reversal, reversal_notified_at, letter_sent_at, correction_sent_at, attempts, next_attempt_at, refund_id,
       refund_batch_id, created_at, updated_at`

// ScanReferralRewardRow scans a row selected with ReferralRewardColumns.
func ScanReferralRewardRow(row interface {
	Scan(dest ...any) error
}) (ReferralRewardRow, error) {
	var r ReferralRewardRow
	err := row.Scan(&r.ID, &r.OrgID, &r.ProgramID, &r.PromoCodeID, &r.ReferrerOrderID, &r.ReferrerShopOrderID,
		&r.FriendOrderID, &r.FriendShopOrderID, &r.AmountMinor, &r.Currency, &r.CashbackPct, &r.CapPct, &r.State,
		&r.Reason, &r.ProviderCode, &r.FriendReversal, &r.ReversalNotifiedAt, &r.LetterSentAt, &r.CorrectionSentAt, &r.Attempts, &r.NextAttemptAt,
		&r.RefundID, &r.RefundBatchID, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

const getReferralRewardByID = `-- name: GetReferralRewardByID :one
SELECT ` + ReferralRewardColumns + ` FROM referral_rewards WHERE id = $1`

// GetReferralRewardByID reads one reward.
func (q *Queries) GetReferralRewardByID(ctx context.Context, id uuid.UUID) (ReferralRewardRow, error) {
	return ScanReferralRewardRow(q.db.QueryRow(ctx, getReferralRewardByID, id))
}

const getReferralRewardByIDForUpdate = `-- name: GetReferralRewardByIDForUpdate :one
SELECT ` + ReferralRewardColumns + ` FROM referral_rewards WHERE id = $1 FOR UPDATE`

// GetReferralRewardByIDForUpdate reads and locks one reward; call it inside a
// transaction.
func (q *Queries) GetReferralRewardByIDForUpdate(ctx context.Context, id uuid.UUID) (ReferralRewardRow, error) {
	return ScanReferralRewardRow(q.db.QueryRow(ctx, getReferralRewardByIDForUpdate, id))
}

// InsertReferralRewardParams are the fields of a new reward.
type InsertReferralRewardParams struct {
	OrgID           uuid.UUID
	ProgramID       uuid.UUID
	PromoCodeID     uuid.UUID
	ReferrerOrderID uuid.UUID
	FriendOrderID   uuid.UUID
	AmountMinor     int64
	Currency        string
	CashbackPct     int16
	CapPct          int16
	State           string
	Reason          string
}

const insertReferralReward = `-- name: InsertReferralReward :one
INSERT INTO referral_rewards (org_id, program_id, promo_code_id, referrer_order_id, friend_order_id,
    amount_minor, currency, cashback_pct, cap_pct, state, reason)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, ''))
ON CONFLICT (friend_order_id) DO NOTHING
RETURNING id`

// InsertReferralReward records the reward of a friend order. inserted=false
// when the friend order already has one (a replayed payment).
func (q *Queries) InsertReferralReward(ctx context.Context, p InsertReferralRewardParams) (uuid.UUID, bool, error) {
	rows, err := q.db.Query(ctx, insertReferralReward,
		p.OrgID, p.ProgramID, p.PromoCodeID, p.ReferrerOrderID, p.FriendOrderID,
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

const sumReferrerLiveRewards = `-- name: SumReferrerLiveRewards :one
SELECT COALESCE(SUM(amount_minor), 0)::bigint FROM referral_rewards
WHERE referrer_order_id = $1 AND state IN ('due', 'sent', 'paid')`

// SumReferrerLiveRewards is what the referrer's order has been promised or
// paid as cashback: due, sent and paid rewards.
func (q *Queries) SumReferrerLiveRewards(ctx context.Context, referrerOrderID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, sumReferrerLiveRewards, referrerOrderID).Scan(&n)
	return n, err
}

const sumOrderLiveOtherRefunds = `-- name: SumOrderLiveOtherRefunds :one
SELECT COALESCE(SUM(amount), 0)::bigint FROM refunds
WHERE order_id = $1 AND origin <> 'referral_cashback' AND state NOT IN ('failed', 'rejected')`

// SumOrderLiveOtherRefunds is the money other refunds (not cashback) have
// returned or are returning on an order.
func (q *Queries) SumOrderLiveOtherRefunds(ctx context.Context, orderID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, sumOrderLiveOtherRefunds, orderID).Scan(&n)
	return n, err
}

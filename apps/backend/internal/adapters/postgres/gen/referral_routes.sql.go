// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: referral_routes.sql
//
// "Refer a friend" (spec 52): the queries behind the organization's routes
// (programme, rewards, summary), the buyer's share link and the refund-options
// warning. The pay-out SQL lives in internal/platform/referral.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const getLatestReferralProgramForEvent = `-- name: GetLatestReferralProgramForEvent :one
SELECT ` + referralProgramColumns + `
FROM referral_programs
WHERE scope = 'event' AND event_id = $1 AND org_id = $2
ORDER BY (status = 'ended'), created_at DESC, id DESC
LIMIT 1`

// GetLatestReferralProgramForEvent returns the event's live (active or paused)
// programme, else its most recent ended one. pgx.ErrNoRows when it never had
// one. orgID scopes the read to the path organization.
func (q *Queries) GetLatestReferralProgramForEvent(ctx context.Context, eventID, orgID uuid.UUID) (ReferralProgramRow, error) {
	return scanReferralProgramRow(q.db.QueryRow(ctx, getLatestReferralProgramForEvent, eventID, orgID))
}

// LockReferralProgramEvent serializes writers of one event's programme (the
// partial unique index allows one live programme per event, and a race of two
// creates would otherwise answer a raw 23505). It is a transaction-scoped
// advisory lock: call it first inside the writer's transaction.
func (q *Queries) LockReferralProgramEvent(ctx context.Context, eventID uuid.UUID) error {
	_, err := q.db.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('arena.referral.program:' || $1::text, 0))`, eventID)
	return err
}

// InsertReferralProgramParams are the fields of a new programme.
type InsertReferralProgramParams struct {
	OrgID             uuid.UUID
	EventID           uuid.UUID
	FriendDiscountPct int16
	CashbackPct       int16
	CashbackCapPct    int16
	MaxFriends        *int16
	EndsAt            *time.Time
	CreatedBy         string
	CreatedVia        string
}

const insertReferralProgram = `-- name: InsertReferralProgram :one
INSERT INTO referral_programs (org_id, scope, event_id, status, friend_discount_pct, cashback_pct,
    cashback_cap_pct, max_friends, ends_at, terms_accepted_at, created_by, created_via)
VALUES ($1, 'event', $2, 'active', $3, $4, $5, $6, $7, now(), NULLIF($8, ''), NULLIF($9, ''))
RETURNING ` + referralProgramColumns

// InsertReferralProgram starts a programme for an event. The terms are
// accepted at this moment (the caller checked the person ticked them).
func (q *Queries) InsertReferralProgram(ctx context.Context, p InsertReferralProgramParams) (ReferralProgramRow, error) {
	return scanReferralProgramRow(q.db.QueryRow(ctx, insertReferralProgram,
		p.OrgID, p.EventID, p.FriendDiscountPct, p.CashbackPct, p.CashbackCapPct, p.MaxFriends,
		p.EndsAt, p.CreatedBy, p.CreatedVia))
}

// UpdateReferralProgramParams are the settings a PUT replaces.
type UpdateReferralProgramParams struct {
	ID                uuid.UUID
	FriendDiscountPct int16
	CashbackPct       int16
	CashbackCapPct    int16
	MaxFriends        *int16
	EndsAt            *time.Time
}

const updateReferralProgram = `-- name: UpdateReferralProgram :one
UPDATE referral_programs
SET    friend_discount_pct = $2, cashback_pct = $3, cashback_cap_pct = $4,
       max_friends = $5, ends_at = $6, updated_at = now()
WHERE  id = $1 AND status <> 'ended'
RETURNING ` + referralProgramColumns

// UpdateReferralProgram replaces the settings of a live programme. Codes
// already issued keep what they promised: this changes the percentages of NEW
// codes only (a code carries the discount, a reward copies the cashback).
// pgx.ErrNoRows for an ended or unknown programme.
func (q *Queries) UpdateReferralProgram(ctx context.Context, p UpdateReferralProgramParams) (ReferralProgramRow, error) {
	return scanReferralProgramRow(q.db.QueryRow(ctx, updateReferralProgram,
		p.ID, p.FriendDiscountPct, p.CashbackPct, p.CashbackCapPct, p.MaxFriends, p.EndsAt))
}

const setReferralProgramStatus = `-- name: SetReferralProgramStatus :one
UPDATE referral_programs
SET    status = $2, updated_at = now()
WHERE  id = $1 AND status <> 'ended'
RETURNING ` + referralProgramColumns

// SetReferralProgramStatus moves a live programme to active, paused or ended.
// pgx.ErrNoRows for an ended or unknown programme (ended is final).
func (q *Queries) SetReferralProgramStatus(ctx context.Context, id uuid.UUID, status string) (ReferralProgramRow, error) {
	return scanReferralProgramRow(q.db.QueryRow(ctx, setReferralProgramStatus, id, status))
}

// ReferralChannel is a sales channel an event sells through and who takes the
// money there.
type ReferralChannel struct {
	ID       uuid.UUID `json:"id"`
	Provider string    `json:"provider"`
	// IsSite is true for a channel with a gateway credential: a WordPress
	// site's, whose money never passes through arena.
	IsSite bool `json:"is_site"`
}

const listReferralEventChannels = `-- name: ListReferralEventChannels :many
SELECT sc.id, COALESCE(sc.provider, ''),
       (sc.settings #>> '{gateway,token_hash}' IS NOT NULL OR sc.settings ->> 'gateway_token_hash' IS NOT NULL)
FROM   event_publications ep
JOIN   agent_feed_tokens  aft ON aft.id = ep.feed_token_id
JOIN   sales_channels     sc  ON sc.id  = aft.sales_channel_id
WHERE  ep.event_id = $1
  AND  sc.org_id   = $2
  AND  sc.deleted_at IS NULL
  AND  aft.is_active
  AND  aft.revoked_at IS NULL
GROUP  BY sc.id
ORDER  BY MIN(ep.published_at), sc.id`

// ListReferralEventChannels lists the organization's channels the event is
// published into through a usable feed token, oldest publication first.
func (q *Queries) ListReferralEventChannels(ctx context.Context, eventID, orgID uuid.UUID) ([]ReferralChannel, error) {
	rows, err := q.db.Query(ctx, listReferralEventChannels, eventID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReferralChannel{}
	for rows.Next() {
		var c ReferralChannel
		if err := rows.Scan(&c.ID, &c.Provider, &c.IsSite); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

const listReferralOrgChannels = `-- name: ListReferralOrgChannels :many
SELECT sc.id, COALESCE(sc.provider, ''),
       (sc.settings #>> '{gateway,token_hash}' IS NOT NULL OR sc.settings ->> 'gateway_token_hash' IS NOT NULL)
FROM   sales_channels sc
WHERE  sc.org_id = $1 AND sc.deleted_at IS NULL
  AND  ` + channelIsSales + `
ORDER  BY sc.created_at, sc.id`

// ListReferralOrgChannels lists the organization's live sales channels. It is
// the fallback for an event that is not published anywhere yet.
func (q *Queries) ListReferralOrgChannels(ctx context.Context, orgID uuid.UUID) ([]ReferralChannel, error) {
	rows, err := q.db.Query(ctx, listReferralOrgChannels, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReferralChannel{}
	for rows.Next() {
		var c ReferralChannel
		if err := rows.Scan(&c.ID, &c.Provider, &c.IsSite); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReferralRewardListRow is one reward as the organizer's list shows it. It
// carries order NUMBERS and amounts, never a buyer's e-mail, phone or name.
type ReferralRewardListRow struct {
	ID                  uuid.UUID `json:"id"`
	Code                string    `json:"code"`
	FriendOrderNumber   *int64    `json:"friend_order_number"`
	FriendAmountMinor   *int64    `json:"friend_amount_minor"`
	FriendCurrency      *string   `json:"friend_currency"`
	ReferrerOrderNumber *int64    `json:"referrer_order_number"`
	AmountMinor         int64     `json:"amount_minor"`
	Currency            string    `json:"currency"`
	CashbackPct         int16     `json:"cashback_pct"`
	State               string    `json:"state"`
	Reason              *string   `json:"reason"`
	FriendReversal      *string   `json:"friend_reversal"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

const listReferralRewardsForEvent = `-- name: ListReferralRewardsForEvent :many
SELECT rw.id, pc.code, fo.system_id, fo.total, fo.currency::text, ro.system_id,
       rw.amount_minor, rw.currency, rw.cashback_pct, rw.state, rw.reason, rw.friend_reversal,
       rw.created_at, rw.updated_at
FROM   referral_rewards  rw
JOIN   referral_programs p  ON p.id  = rw.program_id
JOIN   promo_codes       pc ON pc.id = rw.promo_code_id
LEFT   JOIN orders       fo ON fo.id = rw.friend_order_id
LEFT   JOIN orders       ro ON ro.id = rw.referrer_order_id
WHERE  rw.org_id = $1 AND p.event_id = $2
  AND  ($3::text = '' OR rw.state = $3::text)
  AND  ($4::timestamptz IS NULL OR (rw.created_at, rw.id) < ($4::timestamptz, $5::uuid))
ORDER  BY rw.created_at DESC, rw.id DESC
LIMIT  $6`

// ListReferralRewardsForEvent pages the rewards of an event's programmes,
// newest first. state "" means every state; a nil before is the first page,
// otherwise the page after (before, beforeID) in the list's own order.
func (q *Queries) ListReferralRewardsForEvent(ctx context.Context, orgID, eventID uuid.UUID, state string, before *time.Time, beforeID uuid.UUID, limit int32) ([]ReferralRewardListRow, error) {
	rows, err := q.db.Query(ctx, listReferralRewardsForEvent, orgID, eventID, state, before, beforeID, limit)
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

// ReferralSummaryCounts are the head numbers of an event's programme.
type ReferralSummaryCounts struct {
	CodesIssued     int64 `json:"codes_issued"`
	Friends         int64 `json:"friends"`
	FriendReversals int64 `json:"friend_reversals"`
}

const getReferralSummaryCounts = `-- name: GetReferralSummaryCounts :one
SELECT
  (SELECT count(*) FROM promo_codes pc JOIN referral_programs p ON p.id = pc.referral_program_id
    WHERE p.org_id = $1 AND p.event_id = $2),
  (SELECT count(*) FROM referral_rewards rw JOIN referral_programs p ON p.id = rw.program_id
    WHERE p.org_id = $1 AND p.event_id = $2
      AND NOT (rw.state = 'cancelled' AND rw.reason = 'self_referral')),
  (SELECT count(*) FROM referral_rewards rw JOIN referral_programs p ON p.id = rw.program_id
    WHERE p.org_id = $1 AND p.event_id = $2 AND rw.friend_reversal IS NOT NULL)`

// GetReferralSummaryCounts counts the personal codes issued, the friends who
// used one (a self-invitation is not a friend) and the friends who later got
// their money back or opened a dispute.
func (q *Queries) GetReferralSummaryCounts(ctx context.Context, orgID, eventID uuid.UUID) (ReferralSummaryCounts, error) {
	var c ReferralSummaryCounts
	err := q.db.QueryRow(ctx, getReferralSummaryCounts, orgID, eventID).Scan(&c.CodesIssued, &c.Friends, &c.FriendReversals)
	return c, err
}

// ReferralMoney is a sum of minor units in one currency (and, for cashback,
// one reward state).
type ReferralMoney struct {
	Currency    string `json:"currency"`
	State       string `json:"state"`
	AmountMinor int64  `json:"amount_minor"`
	Count       int64  `json:"count"`
}

const listReferralDiscountTotals = `-- name: ListReferralDiscountTotals :many
SELECT fo.currency::text, '' AS state, COALESCE(sum(fo.discount), 0)::bigint, count(*)
FROM   referral_rewards rw
JOIN   referral_programs p  ON p.id  = rw.program_id
JOIN   orders            fo ON fo.id = rw.friend_order_id
WHERE  p.org_id = $1 AND p.event_id = $2
  AND  NOT (rw.state = 'cancelled' AND rw.reason = 'self_referral')
GROUP  BY fo.currency
ORDER  BY fo.currency`

// ListReferralDiscountTotals sums the discount friends got, per currency.
func (q *Queries) ListReferralDiscountTotals(ctx context.Context, orgID, eventID uuid.UUID) ([]ReferralMoney, error) {
	return q.referralMoney(ctx, listReferralDiscountTotals, orgID, eventID)
}

const listReferralCashbackTotals = `-- name: ListReferralCashbackTotals :many
SELECT rw.currency, rw.state, COALESCE(sum(rw.amount_minor), 0)::bigint, count(*)
FROM   referral_rewards rw
JOIN   referral_programs p ON p.id = rw.program_id
WHERE  p.org_id = $1 AND p.event_id = $2
GROUP  BY rw.currency, rw.state
ORDER  BY rw.currency, rw.state`

// ListReferralCashbackTotals sums the cashback per currency and reward state.
func (q *Queries) ListReferralCashbackTotals(ctx context.Context, orgID, eventID uuid.UUID) ([]ReferralMoney, error) {
	return q.referralMoney(ctx, listReferralCashbackTotals, orgID, eventID)
}

func (q *Queries) referralMoney(ctx context.Context, sql string, orgID, eventID uuid.UUID) ([]ReferralMoney, error) {
	rows, err := q.db.Query(ctx, sql, orgID, eventID)
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

// ReferralShareFacts are the slugs a share link is built from.
type ReferralShareFacts struct {
	OrgSlug      string `json:"org_slug"`
	EventSlug    string `json:"event_slug"`
	PromoterSlug string `json:"promoter_slug"`
}

const getReferralShareFacts = `-- name: GetReferralShareFacts :one
SELECT COALESCE(o.slug, ''), COALESCE(e.slug, ''), COALESCE(p.slug, '')
FROM   events e
JOIN   organizations o ON o.id = e.org_id
LEFT   JOIN event_promoters ep ON ep.event_id = e.id
LEFT   JOIN org_promoters   p  ON p.id = ep.promoter_id AND p.archived_at IS NULL
WHERE  e.id = $1`

// GetReferralShareFacts reads the organization, event and (live) promoter
// slugs of an event; pgx.ErrNoRows for an unknown event.
func (q *Queries) GetReferralShareFacts(ctx context.Context, eventID uuid.UUID) (ReferralShareFacts, error) {
	var f ReferralShareFacts
	err := q.db.QueryRow(ctx, getReferralShareFacts, eventID).Scan(&f.OrgSlug, &f.EventSlug, &f.PromoterSlug)
	return f, err
}

// ReferralCashbackWarning is the cashback a referrer was already given for a
// friend's order.
type ReferralCashbackWarning struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Code        string `json:"code"`
}

const getReferralCashbackForFriendOrder = `-- name: GetReferralCashbackForFriendOrder :one
SELECT rw.amount_minor, rw.currency, pc.code
FROM   referral_rewards rw
JOIN   promo_codes pc ON pc.id = rw.promo_code_id
WHERE  rw.org_id = $1 AND rw.friend_order_id = $2 AND rw.state IN ('sent', 'paid')`

// GetReferralCashbackForFriendOrder returns the cashback paid (or on its way)
// to the referrer for this friend order. pgx.ErrNoRows when there is none.
func (q *Queries) GetReferralCashbackForFriendOrder(ctx context.Context, orgID, friendOrderID uuid.UUID) (ReferralCashbackWarning, error) {
	var w ReferralCashbackWarning
	err := q.db.QueryRow(ctx, getReferralCashbackForFriendOrder, orgID, friendOrderID).Scan(&w.AmountMinor, &w.Currency, &w.Code)
	return w, err
}

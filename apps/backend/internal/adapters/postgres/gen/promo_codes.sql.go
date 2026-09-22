// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: promo_codes.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// PromoCodeRow — shared result type for all promo_codes queries
// ─────────────────────────────────────────────────────────────────────────────

// PromoCodeRow is the result type returned by all promo_codes queries.
//
// discount_value is stored in the smallest currency unit (cents) for
// 'fixed_amount' discount_type, or as a percentage integer (1–100) for
// 'percent' discount_type.
//
// applies_to_tier_ids is the set of tier UUIDs this promo applies to
// (empty slice = applies to any tier). applies_to_session_ids is the set of
// event sessions the code is for (empty = any session of the organization);
// the two narrow each other (migration 0108).
//
// currency is the ISO-4217 code a fixed_amount discount is expressed in; nil
// (a code older than 0108) means it applies to any currency.
//
// max_uses and max_uses_per_customer are nil for unlimited usage.
// valid_from and valid_until are nil for no time bounds.
// deleted_at is nil for active codes and non-nil for soft-deleted ones.
type PromoCodeRow struct {
	ID                  uuid.UUID  `json:"id"`
	OrgID               uuid.UUID  `json:"org_id"`
	Code                string     `json:"code"`
	DiscountType        string     `json:"discount_type"`
	DiscountValue       int64      `json:"discount_value"`
	AppliesToTierIDs    []string   `json:"applies_to_tier_ids"`    // UUID strings
	AppliesToSessionIDs []string   `json:"applies_to_session_ids"` // UUID strings
	Currency            *string    `json:"currency"`
	MaxUses             *int32     `json:"max_uses"`
	MaxUsesPerCustomer  *int32     `json:"max_uses_per_customer"`
	ValidFrom           *time.Time `json:"valid_from"`
	ValidUntil          *time.Time `json:"valid_until"`
	MinOrderAmount      int64      `json:"min_order_amount"`
	Status              string     `json:"status"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	DeletedAt           *time.Time `json:"deleted_at"`
}

// PromoCodeRedemptionRow mirrors one promo_code_redemptions row. Since
// migration 0108 a redemption names the order it paid for, the customer who
// paid and the sales channel the order came through; the three are nil only
// on a pre-0108 row whose order could not be derived.
type PromoCodeRedemptionRow struct {
	ID             uuid.UUID  `json:"id"`
	PromoCodeID    uuid.UUID  `json:"promo_code_id"`
	UserID         *uuid.UUID `json:"user_id"`
	ReservationID  *uuid.UUID `json:"reservation_id"`
	RedeemedAt     time.Time  `json:"redeemed_at"`
	DiscountAmount int64      `json:"discount_amount"`
	OrderAmount    int64      `json:"order_amount"`
	OrderID        *uuid.UUID `json:"order_id"`
	CustomerID     *uuid.UUID `json:"customer_id"`
	ChannelID      *uuid.UUID `json:"channel_id"`
}

// PromoCodeUsageRow is one code's usage tally (ListPromoCodeUsageByOrg).
type PromoCodeUsageRow struct {
	PromoCodeID   uuid.UUID  `json:"promo_code_id"`
	Uses          int32      `json:"uses"`
	DiscountTotal int64      `json:"discount_total"`
	LastUsedAt    *time.Time `json:"last_used_at"`
}

// PromoCodeRedemptionReportRow is one line of the organizer's usage report
// (ListPromoCodeRedemptionsByOrg): the redemption joined to the order it
// paid for. The order columns are nil for a redemption older than
// migration 0108 whose order could not be derived.
type PromoCodeRedemptionReportRow struct {
	ID             uuid.UUID  `json:"id"`
	PromoCodeID    uuid.UUID  `json:"promo_code_id"`
	Code           string     `json:"code"`
	RedeemedAt     time.Time  `json:"redeemed_at"`
	DiscountAmount int64      `json:"discount_amount"`
	OrderAmount    int64      `json:"order_amount"`
	OrderID        *uuid.UUID `json:"order_id"`
	OrderNumber    *int64     `json:"order_number"`
	OrderStatus    *string    `json:"order_status"`
	Currency       *string    `json:"currency"`
	BuyerEmail     *string    `json:"buyer_email"`
	SessionID      *uuid.UUID `json:"session_id"`
	ChannelID      *uuid.UUID `json:"channel_id"`
	ChannelName    *string    `json:"channel_name"`
}

// scanPromoCodeRow scans a single promo_codes row into a PromoCodeRow.
// The uuid[] columns are scanned into []string via the pgx text codec (pgx
// v5 falls back to text representation for uuid[] arrays).
func scanPromoCodeRow(row interface {
	Scan(dest ...any) error
}) (PromoCodeRow, error) {
	var r PromoCodeRow
	err := row.Scan(
		&r.ID,
		&r.OrgID,
		&r.Code,
		&r.DiscountType,
		&r.DiscountValue,
		&r.AppliesToTierIDs,
		&r.AppliesToSessionIDs,
		&r.Currency,
		&r.MaxUses,
		&r.MaxUsesPerCustomer,
		&r.ValidFrom,
		&r.ValidUntil,
		&r.MinOrderAmount,
		&r.Status,
		&r.CreatedAt,
		&r.UpdatedAt,
		&r.DeletedAt,
	)
	return r, err
}

// promoCodeColumns is the SELECT list every promo_codes query shares; a new
// column goes here AND into scanPromoCodeRow, in the same position.
const promoCodeColumns = `id, org_id, code, discount_type, discount_value, applies_to_tier_ids,
       applies_to_session_ids, currency,
       max_uses, max_uses_per_customer, valid_from, valid_until, min_order_amount,
       status, created_at, updated_at, deleted_at`

// ─────────────────────────────────────────────────────────────────────────────
// InsertPromoCode
// ─────────────────────────────────────────────────────────────────────────────

const insertPromoCode = `-- name: InsertPromoCode :one
INSERT INTO promo_codes (org_id, code, discount_type, discount_value, applies_to_tier_ids,
    applies_to_session_ids, currency,
    max_uses, max_uses_per_customer, valid_from, valid_until, min_order_amount, status)
VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, $9, $10, $11, $12, COALESCE(NULLIF($13, ''), 'active'))
RETURNING ` + promoCodeColumns

// InsertPromoCode creates a new promo code for the given organization.
// discount_value is in cents for fixed_amount or 1–100 for percent type.
// appliesToTierIDs / appliesToSessionIDs may be nil or empty for "any";
// an empty currency is stored as NULL ("any currency").
// Returns the created row including the uuidv7 PK assigned by the database.
func (q *Queries) InsertPromoCode(
	ctx context.Context,
	orgID uuid.UUID,
	code, discountType string,
	discountValue int64,
	appliesToTierIDs, appliesToSessionIDs []string,
	currency string,
	maxUses, maxUsesPerCustomer *int32,
	validFrom, validUntil *time.Time,
	minOrderAmount int64,
	status string,
) (PromoCodeRow, error) {
	if appliesToTierIDs == nil {
		appliesToTierIDs = []string{}
	}
	if appliesToSessionIDs == nil {
		appliesToSessionIDs = []string{}
	}
	row := q.db.QueryRow(ctx, insertPromoCode,
		orgID, code, discountType, discountValue, appliesToTierIDs, appliesToSessionIDs, currency,
		maxUses, maxUsesPerCustomer, validFrom, validUntil, minOrderAmount, status,
	)
	return scanPromoCodeRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetPromoCodeByID
// ─────────────────────────────────────────────────────────────────────────────

const getPromoCodeByID = `-- name: GetPromoCodeByID :one
SELECT ` + promoCodeColumns + `
FROM promo_codes
WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`

// GetPromoCodeByID fetches an active promo code by its UUID primary key scoped to the org.
// Returns pgx.ErrNoRows when not found, already deleted, or belongs to a different org.
func (q *Queries) GetPromoCodeByID(ctx context.Context, id, orgID uuid.UUID) (PromoCodeRow, error) {
	row := q.db.QueryRow(ctx, getPromoCodeByID, id, orgID)
	return scanPromoCodeRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetPromoCodeByCode
// ─────────────────────────────────────────────────────────────────────────────

const getPromoCodeByCode = `-- name: GetPromoCodeByCode :one
SELECT ` + promoCodeColumns + `
FROM promo_codes
WHERE org_id = $1 AND code = $2 AND deleted_at IS NULL`

// GetPromoCodeByCode fetches an active promo code by org_id + code string.
// Used by the checkout validation endpoint.
// Returns pgx.ErrNoRows when not found or soft-deleted.
func (q *Queries) GetPromoCodeByCode(ctx context.Context, orgID uuid.UUID, code string) (PromoCodeRow, error) {
	row := q.db.QueryRow(ctx, getPromoCodeByCode, orgID, code)
	return scanPromoCodeRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetPromoCodeByCodeCI
// ─────────────────────────────────────────────────────────────────────────────

const getPromoCodeByCodeCI = `-- name: GetPromoCodeByCodeCI :one
SELECT ` + promoCodeColumns + `
FROM promo_codes
WHERE org_id = $1 AND lower(code) = lower($2) AND deleted_at IS NULL
ORDER BY created_at
LIMIT 1`

// GetPromoCodeByCodeCI fetches an active promo code by org_id + code, matching
// the code CASE-INSENSITIVELY. The Bil24 gateway needs this shape: spec §7.6
// mandates a case-insensitive match on promo_codes.code because the WordPress
// checkout lets buyers type the code by hand. The admin CRUD keeps using the
// exact-match GetPromoCodeByCode.
//
// Returns pgx.ErrNoRows when nothing matches or the row is soft-deleted.
func (q *Queries) GetPromoCodeByCodeCI(ctx context.Context, orgID uuid.UUID, code string) (PromoCodeRow, error) {
	row := q.db.QueryRow(ctx, getPromoCodeByCodeCI, orgID, code)
	return scanPromoCodeRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// ListPromoCodesByOrg
// ─────────────────────────────────────────────────────────────────────────────

const listPromoCodesByOrg = `-- name: ListPromoCodesByOrg :many
SELECT ` + promoCodeColumns + `
FROM promo_codes
WHERE org_id = $1 AND deleted_at IS NULL
ORDER BY created_at DESC`

// ListPromoCodesByOrg returns all active (non-deleted) promo codes for the given org.
// Ordered by created_at DESC (most recent first).
func (q *Queries) ListPromoCodesByOrg(ctx context.Context, orgID uuid.UUID) ([]PromoCodeRow, error) {
	rows, err := q.db.Query(ctx, listPromoCodesByOrg, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var codes []PromoCodeRow
	for rows.Next() {
		r, err := scanPromoCodeRow(rows)
		if err != nil {
			return nil, err
		}
		codes = append(codes, r)
	}
	return codes, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// UpdatePromoCode
// ─────────────────────────────────────────────────────────────────────────────

const updatePromoCode = `-- name: UpdatePromoCode :one
UPDATE promo_codes
SET discount_type          = COALESCE(NULLIF($3, ''), discount_type),
    discount_value         = CASE WHEN $4::bigint IS NOT NULL THEN $4::bigint ELSE discount_value END,
    applies_to_tier_ids    = CASE WHEN $5::uuid[] IS NOT NULL THEN $5::uuid[] ELSE applies_to_tier_ids END,
    applies_to_session_ids = CASE WHEN $6::uuid[] IS NOT NULL THEN $6::uuid[] ELSE applies_to_session_ids END,
    currency               = CASE WHEN $7::text IS NOT NULL THEN NULLIF($7::text, '') ELSE currency END,
    max_uses               = CASE WHEN $8::integer IS NOT NULL THEN $8::integer ELSE max_uses END,
    max_uses_per_customer  = CASE WHEN $9::integer IS NOT NULL THEN $9::integer ELSE max_uses_per_customer END,
    valid_from             = CASE WHEN $10::timestamptz IS NOT NULL THEN $10::timestamptz ELSE valid_from END,
    valid_until            = CASE WHEN $11::timestamptz IS NOT NULL THEN $11::timestamptz ELSE valid_until END,
    min_order_amount       = CASE WHEN $12::bigint IS NOT NULL THEN $12::bigint ELSE min_order_amount END,
    status                 = COALESCE(NULLIF($13, ''), status),
    updated_at             = now()
WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL
RETURNING ` + promoCodeColumns

// UpdatePromoCode applies a partial update to an active promo code scoped by org_id.
// Empty string discountType leaves the existing value unchanged. Nil optional
// fields keep the existing column values; a nil currency keeps it, a pointer
// to "" clears it (any currency), a pointer to a code sets it.
// Returns pgx.ErrNoRows when the code does not exist, belongs to a different org,
// or has been soft-deleted.
func (q *Queries) UpdatePromoCode(
	ctx context.Context,
	id, orgID uuid.UUID,
	discountType string,
	discountValue *int64,
	appliesToTierIDs, appliesToSessionIDs []string,
	currency *string,
	maxUses, maxUsesPerCustomer *int32,
	validFrom, validUntil *time.Time,
	minOrderAmount *int64,
	status string,
) (PromoCodeRow, error) {
	row := q.db.QueryRow(ctx, updatePromoCode,
		id, orgID,
		discountType, discountValue, appliesToTierIDs, appliesToSessionIDs, currency,
		maxUses, maxUsesPerCustomer,
		validFrom, validUntil,
		minOrderAmount, status,
	)
	return scanPromoCodeRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// SoftDeletePromoCode
// ─────────────────────────────────────────────────────────────────────────────

const softDeletePromoCode = `-- name: SoftDeletePromoCode :one
UPDATE promo_codes
SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL
RETURNING ` + promoCodeColumns

// SoftDeletePromoCode marks a promo code as deleted by setting deleted_at.
// Scoped by org_id to enforce owner-gated mutation policy.
// Returns pgx.ErrNoRows when the code does not exist, belongs to a different org,
// or has already been deleted.
func (q *Queries) SoftDeletePromoCode(ctx context.Context, id, orgID uuid.UUID) (PromoCodeRow, error) {
	row := q.db.QueryRow(ctx, softDeletePromoCode, id, orgID)
	return scanPromoCodeRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// Redemption counts
// ─────────────────────────────────────────────────────────────────────────────

const countPromoCodeRedemptions = `-- name: CountPromoCodeRedemptions :one
SELECT COUNT(*)::int FROM promo_code_redemptions WHERE promo_code_id = $1`

// CountPromoCodeRedemptions returns the total number of times a promo code has
// been redeemed. Used to enforce max_uses limits before accepting a redemption.
func (q *Queries) CountPromoCodeRedemptions(ctx context.Context, promoCodeID uuid.UUID) (int32, error) {
	row := q.db.QueryRow(ctx, countPromoCodeRedemptions, promoCodeID)
	var count int32
	err := row.Scan(&count)
	return count, err
}

const countUserRedemptions = `-- name: CountUserRedemptions :one
SELECT COUNT(*)::int FROM promo_code_redemptions WHERE promo_code_id = $1 AND user_id = $2`

// CountUserRedemptions returns the number of times a specific platform user
// has redeemed a given promo code (legacy REST checkout).
func (q *Queries) CountUserRedemptions(ctx context.Context, promoCodeID, userID uuid.UUID) (int32, error) {
	row := q.db.QueryRow(ctx, countUserRedemptions, promoCodeID, userID)
	var count int32
	err := row.Scan(&count)
	return count, err
}

const countPromoRedemptionsByCustomer = `-- name: CountPromoRedemptionsByCustomer :one
SELECT COUNT(*)::int FROM promo_code_redemptions WHERE promo_code_id = $1 AND customer_id = $2`

// CountPromoRedemptionsByCustomer returns how many times one customer has
// redeemed the code — the max_uses_per_customer check where the buyer is
// known before the order exists (the Bil24 gateway session).
func (q *Queries) CountPromoRedemptionsByCustomer(ctx context.Context, promoCodeID, customerID uuid.UUID) (int32, error) {
	row := q.db.QueryRow(ctx, countPromoRedemptionsByCustomer, promoCodeID, customerID)
	var count int32
	err := row.Scan(&count)
	return count, err
}

const countPromoRedemptionsByBuyerEmail = `-- name: CountPromoRedemptionsByBuyerEmail :one
SELECT COUNT(*)::int
FROM promo_code_redemptions r
JOIN orders o ON o.id = r.order_id
WHERE r.promo_code_id = $1 AND lower(o.buyer_email) = lower($2)`

// CountPromoRedemptionsByBuyerEmail returns how many orders bought under this
// e-mail redeemed the code — the max_uses_per_customer check on the widget,
// where only the buyer's e-mail is known when the cart is priced.
func (q *Queries) CountPromoRedemptionsByBuyerEmail(ctx context.Context, promoCodeID uuid.UUID, email string) (int32, error) {
	row := q.db.QueryRow(ctx, countPromoRedemptionsByBuyerEmail, promoCodeID, email)
	var count int32
	err := row.Scan(&count)
	return count, err
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertPromoCodeRedemption
// ─────────────────────────────────────────────────────────────────────────────

const insertPromoCodeRedemption = `-- name: InsertPromoCodeRedemption :exec
INSERT INTO promo_code_redemptions (promo_code_id, user_id, reservation_id, discount_amount, order_amount,
                                    order_id, customer_id, channel_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (order_id) WHERE order_id IS NOT NULL DO NOTHING`

// InsertPromoCodeRedemption records a promo code usage event. userID,
// reservationID, orderID, customerID and channelID are optional; discountAmount
// is the computed discount in minor units, orderAmount the pre-discount total.
// An order that already has a redemption is left alone (migration 0108's
// partial unique index), so a replayed PAY_ORDER or payment webhook never
// counts a buyer twice.
func (q *Queries) InsertPromoCodeRedemption(
	ctx context.Context,
	promoCodeID uuid.UUID,
	userID, reservationID *uuid.UUID,
	discountAmount, orderAmount int64,
	orderID, customerID, channelID *uuid.UUID,
) error {
	_, err := q.db.Exec(ctx, insertPromoCodeRedemption,
		promoCodeID, userID, reservationID, discountAmount, orderAmount,
		orderID, customerID, channelID,
	)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetPromoCodeByIDForUpdate
// ─────────────────────────────────────────────────────────────────────────────

const getPromoCodeByIDForUpdate = `-- name: GetPromoCodeByIDForUpdate :one
SELECT ` + promoCodeColumns + `
FROM promo_codes
WHERE id = $1 AND deleted_at IS NULL
FOR UPDATE`

// GetPromoCodeByIDForUpdate fetches and exclusively locks an active promo code row
// by its UUID primary key. Must be called inside an explicit transaction.
// The lock serialises concurrent redemption count-checks and inserts (feature #368).
// Returns pgx.ErrNoRows when not found or already soft-deleted.
func (q *Queries) GetPromoCodeByIDForUpdate(ctx context.Context, id uuid.UUID) (PromoCodeRow, error) {
	row := q.db.QueryRow(ctx, getPromoCodeByIDForUpdate, id)
	return scanPromoCodeRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// Usage report
// ─────────────────────────────────────────────────────────────────────────────

const listPromoCodeUsageByOrg = `-- name: ListPromoCodeUsageByOrg :many
SELECT p.id                                   AS promo_code_id,
       count(r.id)::int                       AS uses,
       COALESCE(sum(r.discount_amount), 0)::bigint AS discount_total,
       max(r.redeemed_at)                     AS last_used_at
FROM promo_codes p
LEFT JOIN promo_code_redemptions r ON r.promo_code_id = p.id
WHERE p.org_id = $1 AND p.deleted_at IS NULL
GROUP BY p.id`

// ListPromoCodeUsageByOrg returns one usage tally per live promo code of the
// organization — a code never used has uses 0 and a nil last_used_at.
func (q *Queries) ListPromoCodeUsageByOrg(ctx context.Context, orgID uuid.UUID) ([]PromoCodeUsageRow, error) {
	rows, err := q.db.Query(ctx, listPromoCodeUsageByOrg, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PromoCodeUsageRow
	for rows.Next() {
		var r PromoCodeUsageRow
		if err := rows.Scan(&r.PromoCodeID, &r.Uses, &r.DiscountTotal, &r.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const listPromoCodeRedemptionsByOrg = `-- name: ListPromoCodeRedemptionsByOrg :many
SELECT r.id, r.promo_code_id, p.code, r.redeemed_at, r.discount_amount, r.order_amount,
       r.order_id, o.system_id AS order_number, o.status AS order_status, o.currency,
       o.buyer_email, o.session_id, r.channel_id, c.name AS channel_name
FROM promo_code_redemptions r
JOIN promo_codes p ON p.id = r.promo_code_id
LEFT JOIN orders o         ON o.id = r.order_id
LEFT JOIN sales_channels c ON c.id = r.channel_id
WHERE p.org_id = $1
  AND ($2::uuid IS NULL OR r.promo_code_id = $2::uuid)
ORDER BY r.redeemed_at DESC`

// ListPromoCodeRedemptionsByOrg returns the organization's redemptions, newest
// first, each joined to the order it paid for. promoCodeID narrows the report
// to one code; nil returns every code's usage.
func (q *Queries) ListPromoCodeRedemptionsByOrg(ctx context.Context, orgID uuid.UUID, promoCodeID *uuid.UUID) ([]PromoCodeRedemptionReportRow, error) {
	rows, err := q.db.Query(ctx, listPromoCodeRedemptionsByOrg, orgID, promoCodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PromoCodeRedemptionReportRow
	for rows.Next() {
		var r PromoCodeRedemptionReportRow
		if err := rows.Scan(
			&r.ID, &r.PromoCodeID, &r.Code, &r.RedeemedAt, &r.DiscountAmount, &r.OrderAmount,
			&r.OrderID, &r.OrderNumber, &r.OrderStatus, &r.Currency,
			&r.BuyerEmail, &r.SessionID, &r.ChannelID, &r.ChannelName,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

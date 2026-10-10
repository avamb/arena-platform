// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: export.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// GetExportEventHeader
// ─────────────────────────────────────────────────────────────────────────────

const getExportEventHeader = `-- name: GetExportEventHeader :one
SELECT e.id, e.org_id, e.name, e.slug
FROM   events e
WHERE  e.id = $1
  AND  e.org_id = $2
  AND  e.deleted_at IS NULL`

// ExportEventHeaderRow names the event an export is about.
type ExportEventHeaderRow struct {
	ID    uuid.UUID `json:"id"`
	OrgID uuid.UUID `json:"org_id"`
	Name  string    `json:"name"`
	Slug  *string   `json:"slug"`
}

// GetExportEventHeader returns the event scoped to the organization.
// pgx.ErrNoRows means the event does not exist in that organization.
func (q *Queries) GetExportEventHeader(ctx context.Context, eventID, orgID uuid.UUID) (ExportEventHeaderRow, error) {
	var i ExportEventHeaderRow
	err := q.db.QueryRow(ctx, getExportEventHeader, eventID, orgID).Scan(&i.ID, &i.OrgID, &i.Name, &i.Slug)
	return i, err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetExportSessionHeader
// ─────────────────────────────────────────────────────────────────────────────

const getExportSessionHeader = `-- name: GetExportSessionHeader :one
SELECT s.id, s.event_id, e.org_id, e.name AS event_name, e.slug AS event_slug,
       s.start_at, v.timezone AS venue_timezone
FROM      sessions s
JOIN      events   e ON e.id = s.event_id
LEFT JOIN venues   v ON v.id = s.venue_id
WHERE  s.id = $1
  AND  e.org_id = $2
  AND  s.deleted_at IS NULL
  AND  e.deleted_at IS NULL`

// ExportSessionHeaderRow names the session an export is about, with the
// event's slug (the file name) and the venue's zone (the date columns).
type ExportSessionHeaderRow struct {
	ID            uuid.UUID `json:"id"`
	EventID       uuid.UUID `json:"event_id"`
	OrgID         uuid.UUID `json:"org_id"`
	EventName     string    `json:"event_name"`
	EventSlug     *string   `json:"event_slug"`
	StartAt       time.Time `json:"start_at"`
	VenueTimezone *string   `json:"venue_timezone"`
}

// GetExportSessionHeader returns the session scoped to the organization.
// pgx.ErrNoRows means the session does not exist in that organization.
func (q *Queries) GetExportSessionHeader(ctx context.Context, sessionID, orgID uuid.UUID) (ExportSessionHeaderRow, error) {
	var i ExportSessionHeaderRow
	err := q.db.QueryRow(ctx, getExportSessionHeader, sessionID, orgID).Scan(
		&i.ID, &i.EventID, &i.OrgID, &i.EventName, &i.EventSlug, &i.StartAt, &i.VenueTimezone,
	)
	return i, err
}

// ─────────────────────────────────────────────────────────────────────────────
// CountExportSalesRows
// ─────────────────────────────────────────────────────────────────────────────

const countExportSalesRows = `-- name: CountExportSalesRows :one
SELECT count(*)
FROM   tickets  t
JOIN   sessions s ON s.id = t.session_id
WHERE  ($1::uuid IS NULL OR t.session_id = $1::uuid)
  AND  ($2::uuid IS NULL OR s.event_id   = $2::uuid)
  AND  s.deleted_at IS NULL`

// CountExportSalesRows counts the tickets of one session (sessionID set) or
// of one event (eventID set) — the rows a sales export would stream.
func (q *Queries) CountExportSalesRows(ctx context.Context, sessionID, eventID *uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countExportSalesRows, sessionID, eventID).Scan(&n)
	return n, err
}

// ─────────────────────────────────────────────────────────────────────────────
// ListExportSalesRows
// ─────────────────────────────────────────────────────────────────────────────

const listExportSalesRows = `-- name: ListExportSalesRows :many
SELECT t.id, t.system_ticket_id, t.status AS ticket_status, t.used_at, t.issued_at,
       s.start_at AS session_start, v.timezone AS venue_timezone,
       o.system_id AS order_number, o.status AS order_status, o.created_at AS order_created_at,
       o.buyer_name, o.buyer_email, o.buyer_phone,
       COALESCE(o.currency, tt.currency) AS currency,
       tt.name AS tier_name,
       oi.total AS item_total,
       tc.payload AS barcode,
       sc.name AS channel_name,
       pc.code AS promo_code
FROM      tickets t
JOIN      sessions s            ON s.id = t.session_id
LEFT JOIN venues v              ON v.id = s.venue_id
LEFT JOIN orders o              ON o.id = t.order_id
LEFT JOIN checkout_sessions cs  ON cs.id = t.checkout_session_id
LEFT JOIN ticket_tiers tt       ON tt.id = t.tier_id
LEFT JOIN LATERAL (
    SELECT oi.total FROM order_items oi
    WHERE  oi.ticket_id = t.id
    ORDER  BY oi.ordinal
    LIMIT  1
) oi ON true
LEFT JOIN ticket_credentials tc ON tc.ticket_id = t.id AND tc.type = 'ean13'
LEFT JOIN sales_channels sc     ON sc.id = COALESCE(o.channel_id, cs.channel_id)
LEFT JOIN promo_code_redemptions pr ON pr.order_id = o.id
LEFT JOIN promo_codes pc        ON pc.id = COALESCE(pr.promo_code_id, cs.promo_code_id)
WHERE  ($1::uuid IS NULL OR t.session_id = $1::uuid)
  AND  ($2::uuid IS NULL OR s.event_id   = $2::uuid)
  AND  s.deleted_at IS NULL
  AND  t.system_ticket_id > $3
ORDER  BY t.system_ticket_id
LIMIT  $4`

// ExportSalesRow is one ticket of a sales export with its order, buyer,
// category, paid price, barcode, channel and promo code. Every order-side
// field is nil for a ticket that predates migration 0092 (no orders row).
type ExportSalesRow struct {
	ID             uuid.UUID  `json:"id"`
	SystemTicketID int64      `json:"system_ticket_id"`
	TicketStatus   string     `json:"ticket_status"`
	UsedAt         *time.Time `json:"used_at"`
	IssuedAt       time.Time  `json:"issued_at"`
	SessionStart   time.Time  `json:"session_start"`
	VenueTimezone  *string    `json:"venue_timezone"`
	OrderNumber    *int64     `json:"order_number"`
	OrderStatus    *string    `json:"order_status"`
	OrderCreatedAt *time.Time `json:"order_created_at"`
	BuyerName      *string    `json:"buyer_name"`
	BuyerEmail     *string    `json:"buyer_email"`
	BuyerPhone     *string    `json:"buyer_phone"`
	Currency       *string    `json:"currency"`
	TierName       *string    `json:"tier_name"`
	ItemTotal      *int64     `json:"item_total"`
	Barcode        *string    `json:"barcode"`
	ChannelName    *string    `json:"channel_name"`
	PromoCode      *string    `json:"promo_code"`
}

// ListExportSalesRows returns the next page of tickets of one session
// (sessionID set) or one event (eventID set): those with a system_ticket_id
// above afterTicketID, in that order, at most limit of them.
func (q *Queries) ListExportSalesRows(ctx context.Context, sessionID, eventID *uuid.UUID, afterTicketID int64, limit int32) ([]ExportSalesRow, error) {
	rows, err := q.db.Query(ctx, listExportSalesRows, sessionID, eventID, afterTicketID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ExportSalesRow
	for rows.Next() {
		var i ExportSalesRow
		if err := rows.Scan(
			&i.ID, &i.SystemTicketID, &i.TicketStatus, &i.UsedAt, &i.IssuedAt,
			&i.SessionStart, &i.VenueTimezone,
			&i.OrderNumber, &i.OrderStatus, &i.OrderCreatedAt,
			&i.BuyerName, &i.BuyerEmail, &i.BuyerPhone,
			&i.Currency, &i.TierName, &i.ItemTotal, &i.Barcode, &i.ChannelName, &i.PromoCode,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// CountExportPromoRedemptions
// ─────────────────────────────────────────────────────────────────────────────

const countExportPromoRedemptions = `-- name: CountExportPromoRedemptions :one
SELECT count(*) FROM promo_code_redemptions r WHERE r.promo_code_id = $1`

// CountExportPromoRedemptions counts the redemptions of one promo code.
func (q *Queries) CountExportPromoRedemptions(ctx context.Context, promoCodeID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countExportPromoRedemptions, promoCodeID).Scan(&n)
	return n, err
}

// ─────────────────────────────────────────────────────────────────────────────
// ListExportPromoRedemptions
// ─────────────────────────────────────────────────────────────────────────────

const listExportPromoRedemptions = `-- name: ListExportPromoRedemptions :many
SELECT r.id, r.redeemed_at, r.discount_amount,
       o.system_id AS order_number, o.status AS order_status, o.currency,
       o.buyer_name, o.buyer_email,
       v.timezone AS venue_timezone
FROM      promo_code_redemptions r
LEFT JOIN orders   o ON o.id = r.order_id
LEFT JOIN sessions s ON s.id = o.session_id
LEFT JOIN venues   v ON v.id = s.venue_id
WHERE  r.promo_code_id = $1
  AND  r.id > $2
ORDER  BY r.id
LIMIT  $3`

// ExportPromoRedemptionRow is one use of a promo code with the order it
// discounted. The order-side fields are nil for a redemption written before
// migration 0108 (no order id).
type ExportPromoRedemptionRow struct {
	ID             uuid.UUID `json:"id"`
	RedeemedAt     time.Time `json:"redeemed_at"`
	DiscountAmount int64     `json:"discount_amount"`
	OrderNumber    *int64    `json:"order_number"`
	OrderStatus    *string   `json:"order_status"`
	Currency       *string   `json:"currency"`
	BuyerName      *string   `json:"buyer_name"`
	BuyerEmail     *string   `json:"buyer_email"`
	VenueTimezone  *string   `json:"venue_timezone"`
}

// ListExportPromoRedemptions returns the next page of a promo code's
// redemptions: ids above afterID (uuidv7, so time-ordered), at most limit.
func (q *Queries) ListExportPromoRedemptions(ctx context.Context, promoCodeID, afterID uuid.UUID, limit int32) ([]ExportPromoRedemptionRow, error) {
	rows, err := q.db.Query(ctx, listExportPromoRedemptions, promoCodeID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ExportPromoRedemptionRow
	for rows.Next() {
		var i ExportPromoRedemptionRow
		if err := rows.Scan(
			&i.ID, &i.RedeemedAt, &i.DiscountAmount,
			&i.OrderNumber, &i.OrderStatus, &i.Currency,
			&i.BuyerName, &i.BuyerEmail, &i.VenueTimezone,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

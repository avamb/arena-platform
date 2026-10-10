// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: orders.sql — SearchOrdersByOrg / CountOrdersByOrg /
// ListOrderTicketDetails (EC-04 / EC-05, spec 35 §5.5–§5.6).

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// SearchOrdersByOrg / CountOrdersByOrg
// ─────────────────────────────────────────────────────────────────────────────

// OrderListRow is one row of the event-center order search: the order
// itself plus the event name and the session start / venue zone the list
// prints next to it.
type OrderListRow struct {
	OrderRow
	EventName       string    `json:"event_name"`
	SessionStartAt  time.Time `json:"session_start_at"`
	SessionTimezone string    `json:"session_timezone"`
}

// OrderSearchParams carries every filter of SearchOrdersByOrg. The search
// matchers (Barcode, LegacyTicketID, SystemID, Email, PhoneDigits, Text) are
// OR-ed in SQL; all of them empty/nil means "no search". The handler's
// classifier decides which to populate from the single `q` parameter.
type OrderSearchParams struct {
	OrgID     uuid.UUID
	Status    string // exact orders.status, "" = any
	Tab       string // "" | "paid" | "unpaid"
	SessionID *uuid.UUID
	EventID   *uuid.UUID
	From      *time.Time
	To        *time.Time

	Barcode        string // exact 13-digit EAN-13 of a ticket of the order
	LegacyTicketID *int64 // tickets.system_ticket_id decoded from a legacy PlatformCode
	SystemID       *int64 // orders.system_id exact
	Email          string // lower-cased exact e-mail
	PhoneDigits    string // digits only, no leading 00/+
	Text           string // pg_trgm similarity over buyer name/email/phone
}

// searchOrdersByOrgSelect is the projection of SearchOrdersByOrg: OrderRow's
// 29 columns (scanOrderRow order) plus the three list extras.
const searchOrdersByOrgSelect = `-- name: SearchOrdersByOrg :many
SELECT o.id, o.system_id, o.org_id, o.channel_id, o.event_id, o.session_id, o.customer_id,
       o.checkout_session_id, o.reservation_id, o.external_ref, o.source, o.status,
       o.currency, o.subtotal, o.discount, o.charge, o.total, o.charge_percent_bp,
       o.promo_code_id, o.buyer_name, o.buyer_email, o.buyer_phone, o.payment_method,
       o.paid_at, o.cancelled_at, o.expires_at, o.metadata, o.created_at, o.updated_at,
       e.name AS event_name, s.start_at AS session_start_at, v.timezone AS session_timezone
`

// searchOrdersByOrgWhere is shared by the page and the count query so the
// two can never disagree on which orders a filter set covers.
const searchOrdersByOrgWhere = `FROM   orders o
JOIN   events   e ON e.id = o.event_id
JOIN   sessions s ON s.id = o.session_id
JOIN   venues   v ON v.id = s.venue_id
WHERE  o.org_id = $1
  AND  ($2 = '' OR o.status = $2)
  AND  ($3 = ''
        OR ($3 = 'paid'   AND o.status IN ('paid', 'partially_refunded', 'refunded'))
        OR ($3 = 'unpaid' AND o.status IN ('pending_payment', 'expired', 'cancelled', 'abandoned', 'manual_review')))
  AND  ($4::uuid IS NULL OR o.session_id = $4)
  AND  ($5::uuid IS NULL OR o.event_id = $5)
  AND  ($6::timestamptz IS NULL OR o.created_at >= $6)
  AND  ($7::timestamptz IS NULL OR o.created_at <= $7)
  AND  (
        ($8 = '' AND $9::bigint IS NULL AND $10::bigint IS NULL AND $11 = '' AND $12 = '' AND $13 = '')
     OR ($8 <> '' AND o.id IN (
            SELECT oi.order_id FROM barcodes b
            JOIN   order_items oi ON oi.ticket_id = b.ticket_id
            WHERE  b.external_ref = $8
            UNION
            SELECT oi.order_id FROM ticket_credentials tc
            JOIN   order_items oi ON oi.ticket_id = tc.ticket_id
            WHERE  tc.type = 'ean13' AND tc.payload = $8))
     OR ($9::bigint IS NOT NULL AND o.id IN (
            SELECT oi.order_id FROM tickets t
            JOIN   order_items oi ON oi.ticket_id = t.id
            WHERE  t.system_ticket_id = $9
              AND  NOT EXISTS (SELECT 1 FROM ticket_credentials tc
                               WHERE tc.ticket_id = t.id AND tc.type = 'ean13')))
     OR ($10::bigint IS NOT NULL AND o.system_id = $10)
     OR ($11 <> '' AND (lower(o.buyer_email) = $11 OR o.customer_id IN (
            SELECT ci.customer_id FROM customer_identities ci
            WHERE  ci.kind = 'email' AND ci.value_normalized = $11)))
     OR ($12 <> '' AND (
            regexp_replace(regexp_replace(COALESCE(o.buyer_phone, ''), '\D', '', 'g'), '^00', '') = $12
         OR (length($12) >= 9 AND regexp_replace(COALESCE(o.buyer_phone, ''), '\D', '', 'g') LIKE '%' || $12)
         OR o.customer_id IN (
            SELECT ci.customer_id FROM customer_identities ci
            WHERE  ci.kind = 'phone' AND regexp_replace(ci.value_normalized, '\D', '', 'g') = $12)))
     OR ($13 <> '' AND (o.buyer_name % $13 OR o.buyer_email % $13 OR o.buyer_phone % $13))
  )
`

const searchOrdersByOrg = searchOrdersByOrgSelect + searchOrdersByOrgWhere +
	`ORDER  BY o.created_at DESC, o.id DESC
LIMIT  $14 OFFSET $15`

const countOrdersByOrg = `-- name: CountOrdersByOrg :one
SELECT count(*)
` + searchOrdersByOrgWhere

func (p OrderSearchParams) filterArgs() []any {
	return []any{
		p.OrgID, p.Status, p.Tab, p.SessionID, p.EventID, p.From, p.To,
		p.Barcode, p.LegacyTicketID, p.SystemID, p.Email, p.PhoneDigits, p.Text,
	}
}

// SearchOrdersByOrg returns one page of the event-center order search
// (EC-04, spec 35 §5.5), newest first. See OrderSearchParams for the
// filters and the SQL comment in orders.sql for how the search matchers
// combine.
func (q *Queries) SearchOrdersByOrg(ctx context.Context, p OrderSearchParams, limit, offset int32) ([]OrderListRow, error) {
	args := append(p.filterArgs(), limit, offset)
	rows, err := q.db.Query(ctx, searchOrdersByOrg, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []OrderListRow
	for rows.Next() {
		var r OrderListRow
		if err := rows.Scan(
			&r.ID, &r.SystemID, &r.OrgID, &r.ChannelID, &r.EventID, &r.SessionID, &r.CustomerID,
			&r.CheckoutSessionID, &r.ReservationID, &r.ExternalRef, &r.Source, &r.Status,
			&r.Currency, &r.Subtotal, &r.Discount, &r.Charge, &r.Total, &r.ChargePercentBP,
			&r.PromoCodeID, &r.BuyerName, &r.BuyerEmail, &r.BuyerPhone, &r.PaymentMethod,
			&r.PaidAt, &r.CancelledAt, &r.ExpiresAt, &r.Metadata, &r.CreatedAt, &r.UpdatedAt,
			&r.EventName, &r.SessionStartAt, &r.SessionTimezone,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountOrdersByOrg counts every order SearchOrdersByOrg would page through
// for the same filters — the list's total_count.
func (q *Queries) CountOrdersByOrg(ctx context.Context, p OrderSearchParams) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countOrdersByOrg, p.filterArgs()...).Scan(&n)
	return n, err
}

// ─────────────────────────────────────────────────────────────────────────────
// ListOrderTicketDetails
// ─────────────────────────────────────────────────────────────────────────────

// OrderTicketDetailRow is one issued ticket of an order with the line it was
// sold on, its category, EAN-13 credential, entry time and delivery job
// (EC-05, spec 35 §5.6). Nil pointers mean "no such row/value".
type OrderTicketDetailRow struct {
	ItemID         uuid.UUID
	Ordinal        int32
	Price          int64
	TierID         uuid.UUID
	TierName       *string
	TicketID       uuid.UUID
	Status         string
	HolderEmail    *string
	SeatSector     *string
	SeatRow        *string
	SeatNumber     *string
	IssuedAt       time.Time
	CancelledAt    *time.Time
	SystemTicketID int64
	BarcodeStr     *string
	UsedAt         *time.Time
	DeliveryStatus *string
	DeliverySentAt *time.Time
	DeliveryError  *string
}

const listOrderTicketDetails = `-- name: ListOrderTicketDetails :many
SELECT oi.id AS item_id, oi.ordinal, oi.total AS price, oi.tier_id, tt.name AS tier_name,
       t.id AS ticket_id, t.status, t.holder_email, t.seat_sector, t.seat_row, t.seat_number,
       t.issued_at, t.cancelled_at, t.system_ticket_id,
       tc.payload AS barcode_str,
       sc.scanned_at AS used_at,
       dj.status AS delivery_status, dj.sent_at AS delivery_sent_at, dj.last_error AS delivery_error
FROM   order_items oi
JOIN   tickets t ON t.id = oi.ticket_id
LEFT JOIN ticket_tiers tt ON tt.id = oi.tier_id
LEFT JOIN ticket_credentials tc ON tc.ticket_id = t.id AND tc.type = 'ean13'
LEFT JOIN LATERAL (
    SELECT b.scanned_at FROM barcodes b
    WHERE  b.ticket_id = t.id AND b.scanned_at IS NOT NULL
    ORDER  BY b.scanned_at DESC
    LIMIT  1) sc ON true
LEFT JOIN delivery_jobs dj ON dj.ticket_id = t.id
WHERE  oi.order_id = $1
ORDER  BY oi.ordinal ASC`

// ListOrderTicketDetails lists the issued tickets of an order in line order
// with their price, category, EAN-13, entry time and delivery job. Items
// whose ticket is not issued yet are absent (ListOrderItemsByOrder still
// carries them).
func (q *Queries) ListOrderTicketDetails(ctx context.Context, orderID uuid.UUID) ([]OrderTicketDetailRow, error) {
	rows, err := q.db.Query(ctx, listOrderTicketDetails, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []OrderTicketDetailRow
	for rows.Next() {
		var r OrderTicketDetailRow
		if err := rows.Scan(
			&r.ItemID, &r.Ordinal, &r.Price, &r.TierID, &r.TierName,
			&r.TicketID, &r.Status, &r.HolderEmail, &r.SeatSector, &r.SeatRow, &r.SeatNumber,
			&r.IssuedAt, &r.CancelledAt, &r.SystemTicketID,
			&r.BarcodeStr, &r.UsedAt,
			&r.DeliveryStatus, &r.DeliverySentAt, &r.DeliveryError,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

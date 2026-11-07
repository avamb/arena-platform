// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: org_summary.sql
//
// The organization-wide summary (EC-V1) and the accounting workbook (EC-V2).

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// ListOrgSummarySessions
// ─────────────────────────────────────────────────────────────────────────────

const listOrgSummarySessions = `-- name: ListOrgSummarySessions :many
SELECT s.id, s.event_id, e.name AS event_name, s.start_at, s.status,
       v.name AS venue_name, v.timezone AS venue_timezone
FROM      sessions s
JOIN      events   e ON e.id = s.event_id
LEFT JOIN venues   v ON v.id = s.venue_id
WHERE  e.org_id = $1
  AND  s.deleted_at IS NULL
  AND  e.deleted_at IS NULL
  AND  ($2::timestamptz IS NULL OR s.start_at >= $2::timestamptz)
  AND  ($3::timestamptz IS NULL OR s.start_at <  $3::timestamptz)
  AND  ($4::uuid IS NULL OR e.id = $4::uuid)
ORDER  BY s.start_at, s.id
LIMIT  $5`

// OrgSummarySessionRow is one session of the organization-wide summary.
type OrgSummarySessionRow struct {
	ID            uuid.UUID `json:"id"`
	EventID       uuid.UUID `json:"event_id"`
	EventName     string    `json:"event_name"`
	StartAt       time.Time `json:"start_at"`
	Status        string    `json:"status"`
	VenueName     *string   `json:"venue_name"`
	VenueTimezone *string   `json:"venue_timezone"`
}

// ListOrgSummarySessions lists the organization's sessions starting in
// [from, to), oldest first. A nil bound or a nil eventID leaves that filter off.
func (q *Queries) ListOrgSummarySessions(ctx context.Context, orgID uuid.UUID, from, to *time.Time, eventID *uuid.UUID, limit int32) ([]OrgSummarySessionRow, error) {
	rows, err := q.db.Query(ctx, listOrgSummarySessions, orgID, from, to, eventID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []OrgSummarySessionRow
	for rows.Next() {
		var i OrgSummarySessionRow
		if err := rows.Scan(&i.ID, &i.EventID, &i.EventName, &i.StartAt, &i.Status, &i.VenueName, &i.VenueTimezone); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSessionSummaryTicketCards
// ─────────────────────────────────────────────────────────────────────────────

const listSessionSummaryTicketCards = `-- name: ListSessionSummaryTicketCards :many
SELECT t.session_id,
       COALESCE(o.currency, tt.currency, '') AS currency,
       count(*) FILTER (WHERE NOT f.comp)::bigint AS sold,
       count(*) FILTER (WHERE f.comp)::bigint     AS complimentary,
       count(*) FILTER (WHERE NOT f.comp AND t.status = 'cancelled' AND f.refunded)::bigint AS refunded,
       count(*) FILTER (WHERE t.status = 'active')::bigint AS active
FROM      tickets t
LEFT JOIN orders       o  ON o.id  = t.order_id
LEFT JOIN ticket_tiers tt ON tt.id = t.tier_id
CROSS JOIN LATERAL (
    SELECT (t.complimentary_issuance_id IS NOT NULL OR o.source = 'complimentary') AS comp,
           (t.refund_id IS NOT NULL OR t.refund_price IS NOT NULL
            OR EXISTS (SELECT 1 FROM refunds r WHERE r.ticket_id = t.id AND r.state = 'succeeded')) AS refunded
) f
WHERE  t.session_id = ANY($1::uuid[])
  AND  (o.id IS NULL OR (NOT o.is_test AND o.status IN ('paid', 'partially_refunded', 'refunded')))
GROUP  BY t.session_id, COALESCE(o.currency, tt.currency, '')`

// SessionSummaryTicketCardRow is the ticket counts of one session in one currency.
type SessionSummaryTicketCardRow struct {
	SessionID     uuid.UUID `json:"session_id"`
	Currency      string    `json:"currency"`
	Sold          int64     `json:"sold"`
	Complimentary int64     `json:"complimentary"`
	Refunded      int64     `json:"refunded"`
	Active        int64     `json:"active"`
}

// ListSessionSummaryTicketCards counts tickets per (session, currency).
func (q *Queries) ListSessionSummaryTicketCards(ctx context.Context, sessionIDs []uuid.UUID) ([]SessionSummaryTicketCardRow, error) {
	rows, err := q.db.Query(ctx, listSessionSummaryTicketCards, sessionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SessionSummaryTicketCardRow
	for rows.Next() {
		var i SessionSummaryTicketCardRow
		if err := rows.Scan(&i.SessionID, &i.Currency, &i.Sold, &i.Complimentary, &i.Refunded, &i.Active); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSessionSummaryBreakdown
// ─────────────────────────────────────────────────────────────────────────────

const listSessionSummaryBreakdown = `-- name: ListSessionSummaryBreakdown :many
SELECT o.session_id, o.currency, oi.tier_id, tt.name AS tier_name, oi.unit_price,
       COALESCE(pc.code, '') AS promo_code,
       count(*)::bigint                       AS tickets,
       COALESCE(sum(oi.total), 0)::bigint     AS revenue,
       COALESCE(sum(oi.discount), 0)::bigint  AS discount
FROM      order_items oi
JOIN      orders       o  ON o.id  = oi.order_id
JOIN      ticket_tiers tt ON tt.id = oi.tier_id
LEFT JOIN promo_codes  pc ON pc.id = o.promo_code_id
WHERE  o.session_id = ANY($1::uuid[])
  AND  o.status IN ('paid', 'partially_refunded', 'refunded')
  AND  NOT o.is_test
  AND  o.source <> 'complimentary'
GROUP  BY o.session_id, o.currency, oi.tier_id, tt.name, oi.unit_price, COALESCE(pc.code, '')
ORDER  BY o.session_id, tt.name, oi.unit_price, COALESCE(pc.code, '')`

// SessionSummaryBreakdownRow is the units sold in one session at one price in
// one category with one promo code (empty = none).
type SessionSummaryBreakdownRow struct {
	SessionID uuid.UUID `json:"session_id"`
	Currency  string    `json:"currency"`
	TierID    uuid.UUID `json:"tier_id"`
	TierName  string    `json:"tier_name"`
	UnitPrice int64     `json:"unit_price"`
	PromoCode string    `json:"promo_code"`
	Tickets   int64     `json:"tickets"`
	Revenue   int64     `json:"revenue"`
	Discount  int64     `json:"discount"`
}

// ListSessionSummaryBreakdown groups the paid units by session, category,
// price and promo code.
func (q *Queries) ListSessionSummaryBreakdown(ctx context.Context, sessionIDs []uuid.UUID) ([]SessionSummaryBreakdownRow, error) {
	rows, err := q.db.Query(ctx, listSessionSummaryBreakdown, sessionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SessionSummaryBreakdownRow
	for rows.Next() {
		var i SessionSummaryBreakdownRow
		if err := rows.Scan(&i.SessionID, &i.Currency, &i.TierID, &i.TierName, &i.UnitPrice, &i.PromoCode, &i.Tickets, &i.Revenue, &i.Discount); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// CountAccountingTickets / ListAccountingTickets
// ─────────────────────────────────────────────────────────────────────────────

const countAccountingTickets = `-- name: CountAccountingTickets :one
SELECT count(*)
FROM      tickets t
LEFT JOIN orders  o ON o.id = t.order_id
WHERE  t.session_id = ANY($1::uuid[])
  AND  (o.id IS NULL OR (NOT o.is_test AND o.status IN ('paid', 'partially_refunded', 'refunded')))`

// CountAccountingTickets counts the rows ListAccountingTickets would return.
func (q *Queries) CountAccountingTickets(ctx context.Context, sessionIDs []uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countAccountingTickets, sessionIDs).Scan(&n)
	return n, err
}

const listAccountingTickets = `-- name: ListAccountingTickets :many
SELECT t.system_ticket_id, t.status AS ticket_status, t.issued_at,
       s.start_at AS session_start, v.timezone AS venue_timezone, e.name AS event_name,
       o.system_id AS order_number, o.buyer_name, o.buyer_email, o.buyer_phone,
       COALESCE(o.currency, tt.currency, '') AS currency,
       tt.name AS tier_name,
       oi.total AS item_total,
       (t.complimentary_issuance_id IS NOT NULL OR o.source = 'complimentary') AS complimentary,
       COALESCE(t.refund_price, rf.ticket_amount) AS refund_amount,
       rf.settlement AS refund_settlement,
       rf.ref AS refund_ref
FROM      tickets t
JOIN      sessions s     ON s.id = t.session_id
JOIN      events   e     ON e.id = s.event_id
LEFT JOIN venues   v     ON v.id = s.venue_id
LEFT JOIN orders   o     ON o.id = t.order_id
LEFT JOIN ticket_tiers tt ON tt.id = t.tier_id
LEFT JOIN LATERAL (
    SELECT oi.total FROM order_items oi
    WHERE  oi.ticket_id = t.id
    ORDER  BY oi.ordinal
    LIMIT  1
) oi ON true
LEFT JOIN LATERAL (
    SELECT r.settlement,
           CASE WHEN r.ticket_id = t.id THEN r.amount END AS ticket_amount,
           COALESCE(NULLIF(r.provider_refund_id, ''),
                    CASE WHEN r.settlement = 'external' THEN NULLIF(r.reason, '') END) AS ref
    FROM   refunds r
    WHERE  r.state = 'succeeded'
      AND  r.origin <> 'referral_cashback'
      AND  (r.id = t.refund_id OR r.ticket_id = t.id)
    ORDER  BY r.succeeded_at DESC NULLS LAST, r.id
    LIMIT  1
) rf ON true
WHERE  t.session_id = ANY($1::uuid[])
  AND  (o.id IS NULL OR (NOT o.is_test AND o.status IN ('paid', 'partially_refunded', 'refunded')))
  AND  t.system_ticket_id > $2
ORDER  BY t.system_ticket_id
LIMIT  $3`

// AccountingTicketRow is one ticket of the accounting workbook.
type AccountingTicketRow struct {
	SystemTicketID   int64     `json:"system_ticket_id"`
	TicketStatus     string    `json:"ticket_status"`
	IssuedAt         time.Time `json:"issued_at"`
	SessionStart     time.Time `json:"session_start"`
	VenueTimezone    *string   `json:"venue_timezone"`
	EventName        string    `json:"event_name"`
	OrderNumber      *int64    `json:"order_number"`
	BuyerName        *string   `json:"buyer_name"`
	BuyerEmail       *string   `json:"buyer_email"`
	BuyerPhone       *string   `json:"buyer_phone"`
	Currency         string    `json:"currency"`
	TierName         *string   `json:"tier_name"`
	ItemTotal        *int64    `json:"item_total"`
	Complimentary    bool      `json:"complimentary"`
	RefundAmount     *int64    `json:"refund_amount"`
	RefundSettlement *string   `json:"refund_settlement"`
	RefundRef        *string   `json:"refund_ref"`
}

// ListAccountingTickets pages the tickets of the given sessions by
// system_ticket_id, after the last one of the previous page.
func (q *Queries) ListAccountingTickets(ctx context.Context, sessionIDs []uuid.UUID, after int64, limit int32) ([]AccountingTicketRow, error) {
	rows, err := q.db.Query(ctx, listAccountingTickets, sessionIDs, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []AccountingTicketRow
	for rows.Next() {
		var i AccountingTicketRow
		if err := rows.Scan(&i.SystemTicketID, &i.TicketStatus, &i.IssuedAt, &i.SessionStart, &i.VenueTimezone, &i.EventName,
			&i.OrderNumber, &i.BuyerName, &i.BuyerEmail, &i.BuyerPhone, &i.Currency, &i.TierName, &i.ItemTotal,
			&i.Complimentary, &i.RefundAmount, &i.RefundSettlement, &i.RefundRef); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

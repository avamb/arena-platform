// query.go — the SQL projection behind orderexport (moved verbatim from
// macs/export.go:105-300 in W1-B7a; the column list, joins and filters are
// unchanged so the MACS export keeps producing identical bytes).
package orderexport

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Row is one raw row of the export query: one ticket plus its order,
// session, event, venue and organization context. Exported so the
// projection can be unit-tested (and adapters golden-tested) without a
// database.
type Row struct {
	TicketID          uuid.UUID
	SystemTicketID    int64
	CheckoutSessionID uuid.UUID
	TierID            *uuid.UUID
	HolderEmail       *string
	TicketStatus      string
	IssuedAt          *time.Time
	SeatKey           *string
	SeatSector        *string
	SeatRow           *string
	SeatNumber        *string
	Ordinal           int32
	CancelledAt       *time.Time
	RefundDate        *time.Time
	RefundPrice       *int64
	OrderTotal        int64
	OrderSubtotal     int64
	OrderDiscount     int64
	OrderCurrency     string
	PaymentProvider   *string
	OrderCompletedAt  time.Time
	OrderUserID       *uuid.UUID
	SessionStartAt    time.Time
	SessionID         uuid.UUID
	EventID           uuid.UUID
	EventName         string
	OrgLegalName      string
	OrgName           string
	VenueID           uuid.UUID
	VenueName         string
	CityID            *uuid.UUID
	CityName          *string
	SeatSystemID      int64
	BarcodeStr        *string
	TierName          *string
	TierPrice         *int64
	SoldPrice         int64
	PromoCodeName     *string
	VenueTimezone     *string
}

const sessionQuery = `
SELECT
    t.id AS ticket_id,
    t.system_ticket_id,
    t.checkout_session_id,
    t.tier_id,
    t.holder_email,
    t.status AS ticket_status,
    t.issued_at,
    t.seat_key,
    t.seat_sector,
    t.seat_row,
    t.seat_number,
    t.ordinal,
    t.cancelled_at,
    t.refund_date,
    t.refund_price,
    COALESCE(cs.total, 0) AS order_total,
    COALESCE(cs.subtotal, 0) AS order_subtotal,
    COALESCE(cs.discount, 0) AS order_discount,
    COALESCE(cs.currency, s.currency) AS order_currency,
    cs.payment_provider,
    COALESCE(cs.completed_at, cs.created_at) AS order_completed_at,
    cs.user_id AS order_user_id,
    s.start_at AS session_start_at,
    s.id AS session_id,
    e.id AS event_id,
    e.name AS event_name,
    COALESCE(o.legal_name, o.name) AS org_legal_name,
    o.name AS org_name,
    v.id AS venue_id,
    v.name AS venue_name,
    ci.id AS city_id,
    COALESCE(t_en.value, ci.slug) AS city_name,
    -- GA tickets hold no seat row: give them a seatId from a DISJOINT
    -- range (1e9 + ticket id) so it can never collide with a real seat's
    -- system_seat_id (pass-7 review). Seat sequences start at 1.
    COALESCE(ss.system_seat_id, 1000000000 + t.system_ticket_id) AS seat_system_id,
    -- W1-Mb (spec §10 M4 / §11): the exported barcode is the ticket's EAN-13
    -- credential, never the 64-hex static_qr — a site that prints "EAN-13"
    -- needs a number whose check digit validates. static_qr stays the
    -- widget/PDF artifact.
    tc.payload AS barcode_str,
    tt.name AS tier_name,
    tt.price_amount AS tier_price,
    COALESCE(gi.unit_price, tt.price_amount, 0) AS sold_price,
    pc.code AS promo_code_name,
    v.timezone AS venue_timezone
FROM tickets t
JOIN checkout_sessions cs ON cs.id = t.checkout_session_id
JOIN sessions s ON s.id = t.session_id
JOIN events e ON e.id = s.event_id
JOIN organizations o ON o.id = e.org_id
JOIN venues v ON v.id = s.venue_id
LEFT JOIN cities ci ON ci.id = v.city_id
LEFT JOIN i18n_text t_en ON t_en.namespace = 'geo.cities'
    AND t_en.key = ci.slug AND t_en.locale = 'en'
LEFT JOIN session_seats ss ON ss.session_id = t.session_id
    AND ss.seat_key = t.seat_key AND t.seat_key IS NOT NULL
LEFT JOIN ticket_credentials tc ON tc.ticket_id = t.id AND tc.type = 'ean13'
LEFT JOIN ticket_tiers tt ON tt.id = t.tier_id
LEFT JOIN reservations r ON r.id = cs.reservation_id
LEFT JOIN reservation_ga_items gi ON gi.reservation_id = r.id AND gi.tier_id = t.tier_id
LEFT JOIN promo_codes pc ON pc.id = cs.promo_code_id
WHERE t.session_id = $1
  AND t.status IN ('active', 'cancelled', 'revoked')
  AND cs.state = 'completed'
ORDER BY t.checkout_session_id, t.ordinal
`

// ticketQuery is sessionQuery scoped to ONE ticket (webhook data payloads
// carry the same Ticket shape as the export — one projection, one
// contract). A cancelled/revoked ticket is included so ticket.refunded can
// carry a refund status.
var ticketQuery = strings.Replace(sessionQuery, "WHERE t.session_id = $1", "WHERE t.id = $1", 1)

// orderQuery is sessionQuery scoped to ONE order aggregate (orders.id,
// migration 0092). Tickets are linked to their order by tickets.order_id.
var orderQuery = strings.Replace(sessionQuery, "WHERE t.session_id = $1", "WHERE t.order_id = $1", 1)

// checkoutQuery is sessionQuery scoped to ONE checkout session. The Bil24
// gateway addresses orders by checkout session id (spec §7.8 GET_ORDER_INFO),
// which predates the orders aggregate, so it needs its own entry point.
var checkoutQuery = strings.Replace(sessionQuery, "WHERE t.session_id = $1", "WHERE t.checkout_session_id = $1", 1)

// QuerySession projects every exportable ticket of one event session,
// grouped into orders. Returns an empty (non-nil) slice when the session
// has no completed tickets.
func QuerySession(ctx context.Context, pool *pgxpool.Pool, sessionID uuid.UUID) ([]Order, error) {
	rows, err := query(ctx, pool, sessionQuery, sessionID)
	if err != nil {
		return nil, err
	}
	return Build(rows), nil
}

// QueryOrder projects ONE order aggregate (orders.id). Returns nil when the
// order has no exportable tickets (unknown id, unpaid, or nothing issued).
func QueryOrder(ctx context.Context, pool *pgxpool.Pool, orderID uuid.UUID) (*Order, error) {
	rows, err := query(ctx, pool, orderQuery, orderID)
	if err != nil {
		return nil, err
	}
	orders := Build(rows)
	if len(orders) == 0 {
		return nil, nil
	}
	// One orders row belongs to exactly one checkout session (#488), so
	// Build can only have produced a single group here.
	return &orders[0], nil
}

// QueryCheckoutSession projects ONE checkout session as an order. Returns nil
// when the session has no exportable tickets (unknown id, not completed, or
// nothing issued yet) — the caller decides whether that is a 404 or a
// degraded answer.
func QueryCheckoutSession(ctx context.Context, pool *pgxpool.Pool, checkoutSessionID uuid.UUID) (*Order, error) {
	rows, err := query(ctx, pool, checkoutQuery, checkoutSessionID)
	if err != nil {
		return nil, err
	}
	orders := Build(rows)
	if len(orders) == 0 {
		return nil, nil
	}
	// Every row was selected BY checkout session, so Build grouped them into
	// exactly one order.
	return &orders[0], nil
}

// QueryTicket projects ONE ticket together with the header of its owning
// order. Returns (nil, nil, nil) when the ticket is not exportable.
func QueryTicket(ctx context.Context, pool *pgxpool.Pool, ticketID uuid.UUID) (*Ticket, *Order, error) {
	rows, err := query(ctx, pool, ticketQuery, ticketID)
	if err != nil {
		return nil, nil, err
	}
	orders := Build(rows)
	if len(orders) == 0 || len(orders[0].Tickets) == 0 {
		return nil, nil, nil
	}
	order := orders[0]
	ticket := order.Tickets[0]
	return &ticket, &order, nil
}

// query runs one of the projection queries with a single uuid parameter.
func query(ctx context.Context, pool *pgxpool.Pool, sql string, id uuid.UUID) ([]Row, error) {
	rows, err := pool.Query(ctx, sql, id)
	if err != nil {
		return nil, fmt.Errorf("orderexport query: %w", err)
	}
	defer rows.Close()

	var result []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(
			&r.TicketID,
			&r.SystemTicketID,
			&r.CheckoutSessionID,
			&r.TierID,
			&r.HolderEmail,
			&r.TicketStatus,
			&r.IssuedAt,
			&r.SeatKey,
			&r.SeatSector,
			&r.SeatRow,
			&r.SeatNumber,
			&r.Ordinal,
			&r.CancelledAt,
			&r.RefundDate,
			&r.RefundPrice,
			&r.OrderTotal,
			&r.OrderSubtotal,
			&r.OrderDiscount,
			&r.OrderCurrency,
			&r.PaymentProvider,
			&r.OrderCompletedAt,
			&r.OrderUserID,
			&r.SessionStartAt,
			&r.SessionID,
			&r.EventID,
			&r.EventName,
			&r.OrgLegalName,
			&r.OrgName,
			&r.VenueID,
			&r.VenueName,
			&r.CityID,
			&r.CityName,
			&r.SeatSystemID,
			&r.BarcodeStr,
			&r.TierName,
			&r.TierPrice,
			&r.SoldPrice,
			&r.PromoCodeName,
			&r.VenueTimezone,
		); err != nil {
			return nil, fmt.Errorf("orderexport scan: %w", err)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("orderexport rows: %w", err)
	}
	return result, nil
}

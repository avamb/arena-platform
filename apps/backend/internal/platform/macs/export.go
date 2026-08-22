// export.go - MACS JSON export builder (AB-50b, feature #438).
//
// All MACS-shaped JSON types and the export builder live here, isolated from
// the catalog/ticketing domain. The canonical MACS sample is an array of
// orders; each order contains ticketList. The adapter maps our ticket/order
// model to the MACS shape.
package macs

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ── MACS JSON wire types ─────────────────────────────────────────────────────
// Field names are camelCase to match MACS's Python importer expectations.
// omitempty is used for optional fields that MACS tolerates missing.

// Export is the top-level MACS import file: an array of orders.
type Export []Order

// Order represents one checkout_session in MACS format.
type Order struct {
	ID               int64         `json:"id"`
	Date             string        `json:"date"`   // ISO-8601 UTC, e.g. "2026-08-22T10:00:00Z"
	Status           string        `json:"status"` // always "PAID" for completed orders
	Currency         string        `json:"currency"`
	Sum              int64         `json:"sum"`      // subtotal in minor units
	Discount         int64         `json:"discount"` // discount in minor units
	Charge           int64         `json:"charge"`   // total charged (sum - discount)
	TotalSum         int64         `json:"totalSum"`
	DiscountReason   string        `json:"discountReason,omitempty"`
	TicketQuantity   int           `json:"ticketQuantity"`
	User             OrderUser     `json:"user"`
	Email            string        `json:"email,omitempty"`
	PaymentMethod    string        `json:"paymentMethod,omitempty"`
	TicketList       []Ticket      `json:"ticketList"`
	SeatList         []interface{} `json:"seatList"`         // always empty array for compatibility
	GatewayOrderList []interface{} `json:"gatewayOrderList"` // always empty array
}

// OrderUser is the buyer user identity.
type OrderUser struct {
	ID    string `json:"id"`
	Email string `json:"email,omitempty"`
}

// Ticket represents one issued ticket in MACS format.
type Ticket struct {
	ID             int64         `json:"id"`      // system_ticket_id
	SeatID         int64         `json:"seatId"`  // system_seat_id or system_ticket_id for GA
	OrderID        int64         `json:"orderId"` // parent order id
	SeatLocation   SeatLocation  `json:"seatLocation"`
	Category       string        `json:"category,omitempty"` // tier name
	Tariff         string        `json:"tariff,omitempty"`
	Price          int64         `json:"price"` // unit price in minor units
	Discount       int64         `json:"discount"`
	Charge         int64         `json:"charge"`
	TotalPrice     int64         `json:"totalPrice"`
	DiscountReason string        `json:"discountReason,omitempty"`
	Barcode        string        `json:"barcode"`
	BarcodeFormat  BarcodeFormat `json:"barcodeFormat"`
	ActionEvent    ActionEvent   `json:"actionEvent"`
	HolderStatus   int           `json:"holderStatus"` // 0=valid, 3=refunded
	RefundDate     *string       `json:"refundDate,omitempty"`
	RefundPrice    *int64        `json:"refundPrice,omitempty"`
}

// SeatLocation is the sector/row/number triple.
type SeatLocation struct {
	Sector string `json:"sector"`
	Row    string `json:"row"`
	Number string `json:"number"`
}

// BarcodeFormat is always EAN-13 for compatibility.
type BarcodeFormat struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// ActionEvent is the denormalized event context attached to every ticket.
// MACS requires: id, cityName, venueName, actionName, actionLegalOwner, showTime.
type ActionEvent struct {
	ID               int64    `json:"id"` // event integer; derived from UUID first 8 bytes
	ExternalEventID  string   `json:"externalEventId,omitempty"`
	CityID           string   `json:"cityId,omitempty"`
	CityName         string   `json:"cityName"`
	VenueID          string   `json:"venueId,omitempty"`
	VenueName        string   `json:"venueName"`
	ActionID         string   `json:"actionId,omitempty"`
	ActionName       string   `json:"actionName"`
	ActionLegalOwner string   `json:"actionLegalOwner"`
	Currency         string   `json:"currency,omitempty"`
	ShowTime         string   `json:"showTime"` // local time, no TZ, "2026-08-22T20:00:00"
	Gateway          struct{} `json:"gateway"`  // always empty object
}

// ── DB query row ─────────────────────────────────────────────────────────────

// exportRow holds the result of one row from the export SQL query.
type exportRow struct {
	ticketID          uuid.UUID
	systemTicketID    int64
	checkoutSessionID uuid.UUID
	tierID            *uuid.UUID
	holderEmail       *string
	ticketStatus      string
	issuedAt          *time.Time
	seatKey           *string
	seatSector        *string
	seatRow           *string
	seatNumber        *string
	ordinal           int32
	cancelledAt       *time.Time
	refundDate        *time.Time
	refundPrice       *int64
	orderTotal        int64
	orderSubtotal     int64
	orderDiscount     int64
	orderCurrency     string
	paymentProvider   *string
	orderCompletedAt  time.Time
	orderUserID       *uuid.UUID
	sessionStartAt    time.Time
	eventID           uuid.UUID
	eventName         string
	orgLegalName      string
	orgName           string
	venueID           uuid.UUID
	venueName         string
	cityID            *uuid.UUID
	cityName          *string
	seatSystemID      int64
	barcodeStr        *string
	tierName          *string
	tierPrice         *int64
	soldPrice         int64
	promoCodeName     *string
	venueTimezone     *string
}

const exportQuery = `
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
    e.id AS event_id,
    e.name AS event_name,
    COALESCE(o.legal_name, o.name) AS org_legal_name,
    o.name AS org_name,
    v.id AS venue_id,
    v.name AS venue_name,
    ci.id AS city_id,
    COALESCE(t_en.value, ci.slug) AS city_name,
    COALESCE(ss.system_seat_id, t.system_ticket_id) AS seat_system_id,
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
LEFT JOIN ticket_credentials tc ON tc.ticket_id = t.id AND tc.type = 'static_qr'
LEFT JOIN ticket_tiers tt ON tt.id = t.tier_id
LEFT JOIN reservations r ON r.id = cs.reservation_id
LEFT JOIN reservation_ga_items gi ON gi.reservation_id = r.id AND gi.tier_id = t.tier_id
LEFT JOIN promo_codes pc ON pc.id = cs.promo_code_id
WHERE t.session_id = $1
  AND t.status IN ('active', 'cancelled', 'revoked')
  AND cs.state = 'completed'
ORDER BY t.checkout_session_id, t.ordinal
`

// exportQueryByTicket is exportQuery scoped to ONE ticket (webhook data
// payloads carry the same Ticket shape as the export — one builder, one
// contract). A cancelled/revoked ticket is included so ticket.refunded can
// carry holderStatus 3.
var exportQueryByTicket = strings.Replace(exportQuery, "WHERE t.session_id = $1", "WHERE t.id = $1", 1)

// QueryAndBuildTicket returns the MACS Ticket for one platform ticket id
// (plus the owning Order header) — used by the webhook dispatcher so the
// `data` object satisfies MACS's required Ticket fields (id, seatId,
// barcode, actionEvent{...}). Returns nil when the ticket is not
// exportable (unknown id or its order is not completed).
func QueryAndBuildTicket(ctx context.Context, pool *pgxpool.Pool, ticketID uuid.UUID) (*Ticket, *Order, error) {
	rows, err := queryRows(ctx, pool, exportQueryByTicket, ticketID)
	if err != nil {
		return nil, nil, err
	}
	export := buildExport(rows)
	if len(export) == 0 || len(export[0].TicketList) == 0 {
		return nil, nil, nil
	}
	order := export[0]
	ticket := order.TicketList[0]
	return &ticket, &order, nil
}

// queryExportRows executes the export query and returns raw rows.
func queryExportRows(ctx context.Context, pool *pgxpool.Pool, sessionID uuid.UUID) ([]exportRow, error) {
	return queryRows(ctx, pool, exportQuery, sessionID)
}

// queryRows runs one of the export queries with a single uuid parameter.
func queryRows(ctx context.Context, pool *pgxpool.Pool, query string, id uuid.UUID) ([]exportRow, error) {
	rows, err := pool.Query(ctx, query, id)
	if err != nil {
		return nil, fmt.Errorf("macs export query: %w", err)
	}
	defer rows.Close()

	var result []exportRow
	for rows.Next() {
		var r exportRow
		if err := rows.Scan(
			&r.ticketID,
			&r.systemTicketID,
			&r.checkoutSessionID,
			&r.tierID,
			&r.holderEmail,
			&r.ticketStatus,
			&r.issuedAt,
			&r.seatKey,
			&r.seatSector,
			&r.seatRow,
			&r.seatNumber,
			&r.ordinal,
			&r.cancelledAt,
			&r.refundDate,
			&r.refundPrice,
			&r.orderTotal,
			&r.orderSubtotal,
			&r.orderDiscount,
			&r.orderCurrency,
			&r.paymentProvider,
			&r.orderCompletedAt,
			&r.orderUserID,
			&r.sessionStartAt,
			&r.eventID,
			&r.eventName,
			&r.orgLegalName,
			&r.orgName,
			&r.venueID,
			&r.venueName,
			&r.cityID,
			&r.cityName,
			&r.seatSystemID,
			&r.barcodeStr,
			&r.tierName,
			&r.tierPrice,
			&r.soldPrice,
			&r.promoCodeName,
			&r.venueTimezone,
		); err != nil {
			return nil, fmt.Errorf("macs export scan: %w", err)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("macs export rows: %w", err)
	}
	return result, nil
}

// eventIntID derives a stable, non-negative int64 from a UUID by reading
// the first 8 bytes as a big-endian uint64 and keeping the low 63 bits
// (>> 1 clears the sign bit, so the conversion can never overflow). This
// is deterministic for a given event UUID and fits MACS's integer event
// ID requirement.
func eventIntID(id uuid.UUID) int64 {
	return int64(binary.BigEndian.Uint64(id[:8]) >> 1)
}

// buildExport groups exportRows by checkout_session_id and assembles the MACS
// Order/Ticket tree. Called by QueryAndBuildExport after the DB round-trip.
func buildExport(rows []exportRow) Export {
	if len(rows) == 0 {
		return Export{}
	}

	// Preserve insertion order of orders (rows are sorted by checkout_session_id).
	type orderKey = uuid.UUID
	orderIdx := map[orderKey]int{}
	var orders []Order

	for _, row := range rows {
		csID := row.checkoutSessionID

		// First time we see this checkout session: create the Order header.
		if _, exists := orderIdx[csID]; !exists {
			userID := ""
			if row.orderUserID != nil {
				userID = row.orderUserID.String()
			}
			userEmail := ""
			if row.holderEmail != nil {
				userEmail = *row.holderEmail
			}
			paymentMethod := ""
			if row.paymentProvider != nil {
				paymentMethod = *row.paymentProvider
			}
			orderIdx[csID] = len(orders)
			orders = append(orders, Order{
				ID:               0, // will be set to min system_ticket_id after all tickets
				Date:             row.orderCompletedAt.UTC().Format(time.RFC3339),
				Status:           "PAID",
				Currency:         row.orderCurrency,
				Sum:              row.orderSubtotal,
				Discount:         row.orderDiscount,
				Charge:           row.orderTotal,
				TotalSum:         row.orderTotal,
				TicketQuantity:   0, // incremented below
				User:             OrderUser{ID: userID, Email: userEmail},
				Email:            userEmail,
				PaymentMethod:    paymentMethod,
				TicketList:       nil,
				SeatList:         []interface{}{},
				GatewayOrderList: []interface{}{},
			})
		}

		idx := orderIdx[csID]
		o := &orders[idx]

		// Build ActionEvent (same for every ticket in a session).
		cityName := ""
		if row.cityName != nil {
			cityName = *row.cityName
		}
		cityIDStr := ""
		if row.cityID != nil {
			cityIDStr = row.cityID.String()
		}

		// Format showTime in the venue's local timezone.
		loc := time.UTC
		if row.venueTimezone != nil && *row.venueTimezone != "" {
			if l, err := time.LoadLocation(*row.venueTimezone); err == nil {
				loc = l
			}
		}
		showTime := row.sessionStartAt.In(loc).Format("2006-01-02T15:04:05") // allow:timeformat: MACS requires local time without TZ suffix

		ae := ActionEvent{
			ID:               eventIntID(row.eventID),
			CityID:           cityIDStr,
			CityName:         cityName,
			VenueID:          row.venueID.String(),
			VenueName:        row.venueName,
			ActionName:       row.eventName,
			ActionLegalOwner: row.orgLegalName,
			ShowTime:         showTime,
		}

		// Build barcode.
		barcode := fmt.Sprintf("%d", row.systemTicketID)
		if row.barcodeStr != nil && *row.barcodeStr != "" {
			barcode = *row.barcodeStr
		}

		// Determine holder status.
		// MACS holderStatus: 0 not used, 1 checked in, 2 checked out,
		// 3 refunded. Every terminal platform state (cancelled, revoked,
		// transferred) collapses to 3 at this boundary only.
		holderStatus := TicketStatus(row.ticketStatus)

		// Refund fields.
		var refundDate *string
		var refundPrice *int64
		if row.refundDate != nil {
			s := row.refundDate.UTC().Format(time.RFC3339)
			refundDate = &s
		}
		if row.refundPrice != nil {
			refundPrice = row.refundPrice
		}

		// Seat location.
		seatLoc := SeatLocation{}
		if row.seatSector != nil {
			seatLoc.Sector = *row.seatSector
		}
		if row.seatRow != nil {
			seatLoc.Row = *row.seatRow
		}
		if row.seatNumber != nil {
			seatLoc.Number = *row.seatNumber
		}

		// Tier name.
		category := ""
		if row.tierName != nil {
			category = *row.tierName
		}

		// Sold price: use the actual price paid (from reservation GA item or tier price).
		price := row.soldPrice

		// Discount reason: from promo code if present.
		discountReason := ""
		if row.promoCodeName != nil {
			discountReason = *row.promoCodeName
		}

		// Set order.id to the minimum system_ticket_id in this order.
		if o.ID == 0 || row.systemTicketID < o.ID {
			o.ID = row.systemTicketID
		}

		ticket := Ticket{
			ID:             row.systemTicketID,
			SeatID:         row.seatSystemID,
			OrderID:        o.ID, // placeholder; updated after loop
			SeatLocation:   seatLoc,
			Category:       category,
			Tariff:         category,
			Price:          price,
			Discount:       0,
			Charge:         price,
			TotalPrice:     price,
			DiscountReason: discountReason,
			Barcode:        barcode,
			BarcodeFormat: BarcodeFormat{
				ID:   1,
				Name: "EAN-13",
			},
			ActionEvent:  ae,
			HolderStatus: holderStatus,
			RefundDate:   refundDate,
			RefundPrice:  refundPrice,
		}
		o.TicketList = append(o.TicketList, ticket)
		o.TicketQuantity++
		o.Sum += price
	}

	// Second pass: fix OrderID on each ticket now that o.ID is final.
	for i := range orders {
		o := &orders[i]
		for j := range o.TicketList {
			o.TicketList[j].OrderID = o.ID
		}
	}

	return Export(orders)
}

// QueryAndBuildExport fetches all completed tickets for sessionID from the DB
// and assembles the MACS export document. Returns an empty array (not nil)
// when the session has no completed tickets.
func QueryAndBuildExport(ctx context.Context, pool *pgxpool.Pool, sessionID uuid.UUID) (Export, error) {
	rows, err := queryExportRows(ctx, pool, sessionID)
	if err != nil {
		return nil, err
	}
	_ = pgx.ErrNoRows // ensure pgx import is used
	return buildExport(rows), nil
}

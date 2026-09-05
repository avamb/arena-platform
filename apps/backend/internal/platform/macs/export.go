// export.go - MACS JSON export builder (AB-50b, feature #438).
//
// All MACS-shaped JSON types live here, isolated from the catalog/ticketing
// domain. The canonical MACS sample is an array of orders; each order
// contains ticketList.
//
// W1-B7a (feature #504): the DB projection behind this file moved to
// internal/platform/orderexport — the same facts feed the Bil24-compatible
// WordPress wire, so they must not be owned by the MACS adapter. This file
// is now purely an ENCODER: orderexport.Order/Ticket → MACS JSON. No
// behaviour changed with the move (testdata/sample_tickets.json unchanged).
package macs

import (
	"context"
	"encoding/binary"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/orderexport"
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

// ── Encoder: orderexport projection → MACS JSON ──────────────────────────────

// eventIntID derives a stable, non-negative int64 from a UUID by reading
// the first 8 bytes as a big-endian uint64 and keeping the low 63 bits
// (>> 1 clears the sign bit, so the conversion can never overflow). This
// is deterministic for a given event UUID and fits MACS's integer event
// ID requirement.
func eventIntID(id uuid.UUID) int64 {
	return int64(binary.BigEndian.Uint64(id[:8]) >> 1)
}

// encodeExport maps the neutral projection onto the MACS document.
func encodeExport(orders []orderexport.Order) Export {
	out := make(Export, 0, len(orders))
	for _, o := range orders {
		out = append(out, encodeOrder(o))
	}
	return out
}

// encodeOrder maps one projected order onto the MACS order.
func encodeOrder(o orderexport.Order) Order {
	tickets := make([]Ticket, 0, len(o.Tickets))
	for _, t := range o.Tickets {
		tickets = append(tickets, encodeTicket(t))
	}
	if len(tickets) == 0 {
		// Preserve the pre-extraction shape: an order without tickets
		// marshals ticketList as null, not [].
		tickets = nil
	}
	return Order{
		ID:               o.ID,
		Date:             o.CompletedAt.UTC().Format(time.RFC3339),
		Status:           "PAID",
		Currency:         o.Currency,
		Sum:              o.Subtotal,
		Discount:         o.Discount,
		Charge:           o.Total,
		TotalSum:         o.Total,
		DiscountReason:   o.DiscountReason,
		TicketQuantity:   o.TicketQuantity(),
		User:             OrderUser{ID: o.BuyerUserID, Email: o.BuyerEmail},
		Email:            o.BuyerEmail,
		PaymentMethod:    o.PaymentProvider,
		TicketList:       tickets,
		SeatList:         []interface{}{},
		GatewayOrderList: []interface{}{},
	}
}

// encodeTicket maps one projected ticket onto the MACS ticket. The MACS
// vocabulary (integer holderStatus, integer event id, EAN-13 barcode
// format) is applied HERE and nowhere else.
func encodeTicket(t orderexport.Ticket) Ticket {
	var refundDate *string
	if t.RefundDate != nil {
		s := t.RefundDate.UTC().Format(time.RFC3339)
		refundDate = &s
	}

	cityID := ""
	if t.Event.CityID != nil {
		cityID = t.Event.CityID.String()
	}

	return Ticket{
		ID:      t.ID,
		SeatID:  t.SeatID,
		OrderID: t.OrderID,
		SeatLocation: SeatLocation{
			Sector: t.Seat.Sector,
			Row:    t.Seat.Row,
			Number: t.Seat.Number,
		},
		Category:       t.TierName,
		Tariff:         t.TierName,
		Price:          t.Price,
		Discount:       t.Discount,
		Charge:         t.Charge,
		TotalPrice:     t.TotalPrice,
		DiscountReason: t.DiscountReason,
		Barcode:        t.Barcode,
		BarcodeFormat: BarcodeFormat{
			ID:   1,
			Name: "EAN-13",
		},
		ActionEvent: ActionEvent{
			ID:               eventIntID(t.Event.EventID),
			CityID:           cityID,
			CityName:         t.Event.CityName,
			VenueID:          t.Event.VenueID.String(),
			VenueName:        t.Event.VenueName,
			ActionName:       t.Event.EventName,
			ActionLegalOwner: t.Event.OrgLegalName,
			ShowTime:         t.Event.ShowTimeLocal,
		},
		// MACS holderStatus: 0 not used, 1 checked in, 2 checked out,
		// 3 refunded. Every terminal platform state (cancelled, revoked,
		// transferred) collapses to 3 at this boundary only.
		HolderStatus: TicketStatus(t.PlatformStatus),
		RefundDate:   refundDate,
		RefundPrice:  t.RefundPrice,
	}
}

// ── DB entry points ──────────────────────────────────────────────────────────

// QueryAndBuildExport fetches all completed tickets for sessionID from the DB
// and assembles the MACS export document. Returns an empty array (not nil)
// when the session has no completed tickets.
func QueryAndBuildExport(ctx context.Context, pool *pgxpool.Pool, sessionID uuid.UUID) (Export, error) {
	orders, err := orderexport.QuerySession(ctx, pool, sessionID)
	if err != nil {
		return nil, err
	}
	return encodeExport(orders), nil
}

// QueryAndBuildTicket returns the MACS Ticket for one platform ticket id
// (plus the owning Order header) — used by the webhook dispatcher so the
// `data` object satisfies MACS's required Ticket fields (id, seatId,
// barcode, actionEvent{...}). Returns nil when the ticket is not
// exportable (unknown id or its order is not completed).
func QueryAndBuildTicket(ctx context.Context, pool *pgxpool.Pool, ticketID uuid.UUID) (*Ticket, *Order, error) {
	ticket, order, err := orderexport.QueryTicket(ctx, pool, ticketID)
	if err != nil || ticket == nil {
		return nil, nil, err
	}
	encodedOrder := encodeOrder(*order)
	encodedTicket := encodeTicket(*ticket)
	return &encodedTicket, &encodedOrder, nil
}

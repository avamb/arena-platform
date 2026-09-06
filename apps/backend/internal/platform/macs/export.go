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
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/orderexport"
)

// ── MACS JSON wire types ─────────────────────────────────────────────────────
// Field names are camelCase to match MACS's Python importer expectations.
// omitempty is used for optional fields that MACS tolerates missing.

// Barcode format constants. Real Bil24 exports report EAN-13 as id 0 (spec
// §10 M4); MACS tolerates 1 as well, so 0 is the shape both systems agree on.
const (
	barcodeFormatEAN13ID   = 0
	barcodeFormatEAN13Name = "EAN-13"
)

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
	// ID is the SESSION's actionEventId from compatibility_id_map — the
	// same integer the Bil24-compatible gateway and the WordPress sites
	// use to name a showing (spec §10 M3). It is per SESSION, not per
	// event: two showings of one event are two MACS action events.
	ID              int64  `json:"id"`
	ExternalEventID string `json:"externalEventId,omitempty"`
	CityID          string `json:"cityId,omitempty"`
	CityName        string `json:"cityName"`
	VenueID         string `json:"venueId,omitempty"`
	VenueName       string `json:"venueName"`
	// ActionID is the EVENT's actionId from compatibility_id_map (spec
	// §10 M3). 0 means "not mapped", which MACS reads as absent.
	ActionID         int64    `json:"actionId"`
	ActionName       string   `json:"actionName"`
	ActionLegalOwner string   `json:"actionLegalOwner"`
	Currency         string   `json:"currency,omitempty"`
	ShowTime         string   `json:"showTime"` // local time, no TZ, "2026-08-22T20:00:00"
	Gateway          struct{} `json:"gateway"`  // always empty object
}

// ── Encoder: orderexport projection → MACS JSON ──────────────────────────────

// wireIDs carries the compatibility_id_map lookups the neutral projection
// cannot know: an event SESSION's actionEventId and an EVENT's actionId
// (spec §10 M3). A missing entry encodes as 0 — a degraded payload beats an
// undelivered sale, exactly as in bil24wire.EncodeContext.
type wireIDs struct {
	actionEvents map[uuid.UUID]int64
	actions      map[uuid.UUID]int64
}

// encodeExport maps the neutral projection onto the MACS document.
func encodeExport(orders []orderexport.Order, ids wireIDs) Export {
	out := make(Export, 0, len(orders))
	for _, o := range orders {
		out = append(out, encodeOrder(o, ids))
	}
	return out
}

// encodeOrder maps one projected order onto the MACS order.
func encodeOrder(o orderexport.Order, ids wireIDs) Order {
	tickets := make([]Ticket, 0, len(o.Tickets))
	for _, t := range o.Tickets {
		tickets = append(tickets, encodeTicket(t, ids))
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
func encodeTicket(t orderexport.Ticket, ids wireIDs) Ticket {
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
		// Real Bil24/MACS exports carry id 0 for EAN-13 (spec §10 M4);
		// MACS also accepts 1, so this is a shape alignment, not a
		// behaviour change on the receiving side.
		BarcodeFormat: BarcodeFormat{
			ID:   barcodeFormatEAN13ID,
			Name: barcodeFormatEAN13Name,
		},
		ActionEvent: ActionEvent{
			ID:               ids.actionEvents[t.Event.SessionID],
			CityID:           cityID,
			CityName:         t.Event.CityName,
			VenueID:          t.Event.VenueID.String(),
			VenueName:        t.Event.VenueName,
			ActionID:         ids.actions[t.Event.EventID],
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

// resolveWireIDs mints (on first read) and returns the actionEventId /
// actionId of every session and event named by orders. A lookup failure is
// swallowed into a nil map: the affected ids then encode as 0, the same
// degraded-payload rule bil24wire applies, because a webhook that fails to
// deliver costs more than one unmapped id.
func resolveWireIDs(ctx context.Context, pool *pgxpool.Pool, orders ...orderexport.Order) wireIDs {
	var sessions, events []uuid.UUID
	for _, o := range orders {
		for _, t := range o.Tickets {
			sessions = append(sessions, t.Event.SessionID)
			events = append(events, t.Event.EventID)
		}
	}
	return wireIDs{
		actionEvents: ensureIDs(ctx, pool, compatids.KindActionEvent, sessions),
		actions:      ensureIDs(ctx, pool, compatids.KindAction, events),
	}
}

// ensureIDs is resolveWireIDs' per-kind half.
func ensureIDs(ctx context.Context, pool *pgxpool.Pool, kind compatids.Kind, ids []uuid.UUID) map[uuid.UUID]int64 {
	if pool == nil || len(ids) == 0 {
		return nil
	}
	out, err := compatids.EnsureMany(ctx, pool, kind, ids)
	if err != nil {
		return nil
	}
	return out
}

// QueryAndBuildExport fetches all completed tickets for sessionID from the DB
// and assembles the MACS export document. Returns an empty array (not nil)
// when the session has no completed tickets.
func QueryAndBuildExport(ctx context.Context, pool *pgxpool.Pool, sessionID uuid.UUID) (Export, error) {
	orders, err := orderexport.QuerySession(ctx, pool, sessionID)
	if err != nil {
		return nil, err
	}
	return encodeExport(orders, resolveWireIDs(ctx, pool, orders...)), nil
}

// QueryAndBuildOrder returns the MACS Order for ONE order aggregate
// (orders.id, migration 0092) — the `data` object of the order.paid webhook
// (spec §10 M1: {id, status:"PAID", ticketList:[…]}). Returns nil when the
// order has nothing exportable (unknown id, unpaid, or no tickets issued).
func QueryAndBuildOrder(ctx context.Context, pool *pgxpool.Pool, orderID uuid.UUID) (*Order, error) {
	order, err := orderexport.QueryOrder(ctx, pool, orderID)
	if err != nil || order == nil {
		return nil, err
	}
	encoded := encodeOrder(*order, resolveWireIDs(ctx, pool, *order))
	return &encoded, nil
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
	ids := resolveWireIDs(ctx, pool, *order)
	encodedOrder := encodeOrder(*order, ids)
	encodedTicket := encodeTicket(*ticket, ids)
	return &encodedTicket, &encodedOrder, nil
}

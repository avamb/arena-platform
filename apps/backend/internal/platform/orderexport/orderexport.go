// Package orderexport is the NEUTRAL database projection of a completed
// order and its issued tickets.
//
// W1-B7a (spec §3.5, §9.3): the projection used to live inside the MACS
// adapter (`macs/export.go`), which made every other consumer of the same
// facts — the Bil24-compatible WordPress webhook wire (`bil24wire`), the
// gateway GET_ORDER command, admin exports — either import MACS or
// re-derive the joins by hand. The projection is now here and carries NO
// wire vocabulary: no camelCase JSON tags, no MACS integer status codes,
// no Bil24 enums. Money stays in minor units, times stay `time.Time`
// (plus the venue-local wall-clock string, which needs the venue timezone
// and therefore has to be resolved next to the query).
//
// Adapters (macs, bil24wire) are ENCODERS over these structs. Behaviour is
// identical to the pre-extraction MACS builder: the MACS golden file
// `macs/testdata/sample_tickets.json` is unchanged by the move.
package orderexport

import (
	"time"

	"github.com/google/uuid"
)

// Order is one completed checkout session with its issued tickets.
//
// ID is the integer order id on the wire: the minimum system_ticket_id of
// the order. It is derived here (not in an adapter) because both the MACS
// export and the Bil24 wire must agree on it, and because ticket.OrderID
// back-references it.
type Order struct {
	CheckoutSessionID uuid.UUID
	ID                int64
	CompletedAt       time.Time
	Currency          string
	// Subtotal, Discount and Total are minor units (bigint in the DB).
	Subtotal int64
	Discount int64
	Total    int64
	// DiscountReason is "Промокод <code>" when a promo code was applied,
	// "Внешняя система" for a comp/externally-settled order, "" otherwise.
	DiscountReason string
	// PaymentProvider is the checkout payment provider slug ("" when none,
	// i.e. an externally-settled or complimentary order).
	PaymentProvider string
	// BuyerUserID is the platform user UUID as a string, "" for guests.
	BuyerUserID string
	BuyerEmail  string
	Tickets     []Ticket
}

// TicketQuantity is the number of tickets in the order.
func (o Order) TicketQuantity() int { return len(o.Tickets) }

// Ticket is one issued ticket with its per-ticket money already prorated.
type Ticket struct {
	ID             int64 // system_ticket_id
	SeatID         int64 // system_seat_id, or a disjoint synthetic id for GA
	OrderID        int64 // Order.ID of the owning order
	TicketUUID     uuid.UUID
	Seated         bool // false for general admission (Seat is then zero)
	Seat           SeatLocation
	TierName       string
	Price          int64 // sold unit price, minor units
	Discount       int64 // prorated share of Order.Discount
	Charge         int64 // Price - Discount
	TotalPrice     int64 // == Charge
	DiscountReason string
	Barcode        string
	// PlatformStatus is the platform ticket status ("active", "cancelled",
	// "revoked") — adapters map it onto their own vocabulary.
	PlatformStatus string
	RefundDate     *time.Time
	RefundPrice    *int64
	Event          Event
}

// Event is the denormalized event/session context of a ticket.
type Event struct {
	EventID uuid.UUID
	// SessionID is the event SESSION the ticket belongs to. Both wire
	// adapters key their integer `actionEventId` off the session (spec §9.3
	// and MACS М3), not off the event, so the projection has to carry it.
	SessionID uuid.UUID
	EventName string
	// OrgLegalName is organizations.legal_name with a fallback to name.
	OrgLegalName string
	OrgName      string
	VenueID      uuid.UUID
	VenueName    string
	CityID       *uuid.UUID
	CityName     string
	Currency     string
	// SessionStartAt is the session start in UTC.
	SessionStartAt time.Time
	// ShowTimeLocal is SessionStartAt rendered as venue-local wall clock
	// without a timezone suffix ("2006-01-02T15:04:05"). Both MACS and the
	// Bil24 wire want local time; resolving the location needs
	// venues.timezone, which only the query knows.
	ShowTimeLocal string
}

// SeatLocation is the sector/row/number triple of a seated ticket.
type SeatLocation struct {
	Sector string
	Row    string
	Number string
}

// Package bil24wire is the Bil24-compatible ENCODER of the neutral order
// projection (internal/platform/orderexport).
//
// W1-B7b (spec §9.3): the WordPress sites we are migrating off Bil24 read
// orders and tickets in Bil24's own JSON vocabulary — string `holderStatus`,
// float money in MAJOR units, naive venue-local `showTime`, integer catalog
// ids. That vocabulary is applied HERE and nowhere else: `orderexport` stays
// neutral (minor units, time.Time, platform status words) and the MACS
// adapter is a SECOND, independent encoder over the same projection.
//
// The key sets are a BINDING contract taken from 68 real Bil24 order exports
// (`tests/compat/bil24/testdata/wp/bil24_orders_pseudonymized.json`):
// 36 keys on the order, 17 on the ticket, 14 on actionEvent. Every key is
// always present (no `omitempty`) except `ticketList`, which GET_ORDER_INFO
// omits on purpose (spec §7.8 returns the order header without it). Adding,
// renaming or dropping a key here breaks the WordPress receiver and must land
// as a spec change first — `tests/compat/bil24` fails on drift.
//
// This package must not import the HTTP layer, must not touch the database
// and must not know about channels or organizations: everything the
// projection cannot carry (integer catalog ids, the agent/frontend identity
// of the selling channel, buyer contact details) is supplied by the caller
// through EncodeContext.
package bil24wire

// Order is the Bil24 order object (spec §9.3, 36 keys).
//
// Field order follows the spec inventory so the marshalled document reads
// like the real exports; JSON key ORDER is cosmetic, the key SET is binding.
type Order struct {
	ID       int64    `json:"id"`
	Date     string   `json:"date"`
	User     User     `json:"user"`
	Agent    Agent    `json:"agent"`
	Frontend Frontend `json:"frontend"`
	Currency string   `json:"currency"`

	PaymentMethod   PaymentMethod `json:"paymentMethod"`
	LongReservation bool          `json:"longReservation"`
	// Expiration and Processing are RFC3339 with offset, or null.
	Expiration *string `json:"expiration"`
	Processing *string `json:"processing"`

	// TicketList is the ONLY optional key: GET_ORDER_INFO (spec §7.8)
	// answers with the order header alone, webhooks always carry the list.
	TicketList []Ticket `json:"ticketList,omitempty"`
	// SeatList and GatewayOrderList are always empty arrays — Bil24 emits
	// them and the WordPress receiver iterates them unconditionally.
	SeatList         []any `json:"seatList"`
	GatewayOrderList []any `json:"gatewayOrderList"`

	// Money is FLOAT MAJOR units (the DB keeps minor units). The
	// `filtered*` twins are Bil24's per-frontend filtered totals; with one
	// frontend per order they equal their unfiltered counterparts.
	Sum                    float64 `json:"sum"`
	FilteredSum            float64 `json:"filteredSum"`
	Discount               float64 `json:"discount"`
	FilteredDiscount       float64 `json:"filteredDiscount"`
	Charge                 float64 `json:"charge"`
	FilteredCharge         float64 `json:"filteredCharge"`
	TotalSum               float64 `json:"totalSum"`
	FilteredTotalSum       float64 `json:"filteredTotalSum"`
	TicketQuantity         int     `json:"ticketQuantity"`
	FilteredTicketQuantity int     `json:"filteredTicketQuantity"`

	Status    string    `json:"status"`
	Acquiring Acquiring `json:"acquiring"`

	PaymentBankID      string  `json:"paymentBankId"`
	PaymentBankStatus  string  `json:"paymentBankStatus"`
	PaymentBankMessage string  `json:"paymentBankMessage"`
	PaymentRRN         *string `json:"paymentRRN"`
	PaymentTerminalID  *string `json:"paymentTerminalId"`
	PaymentCardPAN     *string `json:"paymentCardPAN"`
	PaymentCardBank    *string `json:"paymentCardBank"`

	Email     *string `json:"email"`
	EmailSent *string `json:"emailSent"`
	Phone     *string `json:"phone"`
	FullName  *string `json:"fullName"`
}

// User is the buyer identity (integer id, Bil24 style).
type User struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
}

// Agent is the selling agent — the arena organization.
type Agent struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Frontend is the selling front end — the arena sales channel.
type Frontend struct {
	ID      int64        `json:"id"`
	AgentID int64        `json:"agentId"`
	Name    string       `json:"name"`
	Type    FrontendType `json:"type"`
}

// FrontendType is Bil24's frontend classification; ticketing systems are
// {id: 8, name: "Ticketing system"}.
type FrontendType struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// PaymentMethod is the payment instrument; arena reports the provider slug
// under the Bil24 "unmapped" id 0.
type PaymentMethod struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Acquiring is the acquiring bank block. Arena settles outside Bil24, so
// every field is the neutral zero value — but the block must be present.
type Acquiring struct {
	ID         int64  `json:"id"`
	SystemID   int64  `json:"systemId"`
	Name       string `json:"name"`
	SystemName string `json:"systemName"`
	AgentID    int64  `json:"agentId"`
	AgentName  string `json:"agentName"`
}

// Ticket is the Bil24 ticket object (spec §9.3, 17 keys).
type Ticket struct {
	ID      int64 `json:"id"`
	SeatID  int64 `json:"seatId"`
	OrderID int64 `json:"orderId"`
	// SeatLocation is null for general admission, an object for a seat.
	SeatLocation *SeatLocation `json:"seatLocation"`
	// Category is the tariff NAME as a plain string (Bil24 sends a string
	// here, not an object).
	Category string `json:"category"`
	// Tariff is always null: arena has no second-level tariff entity.
	Tariff *string `json:"tariff"`

	Price      float64 `json:"price"`
	Discount   float64 `json:"discount"`
	Charge     float64 `json:"charge"`
	TotalPrice float64 `json:"totalPrice"`
	// DiscountReason is "Промокод <code>" or null.
	DiscountReason *string `json:"discountReason"`

	Barcode       string        `json:"barcode"`
	BarcodeFormat BarcodeFormat `json:"barcodeFormat"`
	ActionEvent   ActionEvent   `json:"actionEvent"`

	// HolderStatus is "NEVER_USE" or "REFUND" (string, unlike MACS's int).
	HolderStatus string `json:"holderStatus"`
	// RefundDate is RFC3339 WITH offset, or null.
	RefundDate  *string  `json:"refundDate"`
	RefundPrice *float64 `json:"refundPrice"`
}

// SeatLocation is the sector/row/number triple of a seated ticket.
type SeatLocation struct {
	Sector string `json:"sector"`
	Row    string `json:"row"`
	Number string `json:"number"`
}

// BarcodeFormat is {id: 0, name: "EAN-13"} — id 0 is what real Bil24
// exports carry (spec §10 М4).
type BarcodeFormat struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// ActionEvent is the denormalized session context (spec §9.3, 14 keys).
type ActionEvent struct {
	// ID is the SESSION's actionEventId, ActionID the EVENT's actionId —
	// both integer compatibility ids (spec §3.1).
	ID                  int64      `json:"id"`
	CityID              int64      `json:"cityId"`
	CityName            string     `json:"cityName"`
	VenueID             int64      `json:"venueId"`
	VenueName           string     `json:"venueName"`
	ActionID            int64      `json:"actionId"`
	ActionName          string     `json:"actionName"`
	ActionLegalOwner    string     `json:"actionLegalOwner"`
	ActionLegalOwnerInn string     `json:"actionLegalOwnerInn"`
	ActionKind          ActionKind `json:"actionKind"`
	Currency            string     `json:"currency"`
	// ShowTime is venue-local wall clock WITHOUT a timezone suffix.
	ShowTime string  `json:"showTime"`
	ETickets bool    `json:"eTickets"`
	Gateway  Gateway `json:"gateway"`
}

// ActionKind is Bil24's event classification; arena emits the generic
// {id: 0, name: "Events"}.
type ActionKind struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Gateway is the (unused) external gateway block; arena is the system of
// record, so systemName is "NONE" and the organizer fields are null.
type Gateway struct {
	ID            int64   `json:"id"`
	SystemID      int64   `json:"systemId"`
	Name          string  `json:"name"`
	SystemName    string  `json:"systemName"`
	OrganizerID   *int64  `json:"organizerId"`
	OrganizerName *string `json:"organizerName"`
}

// RefundedTicket is the `ticket.refunded` webhook payload (spec §9.2): the
// subset of Ticket a WordPress receiver needs to void a line item.
type RefundedTicket struct {
	ID           int64       `json:"id"`
	OrderID      int64       `json:"orderId"`
	SeatID       int64       `json:"seatId"`
	Barcode      string      `json:"barcode"`
	RefundPrice  *float64    `json:"refundPrice"`
	RefundDate   *string     `json:"refundDate"`
	Category     string      `json:"category"`
	HolderStatus string      `json:"holderStatus"`
	ActionEvent  ActionEvent `json:"actionEvent"`
}

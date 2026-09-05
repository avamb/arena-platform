// encode.go — orderexport projection → Bil24 wire objects (spec §9.3).
//
// Pure functions over values: no context.Context, no database, no logger.
// Everything the neutral projection cannot know (integer catalog ids, the
// agent/frontend identity, buyer contact details, the service charge) comes
// in through EncodeContext, so the encoder can be golden-tested without a
// pool and reused verbatim by the webhook dispatcher and by GET_ORDER_INFO.
package bil24wire

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/orderexport"
)

// Bil24 wire constants. These are literals of the FOREIGN vocabulary; they
// exist only at this boundary.
const (
	// HolderStatusNeverUse marks a live, never-scanned ticket.
	HolderStatusNeverUse = "NEVER_USE"
	// HolderStatusRefund marks a cancelled / revoked / refunded ticket.
	HolderStatusRefund = "REFUND"

	// StatusPaid is the only order status arena exports (§9.2: order.paid).
	StatusPaid = "PAID"
	// StatusCancelled is the status of the order.cancelled payload.
	StatusCancelled = "CANCELLED"

	// FrontendTypeTicketing is Bil24's frontend type for ticketing systems.
	frontendTypeTicketingID   = 8
	frontendTypeTicketingName = "Ticketing system"

	// defaultPaymentBankMessage is what real Vino&Co exports carry for
	// orders settled outside the acquiring bank.
	defaultPaymentBankMessage = "Paid per protocol"

	// barcodeFormatEAN13Name / ID: real Bil24 exports use id 0.
	barcodeFormatEAN13Name = "EAN-13"
	barcodeFormatEAN13ID   = 0

	// actionKindEventsName is the generic Bil24 action kind.
	actionKindEventsName = "Events"

	// gatewaySystemNone marks "no external gateway".
	gatewaySystemNone = "NONE"

	// promoReasonPrefix is the discount reason the neutral projection uses
	// for a promo code ("Промокод <code>"); other reasons (e.g. the
	// internal "Внешняя система") are NOT exported to Bil24 clients.
	promoReasonPrefix = "Промокод "
)

// EncodeContext carries everything the neutral projection cannot know.
//
// The four id maps translate platform UUIDs into the integer catalog ids the
// wire demands (compatibility_id_map, spec §3.1). A missing entry encodes as
// 0 rather than failing the whole webhook: an unmapped city is a degraded
// payload, an undelivered order is a lost sale.
type EncodeContext struct {
	// Agent is the selling organization; Frontend the sales channel
	// (Frontend.AgentID must equal Agent.ID).
	Agent    Agent
	Frontend Frontend
	// PaymentMethodName overrides the projection's payment provider slug
	// when non-empty (e.g. the WooCommerce gateway title).
	PaymentMethodName string
	// UserID is the buyer's integer id (0 for guests).
	UserID int64
	// Email, Phone and FullName are the order's buyer contact block; empty
	// strings encode as JSON null, matching real exports.
	Email    string
	Phone    string
	FullName string
	// LegalOwnerInn is organizations.tax_id ("" when unknown).
	LegalOwnerInn string
	// LongReservation mirrors Bil24's long-hold flag.
	LongReservation bool
	// Expiration is the hold expiry, Processing the payment timestamp.
	Expiration *time.Time
	Processing *time.Time
	// PaymentBankID / Status / Message describe the bank leg. An empty
	// Message encodes as defaultPaymentBankMessage.
	PaymentBankID      string
	PaymentBankStatus  string
	PaymentBankMessage string
	// Charge is the order's service charge in MINOR units. When nil it is
	// derived from the projection as total - (subtotal - discount), which
	// is the same number by construction.
	Charge *int64

	// ActionEventIDs maps an event SESSION uuid to its actionEventId.
	ActionEventIDs map[uuid.UUID]int64
	// ActionIDs maps an EVENT uuid to its actionId.
	ActionIDs map[uuid.UUID]int64
	// VenueIDs and CityIDs map venue / city uuids to their integer ids.
	VenueIDs map[uuid.UUID]int64
	CityIDs  map[uuid.UUID]int64
}

// EncodeOrder maps the neutral projection onto the full Bil24 order,
// ticketList included (the `order.paid` webhook payload, spec §9.2).
func EncodeOrder(o orderexport.Order, ec EncodeContext) Order {
	out := EncodeOrderHeader(o, ec)
	charge := orderCharge(o, ec)
	tickets := make([]Ticket, 0, len(o.Tickets))
	for i, t := range o.Tickets {
		tickets = append(tickets, encodeTicket(t, ec, ticketCharge(o, charge, i)))
	}
	out.TicketList = tickets
	return out
}

// EncodeOrderHeader is EncodeOrder without ticketList: the GET_ORDER_INFO
// answer (spec §7.8, "Bil24 Order без ticketList"). Every other key is
// identical, so the two surfaces can never drift apart.
func EncodeOrderHeader(o orderexport.Order, ec EncodeContext) Order {
	charge := orderCharge(o, ec)
	net := o.Subtotal - o.Discount

	bankMessage := ec.PaymentBankMessage
	if bankMessage == "" {
		bankMessage = defaultPaymentBankMessage
	}

	paymentMethod := PaymentMethod{Name: ec.PaymentMethodName}
	if paymentMethod.Name == "" {
		paymentMethod.Name = o.PaymentProvider
	}

	frontend := ec.Frontend
	if frontend.Type.Name == "" {
		frontend.Type = FrontendType{ID: frontendTypeTicketingID, Name: frontendTypeTicketingName}
	}

	return Order{
		ID:              o.ID,
		Date:            offsetTime(o.CompletedAt),
		User:            User{ID: ec.UserID, Email: o.BuyerEmail},
		Agent:           ec.Agent,
		Frontend:        frontend,
		Currency:        o.Currency,
		PaymentMethod:   paymentMethod,
		LongReservation: ec.LongReservation,
		Expiration:      nullableTime(ec.Expiration),
		Processing:      nullableTime(ec.Processing),
		// ticketList stays nil here — see the field comment.
		SeatList:               []any{},
		GatewayOrderList:       []any{},
		Sum:                    major(o.Subtotal),
		FilteredSum:            major(o.Subtotal),
		Discount:               major(o.Discount),
		FilteredDiscount:       major(o.Discount),
		Charge:                 major(charge),
		FilteredCharge:         major(charge),
		TotalSum:               major(net + charge),
		FilteredTotalSum:       major(net + charge),
		TicketQuantity:         o.TicketQuantity(),
		FilteredTicketQuantity: o.TicketQuantity(),
		Status:                 StatusPaid,
		Acquiring:              Acquiring{},
		PaymentBankID:          ec.PaymentBankID,
		PaymentBankStatus:      ec.PaymentBankStatus,
		PaymentBankMessage:     bankMessage,
		PaymentRRN:             nil,
		PaymentTerminalID:      nil,
		PaymentCardPAN:         nil,
		PaymentCardBank:        nil,
		Email:                  nullableString(ec.Email),
		EmailSent:              nil,
		Phone:                  nullableString(ec.Phone),
		FullName:               nullableString(ec.FullName),
	}
}

// EncodeTicketRefunded builds the `ticket.refunded` webhook payload
// (spec §9.2). holderStatus is REFUND unconditionally: the event itself is
// the assertion that the ticket is no longer valid, whatever the projection
// managed to read about the ticket row.
func EncodeTicketRefunded(t orderexport.Ticket, ec EncodeContext) RefundedTicket {
	return RefundedTicket{
		ID:           t.ID,
		OrderID:      t.OrderID,
		SeatID:       t.SeatID,
		Barcode:      t.Barcode,
		RefundPrice:  nullableMajor(t.RefundPrice),
		RefundDate:   nullableTime(t.RefundDate),
		Category:     t.TierName,
		HolderStatus: HolderStatusRefund,
		ActionEvent:  encodeActionEvent(t, ec),
	}
}

// encodeTicket maps one projected ticket; charge is its already-prorated
// share of the order's service charge, in minor units.
func encodeTicket(t orderexport.Ticket, ec EncodeContext, charge int64) Ticket {
	var seat *SeatLocation
	if t.Seated {
		seat = &SeatLocation{Sector: t.Seat.Sector, Row: t.Seat.Row, Number: t.Seat.Number}
	}

	return Ticket{
		ID:             t.ID,
		SeatID:         t.SeatID,
		OrderID:        t.OrderID,
		SeatLocation:   seat,
		Category:       t.TierName,
		Tariff:         nil,
		Price:          major(t.Price),
		Discount:       major(t.Discount),
		Charge:         major(charge),
		TotalPrice:     major(t.Price - t.Discount + charge),
		DiscountReason: promoReason(t.DiscountReason),
		Barcode:        t.Barcode,
		BarcodeFormat:  BarcodeFormat{ID: barcodeFormatEAN13ID, Name: barcodeFormatEAN13Name},
		ActionEvent:    encodeActionEvent(t, ec),
		HolderStatus:   holderStatus(t.PlatformStatus),
		RefundDate:     nullableTime(t.RefundDate),
		RefundPrice:    nullableMajor(t.RefundPrice),
	}
}

// encodeActionEvent denormalizes the session context of one ticket.
func encodeActionEvent(t orderexport.Ticket, ec EncodeContext) ActionEvent {
	cityID := int64(0)
	if t.Event.CityID != nil {
		cityID = ec.CityIDs[*t.Event.CityID]
	}
	return ActionEvent{
		ID:                  ec.ActionEventIDs[t.Event.SessionID],
		CityID:              cityID,
		CityName:            t.Event.CityName,
		VenueID:             ec.VenueIDs[t.Event.VenueID],
		VenueName:           t.Event.VenueName,
		ActionID:            ec.ActionIDs[t.Event.EventID],
		ActionName:          t.Event.EventName,
		ActionLegalOwner:    t.Event.OrgLegalName,
		ActionLegalOwnerInn: ec.LegalOwnerInn,
		ActionKind:          ActionKind{ID: 0, Name: actionKindEventsName},
		Currency:            t.Event.Currency,
		ShowTime:            t.Event.ShowTimeLocal,
		ETickets:            true,
		Gateway:             Gateway{SystemName: gatewaySystemNone},
	}
}

// holderStatus maps the platform ticket status onto Bil24's two-value
// vocabulary. Anything that is not an active ticket is a REFUND to a Bil24
// client — it has no word for "revoked" or "transferred".
func holderStatus(platformStatus string) string {
	if platformStatus == "active" {
		return HolderStatusNeverUse
	}
	return HolderStatusRefund
}

// promoReason exports ONLY the promo-code reason. The projection also uses
// the internal "Внешняя система" marker for comps, which is arena
// bookkeeping and must not surface as a customer-visible discount reason.
func promoReason(reason string) *string {
	if !strings.HasPrefix(reason, promoReasonPrefix) {
		return nil
	}
	out := reason
	return &out
}

// orderCharge is the order's service charge in minor units: the caller's
// value when supplied, otherwise total - (subtotal - discount). Never
// negative — a total below the net line is a pricing defect, not a refund,
// and must not travel as a negative fee.
func orderCharge(o orderexport.Order, ec EncodeContext) int64 {
	if ec.Charge != nil {
		return *ec.Charge
	}
	charge := o.Total - (o.Subtotal - o.Discount)
	if charge < 0 {
		return 0
	}
	return charge
}

// ticketCharge prorates the order charge over the tickets by price, with the
// LAST ticket absorbing the rounding remainder so the per-ticket charges sum
// to the order charge EXACTLY (the AB-50i rule the discount already follows).
func ticketCharge(o orderexport.Order, charge int64, i int) int64 {
	n := len(o.Tickets)
	if charge == 0 || n == 0 {
		return 0
	}
	// Prorating by price needs a positive price base; fall back to an even
	// split (remainder on the last ticket) when every price is 0.
	var base int64
	for _, t := range o.Tickets {
		base += t.Price
	}
	var allocated int64
	for j := 0; j < n; j++ {
		var c int64
		switch {
		case j == n-1:
			c = charge - allocated
		case base > 0:
			c = o.Tickets[j].Price * charge / base
		default:
			c = charge / int64(n)
		}
		if j == i {
			return c
		}
		allocated += c
	}
	return 0
}

// major converts minor units to the float major units the wire uses. The
// division by 100 is exact for every amount below 2^53/100, which is every
// amount a ticketing system can produce.
func major(minor int64) float64 { return float64(minor) / 100 }

// nullableMajor converts an optional minor-unit amount.
func nullableMajor(minor *int64) *float64 {
	if minor == nil {
		return nil
	}
	v := major(*minor)
	return &v
}

// offsetTime renders a timestamp as RFC3339 WITH an offset (never naive) —
// the wire form of `date`, `expiration`, `processing` and `refundDate`.
func offsetTime(t time.Time) string { return t.Format(time.RFC3339) }

// nullableTime renders an optional timestamp, or nil for JSON null.
func nullableTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := offsetTime(*t)
	return &s
}

// nullableString maps "" onto JSON null, which is what real exports carry
// for an unknown buyer e-mail / phone / name.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

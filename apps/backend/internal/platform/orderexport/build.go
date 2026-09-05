// build.go — grouping and money projection (moved from macs/export.go:313-534
// in W1-B7a). The arithmetic is byte-for-byte the pre-extraction behaviour:
// order id = min system_ticket_id, GA price fallback from the order subtotal,
// discount prorated per ticket with the last ticket absorbing the rounding
// remainder (AB-50i).
package orderexport

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Build groups rows by checkout session and assembles the order/ticket
// projection. Rows are expected in the query's order (checkout session,
// then ordinal); the grouping preserves first-seen order regardless.
// Returns an empty, non-nil slice for empty input.
func Build(rows []Row) []Order {
	if len(rows) == 0 {
		return []Order{}
	}

	orderIdx := map[uuid.UUID]int{}
	orders := make([]Order, 0, 1)

	for _, row := range rows {
		csID := row.CheckoutSessionID

		if _, exists := orderIdx[csID]; !exists {
			orderIdx[csID] = len(orders)
			orders = append(orders, newOrder(row))
		}

		o := &orders[orderIdx[csID]]

		// order.id is the minimum system_ticket_id of the order.
		if o.ID == 0 || row.SystemTicketID < o.ID {
			o.ID = row.SystemTicketID
		}

		o.Tickets = append(o.Tickets, newTicket(row, o.ID))
	}

	for i := range orders {
		finalizeMoney(&orders[i])
	}
	return orders
}

// newOrder builds the order header from the first row of its group.
func newOrder(row Row) Order {
	userID := ""
	if row.OrderUserID != nil {
		userID = row.OrderUserID.String()
	}
	buyerEmail := ""
	if row.HolderEmail != nil {
		buyerEmail = *row.HolderEmail
	}
	paymentProvider := ""
	if row.PaymentProvider != nil {
		paymentProvider = *row.PaymentProvider
	}
	return Order{
		CheckoutSessionID: row.CheckoutSessionID,
		ID:                0, // set to min system_ticket_id while ticketing
		CompletedAt:       row.OrderCompletedAt,
		Currency:          row.OrderCurrency,
		Subtotal:          row.OrderSubtotal,
		Discount:          row.OrderDiscount,
		Total:             row.OrderTotal,
		DiscountReason:    discountReason(row),
		PaymentProvider:   paymentProvider,
		BuyerUserID:       userID,
		BuyerEmail:        buyerEmail,
	}
}

// newTicket projects one row into a ticket. Per-ticket discount/charge are
// finalized later (they need the whole order).
func newTicket(row Row, orderID int64) Ticket {
	seated := row.SeatKey != nil
	seat := SeatLocation{}
	if row.SeatSector != nil {
		seat.Sector = *row.SeatSector
	}
	if row.SeatRow != nil {
		seat.Row = *row.SeatRow
	}
	if row.SeatNumber != nil {
		seat.Number = *row.SeatNumber
	}

	tierName := ""
	if row.TierName != nil {
		tierName = *row.TierName
	}

	// The barcode credential wins; the bare system ticket id is the
	// pre-credential fallback.
	barcode := fmt.Sprintf("%d", row.SystemTicketID)
	if row.BarcodeStr != nil && *row.BarcodeStr != "" {
		barcode = *row.BarcodeStr
	}

	cityName := ""
	if row.CityName != nil {
		cityName = *row.CityName
	}

	price := row.SoldPrice
	return Ticket{
		ID:             row.SystemTicketID,
		SeatID:         row.SeatSystemID,
		OrderID:        orderID, // rewritten once the order id is final
		TicketUUID:     row.TicketID,
		Seated:         seated,
		Seat:           seat,
		TierName:       tierName,
		Price:          price,
		Discount:       0, // finalizeMoney
		Charge:         price,
		TotalPrice:     price,
		DiscountReason: discountReason(row),
		Barcode:        barcode,
		PlatformStatus: row.TicketStatus,
		RefundDate:     row.RefundDate,
		RefundPrice:    row.RefundPrice,
		Event: Event{
			EventID:        row.EventID,
			EventName:      row.EventName,
			OrgLegalName:   row.OrgLegalName,
			OrgName:        row.OrgName,
			VenueID:        row.VenueID,
			VenueName:      row.VenueName,
			CityID:         row.CityID,
			CityName:       cityName,
			Currency:       row.OrderCurrency,
			SessionStartAt: row.SessionStartAt,
			ShowTimeLocal:  showTimeLocal(row),
		},
	}
}

// discountReason is the human-readable cause of a discount:
//   - promo applied            → "Промокод {code}" (the MACS report format)
//   - no promo, no provider    → "Внешняя система" (comp / externally settled)
//   - ordinary paid purchase   → ""
func discountReason(row Row) string {
	if row.PromoCodeName != nil {
		return "Промокод " + *row.PromoCodeName
	}
	if row.PaymentProvider == nil {
		return "Внешняя система"
	}
	return ""
}

// showTimeLocal renders the session start as venue-local wall clock with no
// timezone suffix. An unset or unloadable timezone falls back to UTC.
func showTimeLocal(row Row) string {
	loc := time.UTC
	if row.VenueTimezone != nil && *row.VenueTimezone != "" {
		if l, err := time.LoadLocation(*row.VenueTimezone); err == nil {
			loc = l
		}
	}
	return row.SessionStartAt.In(loc).Format("2006-01-02T15:04:05") // allow:timeformat: local wall clock without TZ, required by MACS and the Bil24 wire
}

// finalizeMoney fills the per-ticket money that can only be known once the
// whole order is grouped, and back-fills the final order id onto tickets.
func finalizeMoney(o *Order) {
	n := len(o.Tickets)

	// (a) Untiered GA tickets (no tier, no reservation GA item) report a
	//     sold price of 0: fall back to an even split of the subtotal so
	//     the export never claims a free ticket for a paid order.
	if o.Subtotal > 0 && n > 0 {
		for j := range o.Tickets {
			if o.Tickets[j].Price == 0 {
				o.Tickets[j].Price = o.Subtotal / int64(n)
				o.Tickets[j].Charge = o.Tickets[j].Price
				o.Tickets[j].TotalPrice = o.Tickets[j].Price
			}
		}
	}

	// (b) Prorate the order discount over the tickets by price; the last
	//     ticket absorbs the rounding remainder so the per-ticket
	//     discounts sum to the order discount EXACTLY (AB-50i).
	if o.Subtotal > 0 && o.Discount > 0 {
		var allocated int64
		for j := range o.Tickets {
			var d int64
			if j == n-1 {
				d = o.Discount - allocated
			} else {
				d = o.Tickets[j].Price * o.Discount / o.Subtotal
				allocated += d
			}
			o.Tickets[j].Discount = d
			o.Tickets[j].Charge = o.Tickets[j].Price - d
			o.Tickets[j].TotalPrice = o.Tickets[j].Charge
		}
	}

	// (c) The order id is only final after every ticket has been seen.
	for j := range o.Tickets {
		o.Tickets[j].OrderID = o.ID
	}
}

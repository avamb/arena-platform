// export_unit_test.go — white-box unit tests for the MACS export.
// Package macs (internal) so the encoder is directly accessible.
//
// W1-B7a: the DB projection moved to internal/platform/orderexport, so the
// fixtures build orderexport.Row and buildExport below is the composition
// under test (project, then encode). Every assertion is unchanged from
// before the extraction — that is what proves the move was behaviour-free.
package macs

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/orderexport"
)

// buildExport is the pre-extraction entry point, expressed as the
// projection followed by the MACS encoder.
func buildExport(rows []orderexport.Row) Export {
	return encodeExport(orderexport.Build(rows))
}

// helpers

func strPtr(s string) *string { return &s }
func i64Ptr(n int64) *int64   { return &n }

// baseRow returns a minimal valid orderexport.Row for a single active GA ticket.
func baseRow() orderexport.Row {
	eventID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	sessionID := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	ticketID := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	csID := uuid.MustParse("00000000-0000-0000-0000-000000000004")
	venueID := uuid.MustParse("00000000-0000-0000-0000-000000000005")
	_ = sessionID // not in orderexport.Row
	return orderexport.Row{
		TicketID:          ticketID,
		SystemTicketID:    1001,
		CheckoutSessionID: csID,
		TierID:            nil,
		HolderEmail:       strPtr("buyer@example.com"),
		TicketStatus:      "active",
		IssuedAt:          nil,
		SeatKey:           nil, // GA
		SeatSector:        nil,
		SeatRow:           nil,
		SeatNumber:        nil,
		Ordinal:           1,
		CancelledAt:       nil,
		RefundDate:        nil,
		RefundPrice:       nil,
		OrderTotal:        1500,
		OrderSubtotal:     1500,
		OrderDiscount:     0,
		OrderCurrency:     "RUB",
		PaymentProvider:   strPtr("yookassa"),
		OrderCompletedAt:  time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC),
		OrderUserID:       nil,
		SessionStartAt:    time.Date(2026, 8, 22, 20, 0, 0, 0, time.UTC),
		EventID:           eventID,
		EventName:         "Summer Fest",
		OrgLegalName:      "ООО Организатор",
		OrgName:           "Организатор",
		VenueID:           venueID,
		VenueName:         "Arena Hall",
		CityID:            nil,
		CityName:          strPtr("Moscow"),
		SeatSystemID:      1001, // GA: same as systemTicketID
		BarcodeStr:        nil,
		TierName:          strPtr("Standard"),
		TierPrice:         i64Ptr(1500),
		SoldPrice:         1500,
		PromoCodeName:     nil,
		VenueTimezone:     nil,
	}
}

func TestBuildExport_Empty(t *testing.T) {
	result := buildExport(nil)
	if result == nil {
		t.Fatal("expected non-nil Export for empty input")
	}
	if len(result) != 0 {
		t.Fatalf("expected 0 orders, got %d", len(result))
	}
}

func TestBuildExport_SingleActiveTicket(t *testing.T) {
	row := baseRow()
	export := buildExport([]orderexport.Row{row})

	if len(export) != 1 {
		t.Fatalf("expected 1 order, got %d", len(export))
	}
	o := export[0]

	if len(o.TicketList) != 1 {
		t.Fatalf("expected 1 ticket, got %d", len(o.TicketList))
	}
	tk := o.TicketList[0]

	if tk.HolderStatus != StatusNotUsed {
		t.Errorf("expected holderStatus=%d for active ticket, got %d", StatusNotUsed, tk.HolderStatus)
	}
	if tk.Price != 1500 {
		t.Errorf("expected price=1500, got %d", tk.Price)
	}
	if tk.SeatID != row.SeatSystemID {
		t.Errorf("GA ticket: expected seatId=%d (seatSystemID), got %d", row.SeatSystemID, tk.SeatID)
	}
	if tk.Category != "Standard" {
		t.Errorf("expected category=Standard, got %q", tk.Category)
	}
	if tk.Tariff != "Standard" {
		t.Errorf("expected tariff=Standard, got %q", tk.Tariff)
	}
	if tk.DiscountReason != "" {
		t.Errorf("expected empty discountReason, got %q", tk.DiscountReason)
	}
	if tk.ActionEvent.CityName != "Moscow" {
		t.Errorf("expected cityName=Moscow, got %q", tk.ActionEvent.CityName)
	}
	if tk.ActionEvent.VenueName != "Arena Hall" {
		t.Errorf("expected venueName=Arena Hall, got %q", tk.ActionEvent.VenueName)
	}
	if tk.ActionEvent.ActionName != "Summer Fest" {
		t.Errorf("expected actionName=Summer Fest, got %q", tk.ActionEvent.ActionName)
	}
	if tk.ActionEvent.ActionLegalOwner != "ООО Организатор" {
		t.Errorf("expected actionLegalOwner=ООО Организатор, got %q", tk.ActionEvent.ActionLegalOwner)
	}
	// ShowTime must be local-time without TZ suffix.
	if tk.ActionEvent.ShowTime != "2026-08-22T20:00:00" {
		t.Errorf("expected showTime=2026-08-22T20:00:00, got %q", tk.ActionEvent.ShowTime)
	}
	if o.Currency != "RUB" {
		t.Errorf("expected currency=RUB, got %q", o.Currency)
	}
}

func TestBuildExport_CancelledTicket(t *testing.T) {
	row := baseRow()
	row.TicketStatus = "cancelled"
	refundT := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	row.RefundDate = &refundT
	row.RefundPrice = i64Ptr(1500)

	export := buildExport([]orderexport.Row{row})
	if len(export) != 1 || len(export[0].TicketList) != 1 {
		t.Fatal("expected 1 order with 1 ticket")
	}
	tk := export[0].TicketList[0]
	if tk.HolderStatus != StatusRefunded {
		t.Errorf("expected holderStatus=%d for cancelled ticket, got %d", StatusRefunded, tk.HolderStatus)
	}
	if tk.RefundDate == nil {
		t.Error("expected RefundDate to be set")
	}
	if tk.RefundPrice == nil || *tk.RefundPrice != 1500 {
		t.Error("expected RefundPrice=1500")
	}
}

func TestBuildExport_OrderIDIsMinSystemTicketID(t *testing.T) {
	row1 := baseRow()
	row1.SystemTicketID = 2002
	row2 := baseRow()
	row2.SystemTicketID = 1001
	row2.Ordinal = 2

	export := buildExport([]orderexport.Row{row1, row2})
	if len(export) != 1 {
		t.Fatalf("expected 1 order (same checkout session), got %d", len(export))
	}
	o := export[0]
	if o.ID != 1001 {
		t.Errorf("expected order.id=1001 (min system_ticket_id), got %d", o.ID)
	}
	// All tickets in the order should have the same OrderID.
	for _, tk := range o.TicketList {
		if tk.OrderID != 1001 {
			t.Errorf("expected ticket.orderId=1001, got %d", tk.OrderID)
		}
	}
}

func TestBuildExport_TwoOrders(t *testing.T) {
	row1 := baseRow()
	row1.CheckoutSessionID = uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001")
	row1.SystemTicketID = 1001

	row2 := baseRow()
	row2.CheckoutSessionID = uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000002")
	row2.SystemTicketID = 2001

	export := buildExport([]orderexport.Row{row1, row2})
	if len(export) != 2 {
		t.Fatalf("expected 2 orders, got %d", len(export))
	}
}

func TestBuildExport_PromoCodeName(t *testing.T) {
	row := baseRow()
	row.PromoCodeName = strPtr("SUMMER25")

	export := buildExport([]orderexport.Row{row})
	if len(export) != 1 || len(export[0].TicketList) != 1 {
		t.Fatal("expected 1 order with 1 ticket")
	}
	tk := export[0].TicketList[0]
	if tk.DiscountReason != "Промокод SUMMER25" {
		t.Errorf("expected discountReason=%q, got %q", "Промокод SUMMER25", tk.DiscountReason)
	}
	o := export[0]
	if o.DiscountReason != "Промокод SUMMER25" {
		t.Errorf("expected order.discountReason=%q, got %q", "Промокод SUMMER25", o.DiscountReason)
	}
}

func TestBuildExport_VenueTimezone(t *testing.T) {
	row := baseRow()
	// sessionStartAt is 20:00 UTC; in Europe/Moscow (UTC+3) it becomes 23:00.
	row.VenueTimezone = strPtr("Europe/Moscow")

	export := buildExport([]orderexport.Row{row})
	if len(export) != 1 || len(export[0].TicketList) != 1 {
		t.Fatal("expected 1 order with 1 ticket")
	}
	tk := export[0].TicketList[0]
	if tk.ActionEvent.ShowTime != "2026-08-22T23:00:00" {
		t.Errorf("expected showTime=2026-08-22T23:00:00 (Moscow), got %q", tk.ActionEvent.ShowTime)
	}
}

func TestBuildExport_SoldPrice(t *testing.T) {
	row := baseRow()
	// soldPrice from reservation GA item differs from tier price.
	row.TierPrice = i64Ptr(1500)
	row.SoldPrice = 1200 // discounted via promo

	export := buildExport([]orderexport.Row{row})
	tk := export[0].TicketList[0]
	if tk.Price != 1200 {
		t.Errorf("expected price=1200 (soldPrice), got %d", tk.Price)
	}
}

func TestBuildExport_BarcodeFromCredential(t *testing.T) {
	row := baseRow()
	row.BarcodeStr = strPtr("1234567890123")

	export := buildExport([]orderexport.Row{row})
	tk := export[0].TicketList[0]
	if tk.Barcode != "1234567890123" {
		t.Errorf("expected barcode from credential, got %q", tk.Barcode)
	}
}

func TestBuildExport_ProrationRemainder_LastTicketAbsorbsRounding(t *testing.T) {
	// 3 tickets at 1000 each; order_subtotal=3000, order_discount=100.
	// Integer division: 1000*100/3000 = 33 (truncates). First two tickets get 33;
	// last ticket gets 100-66=34. sum = 33+33+34 = 100 = order_discount. ✓
	csID := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-000000000099")
	row := baseRow()
	row.CheckoutSessionID = csID
	row.OrderSubtotal = 3000
	row.OrderDiscount = 100
	row.OrderTotal = 2900
	row.SoldPrice = 1000

	row1 := row
	row1.SystemTicketID = 5001
	row1.SeatSystemID = 5001
	row1.Ordinal = 1

	row2 := row
	row2.SystemTicketID = 5002
	row2.SeatSystemID = 5002
	row2.Ordinal = 2

	row3 := row
	row3.SystemTicketID = 5003
	row3.SeatSystemID = 5003
	row3.Ordinal = 3

	export := buildExport([]orderexport.Row{row1, row2, row3})
	if len(export) != 1 {
		t.Fatalf("expected 1 order, got %d", len(export))
	}
	o := export[0]
	if len(o.TicketList) != 3 {
		t.Fatalf("expected 3 tickets, got %d", len(o.TicketList))
	}

	var totalDiscount int64
	for _, tk := range o.TicketList {
		totalDiscount += tk.Discount
	}
	if totalDiscount != 100 {
		t.Errorf("sum of ticket discounts = %d, want 100 (must equal order discount exactly)", totalDiscount)
	}

	// First two tickets get floor(1000*100/3000) = 33.
	if o.TicketList[0].Discount != 33 {
		t.Errorf("ticket[0].discount = %d, want 33", o.TicketList[0].Discount)
	}
	if o.TicketList[1].Discount != 33 {
		t.Errorf("ticket[1].discount = %d, want 33", o.TicketList[1].Discount)
	}
	// Last ticket absorbs remainder: 100 - 33 - 33 = 34.
	if o.TicketList[2].Discount != 34 {
		t.Errorf("ticket[2].discount = %d, want 34 (absorbs remainder)", o.TicketList[2].Discount)
	}

	// Charge = price - discount for each ticket.
	for i, tk := range o.TicketList {
		if tk.Charge != tk.Price-tk.Discount {
			t.Errorf("ticket[%d].charge = %d, want %d (price-discount)", i, tk.Charge, tk.Price-tk.Discount)
		}
		if tk.TotalPrice != tk.Charge {
			t.Errorf("ticket[%d].totalPrice = %d, want == charge (%d)", i, tk.TotalPrice, tk.Charge)
		}
	}
}

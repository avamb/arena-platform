// export_unit_test.go — white-box unit tests for buildExport.
// Package macs (internal) so buildExport is directly accessible.
package macs

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// helpers

func strPtr(s string) *string { return &s }
func i64Ptr(n int64) *int64   { return &n }

// baseRow returns a minimal valid exportRow for a single active GA ticket.
func baseRow() exportRow {
	eventID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	sessionID := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	ticketID := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	csID := uuid.MustParse("00000000-0000-0000-0000-000000000004")
	venueID := uuid.MustParse("00000000-0000-0000-0000-000000000005")
	_ = sessionID // not in exportRow
	return exportRow{
		ticketID:          ticketID,
		systemTicketID:    1001,
		checkoutSessionID: csID,
		tierID:            nil,
		holderEmail:       strPtr("buyer@example.com"),
		ticketStatus:      "active",
		issuedAt:          nil,
		seatKey:           nil, // GA
		seatSector:        nil,
		seatRow:           nil,
		seatNumber:        nil,
		ordinal:           1,
		cancelledAt:       nil,
		refundDate:        nil,
		refundPrice:       nil,
		orderTotal:        1500,
		orderSubtotal:     1500,
		orderDiscount:     0,
		orderCurrency:     "RUB",
		paymentProvider:   strPtr("yookassa"),
		orderCompletedAt:  time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC),
		orderUserID:       nil,
		sessionStartAt:    time.Date(2026, 8, 22, 20, 0, 0, 0, time.UTC),
		eventID:           eventID,
		eventName:         "Summer Fest",
		orgLegalName:      "ООО Организатор",
		orgName:           "Организатор",
		venueID:           venueID,
		venueName:         "Arena Hall",
		cityID:            nil,
		cityName:          strPtr("Moscow"),
		seatSystemID:      1001, // GA: same as systemTicketID
		barcodeStr:        nil,
		tierName:          strPtr("Standard"),
		tierPrice:         i64Ptr(1500),
		soldPrice:         1500,
		promoCodeName:     nil,
		venueTimezone:     nil,
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
	export := buildExport([]exportRow{row})

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
	if tk.SeatID != row.seatSystemID {
		t.Errorf("GA ticket: expected seatId=%d (seatSystemID), got %d", row.seatSystemID, tk.SeatID)
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
	row.ticketStatus = "cancelled"
	refundT := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	row.refundDate = &refundT
	row.refundPrice = i64Ptr(1500)

	export := buildExport([]exportRow{row})
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
	row1.systemTicketID = 2002
	row2 := baseRow()
	row2.systemTicketID = 1001
	row2.ordinal = 2

	export := buildExport([]exportRow{row1, row2})
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
	row1.checkoutSessionID = uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001")
	row1.systemTicketID = 1001

	row2 := baseRow()
	row2.checkoutSessionID = uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000002")
	row2.systemTicketID = 2001

	export := buildExport([]exportRow{row1, row2})
	if len(export) != 2 {
		t.Fatalf("expected 2 orders, got %d", len(export))
	}
}

func TestBuildExport_PromoCodeName(t *testing.T) {
	row := baseRow()
	row.promoCodeName = strPtr("SUMMER25")

	export := buildExport([]exportRow{row})
	if len(export) != 1 || len(export[0].TicketList) != 1 {
		t.Fatal("expected 1 order with 1 ticket")
	}
	tk := export[0].TicketList[0]
	if tk.DiscountReason != "SUMMER25" {
		t.Errorf("expected discountReason=SUMMER25, got %q", tk.DiscountReason)
	}
}

func TestBuildExport_VenueTimezone(t *testing.T) {
	row := baseRow()
	// sessionStartAt is 20:00 UTC; in Europe/Moscow (UTC+3) it becomes 23:00.
	row.venueTimezone = strPtr("Europe/Moscow")

	export := buildExport([]exportRow{row})
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
	row.tierPrice = i64Ptr(1500)
	row.soldPrice = 1200 // discounted via promo

	export := buildExport([]exportRow{row})
	tk := export[0].TicketList[0]
	if tk.Price != 1200 {
		t.Errorf("expected price=1200 (soldPrice), got %d", tk.Price)
	}
}

func TestBuildExport_BarcodeFromCredential(t *testing.T) {
	row := baseRow()
	row.barcodeStr = strPtr("1234567890123")

	export := buildExport([]exportRow{row})
	tk := export[0].TicketList[0]
	if tk.Barcode != "1234567890123" {
		t.Errorf("expected barcode from credential, got %q", tk.Barcode)
	}
}

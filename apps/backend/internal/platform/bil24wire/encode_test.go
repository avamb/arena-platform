// encode_test.go — unit tests of the Bil24 encoder (feature #505, W1-B7b).
//
// These tests own the VALUE semantics of spec §9.3 (string holderStatus,
// float major-unit money, naive showTime, prorated charge, promo-only
// discountReason). The KEY SETS are owned by the BINDING test in
// apps/backend/tests/compat/bil24, which diffs the encoder output against
// the inventories of 68 real Bil24 exports.
package bil24wire

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/orderexport"
)

var (
	testSessionID = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	testEventID   = uuid.MustParse("22222222-2222-4222-8222-222222222222")
	testVenueID   = uuid.MustParse("33333333-3333-4333-8333-333333333333")
	testCityID    = uuid.MustParse("44444444-4444-4444-8444-444444444444")
)

// testContext is the encode context of a fully mapped Lampyris-style channel.
func testContext() EncodeContext {
	return EncodeContext{
		Agent:          Agent{ID: 7, Name: "Lampyris Events s.r.o."},
		Frontend:       Frontend{ID: 3, AgentID: 7, Name: "https://lampyrisevents.com/"},
		UserID:         1000000100,
		Email:          "a@b.cz",
		Phone:          "+420111222333",
		FullName:       "Jan Novák",
		ActionEventIDs: map[uuid.UUID]int64{testSessionID: 1000000011},
		ActionIDs:      map[uuid.UUID]int64{testEventID: 1000000010},
		VenueIDs:       map[uuid.UUID]int64{testVenueID: 1000000003},
		CityIDs:        map[uuid.UUID]int64{testCityID: 1000000002},
	}
}

// testEvent is the denormalized session context shared by the fixtures.
func testEvent() orderexport.Event {
	cityID := testCityID
	return orderexport.Event{
		EventID:        testEventID,
		SessionID:      testSessionID,
		EventName:      "Koncert",
		OrgLegalName:   "Lampyris Events s.r.o.",
		OrgName:        "Lampyris",
		VenueID:        testVenueID,
		VenueName:      "Palác Akropolis",
		CityID:         &cityID,
		CityName:       "Praha",
		Currency:       "CZK",
		SessionStartAt: time.Date(2026, 4, 26, 17, 0, 0, 0, time.UTC),
		ShowTimeLocal:  "2026-04-26T19:00:00",
	}
}

// twoSeatedTickets is a 2-ticket seated order: subtotal 2500, no discount,
// total 2625 → a 125-minor service charge to prorate.
func twoSeatedTickets() orderexport.Order {
	return orderexport.Order{
		ID:          1000000500,
		CompletedAt: time.Date(2026, 4, 16, 12, 23, 55, 0, time.UTC),
		Currency:    "CZK",
		Subtotal:    2500,
		Discount:    0,
		Total:       2625,
		Tickets: []orderexport.Ticket{
			{
				ID: 4021, SeatID: 1731, OrderID: 1000000500, Seated: true,
				Seat:     orderexport.SeatLocation{Sector: "Parter", Row: "3", Number: "12"},
				TierName: "Parter", Price: 1500, Charge: 1500, TotalPrice: 1500,
				Barcode: "2100000040218", PlatformStatus: "active", Event: testEvent(),
			},
			{
				ID: 4022, SeatID: 1732, OrderID: 1000000500, Seated: true,
				Seat:     orderexport.SeatLocation{Sector: "Parter", Row: "3", Number: "13"},
				TierName: "Parter", Price: 1000, Charge: 1000, TotalPrice: 1000,
				Barcode: "2100000040225", PlatformStatus: "active", Event: testEvent(),
			},
		},
	}
}

func TestEncodeOrder_OrderHeaderValues(t *testing.T) {
	got := EncodeOrder(twoSeatedTickets(), testContext())

	if got.ID != 1000000500 {
		t.Errorf("id = %d, want 1000000500", got.ID)
	}
	if got.Status != StatusPaid {
		t.Errorf("status = %q, want %q", got.Status, StatusPaid)
	}
	if got.Date != "2026-04-16T12:23:55Z" {
		t.Errorf("date = %q, want RFC3339 with offset", got.Date)
	}
	// Money is float MAJOR units: 2500 minor → 25.
	if got.Sum != 25 || got.Discount != 0 || got.Charge != 1.25 || got.TotalSum != 26.25 {
		t.Errorf("money = sum %v discount %v charge %v total %v; want 25/0/1.25/26.25",
			got.Sum, got.Discount, got.Charge, got.TotalSum)
	}
	// The filtered twins mirror their counterparts (one frontend per order).
	if got.FilteredSum != got.Sum || got.FilteredDiscount != got.Discount ||
		got.FilteredCharge != got.Charge || got.FilteredTotalSum != got.TotalSum ||
		got.FilteredTicketQuantity != got.TicketQuantity {
		t.Errorf("filtered totals diverge from unfiltered: %+v", got)
	}
	if got.TicketQuantity != 2 {
		t.Errorf("ticketQuantity = %d, want 2", got.TicketQuantity)
	}
	if got.Frontend.Type.ID != 8 || got.Frontend.Type.Name != "Ticketing system" {
		t.Errorf("frontend.type = %+v, want the ticketing-system default", got.Frontend.Type)
	}
	if got.PaymentBankMessage != defaultPaymentBankMessage {
		t.Errorf("paymentBankMessage = %q, want the default", got.PaymentBankMessage)
	}
	if got.Email == nil || *got.Email != "a@b.cz" || got.FullName == nil || got.Phone == nil {
		t.Errorf("buyer block not carried: %+v", got)
	}
	if got.EmailSent != nil || got.PaymentRRN != nil || got.PaymentCardPAN != nil {
		t.Errorf("unknown payment/e-mail fields must be null, got %+v", got)
	}
	if got.SeatList == nil || len(got.SeatList) != 0 ||
		got.GatewayOrderList == nil || len(got.GatewayOrderList) != 0 {
		t.Errorf("seatList/gatewayOrderList must be EMPTY ARRAYS, not null")
	}
}

func TestEncodeOrder_TicketValuesAndActionEvent(t *testing.T) {
	got := EncodeOrder(twoSeatedTickets(), testContext())
	if len(got.TicketList) != 2 {
		t.Fatalf("ticketList len = %d, want 2", len(got.TicketList))
	}
	tk := got.TicketList[0]

	if tk.HolderStatus != HolderStatusNeverUse {
		t.Errorf("holderStatus = %q, want %q", tk.HolderStatus, HolderStatusNeverUse)
	}
	if tk.SeatLocation == nil || tk.SeatLocation.Sector != "Parter" || tk.SeatLocation.Number != "12" {
		t.Errorf("seatLocation = %+v, want the seat triple", tk.SeatLocation)
	}
	if tk.Category != "Parter" {
		t.Errorf("category = %q, want the tier name as a STRING", tk.Category)
	}
	if tk.Tariff != nil || tk.DiscountReason != nil || tk.RefundDate != nil || tk.RefundPrice != nil {
		t.Errorf("unset ticket fields must be null: %+v", tk)
	}
	if tk.BarcodeFormat.ID != 0 || tk.BarcodeFormat.Name != "EAN-13" {
		t.Errorf("barcodeFormat = %+v, want {0, EAN-13}", tk.BarcodeFormat)
	}
	// price 1500 minor → 15; charge = 125 * 1500/2500 = 75 minor → 0.75.
	if tk.Price != 15 || tk.Charge != 0.75 || tk.TotalPrice != 15.75 {
		t.Errorf("ticket money = %v/%v/%v, want 15/0.75/15.75", tk.Price, tk.Charge, tk.TotalPrice)
	}

	ev := tk.ActionEvent
	if ev.ID != 1000000011 {
		t.Errorf("actionEvent.id = %d, want the SESSION actionEventId 1000000011", ev.ID)
	}
	if ev.ActionID != 1000000010 || ev.VenueID != 1000000003 || ev.CityID != 1000000002 {
		t.Errorf("actionEvent ids = %+v, want the compat ids from the context", ev)
	}
	if ev.ShowTime != "2026-04-26T19:00:00" {
		t.Errorf("showTime = %q, want naive venue-local wall clock", ev.ShowTime)
	}
	if !ev.ETickets || ev.ActionKind.Name != "Events" || ev.Gateway.SystemName != "NONE" {
		t.Errorf("actionEvent constants wrong: %+v", ev)
	}
	if ev.Gateway.OrganizerID != nil || ev.Gateway.OrganizerName != nil {
		t.Errorf("gateway organizer fields must be null: %+v", ev.Gateway)
	}
	if ev.ActionLegalOwner != "Lampyris Events s.r.o." || ev.ActionLegalOwnerInn != "" {
		t.Errorf("legal owner = %q/%q", ev.ActionLegalOwner, ev.ActionLegalOwnerInn)
	}
}

func TestEncodeOrder_ChargeProrationSumsExactly(t *testing.T) {
	// 3 tickets of 1000 minor each and a 100-minor charge: 33/33/34.
	o := orderexport.Order{
		ID: 1, Currency: "CZK", Subtotal: 3000, Total: 3100,
		CompletedAt: time.Now(),
	}
	for i := 0; i < 3; i++ {
		o.Tickets = append(o.Tickets, orderexport.Ticket{
			ID: int64(i + 1), Price: 1000, PlatformStatus: "active", Event: testEvent(),
		})
	}
	got := EncodeOrder(o, testContext())

	var sum float64
	for _, tk := range got.TicketList {
		sum += tk.Charge
	}
	if sum != got.Charge {
		t.Errorf("per-ticket charges sum to %v, order charge is %v", sum, got.Charge)
	}
	if got.TicketList[0].Charge != 0.33 || got.TicketList[2].Charge != 0.34 {
		t.Errorf("remainder must land on the LAST ticket, got %v/%v/%v",
			got.TicketList[0].Charge, got.TicketList[1].Charge, got.TicketList[2].Charge)
	}
}

func TestEncodeOrder_ZeroPricedTicketsSplitChargeEvenly(t *testing.T) {
	// A comp order with no prices still has to distribute a charge without
	// dividing by zero.
	o := orderexport.Order{
		ID: 1, Currency: "CZK", Subtotal: 0, Total: 90, CompletedAt: time.Now(),
		Tickets: []orderexport.Ticket{
			{ID: 1, PlatformStatus: "active", Event: testEvent()},
			{ID: 2, PlatformStatus: "active", Event: testEvent()},
		},
	}
	got := EncodeOrder(o, testContext())
	if got.TicketList[0].Charge != 0.45 || got.TicketList[1].Charge != 0.45 {
		t.Errorf("even split expected, got %v/%v", got.TicketList[0].Charge, got.TicketList[1].Charge)
	}
}

func TestEncodeOrder_NegativeDerivedChargeClampedToZero(t *testing.T) {
	o := twoSeatedTickets()
	o.Total = 100 // below subtotal - discount: a pricing defect, not a fee
	got := EncodeOrder(o, testContext())
	if got.Charge != 0 {
		t.Errorf("charge = %v, want 0 (never negative)", got.Charge)
	}
}

func TestEncodeOrder_ExplicitChargeOverridesDerivation(t *testing.T) {
	o := twoSeatedTickets()
	charge := int64(500)
	ec := testContext()
	ec.Charge = &charge
	got := EncodeOrder(o, ec)
	if got.Charge != 5 || got.TotalSum != 30 {
		t.Errorf("charge/total = %v/%v, want 5/30", got.Charge, got.TotalSum)
	}
}

func TestEncodeOrder_GeneralAdmissionSeatLocationIsNull(t *testing.T) {
	o := twoSeatedTickets()
	o.Tickets[0].Seated = false
	o.Tickets[0].Seat = orderexport.SeatLocation{}
	got := EncodeOrder(o, testContext())
	if got.TicketList[0].SeatLocation != nil {
		t.Errorf("GA seatLocation = %+v, want null", got.TicketList[0].SeatLocation)
	}

	raw, err := json.Marshal(got.TicketList[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(m["seatLocation"]) != "null" {
		t.Errorf("seatLocation JSON = %s, want null", m["seatLocation"])
	}
}

func TestEncodeOrder_DiscountReasonOnlyForPromoCodes(t *testing.T) {
	cases := map[string]*string{
		"Промокод SPRING10": strPtr("Промокод SPRING10"),
		"Внешняя система":   nil, // internal bookkeeping, never exported
		"":                  nil,
	}
	for reason, want := range cases {
		o := twoSeatedTickets()
		o.Tickets[0].DiscountReason = reason
		got := EncodeOrder(o, testContext()).TicketList[0].DiscountReason
		switch {
		case want == nil && got != nil:
			t.Errorf("reason %q → %q, want null", reason, *got)
		case want != nil && (got == nil || *got != *want):
			t.Errorf("reason %q → %v, want %q", reason, got, *want)
		}
	}
}

func TestEncodeOrder_HolderStatusRefundForTerminalTickets(t *testing.T) {
	for _, status := range []string{"cancelled", "revoked", "transferred"} {
		o := twoSeatedTickets()
		o.Tickets[0].PlatformStatus = status
		got := EncodeOrder(o, testContext()).TicketList[0].HolderStatus
		if got != HolderStatusRefund {
			t.Errorf("platform status %q → %q, want %q", status, got, HolderStatusRefund)
		}
	}
}

func TestEncodeOrder_RefundedTicketCarriesDateAndPrice(t *testing.T) {
	when := time.Date(2026, 4, 15, 16, 15, 42, 0, time.FixedZone("CEST", 2*3600))
	price := int64(1400)
	o := twoSeatedTickets()
	o.Tickets[0].PlatformStatus = "cancelled"
	o.Tickets[0].RefundDate = &when
	o.Tickets[0].RefundPrice = &price

	tk := EncodeOrder(o, testContext()).TicketList[0]
	if tk.RefundDate == nil || *tk.RefundDate != "2026-04-15T16:15:42+02:00" {
		t.Errorf("refundDate = %v, want RFC3339 WITH offset", tk.RefundDate)
	}
	if tk.RefundPrice == nil || *tk.RefundPrice != 14 {
		t.Errorf("refundPrice = %v, want 14 major units", tk.RefundPrice)
	}
}

func TestEncodeOrderHeader_OmitsTicketList(t *testing.T) {
	full := EncodeOrder(twoSeatedTickets(), testContext())
	header := EncodeOrderHeader(twoSeatedTickets(), testContext())

	if header.TicketList != nil {
		t.Errorf("header must not carry ticketList: %+v", header.TicketList)
	}
	raw, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := m["ticketList"]; present {
		t.Errorf("ticketList key must be ABSENT in the GET_ORDER_INFO header")
	}
	// Every other field must be identical to the full encoding, otherwise
	// the two surfaces drift.
	full.TicketList = nil
	if !reflect.DeepEqual(full, header) {
		t.Errorf("header differs from the full order beyond ticketList:\n%+v\n%+v", header, full)
	}
	// ticketQuantity still reports the real count.
	if header.TicketQuantity != 2 {
		t.Errorf("ticketQuantity = %d, want 2 even without the list", header.TicketQuantity)
	}
}

func TestEncodeTicketRefunded_Shape(t *testing.T) {
	when := time.Date(2026, 4, 15, 19, 23, 37, 0, time.UTC)
	price := int64(2300)
	tk := twoSeatedTickets().Tickets[0]
	tk.PlatformStatus = "active" // the EVENT asserts the refund, not the row
	tk.RefundDate = &when
	tk.RefundPrice = &price

	got := EncodeTicketRefunded(tk, testContext())
	if got.HolderStatus != HolderStatusRefund {
		t.Errorf("holderStatus = %q, want REFUND unconditionally", got.HolderStatus)
	}
	if got.ID != 4021 || got.OrderID != 1000000500 || got.SeatID != 1731 {
		t.Errorf("ids = %+v", got)
	}
	if got.Barcode != "2100000040218" || got.Category != "Parter" {
		t.Errorf("barcode/category = %q/%q", got.Barcode, got.Category)
	}
	if got.RefundPrice == nil || *got.RefundPrice != 23 {
		t.Errorf("refundPrice = %v, want 23 major units", got.RefundPrice)
	}
	if got.ActionEvent.ID != 1000000011 {
		t.Errorf("actionEvent.id = %d, want the session compat id", got.ActionEvent.ID)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Spec §9.2 inventory for ticket.refunded.
	want := []string{"id", "orderId", "seatId", "barcode", "refundPrice",
		"refundDate", "category", "holderStatus", "actionEvent"}
	if len(m) != len(want) {
		t.Errorf("ticket.refunded has %d keys, want %d: %v", len(m), len(want), m)
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Errorf("ticket.refunded is missing key %q", k)
		}
	}
}

func TestEncodeOrder_UnmappedCatalogIdsDegradeToZero(t *testing.T) {
	// An unmapped city/venue/session must not panic and must not fabricate
	// an id — a degraded payload beats a lost webhook.
	got := EncodeOrder(twoSeatedTickets(), EncodeContext{})
	ev := got.TicketList[0].ActionEvent
	if ev.ID != 0 || ev.ActionID != 0 || ev.VenueID != 0 || ev.CityID != 0 {
		t.Errorf("unmapped ids = %+v, want zeros", ev)
	}
}

func strPtr(s string) *string { return &s }

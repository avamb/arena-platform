// build_test.go — unit tests for the NEUTRAL projection (W1-B7a, feature #504).
//
// These assert the projection itself, in its own vocabulary: no MACS integer
// status codes, no camelCase JSON. The MACS golden test (macs package) keeps
// guarding the encoder on top of it; this file guards the arithmetic that
// both MACS and the Bil24 wire share.
package orderexport

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

func sp(s string) *string { return &s }
func ip(i int64) *int64   { return &i }

// baseRow is one seated, tiered, promo-free ticket of a 3000/-300 order.
func baseRow() Row {
	return Row{
		TicketID:          uuid.MustParse("00000000-0000-0000-0000-000000000003"),
		SystemTicketID:    1001,
		CheckoutSessionID: uuid.MustParse("00000000-0000-0000-0000-000000000004"),
		HolderEmail:       sp("buyer@example.com"),
		TicketStatus:      "active",
		SeatKey:           sp("A-1-1"),
		SeatSector:        sp("A"),
		SeatRow:           sp("1"),
		SeatNumber:        sp("1"),
		Ordinal:           1,
		OrderTotal:        2700,
		OrderSubtotal:     3000,
		OrderDiscount:     300,
		OrderCurrency:     "RUB",
		PaymentProvider:   sp("yookassa"),
		OrderCompletedAt:  time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC),
		SessionStartAt:    time.Date(2026, 8, 22, 20, 0, 0, 0, time.UTC),
		EventID:           uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		EventName:         "Summer Fest",
		OrgLegalName:      "ООО Организатор",
		OrgName:           "Организатор",
		VenueID:           uuid.MustParse("00000000-0000-0000-0000-000000000005"),
		VenueName:         "Arena Hall",
		CityName:          sp("Moscow"),
		SeatSystemID:      55,
		TierName:          sp("Standard"),
		TierPrice:         ip(1000),
		SoldPrice:         1000,
	}
}

func TestBuild_EmptyInputIsEmptyNonNil(t *testing.T) {
	got := Build(nil)
	if got == nil {
		t.Fatal("Build(nil) = nil, want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("Build(nil) has %d orders, want 0", len(got))
	}
}

func TestBuild_OrderIDIsMinSystemTicketID(t *testing.T) {
	r1, r2, r3 := baseRow(), baseRow(), baseRow()
	r1.SystemTicketID, r1.Ordinal = 1007, 1
	r2.SystemTicketID, r2.Ordinal = 1002, 2
	r3.SystemTicketID, r3.Ordinal = 1005, 3

	orders := Build([]Row{r1, r2, r3})
	if len(orders) != 1 {
		t.Fatalf("got %d orders, want 1", len(orders))
	}
	o := orders[0]
	if o.ID != 1002 {
		t.Errorf("order.ID = %d, want 1002 (min system_ticket_id)", o.ID)
	}
	// Every ticket back-references the FINAL order id, not the id that was
	// current when it was appended.
	for i, tk := range o.Tickets {
		if tk.OrderID != 1002 {
			t.Errorf("ticket[%d].OrderID = %d, want 1002", i, tk.OrderID)
		}
	}
	if o.TicketQuantity() != 3 {
		t.Errorf("TicketQuantity() = %d, want 3", o.TicketQuantity())
	}
}

func TestBuild_GroupsByCheckoutSession(t *testing.T) {
	r1 := baseRow()
	r2 := baseRow()
	r2.CheckoutSessionID = uuid.MustParse("00000000-0000-0000-0000-0000000000ff")
	r2.SystemTicketID = 2001

	orders := Build([]Row{r1, r2})
	if len(orders) != 2 {
		t.Fatalf("got %d orders, want 2", len(orders))
	}
	if orders[0].ID != 1001 || orders[1].ID != 2001 {
		t.Errorf("order ids = %d,%d want 1001,2001", orders[0].ID, orders[1].ID)
	}
}

// Proration must sum EXACTLY to the order discount; the last ticket absorbs
// the rounding remainder (AB-50i).
func TestBuild_DiscountProrationRemainder(t *testing.T) {
	r1, r2, r3 := baseRow(), baseRow(), baseRow()
	r1.SystemTicketID, r1.Ordinal = 1001, 1
	r2.SystemTicketID, r2.Ordinal = 1002, 2
	r3.SystemTicketID, r3.Ordinal = 1003, 3
	// 3 x 1000, discount 100 → 33 / 33 / 34.
	for _, r := range []*Row{&r1, &r2, &r3} {
		r.OrderSubtotal = 3000
		r.OrderDiscount = 100
		r.OrderTotal = 2900
	}

	orders := Build([]Row{r1, r2, r3})
	tickets := orders[0].Tickets
	want := []int64{33, 33, 34}
	var sum int64
	for i, tk := range tickets {
		if tk.Discount != want[i] {
			t.Errorf("ticket[%d].Discount = %d, want %d", i, tk.Discount, want[i])
		}
		if tk.Charge != tk.Price-tk.Discount {
			t.Errorf("ticket[%d].Charge = %d, want %d", i, tk.Charge, tk.Price-tk.Discount)
		}
		if tk.TotalPrice != tk.Charge {
			t.Errorf("ticket[%d].TotalPrice = %d, want == Charge %d", i, tk.TotalPrice, tk.Charge)
		}
		sum += tk.Discount
	}
	if sum != 100 {
		t.Errorf("sum of prorated discounts = %d, want exactly 100", sum)
	}
}

func TestBuild_NoDiscountLeavesFullPrice(t *testing.T) {
	r := baseRow()
	r.OrderDiscount = 0
	r.OrderTotal = 3000

	tk := Build([]Row{r})[0].Tickets[0]
	if tk.Discount != 0 {
		t.Errorf("Discount = %d, want 0", tk.Discount)
	}
	if tk.Charge != 1000 || tk.TotalPrice != 1000 {
		t.Errorf("Charge/TotalPrice = %d/%d, want 1000/1000", tk.Charge, tk.TotalPrice)
	}
}

// A GA ticket with no tier reports sold_price 0; the projection must fall
// back to an even split of the subtotal so no paid order exports a free
// ticket.
func TestBuild_GAZeroPriceFallsBackToSubtotalSplit(t *testing.T) {
	r1, r2 := baseRow(), baseRow()
	for _, r := range []*Row{&r1, &r2} {
		r.SeatKey = nil
		r.SeatSector, r.SeatRow, r.SeatNumber = nil, nil, nil
		r.TierName, r.TierPrice = nil, nil
		r.SoldPrice = 0
		r.OrderSubtotal = 2000
		r.OrderDiscount = 0
		r.OrderTotal = 2000
	}
	r1.SystemTicketID, r1.Ordinal = 1001, 1
	r2.SystemTicketID, r2.Ordinal = 1002, 2

	o := Build([]Row{r1, r2})[0]
	for i, tk := range o.Tickets {
		if tk.Price != 1000 {
			t.Errorf("ticket[%d].Price = %d, want 1000 (subtotal/2)", i, tk.Price)
		}
		if tk.Seated {
			t.Errorf("ticket[%d].Seated = true, want false for GA", i)
		}
		if tk.Seat != (SeatLocation{}) {
			t.Errorf("ticket[%d].Seat = %+v, want zero for GA", i, tk.Seat)
		}
	}
}

func TestBuild_SeatedFlagAndSeatLocation(t *testing.T) {
	tk := Build([]Row{baseRow()})[0].Tickets[0]
	if !tk.Seated {
		t.Error("Seated = false, want true when seat_key is present")
	}
	want := SeatLocation{Sector: "A", Row: "1", Number: "1"}
	if tk.Seat != want {
		t.Errorf("Seat = %+v, want %+v", tk.Seat, want)
	}
	if tk.SeatID != 55 {
		t.Errorf("SeatID = %d, want 55 (system_seat_id)", tk.SeatID)
	}
}

func TestBuild_DiscountReasonVariants(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Row)
		expected string
	}{
		{"promo code", func(r *Row) { r.PromoCodeName = sp("SUMMER10") }, "Промокод SUMMER10"},
		{"no payment provider", func(r *Row) { r.PaymentProvider = nil }, "Внешняя система"},
		{"ordinary paid order", func(_ *Row) {}, ""},
		{"promo wins over missing provider", func(r *Row) {
			r.PromoCodeName = sp("COMP")
			r.PaymentProvider = nil
		}, "Промокод COMP"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := baseRow()
			tc.mutate(&r)
			o := Build([]Row{r})[0]
			if o.DiscountReason != tc.expected {
				t.Errorf("order.DiscountReason = %q, want %q", o.DiscountReason, tc.expected)
			}
			if o.Tickets[0].DiscountReason != tc.expected {
				t.Errorf("ticket.DiscountReason = %q, want %q", o.Tickets[0].DiscountReason, tc.expected)
			}
		})
	}
}

func TestBuild_ShowTimeLocalUsesVenueTimezone(t *testing.T) {
	tests := []struct {
		name string
		tz   *string
		want string
	}{
		{"venue tz applied", sp("Europe/Moscow"), "2026-08-22T23:00:00"},
		{"nil tz falls back to UTC", nil, "2026-08-22T20:00:00"},
		{"empty tz falls back to UTC", sp(""), "2026-08-22T20:00:00"},
		{"unloadable tz falls back to UTC", sp("Not/AZone"), "2026-08-22T20:00:00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := baseRow()
			r.VenueTimezone = tc.tz
			got := Build([]Row{r})[0].Tickets[0].Event.ShowTimeLocal
			if got != tc.want {
				t.Errorf("ShowTimeLocal = %q, want %q", got, tc.want)
			}
		})
	}
}

// W1-Mb (spec §10 M4 / §11): with no stored credential the projection DERIVES
// the platform EAN-13 with the same formula the issuance path and the backfill
// job mint, so the exported number always carries a valid check digit and can
// never be contradicted by a later backfill.
func TestBuild_BarcodeCredentialWinsOverDerivedEAN13(t *testing.T) {
	r := baseRow()
	want := ean13.PlatformCode(r.SystemTicketID)
	if got := Build([]Row{r})[0].Tickets[0].Barcode; got != want {
		t.Errorf("Barcode = %q, want %q (derived EAN-13 fallback)", got, want)
	}
	if !ean13.Valid(want) {
		t.Errorf("derived fallback %q is not a valid EAN-13", want)
	}

	r.BarcodeStr = sp("4600000000019")
	if got := Build([]Row{r})[0].Tickets[0].Barcode; got != "4600000000019" {
		t.Errorf("Barcode = %q, want the credential payload", got)
	}

	r.BarcodeStr = sp("")
	if got := Build([]Row{r})[0].Tickets[0].Barcode; got != want {
		t.Errorf("Barcode = %q, want the derived fallback for an empty credential", got)
	}
}

// The projection must stay in PLATFORM vocabulary — mapping onto MACS
// integers or Bil24 enums is the adapter's job.
func TestBuild_PlatformStatusIsNotTranslated(t *testing.T) {
	for _, status := range []string{"active", "cancelled", "revoked"} {
		r := baseRow()
		r.TicketStatus = status
		if got := Build([]Row{r})[0].Tickets[0].PlatformStatus; got != status {
			t.Errorf("PlatformStatus = %q, want %q verbatim", got, status)
		}
	}
}

func TestBuild_OrderHeaderCarriesBuyerAndMoney(t *testing.T) {
	userID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
	r := baseRow()
	r.OrderUserID = &userID

	o := Build([]Row{r})[0]
	if o.BuyerUserID != userID.String() {
		t.Errorf("BuyerUserID = %q, want %q", o.BuyerUserID, userID.String())
	}
	if o.BuyerEmail != "buyer@example.com" {
		t.Errorf("BuyerEmail = %q", o.BuyerEmail)
	}
	if o.PaymentProvider != "yookassa" {
		t.Errorf("PaymentProvider = %q", o.PaymentProvider)
	}
	if o.Subtotal != 3000 || o.Discount != 300 || o.Total != 2700 {
		t.Errorf("money = %d/%d/%d, want 3000/300/2700", o.Subtotal, o.Discount, o.Total)
	}
	if o.Currency != "RUB" {
		t.Errorf("Currency = %q, want RUB", o.Currency)
	}
	if !o.CompletedAt.Equal(time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("CompletedAt = %v", o.CompletedAt)
	}
	if o.CheckoutSessionID != r.CheckoutSessionID {
		t.Errorf("CheckoutSessionID = %v, want %v", o.CheckoutSessionID, r.CheckoutSessionID)
	}
}

func TestBuild_GuestOrderHasEmptyBuyerUserID(t *testing.T) {
	r := baseRow()
	r.OrderUserID = nil
	r.HolderEmail = nil
	o := Build([]Row{r})[0]
	if o.BuyerUserID != "" {
		t.Errorf("BuyerUserID = %q, want empty for a guest", o.BuyerUserID)
	}
	if o.BuyerEmail != "" {
		t.Errorf("BuyerEmail = %q, want empty", o.BuyerEmail)
	}
}

func TestBuild_RefundFieldsPassThrough(t *testing.T) {
	when := time.Date(2026, 8, 25, 9, 30, 0, 0, time.UTC)
	r := baseRow()
	r.TicketStatus = "cancelled"
	r.RefundDate = &when
	r.RefundPrice = ip(1000)

	tk := Build([]Row{r})[0].Tickets[0]
	if tk.RefundDate == nil || !tk.RefundDate.Equal(when) {
		t.Errorf("RefundDate = %v, want %v", tk.RefundDate, when)
	}
	if tk.RefundPrice == nil || *tk.RefundPrice != 1000 {
		t.Errorf("RefundPrice = %v, want 1000", tk.RefundPrice)
	}
}

func TestBuild_EventContextIsDenormalizedOntoTicket(t *testing.T) {
	cityID := uuid.MustParse("00000000-0000-0000-0000-0000000000cc")
	r := baseRow()
	r.CityID = &cityID

	ev := Build([]Row{r})[0].Tickets[0].Event
	if ev.EventID != r.EventID || ev.EventName != "Summer Fest" {
		t.Errorf("event id/name = %v/%q", ev.EventID, ev.EventName)
	}
	if ev.OrgLegalName != "ООО Организатор" || ev.OrgName != "Организатор" {
		t.Errorf("org = %q/%q", ev.OrgLegalName, ev.OrgName)
	}
	if ev.VenueID != r.VenueID || ev.VenueName != "Arena Hall" {
		t.Errorf("venue = %v/%q", ev.VenueID, ev.VenueName)
	}
	if ev.CityID == nil || *ev.CityID != cityID || ev.CityName != "Moscow" {
		t.Errorf("city = %v/%q", ev.CityID, ev.CityName)
	}
	if ev.Currency != "RUB" {
		t.Errorf("event currency = %q, want RUB", ev.Currency)
	}
	if !ev.SessionStartAt.Equal(r.SessionStartAt) {
		t.Errorf("SessionStartAt = %v, want %v", ev.SessionStartAt, r.SessionStartAt)
	}
}

func TestBuild_NilCityNameIsEmptyString(t *testing.T) {
	r := baseRow()
	r.CityName = nil
	if got := Build([]Row{r})[0].Tickets[0].Event.CityName; got != "" {
		t.Errorf("CityName = %q, want empty", got)
	}
}

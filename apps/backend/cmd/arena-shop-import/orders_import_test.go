package main

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const histOrdersHeader = "Name,Email,Financial Status,Paid at,Fulfillment Status,Fulfilled at,Currency,Subtotal,Shipping,Taxes,Total,Discount Code,Discount Amount,Created at,Lineitem quantity,Lineitem name,Lineitem price,Cancelled at,Refunded Amount,Billing Name,Shipping Name,Shipping Address1,Shipping City,Shipping Zip,Shipping Country,Phone,Notes,Id\n"

func parseHist(t *testing.T, rows string) []ImportOrder {
	t.Helper()
	of, err := ParseOrders(strings.NewReader(histOrdersHeader + rows))
	if err != nil || len(of.Problems) != 0 {
		t.Fatalf("parse: err=%v problems=%v", err, of.Problems)
	}
	return of.Orders
}

func histIndex() *variantIndex {
	idx := newVariantIndex()
	idx.add("Red Wine", &variantRef{ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), ProductTitle: "Red Wine", VariantTitle: "Default Title", SKU: "RW-1", Kind: "physical"})
	idx.add("Gift Set - Large", &variantRef{ID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), ProductTitle: "Gift Set", VariantTitle: "Large", Kind: "physical"})
	return idx
}

var importedAt = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

func TestParseOrders_ReadsTheFieldsAnImportNeeds(t *testing.T) {
	o := parseHist(t, `#1872,Buyer@Example.com,paid,2026-10-17 12:37:51 +0300,fulfilled,2026-10-18 09:00:00 +0300,ILS,830.00,50.00,134.24,880.00,,0.00,2026-10-17 12:37:50 +0300,1,Red Wine,830.00,,0.00,Ann Buyer,Ann Buyer,1 Main St,Haifa,'3300000,il,'+972501234567,Leave at door,12345678901234
`)[0]
	if o.Email != "buyer@example.com" || o.ShopifyID != "12345678901234" || o.ShipCountry != "IL" || o.ShipPostal != "3300000" || o.Phone != "+972501234567" {
		t.Fatalf("fields: %+v", o)
	}
	wantCreated := time.Date(2026, 10, 17, 9, 37, 50, 0, time.UTC)
	if !o.CreatedAt.Equal(wantCreated) || o.FulfilledAt.IsZero() || !o.CancelledAt.IsZero() {
		t.Fatalf("times: created=%v fulfilled=%v cancelled=%v", o.CreatedAt, o.FulfilledAt, o.CancelledAt)
	}
	if o.TaxesMinor != 13424 || o.Note != "Leave at door" {
		t.Fatalf("money/note: %+v", o)
	}
}

func TestPlanOrder_PaidAndFulfilledIsShipped(t *testing.T) {
	o := parseHist(t, `#1,a@b.co,paid,2026-10-17 12:00:01 +0300,fulfilled,2026-10-18 09:00:00 +0300,ILS,830.00,50.00,0,880.00,,0.00,2026-10-17 12:00:00 +0300,1,Red Wine,830.00,,0.00,Ann,Ann,1 Main St,Haifa,3300000,IL,,,1
`)[0]
	p, skip, err := planOrder(o, histIndex(), importedAt)
	if err != nil || skip != "" {
		t.Fatalf("skip=%q err=%v", skip, err)
	}
	if p.Status != "shipped" || p.ShippedAt == nil || p.PaidAt == nil || p.NotShipped {
		t.Fatalf("plan: %+v", p)
	}
	if p.Subtotal != 83000 || p.Discount != 0 || p.ShippingFee != 5000 || p.Total != 88000 || p.DeliveryKind != "shipping" {
		t.Fatalf("money: %+v", p)
	}
	if p.Unmatched != 0 || p.Lines[0].VariantID == nil || p.Lines[0].VariantTitle != "" || p.Lines[0].SKU == nil || *p.Lines[0].SKU != "RW-1" {
		t.Fatalf("line: %+v", p.Lines[0])
	}
	if !strings.Contains(string(p.Metadata), `"name":"#1"`) || p.StaffNote == "" {
		t.Fatalf("metadata/note: %s %q", p.Metadata, p.StaffNote)
	}
}

func TestPlanOrder_UnfulfilledStaysPaidAndIsFlagged(t *testing.T) {
	o := parseHist(t, `#2,a@b.co,paid,2026-10-17 12:00:01 +0300,unfulfilled,,ILS,200.00,0.00,0,200.00,,0.00,2026-10-17 12:00:00 +0300,1,Gift Set - Large,200.00,,0.00,,,,,,,,,2
`)[0]
	p, _, err := planOrder(o, histIndex(), importedAt)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "paid" || !p.NotShipped || p.ShippedAt != nil || p.DeliveryKind != "pickup" {
		t.Fatalf("plan: %+v", p)
	}
	if p.Lines[0].VariantTitle != "Large" || p.Lines[0].ProductTitle != "Gift Set" {
		t.Fatalf("line: %+v", p.Lines[0])
	}
}

func TestPlanOrder_DiscountKeepsTheAmountsHonest(t *testing.T) {
	// Shopify's Subtotal is already after the discount: lines 300 - discount 30 + shipping 50 = total 320.
	o := parseHist(t, `#3,a@b.co,paid,2026-10-17 12:00:01 +0300,fulfilled,2026-10-18 09:00:00 +0300,ILS,270.00,50.00,0,320.00,SAVE10,30.00,2026-10-17 12:00:00 +0300,3,Unknown Thing,100.00,,0.00,,,1 St,Haifa,,IL,,,3
`)[0]
	p, _, err := planOrder(o, histIndex(), importedAt)
	if err != nil {
		t.Fatal(err)
	}
	if p.Subtotal != 30000 || p.Discount != 3000 || p.Total != 32000 || p.Unmatched != 1 || p.Lines[0].VariantID != nil {
		t.Fatalf("plan: %+v", p)
	}
	if p.Lines[0].TotalMinor != 30000 || p.Lines[0].ProductTitle != "Unknown Thing" {
		t.Fatalf("line: %+v", p.Lines[0])
	}
}

func TestPlanOrder_RefundedAndPartlyRefunded(t *testing.T) {
	full := parseHist(t, `#4,a@b.co,refunded,2026-10-17 12:00:01 +0300,fulfilled,2026-10-18 09:00:00 +0300,ILS,100.00,0.00,0,100.00,,0.00,2026-10-17 12:00:00 +0300,1,Red Wine,100.00,,100.00,,,,,,,,,4
`)[0]
	p, _, err := planOrder(full, histIndex(), importedAt)
	if err != nil || p.Status != "refunded" || p.Refunded != 10000 || p.ShippedAt == nil {
		t.Fatalf("refunded: %+v err=%v", p, err)
	}
	part := parseHist(t, `#5,a@b.co,partially_refunded,2026-10-17 12:00:01 +0300,unfulfilled,,ILS,100.00,0.00,0,100.00,,0.00,2026-10-17 12:00:00 +0300,1,Red Wine,100.00,,25.00,,,,,,,,,5
`)[0]
	p, _, err = planOrder(part, histIndex(), importedAt)
	if err != nil || p.Status != "partially_refunded" || p.Refunded != 2500 || !p.NotShipped {
		t.Fatalf("partial: %+v err=%v", p, err)
	}
	// A "partly refunded" order that names no amount is just a paid one.
	none := parseHist(t, `#6,a@b.co,partially_refunded,2026-10-17 12:00:01 +0300,fulfilled,2026-10-18 09:00:00 +0300,ILS,100.00,0.00,0,100.00,,0.00,2026-10-17 12:00:00 +0300,1,Red Wine,100.00,,0.00,,,,,,,,,6
`)[0]
	p, _, err = planOrder(none, histIndex(), importedAt)
	if err != nil || p.Status != "shipped" || p.Refunded != 0 {
		t.Fatalf("no amount: %+v err=%v", p, err)
	}
}

func TestPlanOrder_RefusesWhatItCannotStoreHonestly(t *testing.T) {
	bad := parseHist(t, `#7,a@b.co,paid,2026-10-17 12:00:01 +0300,fulfilled,,ILS,100.00,0.00,0,150.00,,0.00,2026-10-17 12:00:00 +0300,1,Red Wine,100.00,,0.00,,,,,,,,,7
`)[0]
	if _, _, err := planOrder(bad, histIndex(), importedAt); err == nil || !strings.Contains(err.Error(), "do not add up") {
		t.Fatalf("a total that is not lines - discount + shipping must fail, got %v", err)
	}
	unpaid := parseHist(t, `#8,a@b.co,pending,,unfulfilled,,ILS,100.00,0.00,0,100.00,,0.00,2026-10-17 12:00:00 +0300,1,Red Wine,100.00,,0.00,,,,,,,,,8
`)[0]
	if _, skip, err := planOrder(unpaid, histIndex(), importedAt); err != nil || !strings.Contains(skip, "not paid") {
		t.Fatalf("an unpaid order is skipped, got skip=%q err=%v", skip, err)
	}
	noTime := parseHist(t, `#9,a@b.co,paid,,fulfilled,,ILS,100.00,0.00,0,100.00,,0.00,,1,Red Wine,100.00,,0.00,,,,,,,,,9
`)[0]
	if _, _, err := planOrder(noTime, histIndex(), importedAt); err == nil {
		t.Fatal("an order with no creation time must fail")
	}
}

func TestVariantIndex_AmbiguousNamesMatchNothing(t *testing.T) {
	idx := newVariantIndex()
	idx.add("Twin", &variantRef{ID: uuid.MustParse("00000000-0000-0000-0000-000000000010")})
	idx.add("  twin ", &variantRef{ID: uuid.MustParse("00000000-0000-0000-0000-000000000011")})
	idx.add("Twin", &variantRef{ID: uuid.MustParse("00000000-0000-0000-0000-000000000012")})
	if idx.find("TWIN") != nil {
		t.Fatal("two variants of one name must not be guessed between")
	}
}

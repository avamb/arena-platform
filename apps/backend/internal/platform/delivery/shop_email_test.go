package delivery

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

func shopOrderFixture() (gen.ShopOrderRow, []gen.ShopOrderItemRow) {
	o := gen.ShopOrderRow{
		Number: 1000777, Currency: "ILS", Subtotal: 18000, Discount: 1000, ShippingFee: 5000, Total: 22000,
		BuyerName: "Anna K", BuyerPhone: "+972500000000", ShipCity: "Haifa", ShipAddress1: "Herzl 1", ShipAddress2: "apt 4",
		ShipPostal: "3100000", DeliveryKind: "shipping", DeliveryNote: " call before ", TrackingNote: "ZX-99",
	}
	items := []gen.ShopOrderItemRow{
		{ProductTitle: "Rosé", VariantTitle: "Default Title", Qty: 2, UnitPriceMinor: 6000, TotalMinor: 12000, Ordinal: 1},
		{ProductTitle: "Guide", Qty: 1, UnitPriceMinor: 6000, TotalMinor: 6000, Ordinal: 2},
	}
	return o, items
}

func TestBuildShopOrderData_PhysicalOrder(t *testing.T) {
	o, items := shopOrderFixture()
	d := buildShopOrderData(o, items, "https://shop.example/checkout/t")
	if d.OrderNumber != "1000777" || d.Total != "220.00 ILS" || d.Subtotal != "180.00 ILS" ||
		d.Discount != "10.00 ILS" || d.Shipping != "50.00 ILS" {
		t.Errorf("amounts wrong: %+v", d)
	}
	wantAddr := []string{"Anna K", "+972500000000", "Haifa", "Herzl 1", "apt 4", "3100000"}
	if !reflect.DeepEqual(d.Address, wantAddr) || d.Pickup {
		t.Errorf("address = %q pickup=%v", d.Address, d.Pickup)
	}
	if d.Note != "call before" || d.TrackingNote != "ZX-99" || d.StatusURL != "https://shop.example/checkout/t" {
		t.Errorf("note/tracking/link wrong: %+v", d)
	}
	if len(d.Items) != 2 || d.Items[0].LineTotal != "120.00 ILS" || d.Items[0].Kind != "physical" || len(d.Digital) != 0 {
		t.Errorf("items wrong: %+v", d.Items)
	}
}

func TestBuildShopOrderData_DigitalLinesAndNothingToShip(t *testing.T) {
	o, items := shopOrderFixture()
	items[0].ProductKind, items[1].ProductKind = "digital", "service"
	o.DeliveryKind = "none"
	d := buildShopOrderData(o, items, "https://shop.example/checkout/t")
	if len(d.Digital) != 1 || d.Digital[0].Title != "Rosé" {
		t.Errorf("digital lines = %+v", d.Digital)
	}
	if len(d.Address) != 0 || d.Pickup || d.Shipping != "" {
		t.Errorf("nothing to ship, yet address=%q pickup=%v shipping=%q", d.Address, d.Pickup, d.Shipping)
	}
	// A mixed order still ships.
	items[1].ProductKind = "physical"
	o.DeliveryKind = "shipping"
	d = buildShopOrderData(o, items, "")
	if len(d.Address) == 0 || d.Shipping == "" || len(d.Digital) != 1 {
		t.Errorf("a mixed order must keep its address and fee: %+v", d)
	}
}

func TestBuildShopOrderData_PickupAndPlainTotal(t *testing.T) {
	o, items := shopOrderFixture()
	o.DeliveryKind, o.Discount, o.ShippingFee, o.Total = "pickup", 0, 0, 18000
	d := buildShopOrderData(o, items, "")
	if !d.Pickup || len(d.Address) != 0 {
		t.Errorf("pickup = %v address = %q", d.Pickup, d.Address)
	}
	if d.Subtotal != "" || d.Shipping != "" || d.Discount != "" || d.Total != "180.00 ILS" {
		t.Errorf("a plain order prints only its total: %+v", d)
	}
}

func TestShopEmailSkipReason(t *testing.T) {
	o, _ := shopOrderFixture()
	o.BuyerEmail = "b@example.com"
	now := time.Now()
	cases := []struct {
		status, kind string
		shipped      *time.Time
		email        string
		skip         bool
	}{
		{shop.StatusPaid, shop.EmailReceived, nil, "b@example.com", false},
		{shop.StatusShipped, shop.EmailReceived, &now, "b@example.com", false},
		{shop.StatusShipped, shop.EmailShipped, &now, "b@example.com", false},
		{shop.StatusPaid, shop.EmailShipped, nil, "b@example.com", true},
		{shop.StatusExpired, shop.EmailReceived, nil, "b@example.com", true},
		{shop.StatusCancelled, shop.EmailReceived, nil, "b@example.com", true},
		{shop.StatusPaid, shop.EmailReceived, nil, "  ", true},
		{shop.StatusManualReview, shop.EmailReceived, nil, "b@example.com", true},
		{shop.StatusPartiallyRefunded, shop.EmailRefunded, nil, "b@example.com", false},
		{shop.StatusRefunded, shop.EmailRefunded, nil, "b@example.com", false},
		{shop.StatusManualReview, shop.EmailRefunded, nil, "b@example.com", false},
		{shop.StatusCancelled, shop.EmailRefunded, nil, "b@example.com", true},
		{shop.StatusRefunded, shop.EmailRefunded, nil, "", true},
	}
	for i, c := range cases {
		o.Status, o.ShippedAt, o.BuyerEmail = c.status, c.shipped, c.email
		if got := shopEmailSkipReason(o, c.kind) != ""; got != c.skip {
			t.Errorf("case %d (%s/%s): skip=%v want %v", i, c.status, c.kind, got, c.skip)
		}
	}
}

func TestShopEmailSkipReason_DigitalOnlyOrderIsNeverShipped(t *testing.T) {
	o, _ := shopOrderFixture()
	now := time.Now()
	o.Status, o.ShippedAt, o.BuyerEmail, o.DeliveryKind = shop.StatusShipped, &now, "b@example.com", shop.DeliveryNone
	if shopEmailSkipReason(o, shop.EmailShipped) == "" {
		t.Error("a digital-only order must not get a shipped letter")
	}
	if shopEmailSkipReason(o, shop.EmailReceived) != "" {
		t.Error("but it does get the received letter")
	}
}

func TestStatusURL(t *testing.T) {
	domain := "shop.vino.example"
	if got := statusURL(shopOrg{Slug: "vino", ShopDomain: &domain}, "https://shop.arena.example", "tok"); got != "https://shop.vino.example/checkout/result?checkout_token=tok" {
		t.Errorf("own domain: %q", got)
	}
	if got := statusURL(shopOrg{Slug: "vino"}, "https://shop.arena.example/", "tok"); got != "https://shop.arena.example/vino/checkout/result?checkout_token=tok" {
		t.Errorf("platform host: %q", got)
	}
	if got := statusURL(shopOrg{Slug: "vino"}, "", "tok"); got != "" {
		t.Errorf("no base, no link, got %q", got)
	}
}

func TestSentKey_RefundLetterIsRememberedPerRefund(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	if sentKey(shop.EmailRefunded, a) == sentKey(shop.EmailRefunded, b) {
		t.Error("two refunds of one order share a sent marker")
	}
	if sentKey(shop.EmailReceived, a) != "email_received_sent_at" || sentKey(shop.EmailShipped, b) != "email_shipped_sent_at" {
		t.Error("the received and shipped markers changed")
	}
}

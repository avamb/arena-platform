package salesnotify

import (
	"context"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

func shopSample() ShopOrder {
	return ShopOrder{
		OrgName: "Vino&Co",
		Order: gen.ShopOrderRow{
			Number: 1000777, Currency: "ILS", Total: 23000, ShippingFee: 5000, Subtotal: 18000,
			BuyerName: "Anna <K>", BuyerPhone: "+972500000000", BuyerEmail: "anna@example.com",
			ShipName: "Dan L", ShipCity: "Haifa", ShipAddress1: "Herzl 1", ShipAddress2: "apt 4", ShipPostal: "3100000",
			DeliveryKind: "shipping", DeliveryNote: "call before",
		},
		Items: []gen.ShopOrderItemRow{
			{ProductTitle: "Rosé", VariantTitle: "Default Title", Qty: 2, UnitPriceMinor: 6000},
			{ProductTitle: "Merlot", VariantTitle: "Magnum", Qty: 1, UnitPriceMinor: 6000},
		},
	}
}

func TestRoute_ShopKindsSeparateTicketsFromShop(t *testing.T) {
	subs := []Subscription{
		{ID: "tickets", OrgID: orgVino, ChatID: "-1", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindTickets}},
		{ID: "legacy", OrgID: orgVino, ChatID: "-2", OnOrderPaid: true, OnTicketRefunded: true}, // no kinds = tickets
		{ID: "logistics", OrgID: orgVino, ChatID: "-3", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindShop}},
		{ID: "both", OrgID: orgVino, ChatID: "-4", OnOrderPaid: true, OnTicketRefunded: false, Kinds: []string{KindTickets, KindShop}},
		{ID: "operator-shop", ChatID: "-5", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindShop}},
		{ID: "other-org", OrgID: orgLampyris, ChatID: "-6", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindShop}},
	}
	ids := func(ss []Subscription) string {
		var out []string
		for _, s := range ss {
			out = append(out, s.ID)
		}
		return strings.Join(out, ",")
	}
	if got := ids(Route(subs, orgVino, TriggerShopOrderPaid)); got != "logistics,both,operator-shop" {
		t.Errorf("shop paid routed to %q", got)
	}
	if got := ids(Route(subs, orgVino, TriggerShopOrderRefunded)); got != "logistics,operator-shop" {
		t.Errorf("shop refund routed to %q (the refund switch of 'both' is off)", got)
	}
	if got := ids(Route(subs, orgVino, TriggerOrderPaid)); got != "tickets,legacy,both" {
		t.Errorf("a ticket sale routed to %q: the logistics group must not hear it", got)
	}
	if got := ids(Route(subs, orgVino, TriggerTicketRefunded)); got != "tickets,legacy" {
		t.Errorf("a ticket refund routed to %q", got)
	}
}

func TestFormatShopOrderPaid_CourierFormOrder(t *testing.T) {
	msg := FormatShopOrderPaid(shopSample())
	// One block of consecutive lines in the courier form's order.
	form := "Dan L\n+972500000000\nHaifa\nHerzl 1\napt 4\n3100000"
	if !strings.Contains(msg, form) {
		t.Errorf("the courier form lines are missing or out of order:\n%s", msg)
	}
	for _, want := range []string{
		"🛍 <b>New shop order</b> — Vino&amp;Co",
		"🧾 Order #1000777",
		"💰 Total: <b>230.00 ILS</b>",
		"👤 Anna &lt;K&gt;", "📞 +972500000000", "✉️ anna@example.com",
		"🚚 Delivery: shipping",
		"2 × Rosé — 60.00 ILS each",
		"1 × Merlot (Magnum) — 60.00 ILS each",
		"🚚 Shipping: 50.00 ILS",
		"📝 Buyer's note: call before",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	// Name, phone and e-mail first, then the form, then the goods, then the note.
	order := []string{"Order #1000777", "Anna", "Delivery:", "Dan L", "Herzl 1", "3100000", "Rosé", "Shipping:", "Buyer's note"}
	at := -1
	for _, w := range order {
		i := strings.Index(msg, w)
		if i <= at {
			t.Errorf("%q is out of order in:\n%s", w, msg)
		}
		at = i
	}
}

func TestFormatShopOrderPaid_LongOrderIsCappedAndPickupSaysSo(t *testing.T) {
	s := shopSample()
	s.Order.DeliveryKind = "pickup"
	s.Order.ShipCity, s.Order.ShipAddress1, s.Order.ShipAddress2, s.Order.ShipPostal = "", "", "", ""
	s.Items = nil
	for i := 0; i < maxShopItemLines+5; i++ {
		s.Items = append(s.Items, gen.ShopOrderItemRow{ProductTitle: "Wine", Qty: 1, UnitPriceMinor: 100})
	}
	msg := FormatShopOrderPaid(s)
	if !strings.Contains(msg, "🏬 Delivery: pickup") || !strings.Contains(msg, "… and 5 more line(s)") {
		t.Errorf("pickup or cap missing:\n%s", msg)
	}
	if len([]rune(msg)) > 4096 {
		t.Errorf("message is %d characters, over Telegram's limit", len([]rune(msg)))
	}
}

func shopEvent(typ, orderID string, extra map[string]any) outbox.Event {
	p := map[string]any{"order_id": orderID, "org_id": orgVino}
	for k, v := range extra {
		p[k] = v
	}
	return outbox.Event{ID: "ev-" + typ + orderID, AggregateType: "shop_order", AggregateID: orderID, EventType: typ, Payload: p}
}

func TestDispatcher_ShopOrderGoesToShopChatsOncePerAnnouncement(t *testing.T) {
	store := newFakeStore(
		Subscription{ID: "tickets", OrgID: orgVino, Name: "Vino tickets", ChatID: "-1", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindTickets}},
		Subscription{ID: "logistics", OrgID: orgVino, Name: "Vino logistics", ChatID: "-2", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindShop}},
	)
	order := shopSample()
	order.Order.RefundedMinor = 5000
	store.shopOrds["o-1"] = order
	snd := &fakeSender{}
	d := NewDispatcher(store, snd, nil)
	ctx := context.Background()

	d.Handle(ctx, shopEvent(EventShopOrderPaid, "o-1", nil))
	d.Handle(ctx, shopEvent(EventShopOrderPaid, "o-1", nil)) // an outbox retry
	d.Handle(ctx, shopEvent(EventShopOrderRefunded, "o-1", map[string]any{"refunded_minor": float64(5000), "refunded_now": float64(5000)}))
	d.Handle(ctx, shopEvent(EventShopOrderRefunded, "o-1", map[string]any{"refunded_minor": float64(5000), "refunded_now": float64(5000)}))
	d.Handle(ctx, shopEvent(EventShopOrderRefunded, "o-1", map[string]any{"refunded_minor": float64(9000), "refunded_now": float64(4000)}))
	d.Handle(ctx, shopEvent(EventShopOrderPaid, "o-missing", nil)) // an order that is gone

	if len(snd.sent) != 3 {
		t.Fatalf("sent %d messages, want 3 (paid, two distinct refunds): %+v", len(snd.sent), snd.sent)
	}
	for _, m := range snd.sent {
		if m.chat != "-2" {
			t.Errorf("a shop announcement reached chat %s, only the logistics chat -2 listens to shop", m.chat)
		}
	}
	if !strings.Contains(snd.sent[0].text, "New shop order") || !strings.Contains(snd.sent[1].text, "Shop refund") ||
		!strings.Contains(snd.sent[1].text, "Refunded: <b>50.00 ILS</b>") {
		t.Errorf("unexpected messages: %+v", snd.sent)
	}
	for _, k := range []string{"shop_paid:o-1", "shop_refund:o-1:5000", "shop_refund:o-1:9000"} {
		if !store.claimed[k] {
			t.Errorf("announcement %q was not claimed", k)
		}
	}
}

func TestDispatcher_SubscriptionWithoutShopKindGetsNoShopOrders(t *testing.T) {
	store := newFakeStore(Subscription{ID: "tickets", OrgID: orgVino, ChatID: "-1", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindTickets}})
	store.shopOrds["o-1"] = shopSample()
	snd := &fakeSender{}
	d := NewDispatcher(store, snd, nil)
	d.Handle(context.Background(), shopEvent(EventShopOrderPaid, "o-1", nil))
	if len(snd.sent) != 0 {
		t.Fatalf("a tickets-only chat got %d shop message(s)", len(snd.sent))
	}
	if len(store.claimed) != 0 {
		t.Errorf("nothing was routed, so nothing may be claimed: %v", store.claimed)
	}
}

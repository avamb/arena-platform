package salesnotify

import (
	"context"
	"os"
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
			ShipName: "Dan L", ShipCity: "Haifa", ShipAddress1: "Herzl 1", ShipAddress2: "apt 4", ShipPostal: "3100000", ShipCountry: "IL",
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
	form := "Dan L\n+972500000000\nHerzl 1\napt 4\nHaifa\n3100000\nIsrael"
	if !strings.Contains(msg, form) {
		t.Errorf("the courier form lines are missing or out of order:\n%s", msg)
	}
	for _, want := range []string{
		"🛍 <b>New shop order</b> — Vino&amp;Co",
		"🧾 Order #1000777",
		"💰 Total: <b>230.00 ₪</b>",
		"👤 Anna &lt;K&gt;", "📞 +972500000000", "✉️ anna@example.com",
		"🚚 Delivery: shipping",
		"🗺 Google Maps: https://www.google.com/maps/search/?api=1&amp;query=Herzl+1%2C+Haifa%2C+3100000%2C+Israel",
		"🚗 Waze: https://waze.com/ul?q=Herzl+1%2C+Haifa%2C+3100000%2C+Israel&amp;navigate=yes",
		"2 × Rosé — 60.00 ₪ each",
		"1 × Merlot (Magnum) — 60.00 ₪ each",
		"🚚 Shipping: 50.00 ₪",
		"📝 Buyer's note: call before",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	// Name, phone and e-mail first, then the form, then the goods, then the note.
	order := []string{"Order #1000777", "Anna", "Delivery:", "Dan L", "Herzl 1", "Haifa", "3100000", "Israel", "Google Maps:", "Waze:", "Rosé", "Shipping:", "Buyer's note"}
	at := -1
	for _, w := range order {
		i := strings.Index(msg, w)
		if i <= at {
			t.Errorf("%q is out of order in:\n%s", w, msg)
		}
		at = i
	}
}

func TestFormatShopOrderPaid_NoDeliveryPrintsNoCourierForm(t *testing.T) {
	s := shopSample()
	s.Order.DeliveryKind = "none"
	s.Order.ShipName, s.Order.ShipCity, s.Order.ShipAddress1, s.Order.ShipAddress2, s.Order.ShipPostal, s.Order.ShipCountry = "", "", "", "", "", ""
	msg := FormatShopOrderPaid(s)
	if !strings.Contains(msg, "📦 No delivery (digital / service)") {
		t.Errorf("a digital / service order must say there is no delivery:\n%s", msg)
	}
	for _, bad := range []string{"Delivery: shipping", "Delivery: pickup", "Courier form", "Google Maps", "Waze", "maps"} {
		if strings.Contains(msg, bad) {
			t.Errorf("a no-delivery order must not contain %q:\n%s", bad, msg)
		}
	}
	// The buyer and the goods are still there.
	for _, want := range []string{"👤 Anna &lt;K&gt;", "📞 +972500000000", "🛒 <b>Items</b>"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
}

func TestFormatShopOrderPaid_PickupHasNoMapLinks(t *testing.T) {
	s := shopSample()
	s.Order.DeliveryKind = "pickup"
	msg := FormatShopOrderPaid(s)
	if !strings.Contains(msg, "🏬 Delivery: pickup") || strings.Contains(msg, "Google Maps") || strings.Contains(msg, "Waze") {
		t.Errorf("pickup says so and has no map links:\n%s", msg)
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
		!strings.Contains(snd.sent[1].text, "Refunded: <b>50.00 ₪</b>") {
		t.Errorf("unexpected messages: %+v", snd.sent)
	}
	for _, k := range []string{"shop_paid:o-1", "shop_refund:o-1:5000", "shop_refund:o-1:9000"} {
		if !store.claimed[k] {
			t.Errorf("announcement %q was not claimed", k)
		}
	}
}

// The events-bot footer is for organizers; the logistics chat is a courier's.
func TestDispatcher_ShopMessagesCarryNoEventsBotFooter(t *testing.T) {
	store := newFakeStore(
		Subscription{ID: "logistics", OrgID: orgVino, ChatID: "-2", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindShop}},
	)
	store.shopOrds["o-1"] = shopSample()
	snd := &fakeSender{}
	d := NewDispatcher(store, snd, nil).WithEventsBot("ArenaAppTestZone_bot")
	ctx := context.Background()
	d.Handle(ctx, shopEvent(EventShopOrderPaid, "o-1", nil))
	d.Handle(ctx, shopEvent(EventShopOrderRefunded, "o-1", map[string]any{"refunded_minor": float64(5000), "refunded_now": float64(5000)}))
	d.Handle(ctx, shopEvent(EventShopOrderManualReview, "o-1", nil))
	if len(snd.sent) != 3 {
		t.Fatalf("sent %d messages, want 3", len(snd.sent))
	}
	for _, m := range snd.sent {
		if strings.Contains(m.text, "t.me/") || strings.Contains(m.text, "ArenaAppTestZone_bot") || strings.Contains(m.text, "🤖") {
			t.Errorf("a shop message carries the events-bot footer:\n%s", m.text)
		}
	}
	// The same bot still tags a ticket sale (footer_test.go).
	if d.withFooter("x") == "x" {
		t.Error("the footer must stay available to ticket sales")
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

func TestDispatcher_ManualReviewReachesShopChatsOnce(t *testing.T) {
	store := newFakeStore(
		Subscription{ID: "tickets", OrgID: orgVino, ChatID: "-1", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindTickets}},
		Subscription{ID: "logistics", OrgID: orgVino, ChatID: "-2", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindShop}},
	)
	store.shopOrds["o-1"] = shopSample()
	snd := &fakeSender{}
	d := NewDispatcher(store, snd, nil)
	ctx := context.Background()
	d.Handle(ctx, shopEvent(EventShopOrderManualReview, "o-1", nil))
	d.Handle(ctx, shopEvent(EventShopOrderManualReview, "o-1", nil))
	if len(snd.sent) != 1 || snd.sent[0].chat != "-2" {
		t.Fatalf("manual review announcements = %+v; want one, to the logistics chat only", snd.sent)
	}
	for _, want := range []string{"needs your attention", "Order #1000777", "arrived after the hold ended", "Check the order in the event center", "anna@example.com"} {
		if !strings.Contains(snd.sent[0].text, want) {
			t.Errorf("review message lacks %q:\n%s", want, snd.sent[0].text)
		}
	}
	if !store.claimed["shop_review:o-1"] {
		t.Error("the announcement was not claimed")
	}
}

// The shop texts follow the organization's language.
func TestFormatShopOrderPaid_Russian(t *testing.T) {
	s := shopSample()
	s.Locale = "ru"
	msg := FormatShopOrderPaid(s)
	for _, want := range []string{
		"🛍 <b>Новый заказ в магазине</b> — Vino&amp;Co",
		"🧾 Заказ №1000777",
		"💰 Сумма: <b>230,00 ₪</b>",
		"🚚 Доставка: курьером",
		"📦 <b>Данные для курьера</b>",
		"Dan L\n+972500000000\nHerzl 1\napt 4\nHaifa\n3100000\nИзраиль",
		"🗺 Google Карты: https://www.google.com/maps/search/?api=1&amp;query=",
		"🚗 Waze: https://waze.com/ul?q=",
		"🛒 <b>Товары</b>",
		"2 × Rosé — по 60,00 ₪",
		"🚚 Доставка: 50,00 ₪",
		"📝 Комментарий покупателя: call before",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the Russian message lacks %q:\n%s", want, msg)
		}
	}
	for _, english := range []string{"New shop order", "Courier form", "Total:", "Buyer's note", "Items", "Israel"} {
		if strings.Contains(msg, english) {
			t.Errorf("the Russian message still has %q:\n%s", english, msg)
		}
	}

	s.Order.DeliveryKind = "none"
	if none := FormatShopOrderPaid(s); !strings.Contains(none, "📦 Без доставки (цифровой товар или услуга)") || strings.Contains(none, "Данные для курьера") {
		t.Errorf("Russian no-delivery message:\n%s", none)
	}
}

func TestFormatShopOrderPaid_EnglishIsTheDefaultAndTheFallback(t *testing.T) {
	want := FormatShopOrderPaid(shopSample()) // no locale
	for _, loc := range []string{"en", "en-GB", "EN", "xx", "  "} {
		s := shopSample()
		s.Locale = loc
		if got := FormatShopOrderPaid(s); got != want {
			t.Errorf("locale %q renders differently from the default:\n%s\n---\n%s", loc, got, want)
		}
	}
	for _, w := range []string{"New shop order", "Israel", "Buyer's note: call before"} {
		if !strings.Contains(want, w) {
			t.Errorf("English message lacks %q:\n%s", w, want)
		}
	}
}

func TestFormatShopOrderRefund_RussianAndEnglish(t *testing.T) {
	s := shopSample()
	s.Order.RefundedMinor = 5000
	en := FormatShopOrderRefund(s, 5000)
	for _, want := range []string{"↩️ <b>Shop refund</b> — Vino&amp;Co", "🧾 Order #1000777", "💸 Refunded: <b>50.00 ₪</b>", "💸 Refunded so far: 50.00 ₪ of 230.00 ₪", "👤 Anna &lt;K&gt;"} {
		if !strings.Contains(en, want) {
			t.Errorf("English refund lacks %q:\n%s", want, en)
		}
	}
	s.Locale = "ru"
	ru := FormatShopOrderRefund(s, 5000)
	for _, want := range []string{"↩️ <b>Возврат в магазине</b> — Vino&amp;Co", "🧾 Заказ №1000777", "💸 Возвращено: <b>50,00 ₪</b>", "💸 Возвращено всего: 50,00 ₪ из 230,00 ₪", "👤 Anna &lt;K&gt;"} {
		if !strings.Contains(ru, want) {
			t.Errorf("Russian refund lacks %q:\n%s", want, ru)
		}
	}
	if strings.Contains(ru, "Refunded") || strings.Contains(ru, "Order #") {
		t.Errorf("Russian refund still has English:\n%s", ru)
	}
}

// Every salesnotify.shop.* key exists in every catalog and keeps its
// placeholders, so no language shows an empty value or a raw key.
func TestShopTexts_EveryLocaleHasEveryKey(t *testing.T) {
	data := map[string]any{"Org": "O", "Number": 7, "Amount": "A", "Total": "T", "Url": "U", "Count": 3, "Qty": 2, "Name": "N", "Price": "P", "Note": "X"}
	for _, loc := range []string{"en", "ru", "cs", "he", "es"} {
		for _, k := range shopKeys {
			got := say(loc, k, data)
			if got == k || strings.TrimSpace(got) == "" || strings.Contains(got, "{{") || strings.Contains(got, "<no value>") {
				t.Errorf("%s / %s renders %q", loc, k, got)
			}
		}
	}
	// A missing key would fall back to English silently: look in the files.
	for _, loc := range []string{"en", "ru", "cs", "he", "es"} {
		raw, err := os.ReadFile("../i18n/locales/" + loc + ".toml")
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range shopKeys {
			if !strings.Contains(string(raw), "[\""+k+"\"]") {
				t.Errorf("%s.toml lacks %s", loc, k)
			}
		}
	}
	en := say("en", "salesnotify.shop.map_google", map[string]any{"Url": "U"})
	for _, loc := range []string{"ru", "cs", "he", "es"} {
		if !strings.Contains(say(loc, "salesnotify.shop.map_google", map[string]any{"Url": "U"}), "U") {
			t.Errorf("%s map_google dropped the link (en: %s)", loc, en)
		}
	}
}

// SA-37: thirty long-named lines and a long note still fit Telegram's limit,
// and the lines that do not fit are counted in the closing line.
func TestFormatShopOrderPaid_LongTitlesStayUnderTelegramsLimit(t *testing.T) {
	s := shopSample()
	s.Order.DeliveryNote = strings.Repeat("n", 700)
	s.Items = nil
	for i := 0; i < maxShopItemLines; i++ {
		s.Items = append(s.Items, gen.ShopOrderItemRow{ProductTitle: strings.Repeat("W", 500), Qty: 1, UnitPriceMinor: 100})
	}
	msg := FormatShopOrderPaid(s)
	if n := len([]rune(msg)); n > 4096 {
		t.Fatalf("message is %d characters, over Telegram's limit", n)
	}
	if !strings.Contains(msg, "more line(s)") {
		t.Errorf("the lines left out are not counted:\n%s", msg)
	}
	if !strings.Contains(msg, "nnnn") {
		t.Errorf("the closing note was cut instead of the goods list:\n%s", msg)
	}
}

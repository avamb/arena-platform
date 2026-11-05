package salesnotify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/orgmodules"
)

// Spec 61 section 7a: a sales chat hears the fact, the delivery group the card.

func chatIDs(ss []Subscription) string {
	var out []string
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return strings.Join(out, ",")
}

func TestRouteShop_Matrix(t *testing.T) {
	subs := []Subscription{
		{ID: "tickets", OrgID: orgVino, ChatID: "-1", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindTickets}},
		{ID: "legacy", OrgID: orgVino, ChatID: "-2", OnOrderPaid: true, OnTicketRefunded: true}, // no kinds = tickets
		{ID: "sales", OrgID: orgVino, ChatID: "-3", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindTickets, KindShopSale}},
		{ID: "delivery", OrgID: orgVino, ChatID: "-4", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindShopDelivery}},
		{ID: "both", OrgID: orgVino, ChatID: "-5", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindShopSale, KindShopDelivery}},
		{ID: "op-sale", ChatID: "-6", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindShopSale}},
		{ID: "op-delivery", ChatID: "-7", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindShopDelivery}},
		{ID: "other-org", OrgID: orgLampyris, ChatID: "-8", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindShopSale, KindShopDelivery}},
		{ID: "paid-off", OrgID: orgVino, ChatID: "-9", OnOrderPaid: false, OnTicketRefunded: true, Kinds: []string{KindShopSale}},
		{ID: "refund-off", OrgID: orgVino, ChatID: "-10", OnOrderPaid: true, OnTicketRefunded: false, Kinds: []string{KindShopDelivery}},
	}
	cases := []struct {
		name         string
		trigger      Trigger
		ships        bool
		sale, delive string
	}{
		// A shipping order that is paid: the fact to the sales chats, the card
		// to the delivery groups and to the chat with both kinds (card only).
		{"paid shipping", TriggerShopOrderPaid, true, "sales,op-sale", "delivery,both,op-delivery,refund-off"},
		// A file or a service: nothing for the delivery groups, the chat with
		// both kinds falls back to the fact.
		{"paid digital", TriggerShopOrderPaid, false, "sales,both,op-sale", ""},
		{"refund shipping", TriggerShopOrderRefunded, true, "sales,op-sale,paid-off", "delivery,both,op-delivery"},
		{"refund digital", TriggerShopOrderRefunded, false, "sales,both,op-sale,paid-off", ""},
		// A review notice is for the sales chats only, even when the order ships.
		{"review shipping", TriggerShopOrderReview, true, "sales,both,op-sale", ""},
		{"review digital", TriggerShopOrderReview, false, "sales,both,op-sale", ""},
	}
	for _, c := range cases {
		sale, delivery := RouteShop(subs, orgVino, c.trigger, c.ships)
		if got := chatIDs(sale); got != c.sale {
			t.Errorf("%s: sales chats = %q, want %q", c.name, got, c.sale)
		}
		if got := chatIDs(delivery); got != c.delive {
			t.Errorf("%s: delivery groups = %q, want %q", c.name, got, c.delive)
		}
	}
	// A ticket sale never reaches a shop-only chat.
	if got := chatIDs(Route(subs, orgVino, TriggerOrderPaid)); got != "tickets,legacy,sales" {
		t.Errorf("a ticket sale routed to %q", got)
	}
	if got := chatIDs(Route(subs, orgVino, TriggerTicketRefunded)); got != "tickets,legacy,sales" {
		t.Errorf("a ticket refund routed to %q", got)
	}
}

// The fact carries the number, the total and the goods and NO personal data.
func TestFormatShopSale_FactOnly(t *testing.T) {
	s := shopSample()
	s.Order.Total = 23000
	s.Items = []gen.ShopOrderItemRow{
		{ProductTitle: "Rosé <red>", VariantTitle: "Default Title", Qty: 2, UnitPriceMinor: 6000},
		{ProductTitle: "Merlot", VariantTitle: "Magnum", Qty: 1, UnitPriceMinor: 6000},
	}
	msg := FormatShopSale(s)
	for _, want := range []string{
		"🛍 <b>Shop sale</b> — Vino&amp;Co",
		"🧾 Order #1000777 · 💰 Total: <b>230.00 ₪</b>",
		"🛒 Items (2): Rosé &lt;red&gt; ×2, Merlot (Magnum) ×1",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the fact lacks %q:\n%s", want, msg)
		}
	}
	for _, personal := range []string{"Anna", "anna@example.com", "+972500000000", "Dan L", "Herzl", "Haifa", "3100000", "call before", "Google", "Waze", "Israel"} {
		if strings.Contains(msg, personal) {
			t.Errorf("the sales fact carries %q:\n%s", personal, msg)
		}
	}
}

func TestFormatShopSale_RussianAndLong(t *testing.T) {
	s := shopSample()
	s.Locale = "ru"
	msg := FormatShopSale(s)
	for _, want := range []string{"🛍 <b>Продажа в магазине</b> — Vino&amp;Co", "🧾 Заказ №1000777 · 💰 Сумма: <b>230,00 ₪</b>", "🛒 Позиции (2): Rosé ×2, Merlot (Magnum) ×1"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the Russian fact lacks %q:\n%s", want, msg)
		}
	}
	// A very long order stays inside Telegram's limit and counts what it left out.
	s.Items = nil
	for i := 0; i < maxShopItemLines+40; i++ {
		s.Items = append(s.Items, gen.ShopOrderItemRow{ProductTitle: strings.Repeat("W", 500), Qty: 1})
	}
	s.Locale = "en"
	long := FormatShopSale(s)
	if n := len([]rune(long)); n > 4096 {
		t.Fatalf("the fact is %d characters, over Telegram's limit", n)
	}
	if !strings.Contains(long, "more line(s)") {
		t.Errorf("the lines left out are not counted:\n%s", long)
	}
}

func TestFormatShopSaleRefund_FullOrPartAndNoPersonalData(t *testing.T) {
	s := shopSample()
	s.Order.RefundedMinor = 5000
	part := FormatShopSaleRefund(s, 5000)
	for _, want := range []string{"↩️ <b>Shop refund</b> — Vino&amp;Co", "🧾 Order #1000777", "💸 Refunded: <b>50.00 ₪</b>", "Partly refunded: 50.00 ₪ of 230.00 ₪"} {
		if !strings.Contains(part, want) {
			t.Errorf("the partial refund lacks %q:\n%s", want, part)
		}
	}
	s.Order.RefundedMinor = 23000
	s.Order.Status = "refunded"
	full := FormatShopSaleRefund(s, 18000)
	if !strings.Contains(full, "Refunded in full") || strings.Contains(full, "Partly") {
		t.Errorf("the full refund:\n%s", full)
	}
	for _, m := range []string{part, full} {
		for _, personal := range []string{"Anna", "anna@example.com", "+972500000000", "Dan L", "Herzl", "Haifa"} {
			if strings.Contains(m, personal) {
				t.Errorf("a sales refund carries %q:\n%s", personal, m)
			}
		}
	}
	s.Locale = "ru"
	if ru := FormatShopSaleRefund(s, 18000); !strings.Contains(ru, "Заказ возвращён полностью") || !strings.Contains(ru, "Возвращено: <b>180,00 ₪</b>") {
		t.Errorf("Russian refund:\n%s", ru)
	}
}

func TestFormatShopDeliveryRefund_ShipmentLine(t *testing.T) {
	shipped := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	s := shopSample()
	s.Order.RefundedMinor = 23000
	s.Order.Status = "refunded"
	if m := FormatShopDeliveryRefund(s, 23000); !strings.Contains(m, "Do not ship. Stop the shipment") || strings.Contains(m, "Already shipped") ||
		!strings.Contains(m, "Rosé ×2") {
		t.Errorf("full refund, not shipped:\n%s", m)
	}
	s.Order.RefundedMinor = 5000
	s.Order.Status = "partially_refunded"
	if m := FormatShopDeliveryRefund(s, 5000); !strings.Contains(m, "Do not ship until the organizer confirms") {
		t.Errorf("partial refund, not shipped:\n%s", m)
	}
	s.Order.ShippedAt = &shipped
	m := FormatShopDeliveryRefund(s, 5000)
	if !strings.Contains(m, "Already shipped") || strings.Contains(m, "Do not ship") {
		t.Errorf("refund after shipping:\n%s", m)
	}
	for _, want := range []string{"🧾 Order #1000777", "Anna", "🛒 <b>Items</b>", "Rosé ×2", "Merlot (Magnum) ×1"} {
		if !strings.Contains(m, want) {
			t.Errorf("the delivery refund lacks %q:\n%s", want, m)
		}
	}
	s.Locale = "ru"
	s.Order.ShippedAt = nil
	s.Order.RefundedMinor = 23000
	s.Order.Status = "refunded"
	if ru := FormatShopDeliveryRefund(s, 23000); !strings.Contains(ru, "🛑 Не отправляйте. Остановите отправку: заказ возвращён полностью.") {
		t.Errorf("Russian stop line:\n%s", ru)
	}
	s.Order.ShippedAt = &shipped
	if ru := FormatShopDeliveryRefund(s, 23000); !strings.Contains(ru, "📮 Уже отправлен. Возврат посылку не остановит.") {
		t.Errorf("Russian shipped line:\n%s", ru)
	}
}

// Dispatcher scenarios over the matrix. A sales chat and a delivery group of
// one organization, plus a chat that holds both kinds.
func shopChats() []Subscription {
	return []Subscription{
		{ID: "sales", OrgID: orgVino, Name: "sales", ChatID: "-1", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindTickets, KindShopSale}},
		{ID: "delivery", OrgID: orgVino, Name: "delivery", ChatID: "-2", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindShopDelivery}},
	}
}

func sentTo(snd *fakeSender, chat string) []string {
	var out []string
	for _, m := range snd.sent {
		if m.chat == chat {
			out = append(out, m.text)
		}
	}
	return out
}

func TestDispatcher_ShopPaid_FactToSalesCardToDelivery_RetryIsSilent(t *testing.T) {
	store := newFakeStore(shopChats()...)
	store.shopOrds["o-1"] = shopSample()
	snd := &fakeSender{}
	d := NewDispatcher(store, snd, nil)
	ctx := context.Background()
	d.Handle(ctx, shopEvent(EventShopOrderPaid, "o-1", nil))
	d.Handle(ctx, shopEvent(EventShopOrderPaid, "o-1", nil)) // an outbox retry
	if len(snd.sent) != 2 {
		t.Fatalf("sent %d messages, want one per chat: %+v", len(snd.sent), snd.sent)
	}
	fact, card := sentTo(snd, "-1"), sentTo(snd, "-2")
	if len(fact) != 1 || !strings.Contains(fact[0], "Shop sale") || strings.Contains(fact[0], "anna@example.com") {
		t.Errorf("sales chat got: %v", fact)
	}
	if len(card) != 1 || !strings.Contains(card[0], "New shop order") || !strings.Contains(card[0], "anna@example.com") {
		t.Errorf("delivery group got: %v", card)
	}
	for _, k := range []string{"shop_sale_paid:o-1", "shop_paid:o-1"} {
		if !store.claimed[k] {
			t.Errorf("announcement %q was not claimed", k)
		}
	}
}

// A retry after only ONE of the two claims survived still sends the other one
// once and nothing twice: the keys are independent.
func TestDispatcher_ShopPaid_KeysAreIndependent(t *testing.T) {
	store := newFakeStore(shopChats()...)
	store.shopOrds["o-1"] = shopSample()
	store.claimed["shop_paid:o-1"] = true // the card went out before this change
	snd := &fakeSender{}
	d := NewDispatcher(store, snd, nil)
	d.Handle(context.Background(), shopEvent(EventShopOrderPaid, "o-1", nil))
	if len(snd.sent) != 1 || snd.sent[0].chat != "-1" {
		t.Fatalf("want only the fact to the sales chat: %+v", snd.sent)
	}
}

func TestDispatcher_ShopPaid_BothKindsGetOneCard(t *testing.T) {
	store := newFakeStore(Subscription{ID: "both", OrgID: orgVino, ChatID: "-1", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindShopSale, KindShopDelivery}})
	store.shopOrds["o-1"] = shopSample()
	snd := &fakeSender{}
	d := NewDispatcher(store, snd, nil)
	d.Handle(context.Background(), shopEvent(EventShopOrderPaid, "o-1", nil))
	if len(snd.sent) != 1 || !strings.Contains(snd.sent[0].text, "New shop order") {
		t.Fatalf("a chat with both kinds must get the card alone: %+v", snd.sent)
	}
	if store.claimed["shop_sale_paid:o-1"] {
		t.Error("nobody heard the fact, so it must not be claimed")
	}
}

func TestDispatcher_ShopDigitalOrder_FactOnly(t *testing.T) {
	store := newFakeStore(append(shopChats(),
		Subscription{ID: "both", OrgID: orgVino, Name: "both", ChatID: "-3", OnOrderPaid: true, OnTicketRefunded: true, Kinds: []string{KindShopSale, KindShopDelivery}})...)
	o := shopSample()
	o.Order.DeliveryKind = "none"
	o.Order.RefundedMinor = 5000
	store.shopOrds["o-1"] = o
	snd := &fakeSender{}
	d := NewDispatcher(store, snd, nil)
	ctx := context.Background()
	d.Handle(ctx, shopEvent(EventShopOrderPaid, "o-1", nil))
	d.Handle(ctx, shopEvent(EventShopOrderRefunded, "o-1", map[string]any{"refunded_minor": float64(5000), "refunded_now": float64(5000)}))
	if got := sentTo(snd, "-2"); len(got) != 0 {
		t.Errorf("the delivery group heard a digital order: %v", got)
	}
	for _, chat := range []string{"-1", "-3"} {
		got := sentTo(snd, chat)
		if len(got) != 2 || !strings.Contains(got[0], "Shop sale") || !strings.Contains(got[1], "Shop refund") {
			t.Errorf("chat %s got %v, want the fact of the sale and of the refund", chat, got)
		}
	}
	if store.claimed["shop_paid:o-1"] || store.claimed["shop_refund:o-1:5000"] {
		t.Errorf("the card was claimed although it was never sent: %v", store.claimed)
	}
}

func TestDispatcher_ShopRefund_BothChatsAndShipmentLine(t *testing.T) {
	store := newFakeStore(shopChats()...)
	o := shopSample()
	o.Order.RefundedMinor = 23000
	o.Order.Status = "refunded"
	store.shopOrds["o-1"] = o
	snd := &fakeSender{}
	d := NewDispatcher(store, snd, nil)
	ctx := context.Background()
	ev := shopEvent(EventShopOrderRefunded, "o-1", map[string]any{"refunded_minor": float64(23000), "refunded_now": float64(23000)})
	d.Handle(ctx, ev)
	d.Handle(ctx, ev)
	fact, card := sentTo(snd, "-1"), sentTo(snd, "-2")
	if len(fact) != 1 || !strings.Contains(fact[0], "Refunded in full") || strings.Contains(fact[0], "Anna") {
		t.Errorf("sales chat got: %v", fact)
	}
	if len(card) != 1 || !strings.Contains(card[0], "Do not ship") || !strings.Contains(card[0], "Rosé ×2") {
		t.Errorf("delivery group got: %v", card)
	}
	for _, k := range []string{"shop_sale_refund:o-1:23000", "shop_refund:o-1:23000"} {
		if !store.claimed[k] {
			t.Errorf("announcement %q was not claimed", k)
		}
	}
}

func TestDispatcher_ShopMode(t *testing.T) {
	shipped := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		mode orgmodules.Mode
		ev   string
		edit func(*ShopOrder)
		want int
	}{
		{"full, paid", orgmodules.ModeFull, EventShopOrderPaid, nil, 2},
		{"orders only, paid", orgmodules.ModeOrdersOnly, EventShopOrderPaid, nil, 2},
		{"none, paid", orgmodules.ModeNone, EventShopOrderPaid, nil, 0},
		{"none, review", orgmodules.ModeNone, EventShopOrderManualReview, nil, 0},
		{"full, review", orgmodules.ModeFull, EventShopOrderManualReview, nil, 1},
		{"none, refund of a parked parcel", orgmodules.ModeNone, EventShopOrderRefunded, func(o *ShopOrder) { o.Order.RefundedMinor = 23000 }, 2},
		{"none, refund after shipping", orgmodules.ModeNone, EventShopOrderRefunded, func(o *ShopOrder) { o.Order.RefundedMinor = 23000; o.Order.ShippedAt = &shipped }, 0},
		{"none, refund of a digital order", orgmodules.ModeNone, EventShopOrderRefunded, func(o *ShopOrder) { o.Order.RefundedMinor = 23000; o.Order.DeliveryKind = "none" }, 0},
	} {
		store := newFakeStore(shopChats()...)
		store.modes = map[string]orgmodules.Mode{orgVino: c.mode}
		o := shopSample()
		if c.edit != nil {
			c.edit(&o)
		}
		store.shopOrds["o-1"] = o
		snd := &fakeSender{}
		d := NewDispatcher(store, snd, nil)
		d.Handle(context.Background(), shopEvent(c.ev, "o-1", map[string]any{"refunded_minor": float64(o.Order.RefundedMinor), "refunded_now": float64(o.Order.RefundedMinor)}))
		if len(snd.sent) != c.want {
			t.Errorf("%s: sent %d messages, want %d: %+v", c.name, len(snd.sent), c.want, snd.sent)
		}
		if c.want == 0 && len(store.claimed) != 0 {
			t.Errorf("%s: nothing was sent, so nothing may be claimed: %v", c.name, store.claimed)
		}
	}
}

// Operator rows (no org_id) follow the same kinds.
func TestDispatcher_ShopOperatorRowsFollowTheKinds(t *testing.T) {
	store := newFakeStore(
		Subscription{ID: "op-sale", ChatID: "-1", OnOrderPaid: true, Kinds: []string{KindShopSale}},
		Subscription{ID: "op-delivery", ChatID: "-2", OnOrderPaid: true, Kinds: []string{KindShopDelivery}},
		Subscription{ID: "op-tickets", ChatID: "-3", OnOrderPaid: true, Kinds: []string{KindTickets}},
	)
	store.shopOrds["o-1"] = shopSample()
	snd := &fakeSender{}
	NewDispatcher(store, snd, nil).Handle(context.Background(), shopEvent(EventShopOrderPaid, "o-1", nil))
	if len(snd.sent) != 2 || len(sentTo(snd, "-1")) != 1 || len(sentTo(snd, "-2")) != 1 || len(sentTo(snd, "-3")) != 0 {
		t.Fatalf("operator rows: %+v", snd.sent)
	}
}

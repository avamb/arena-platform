package eventbot

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func listItem(t *testing.T, status string, total int, name string) openapi.OrderListItem {
	t.Helper()
	raw := `{"id":"` + uuid.NewString() + `","system_id":1000000500,"status":"` + status + `","total":` + itoa(total) + `,"currency":"EUR",
	  "created_at":"2099-01-15T19:30:00Z","session_timezone":"Europe/Madrid","event_name":"Swan","session_start_at":"2099-02-01T19:00:00Z"`
	if name != "" {
		raw += `,"buyer_name":"` + name + `"`
	}
	raw += `}`
	var o openapi.OrderListItem
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return o
}

func TestOrderChip(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]string{
		"paid": "✅", "pending_payment": "⏳", "expired": "⌛", "cancelled": "✖",
		"partially_refunded": "↩", "refunded": "↩", "manual_review": "⚠", "": "·", "weird": "·",
	} {
		if got := OrderChip(status); got != want {
			t.Errorf("OrderChip(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestOrderRowLabel(t *testing.T) {
	t.Parallel()
	// 19:30 UTC on 15 January is 20:30 in Madrid: still the 15th.
	got := OrderRowLabel(listItem(t, "paid", 4700, "Buyer Name"), "en")
	if got != "№1000000500 · ✅ · 47 EUR · Buyer Name · 15.01" {
		t.Errorf("row = %q", got)
	}
	if got := OrderRowLabel(listItem(t, "pending_payment", 2550, ""), "ru"); got != "№1000000500 · ⏳ · 25,50 EUR · 15.01" {
		t.Errorf("row without a name = %q", got)
	}
	long := OrderRowLabel(listItem(t, "paid", 100, "A very long buyer name indeed"), "en")
	if !strings.Contains(long, "…") || len([]rune(long)) > 60 {
		t.Errorf("a long name must be cut: %q", long)
	}
}

func TestOrderAmountHTML_StrikesOnlyAFullRefund(t *testing.T) {
	t.Parallel()
	if got := OrderAmountHTML("refunded", 4700, "EUR", "en"); got != "<s>47 EUR</s>" {
		t.Errorf("refunded = %q", got)
	}
	for _, status := range []string{"paid", "partially_refunded", "pending_payment", "cancelled"} {
		if got := OrderAmountHTML(status, 4700, "EUR", "en"); got != "47 EUR" {
			t.Errorf("%s = %q, must not be struck through", status, got)
		}
	}
}

func TestNormalizeE164(t *testing.T) {
	t.Parallel()
	valid := map[string]string{
		"+34 600 111 222":    "+34600111222",
		"+34-600.111.222":    "+34600111222",
		"+1 (415) 555-2671":  "+14155552671",
		"0034600111222":      "+34600111222",
		"  +380 44 123 4567": "+380441234567",
		"+7 495 123-45-67":   "+74951234567",
	}
	for in, want := range valid {
		if got, ok := NormalizeE164(in); !ok || got != want {
			t.Errorf("NormalizeE164(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{
		"", "600111222", "34600111222", "+0 600 111 222", "+34 600 111 222 333 444", "+34 6", "+34 abc 222 111",
		"+34 600 111 222 ext 5", "tel:+34600111222", "++34600111222", "+", "00",
	} {
		if got, ok := NormalizeE164(in); ok {
			t.Errorf("NormalizeE164(%q) = %q, must be invalid", in, got)
		}
	}
}

func TestPhoneHTML_LinksOnlyForValidE164(t *testing.T) {
	t.Parallel()
	html, plain := PhoneHTML("+34 600 111 222", "call", "WhatsApp")
	for _, want := range []string{`href="tel:+34600111222"`, `href="https://wa.me/34600111222"`, "+34 600 111 222"} {
		if !strings.Contains(html, want) {
			t.Errorf("html lacks %q: %s", want, html)
		}
	}
	if plain != "+34 600 111 222" || strings.Contains(plain, "<a") {
		t.Errorf("plain = %q", plain)
	}
	html, plain = PhoneHTML("600 111 222", "call", "WhatsApp")
	if strings.Contains(html, "<a") || html != "600 111 222" || plain != html {
		t.Errorf("a national number must get no link: %q", html)
	}
	// What the buyer typed is escaped, never trusted as markup.
	html, _ = PhoneHTML(`<b>1</b>`, "call", "WhatsApp")
	if strings.Contains(html, "<b>") {
		t.Errorf("phone not escaped: %q", html)
	}
}

func TestUnpaidReasonKey(t *testing.T) {
	t.Parallel()
	cases := []struct{ status, reason, want string }{
		{"paid", "", ""},
		{"partially_refunded", "", ""},
		{"refunded", "payment_failed", ""},
		{"expired", "payment_failed", "bot.ord.reason_payment_failed"},
		{"expired", "payment_abandoned", "bot.ord.reason_payment_abandoned"},
		{"expired", "hold_expired", "bot.ord.reason_hold_expired"},
		{"cancelled", "cancelled", "bot.ord.reason_cancelled"},
		{"pending_payment", "awaiting_payment", "bot.ord.reason_awaiting_payment"},
		{"manual_review", "manual_review", "bot.ord.reason_manual_review"},
		// an empty or newer reason falls back to the status, never to a raw code
		{"pending_payment", "", "bot.ord.reason_awaiting_payment"},
		{"cancelled", "", "bot.ord.reason_cancelled"},
		{"expired", "from_the_future", "bot.ord.reason_hold_expired"},
		{"abandoned", "", "bot.ord.reason_unknown"},
	}
	for _, c := range cases {
		if got := UnpaidReasonKey(c.status, c.reason); got != c.want {
			t.Errorf("UnpaidReasonKey(%q, %q) = %q, want %q", c.status, c.reason, got, c.want)
		}
	}
}

func TestPagesFor(t *testing.T) {
	t.Parallel()
	for total, want := range map[int64]int{-1: 1, 0: 1, 1: 1, 5: 1, 6: 2, 10: 2, 11: 3} {
		if got := PagesFor(total, 5); got != want {
			t.Errorf("PagesFor(%d, 5) = %d, want %d", total, got, want)
		}
	}
}

func TestOrdersTabCodesRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tab := range []string{ordTabRecent, ordTabPaid, ordTabUnpaid} {
		got, ok := tabFromCode(tabCode(tab))
		if !ok || got != tab {
			t.Errorf("tab %q -> %q -> %q", tab, tabCode(tab), got)
		}
	}
	if _, ok := tabFromCode("z"); ok {
		t.Error("an unknown tab code must be refused")
	}
}

func TestIsOrdersCallback(t *testing.T) {
	t.Parallel()
	for prefix, want := range map[string]bool{"or": true, "noop": true, "el": false, "home": false, "ec": false, "ses": false} {
		if got := isOrdersCallback(prefix); got != want {
			t.Errorf("isOrdersCallback(%q) = %v", prefix, got)
		}
	}
}

func TestOrdersDialog_StateSurvivesJSON(t *testing.T) {
	t.Parallel()
	event, back := uuid.New(), uuid.New()
	card := uuid.New()
	in := newOrdersDialog(uuid.New())
	in.Tab, in.Query, in.Page = ordTabUnpaid, "buyer@example.test", 3
	in.EventID, in.BackEvent, in.ScopeName = &event, &back, "Swan Lake"
	in.IDs, in.CardID, in.MsgID = []uuid.UUID{uuid.New(), uuid.New()}, &card, 77
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out ordersDialog
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Tab != in.Tab || out.Query != in.Query || out.Page != 3 || *out.EventID != event || *out.BackEvent != back ||
		out.ScopeName != "Swan Lake" || len(out.IDs) != 2 || *out.CardID != card || out.MsgID != 77 || out.SessionID != nil {
		t.Errorf("round trip changed the state: %+v", out)
	}
	if d := newOrdersDialog(uuid.New()); d.Tab != ordTabRecent || d.Page != 1 {
		t.Errorf("default dialog = %+v", d)
	}
}

// The query string of the search holds what the organizer typed — a buyer's
// e-mail or phone. An error that reaches a log line must not carry it.
func TestRouteOf_DropsTheSearchTextFromErrors(t *testing.T) {
	t.Parallel()
	if got := routeOf("/v1/organizations/o/orders?q=buyer%40example.test&tab=paid"); got != "/v1/organizations/o/orders" {
		t.Errorf("routeOf = %q", got)
	}
	inner := errors.New("connection refused")
	wrapped := &url.Error{Op: "Get", URL: "http://api/v1/orders?q=buyer@example.test", Err: inner}
	if got := unwrapURLError(wrapped); got != inner || strings.Contains(got.Error(), "buyer@") {
		t.Errorf("unwrapURLError = %v", got)
	}
	if got := unwrapURLError(inner); got != inner {
		t.Errorf("a plain error must pass through, got %v", got)
	}
}

// ─── the card ─────────────────────────────────────────────────────────────────

func orderDetail(t *testing.T, status, reason string, tickets int, extra string) openapi.OrderDetail {
	t.Helper()
	var tk []string
	for i := 0; i < tickets; i++ {
		used := `null`
		if i == 0 {
			used = `"2099-02-01T19:20:00Z"`
		}
		tk = append(tk, `{"id":"`+uuid.NewString()+`","barcode":"46001234567`+string(rune('0'+i%10))+string(rune('0'+i/10))+`","tier_name":"Standing <b>","price":2350,"currency":"EUR",
		   "status":"active","used_at":`+used+`,"seat_label":null,"issued_at":"2099-01-15T19:31:00Z","item_id":"`+uuid.NewString()+`","tier_id":"`+uuid.NewString()+`","system_ticket_id":1,"cancelled_at":null}`)
	}
	raw := `{"id":"` + uuid.NewString() + `","system_id":1000000500,"status":"` + status + `","currency":"EUR","total":4700,
	  "created_at":"2099-01-15T19:30:00Z","unpaid_reason":"` + reason + `","delivery_state":"sent",
	  "buyer_name":"Anna <Buyer>","buyer_email":"anna@example.test","buyer_phone":"+34 600 111 222",
	  "channel":{"id":"` + uuid.NewString() + `","kind":"hosted_page","name":"Main page"},
	  "tickets":[` + strings.Join(tk, ",") + `]` + extra + `}`
	var d openapi.OrderDetail
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return d
}

var testMeta = &orderMeta{EventName: "Swan <Lake>", StartAt: time.Date(2099, 2, 1, 19, 0, 0, 0, time.UTC), TZ: "Europe/Madrid"}

func TestOrderCard_PaidShowsBuyerTicketsChannelAndDelivery(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	d := orderDetail(t, "paid", "", 2, `,"payment":{"provider":"stripe","amount":4700,"currency":"EUR","id":"`+uuid.NewString()+`","created_at":"2099-01-15T19:30:00Z"}`)
	got := b.orderCardText("en", d, testMeta, true, false)
	for _, want := range []string{
		"Order №1000000500", "✅ Paid · 47 EUR · 15.01.2099 20:30",
		"Swan &lt;Lake&gt;", "01.02.2099 20:00",
		"<b>Anna &lt;Buyer&gt;</b>", "✉ anna@example.test", `href="tel:+34600111222"`, `href="https://wa.me/34600111222"`,
		"Tickets: 2", "Standing &lt;b&gt; · 23.50 EUR · <code>460012345670", "· valid · ✔ entered 01.02.2099 20:20",
		"the event page on Arena (Main page)", "Payment: stripe", "The tickets were e-mailed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("card lacks %q:\n%s", want, got)
		}
	}
	// A paid order has no "why it is unpaid" text.
	if strings.Contains(got, "not paid") || strings.Contains(got, "did not go through") {
		t.Errorf("a paid card explains an unpaid reason:\n%s", got)
	}
	// Only the first ticket was scanned.
	if strings.Count(got, "✔ entered") != 1 {
		t.Errorf("entered marks:\n%s", got)
	}
}

func TestOrderCard_RefundedIsStruckThrough(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	got := b.orderCardText("en", orderDetail(t, "refunded", "", 1, ""), testMeta, true, false)
	if !strings.Contains(got, "↩ Refunded · <s>47 EUR</s>") {
		t.Errorf("a refunded card must strike the amount:\n%s", got)
	}
}

func TestOrderCard_UnpaidExplainsWhyWithTheProvidersWords(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	d := orderDetail(t, "expired", "payment_failed", 0,
		`,"payment":{"provider":"stripe","amount":4700,"currency":"EUR","id":"`+uuid.NewString()+`","created_at":"2099-01-15T19:30:00Z","failure_code":"card_declined","failure_message":"Your card <was> declined."}`)
	d.DeliveryState = "none"
	got := b.orderCardText("en", d, testMeta, true, false)
	for _, want := range []string{"⌛ Expired", "The payment did not go through", "Message from the payment provider: Your card &lt;was&gt; declined."} {
		if !strings.Contains(got, want) {
			t.Errorf("card lacks %q:\n%s", want, got)
		}
	}
	// No tickets and no delivery line for an unpaid order.
	if strings.Contains(got, "Tickets:") || strings.Contains(got, "e-mailed") || strings.Contains(got, "No tickets issued") {
		t.Errorf("an unpaid card talks about tickets:\n%s", got)
	}
	// The provider's code stands in when it sent no message.
	code := "card_declined"
	d.Payment.FailureMessage = nil
	d.Payment.FailureCode = &code
	if got := b.orderCardText("en", d, testMeta, true, false); !strings.Contains(got, "provider: card_declined") {
		t.Errorf("failure code missing:\n%s", got)
	}
}

func TestOrderCard_ContactsOnlyInAPrivateChat(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	d := orderDetail(t, "paid", "", 1, "")
	got := b.orderCardText("en", d, testMeta, false, false)
	for _, leak := range []string{"anna@example.test", "600 111 222", "Anna", "tel:", "wa.me"} {
		if strings.Contains(got, leak) {
			t.Errorf("a group chat saw %q:\n%s", leak, got)
		}
	}
	if !strings.Contains(got, "only in a private chat") {
		t.Errorf("the group card must say why:\n%s", got)
	}
}

func TestOrderCard_PlainVersionHasNoLinks(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	d := orderDetail(t, "paid", "", 1, "")
	plain := b.orderCardText("en", d, testMeta, true, true)
	if strings.Contains(plain, "<a ") || strings.Contains(plain, "wa.me") || !strings.Contains(plain, "+34 600 111 222") {
		t.Errorf("plain card:\n%s", plain)
	}
}

func TestOrderCard_BadPhoneGetsNoLinkAndMissingFieldsAreSkipped(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	d := orderDetail(t, "paid", "", 1, "")
	phone := "600 111 222" // no country code
	d.BuyerPhone, d.BuyerEmail, d.BuyerName = &phone, nil, nil
	got := b.orderCardText("en", d, nil, true, false)
	if strings.Contains(got, "<a href") || !strings.Contains(got, "📞 600 111 222") || strings.Contains(got, "✉") || !strings.Contains(got, "Buyer not specified") {
		t.Errorf("card:\n%s", got)
	}
	// Without the list row there is no event block, and the date is in UTC.
	if strings.Contains(got, "🎭") || !strings.Contains(got, "UTC") {
		t.Errorf("a card without meta:\n%s", got)
	}
}

func TestOrderCard_ManyTicketsAreCapped(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	got := b.orderCardText("en", orderDetail(t, "paid", "", maxOrderTickets+5, ""), testMeta, true, false)
	if strings.Count(got, "<code>") != maxOrderTickets || !strings.Contains(got, "… and 5 more") {
		t.Errorf("ticket cap:\n%s", got)
	}
}

func TestOrderCardKeyboard_CancelOnlyForAnOrderWaitingForPayment(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	for status, wantCancel := range map[string]bool{
		"pending_payment": true, "paid": false, "expired": false, "cancelled": false,
		"refunded": false, "partially_refunded": false, "manual_review": false,
	} {
		d := orderDetail(t, status, "", 0, "")
		kb := b.orderCardKeyboard("en", d)
		assertCallbacksFit(t, status, kb.InlineKeyboard)
		has := false
		for _, row := range kb.InlineKeyboard {
			for _, btn := range row {
				if btn.CallbackData == "or:c:"+d.Id.String() {
					has = true
				}
			}
		}
		if has != wantCancel {
			t.Errorf("%s: cancel button = %v, want %v", status, has, wantCancel)
		}
		last := kb.InlineKeyboard[len(kb.InlineKeyboard)-1]
		if len(last) != 2 || last[0].CallbackData != "or:b" || last[1].CallbackData != "home" {
			t.Errorf("%s: the last row must be Back and Home: %+v", status, last)
		}
	}
}

func TestOrderCallbackPayloadsFitTelegram(t *testing.T) {
	t.Parallel()
	id := uuid.NewString()
	for _, data := range []string{"or:new", "or:e:" + id, "or:s:" + id, "or:c:" + id, "or:v:" + id, "or:o:4", "or:p:99", "or:t:u", "or:x", "or:a", "or:b", "ec:o:" + id} {
		if n := len([]byte(data)); n > 64 {
			t.Errorf("%q is %d bytes, over Telegram's 64", data, n)
		}
	}
}

// Every Orders text renders on every language without a raw key or an unfilled
// template value (the events screens and the wizard have the same test).
func TestOrdersScreens_RenderOnEveryLanguage(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	for _, loc := range SupportedLocales {
		screens := map[string]string{}
		for _, status := range []string{"paid", "pending_payment", "expired", "cancelled", "refunded", "partially_refunded", "manual_review"} {
			d := orderDetail(t, status, map[string]string{"expired": "payment_abandoned", "manual_review": "manual_review"}[status], 2, "")
			screens["card/"+status] = b.orderCardText(loc, d, testMeta, true, false)
		}
		for _, reason := range []string{"payment_failed", "payment_abandoned", "hold_expired", "cancelled", "awaiting_payment", "manual_review", "weird"} {
			screens["reason/"+reason] = b.orderCardText(loc, orderDetail(t, "expired", reason, 0, ""), testMeta, true, false)
		}
		for _, kind := range []string{"site", "hosted_page", "widget", "", "x"} {
			d := orderDetail(t, "paid", "", 1, "")
			d.Channel.Kind = kind
			screens["channel/"+kind] = b.orderCardText(loc, d, testMeta, true, false)
		}
		for _, state := range []string{"sent", "pending", "failed", "none"} {
			d := orderDetail(t, "paid", "", 1, "")
			d.DeliveryState = state
			screens["delivery/"+state] = b.orderCardText(loc, d, testMeta, true, false)
		}
		screens["group"] = b.orderCardText(loc, orderDetail(t, "paid", "", 1, ""), testMeta, false, false)
		screens["notickets"] = b.orderCardText(loc, orderDetail(t, "paid", "", 0, ""), testMeta, true, false)
		screens["cancel_ask"] = b.texts.T(loc, "bot.ord.cancel_ask", map[string]any{"Num": 1, "Amount": "1 EUR", "Word": b.sesCancelWord(loc)})
		screens["cancel_wrong"] = b.texts.T(loc, "bot.ord.cancel_wrong", map[string]any{"Word": b.sesCancelWord(loc)})
		for _, tab := range []string{ordTabRecent, ordTabPaid, ordTabUnpaid} {
			screens["empty/"+tab] = b.emptyOrdersText(loc, ordersDialog{Tab: tab})
			screens["tab/"+tab] = b.texts.T(loc, "bot.ord.tab_"+tab, nil)
		}
		screens["search_none"] = b.emptyOrdersText(loc, ordersDialog{Tab: ordTabRecent, Query: "zzz"})
		screens["title"] = b.texts.T(loc, "bot.ord.list_title", map[string]any{
			"Org": "O", "Scope": b.texts.T(loc, "bot.ord.scope_line", map[string]any{"Name": "N"}), "Tab": "t", "Total": 3,
			"Page": 1, "Pages": 1, "Search": "", "Legend": b.texts.T(loc, "bot.ord.legend", nil),
		})
		for name, text := range screens {
			if strings.TrimSpace(text) == "" || strings.Contains(text, "bot.ord.") || strings.Contains(text, "<no value>") || strings.Contains(text, "%!") {
				t.Errorf("%s/%s renders badly:\n%s", loc, name, text)
			}
		}
		if loc == "ru" && !strings.Contains(screens["card/paid"], "Заказ №1000000500") {
			t.Errorf("ru card:\n%s", screens["card/paid"])
		}
		if loc == "es" && !strings.Contains(screens["card/paid"], "Pedido №1000000500") {
			t.Errorf("es card:\n%s", screens["card/paid"])
		}
	}
}

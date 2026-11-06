package eventbot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func shopIdentityOf(role string, super bool, modules []string, mode string) *Identity {
	return &Identity{Current: &Membership{OrgID: uuid.New(), Role: role, Modules: modules, ShopMode: mode}, Superadmin: super}
}

// The shop is offered when the organization has the module (or orders to
// finish) and the role reads sales; nobody else sees it.
func TestShopVisibility_ModuleModeAndRole(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		id   *Identity
		mode string
		show bool
	}{
		{"no module, owner", shopIdentityOf("org_admin", false, nil, ""), "", false},
		{"no module, operator", shopIdentityOf("org_admin", true, []string{}, ""), "", false},
		{"module, owner", shopIdentityOf("org_admin", false, []string{"shop"}, "full"), "full", true},
		{"module, manager", shopIdentityOf("organizer", false, []string{"shop"}, "full"), "full", true},
		{"module, operator", shopIdentityOf("org_admin", true, []string{"shop"}, "full"), "full", true},
		{"module, agent", shopIdentityOf("agent", false, []string{"shop"}, "full"), "full", false},
		{"module without a mode is full", shopIdentityOf("organizer", false, []string{"shop"}, ""), "full", true},
		{"orders only, owner", shopIdentityOf("org_admin", false, nil, "orders_only"), "orders_only", true},
		{"orders only, manager", shopIdentityOf("organizer", false, nil, "orders_only"), "orders_only", true},
		{"orders only, agent", shopIdentityOf("agent", false, nil, "orders_only"), "orders_only", false},
		{"other module only", shopIdentityOf("organizer", false, []string{"other"}, ""), "", false},
	}
	b := ecTestBot(t)
	for _, c := range cases {
		if got := shopMode(c.id); got != c.mode {
			t.Errorf("%s: mode = %q, want %q", c.name, got, c.mode)
		}
		if got := shopVisible(c.id); got != c.show {
			t.Errorf("%s: visible = %v, want %v", c.name, got, c.show)
		}
		row := b.shopMenuRow("ru", c.id)
		if (row != nil) != c.show {
			t.Errorf("%s: menu row = %v, want shown=%v", c.name, row, c.show)
		}
		if row != nil && (row[0].CallbackData != "sh:home" || !strings.Contains(row[0].Text, "Магазин")) {
			t.Errorf("%s: row = %+v", c.name, row)
		}
	}
	if shopMode(nil) != "" || shopMode(&Identity{}) != "" {
		t.Error("no identity, no shop")
	}
}

// On the main menu the shop row sits right after the Orders row and is absent
// for an organization without the module.
func TestHomeKeyboard_ShopRowAfterOrders(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	rowsOf := func(id *Identity) []string {
		var out []string
		for _, row := range b.homeKeyboard(context.Background(), id, "").InlineKeyboard {
			for _, btn := range row {
				out = append(out, btn.CallbackData)
			}
		}
		return out
	}
	with := rowsOf(shopIdentityOf("organizer", false, []string{"shop"}, "full"))
	oi, si := -1, -1
	for i, d := range with {
		switch d {
		case "or:new":
			oi = i
		case "sh:home":
			si = i
		}
	}
	if oi < 0 || si != oi+1 {
		t.Errorf("shop must follow Orders: orders at %d, shop at %d in %v", oi, si, with)
	}
	for _, d := range rowsOf(shopIdentityOf("organizer", false, nil, "")) {
		if d == "sh:home" {
			t.Error("an organization without the module has no shop button")
		}
	}
	for _, d := range rowsOf(shopIdentityOf("agent", false, []string{"shop"}, "full")) {
		if d == "sh:home" {
			t.Error("an agent has no shop button")
		}
	}
}

func shopTestOrder(status string) openapi.ShopOrder {
	phone := "+34 600 111 222"
	pst := "08001"
	addr2 := "2º"
	reason := "late_payment_no_stock"
	shipped := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	o := openapi.ShopOrder{
		Id: openapi_types.UUID(uuid.New()), Number: 1000000501, Status: status, Currency: "EUR",
		Buyer:        openapi.ShopBuyer{Name: "Ana <b>", Email: "ana@example.test", Phone: &phone},
		Ship:         openapi.ShopAddress{Address1: "Calle Mayor 1", Address2: &addr2, City: "Barcelona", Postal: &pst, Country: "ES"},
		DeliveryKind: "shipping", Subtotal: 4000, ShippingFee: 500, Total: 4500,
		CreatedAt: time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC),
		Items:     []openapi.ShopOrderItem{{ProductTitle: "Wine", VariantTitle: "Red 0.75", Qty: 2, TotalMinor: 4000}},
		Payment:   &openapi.ShopOrderPayment{Provider: "stripe", State: "succeeded"},
	}
	if status == shStatusManual {
		o.ReviewReason = &reason
	}
	if status == shStatusShipped {
		o.ShippedAt = &shipped
		o.TrackingNote = "TRACK-1"
	}
	return o
}

func shopCardButtons(t *testing.T, b *Bot, o openapi.ShopOrder) []string {
	t.Helper()
	var out []string
	for _, row := range b.shopCardKeyboard("ru", o).InlineKeyboard {
		for _, btn := range row {
			out = append(out, btn.CallbackData)
		}
	}
	return out
}

func hasPrefixIn(list []string, prefix string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// The card names the order, the buyer, the address, the lines and the money,
// and escapes what the buyer typed.
func TestShopCardText_ContentAndEscaping(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	o := shopTestOrder(shStatusPaid)
	o.DeliveryNote = "Ring <twice>"
	o.StaffNote = openapi.ShopStaffNote{Text: "call first", By: "Marina"}
	text := b.shopCardText("ru", o, "Europe/Madrid", true)
	for _, want := range []string{
		"№1000000501", "Оплачен", "45 EUR", "02.11.2026 10:30",
		"Ana &lt;b&gt;", "+34 600 111 222", "ana@example.test",
		"Calle Mayor 1", "08001 Barcelona", "Wine — Red 0.75 × 2",
		"Доставка: 5 EUR", "Итого: 45 EUR", "stripe · succeeded",
		"Ring &lt;twice&gt;", "call first", "Marina",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("card lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Ana <b>") {
		t.Error("the buyer's name must be escaped")
	}
	// A group chat gets no contacts and no address.
	group := b.shopCardText("ru", o, "Europe/Madrid", false)
	for _, leak := range []string{"ana@example.test", "+34 600", "Calle Mayor", "Ring"} {
		if strings.Contains(group, leak) {
			t.Errorf("a group card shows %q", leak)
		}
	}
	// No delivery and pickup say so.
	o.DeliveryKind = "none"
	if got := b.shopCardText("ru", o, "", true); !strings.Contains(got, "Без доставки") || strings.Contains(got, "Calle Mayor") {
		t.Errorf("no-delivery card:\n%s", got)
	}
	o.DeliveryKind = "pickup"
	if got := b.shopCardText("ru", o, "", true); !strings.Contains(got, "Самовывоз") {
		t.Errorf("pickup card:\n%s", got)
	}
	// A shipped order shows the date and the tracking number.
	shipped := b.shopCardText("en", shopTestOrder(shStatusShipped), "", true)
	if !strings.Contains(shipped, "Tracking number: TRACK-1") || !strings.Contains(shipped, "03.11.2026 10:00 UTC") {
		t.Errorf("shipped card:\n%s", shipped)
	}
}

// The buttons of a card follow the order's state.
func TestShopCardKeyboard_ByStatus(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)

	paid := shopCardButtons(t, b, shopTestOrder(shStatusPaid))
	for _, p := range []string{"sh:sh:", "sh:cr:", "sh:nt:", "sh:rs:r:", "sh:f:s:", "sh:w:o:", "sh:b"} {
		if !hasPrefixIn(paid, p) {
			t.Errorf("paid order lacks %q: %v", p, paid)
		}
	}
	for _, p := range []string{"sh:cx:", "sh:rv:"} {
		if hasPrefixIn(paid, p) {
			t.Errorf("paid order must not offer %q: %v", p, paid)
		}
	}

	unpaid := shopTestOrder(shStatusPending)
	unpaid.Payment = nil
	pending := shopCardButtons(t, b, unpaid)
	if !hasPrefixIn(pending, "sh:cx:") || hasPrefixIn(pending, "sh:f:s:") || hasPrefixIn(pending, "sh:sh:") || hasPrefixIn(pending, "sh:rs:") {
		t.Errorf("unpaid order: %v", pending)
	}

	review := shopCardButtons(t, b, shopTestOrder(shStatusManual))
	if !hasPrefixIn(review, "sh:rv:f:") || !hasPrefixIn(review, "sh:rv:r:") {
		t.Errorf("late-payment review order must offer both decisions: %v", review)
	}
	unconfirmed := shopTestOrder(shStatusManual)
	other := "payment_unconfirmed"
	unconfirmed.ReviewReason = &other
	if got := shopCardButtons(t, b, unconfirmed); hasPrefixIn(got, "sh:rv:") || hasPrefixIn(got, "sh:sh:") {
		t.Errorf("an unconfirmed payment has nothing to decide or ship: %v", got)
	}

	shipped := shopCardButtons(t, b, shopTestOrder(shStatusShipped))
	if !hasPrefixIn(shipped, "sh:sh:") { // the same press edits the tracking number
		t.Errorf("shipped order keeps the tracking edit: %v", shipped)
	}
	if !hasPrefixIn(shipped, "sh:rm:") { // received + shipped letters: a choice
		t.Errorf("shipped order chooses among letters: %v", shipped)
	}

	none := shopTestOrder(shStatusPaid)
	none.DeliveryKind = "none"
	if got := shopCardButtons(t, b, none); hasPrefixIn(got, "sh:sh:") || hasPrefixIn(got, "sh:cr:") {
		t.Errorf("an order with no delivery has nothing to ship: %v", got)
	}

	refunded := shopTestOrder(shStatusRefunded)
	refunded.RefundedMinor = refunded.Total
	if got := shopCardButtons(t, b, refunded); hasPrefixIn(got, "sh:f:s:") {
		t.Errorf("a fully refunded order cannot be refunded again: %v", got)
	}

	// Every payload fits Telegram's 64 bytes.
	for _, d := range append(append(paid, review...), shipped...) {
		if len(d) > 64 {
			t.Errorf("callback data %q is %d bytes", d, len(d))
		}
	}
}

func TestShopRowLabel_AndTabs(t *testing.T) {
	t.Parallel()
	o := shopTestOrder(shStatusShipped)
	o.Buyer.Name = "A very long buyer name indeed"
	got := shopRowLabel(o, "ru", "Europe/Madrid")
	if !strings.HasPrefix(got, "№1000000501 · 📦 · 45 EUR · ") || !strings.HasSuffix(got, " · 02.11") {
		t.Errorf("row = %q", got)
	}
	if strings.Contains(got, "indeed") {
		t.Errorf("the name must be cut: %q", got)
	}
	for _, code := range []string{"s", "r", "u", "a"} {
		tab, ok := shopTabFromCode(code)
		if !ok || shopTabCode(tab) != code {
			t.Errorf("tab code %q does not round-trip: %q", code, tab)
		}
	}
	if _, ok := shopTabFromCode("z"); ok {
		t.Error("unknown tab code accepted")
	}
}

func TestShopLetters_ByOrderState(t *testing.T) {
	t.Parallel()
	if got := shopLetters(shopTestOrder(shStatusPaid)); strings.Join(got, ",") != "received" {
		t.Errorf("paid: %v", got)
	}
	if got := shopLetters(shopTestOrder(shStatusShipped)); strings.Join(got, ",") != "received,shipped" {
		t.Errorf("shipped: %v", got)
	}
	ref := shopTestOrder(shStatusPartRefund)
	ref.RefundedMinor = 100
	if got := shopLetters(ref); strings.Join(got, ",") != "received,refunded" {
		t.Errorf("partly refunded: %v", got)
	}
	if got := shopLetters(shopTestOrder(shStatusPending)); got != nil {
		t.Errorf("unpaid has no letters: %v", got)
	}
	noMail := shopTestOrder(shStatusPaid)
	noMail.Buyer.Email = ""
	if got := shopLetters(noMail); got != nil {
		t.Errorf("no e-mail, no letters: %v", got)
	}
}

func TestShopWebTarget(t *testing.T) {
	t.Parallel()
	org, order := uuid.New(), uuid.New()
	if got := shopWebTarget(org, "h", nil); got != "/o/"+org.String()+"/shop" {
		t.Errorf("home: %q", got)
	}
	if got := shopWebTarget(org, "s", nil); got != "/o/"+org.String()+"/shop/settings" {
		t.Errorf("settings: %q", got)
	}
	if got := shopWebTarget(org, "o", &order); got != "/o/"+org.String()+"/shop/orders/"+order.String() {
		t.Errorf("order: %q", got)
	}
	if got := shopWebTarget(org, "o", nil); got != "/o/"+org.String()+"/shop" {
		t.Errorf("order without an id falls back to the shop: %q", got)
	}
}

func TestShopBlockersText_PlainWords(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	text := b.shopBlockersText("ru", []string{"no_active_product", "payment_not_configured", "settings_not_saved", "weird"})
	for _, want := range []string{"нет ни одного активного товара", "онлайн-оплата не подключена", "настройки магазина ещё не сохранены", "не хватает чего-то ещё"} {
		if !strings.Contains(text, want) {
			t.Errorf("blockers lack %q:\n%s", want, text)
		}
	}
}

// ─── the refund key ───────────────────────────────────────────────────────────

func newShopMoneyBot(t *testing.T, apiURL string) *Bot {
	t.Helper()
	clock := &testClock{t: time.Now()}
	return &Bot{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		dialogs: newMemDialogStore(clock),
		arena:   NewArenaClient(apiURL, "", &http.Client{Timeout: 5 * time.Second}),
		minter:  NewTokenMinter("test-secret-test-secret-0123456789", "iss", "aud"),
		texts:   ecTestBot(t).texts,
	}
}

// A refund call that gets no answer is repeated with the SAME Idempotency-Key
// and the same body, so the server answers the first operation and never
// starts a second one.
func TestCallShopMoney_RepeatsWithTheSameKey(t *testing.T) {
	old := refundReplayPause
	refundReplayPause = time.Millisecond
	defer func() { refundReplayPause = old }()

	var mu sync.Mutex
	var keys, bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		bodies = append(bodies, string(raw))
		n := len(keys)
		mu.Unlock()
		if n < 3 {
			// No answer: the connection is cut.
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"order":{"id":"` + uuid.NewString() + `","number":1,"status":"refunded","currency":"EUR","total":4500,"refunded_minor":4500,"items":[],"buyer":{"name":"","email":""},"ship":{"address1":"","city":"","country":""},"staff_note":{"text":"","by":"","at":null}},"refund":{"id":"` + uuid.NewString() + `","state":"succeeded","amount":4500,"currency":"EUR"}}`))
	}))
	defer srv.Close()

	b := newShopMoneyBot(t, srv.URL)
	amount := int64(4500)
	st := shopRefundDialog{OrgID: uuid.New(), OrderID: uuid.New(), Num: 1, Currency: "EUR", Mode: shRefModeRefund, Left: 4500, Amount: &amount, Reason: "damaged", Key: "tg-fixed-key"}
	id := &Identity{Link: gen.BotTelegramLinkRow{UserID: uuid.New(), TelegramUserID: 42}}
	res := b.callShopMoney(context.Background(), id, st)
	if res.Err != nil || res.Unknown || res.Refund == nil || res.Refund.State != "succeeded" {
		t.Fatalf("result = %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 3 {
		t.Fatalf("calls = %d, want 3", len(keys))
	}
	for i := range keys {
		if keys[i] != "tg-fixed-key" || bodies[i] != bodies[0] {
			t.Errorf("attempt %d: key %q body %s", i, keys[i], bodies[i])
		}
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &body); err != nil || body["amount_minor"] != float64(4500) || body["reason"] != "damaged" {
		t.Errorf("body = %s (%v)", bodies[0], err)
	}
}

// A refusal of the server is an answer: it is not repeated.
func TestCallShopMoney_ARefusalIsNotRepeated(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"code":"shop.invalid_refund_amount","message":"too much","details":{"refundable_minor":1200}}}`))
	}))
	defer srv.Close()
	b := newShopMoneyBot(t, srv.URL)
	st := shopRefundDialog{OrgID: uuid.New(), OrderID: uuid.New(), Mode: shRefModeRefund, Key: "tg-k"}
	res := b.callShopMoney(context.Background(), &Identity{Link: gen.BotTelegramLinkRow{UserID: uuid.New()}}, st)
	var ae *APIError
	if calls != 1 || res.Unknown || !errors.As(res.Err, &ae) || ae.RefundableMinor == nil || *ae.RefundableMinor != 1200 {
		t.Fatalf("calls=%d result=%+v", calls, res)
	}
}

// A dialog whose operation may have started keeps its key when the person
// leaves, and another order's refund cannot overwrite it.
func TestShopRefund_AttemptedKeySurvivesLeavingAndIsNotOverwritten(t *testing.T) {
	b := newShopMoneyBot(t, "http://127.0.0.1:1")
	ctx := context.Background()
	org, order := uuid.New(), uuid.New()
	st := shopRefundDialog{OrgID: org, OrderID: order, Num: 7, Currency: "EUR", Mode: shRefModeRefund, Left: 100, Key: "tg-keep", Attempted: true}
	if !b.saveShopRefund(ctx, 42, st, shRefStepRetry) {
		t.Fatal("save failed")
	}
	b.leaveIdleShopRefund(ctx, 42)
	var got shopRefundDialog
	if _, found, _, err := b.dialogs.Load(ctx, 42, shopRefundDialogKind, &got); err != nil || !found || got.Key != "tg-keep" || !got.Parked {
		t.Fatalf("the attempted dialog must be parked with its key: found=%v %+v err=%v", found, got, err)
	}
	// A parked dialog takes no typed text.
	if b.shopRefundText(ctx, 42, &models.User{ID: 42}, "REFUND") {
		t.Error("a parked dialog took a typed text")
	}
	if _, blocked := b.shopOtherAttempt(ctx, 42, uuid.New()); !blocked {
		t.Error("another order's refund must be blocked while this one may have started")
	}
	if _, blocked := b.shopOtherAttempt(ctx, 42, order); blocked {
		t.Error("the same order must be allowed to resume")
	}

	// A dialog that sent nothing is simply dropped.
	clean := shopRefundDialog{OrgID: org, OrderID: order, Key: "tg-clean"}
	b.saveShopRefund(ctx, 43, clean, shRefStepWord)
	b.leaveIdleShopRefund(ctx, 43)
	if _, found, _, _ := b.dialogs.Load(ctx, 43, shopRefundDialogKind, &got); found {
		t.Error("an unsent refund dialog must be dropped on leaving")
	}
}

func TestShopRefundDialog_AmountAndWord(t *testing.T) {
	t.Parallel()
	d := shopRefundDialog{Left: 4500, Mode: shRefModeRefund}
	if d.amountMinor() != 4500 || d.ttl() != dialogTTL || d.wordKey() != shopRefundWordKey {
		t.Errorf("whole refund: %+v", d)
	}
	part := int64(1000)
	d.Amount, d.Attempted = &part, true
	if d.amountMinor() != 1000 || d.ttl() != shopRefundAttemptedTTL {
		t.Errorf("part refund: %+v", d)
	}
	if (shopRefundDialog{Mode: shRefModeFulfil}).wordKey() != shopFulfilWordKey {
		t.Error("fulfil has its own word")
	}
	b := ecTestBot(t)
	if !b.typedPhrase("ru", shopRefundWordKey, " «вернуть». ") || !b.typedPhrase("ru", shopRefundWordKey, "refund") || b.typedPhrase("ru", shopRefundWordKey, "cancel") {
		t.Error("the refund word: localized or English, nothing else")
	}
	if b.typedPhrase("en", shopFulfilWordKey, "REFUND") {
		t.Error("the refund word must not confirm a fulfil")
	}
}

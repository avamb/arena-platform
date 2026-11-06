//go:build integration

package httpserver

// End-to-end proof of the bot's "Shop" section (spec 61 stage M3) through the
// real router, the real bot, the real shop service and refund engine and a
// stub Stripe. The owner opens the shop, reads the counts, the orders and a
// card, copies the courier text, ships with a tracking number, writes a note,
// asks for a letter again (and meets the rate limit), cancels an unpaid order
// by a typed word, returns part of the money and then the rest by a typed word
// (the idempotency key of the dialog is the key stored on the refund), settles
// an order in review by refunding it, and opens and closes the storefront.
// An organization without the module has no shop button, and "Open in browser"
// carries the target. The stub Telegram records message TEXT only, so every
// effect is checked in the database.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

type shopBotSeed struct {
	paid, pending, review   uuid.UUID
	paidNum, pendingNum     int64
	reviewNum               int64
	channel                 uuid.UUID
	paidPI, reviewPI        uuid.UUID
	buyerName, buyerAddress string
}

// seedShopBotOrg gives the organization a shop: settings, a Stripe channel and
// payment config, a paid order (with a succeeded payment), an unpaid one and
// one parked in review (also paid), and everything their cleanup needs.
func seedShopBotOrg(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) *shopBotSeed {
	t.Helper()
	ctx := context.Background()
	s := &shopBotSeed{
		paid: uuid.New(), pending: uuid.New(), review: uuid.New(), channel: uuid.New(),
		buyerName: "Ana Lopez", buyerAddress: "Calle Mayor 1",
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed %q: %v", sql, err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		for _, sql := range []string{
			`DELETE FROM worker_jobs WHERE job_type = 'shop.order_email' AND payload->>'org_id' = $1::text`,
			`DELETE FROM sales_notification_deliveries WHERE key IN (
			   SELECT 'shop_paid:' || id::text FROM shop_orders WHERE org_id = $1
			   UNION ALL SELECT 'shop_review:' || id::text FROM shop_orders WHERE org_id = $1
			   UNION ALL SELECT 'shop_refund:' || id::text || ':' || refunded_minor::text FROM shop_orders WHERE org_id = $1)`,
			`DELETE FROM outbox_events WHERE aggregate_type = 'shop_order'
			   AND aggregate_id IN (SELECT id::text FROM shop_orders WHERE org_id = $1)`,
			`DELETE FROM shop_stock_moves WHERE org_id = $1`,
			`DELETE FROM shop_order_items WHERE order_id IN (SELECT id FROM shop_orders WHERE org_id = $1)`,
			`UPDATE shop_orders SET payment_intent_id = NULL WHERE org_id = $1`,
			`DELETE FROM refunds WHERE org_id = $1`,
			`DELETE FROM payment_intent_events WHERE payment_intent_id IN (SELECT id FROM payment_intents WHERE org_id = $1)`,
			`DELETE FROM worker_jobs WHERE job_type = 'payment.receipt_archive' AND payload->>'payment_intent_id' IN (
			   SELECT id::text FROM payment_intents WHERE org_id = $1)`,
			`DELETE FROM payment_intents WHERE org_id = $1`,
			`DELETE FROM shop_orders WHERE org_id = $1`,
			`DELETE FROM shop_variants WHERE product_id IN (SELECT id FROM shop_products WHERE org_id = $1)`,
			`DELETE FROM shop_products WHERE org_id = $1`,
			`DELETE FROM shop_settings WHERE org_id = $1`,
			`DELETE FROM payment_provider_configs WHERE org_id = $1`,
			`DELETE FROM sales_channels WHERE org_id = $1`,
		} {
			if strings.Contains(sql, "::text") {
				_, _ = pool.Exec(c, sql, orgID.String())
			} else {
				_, _ = pool.Exec(c, sql, orgID)
			}
		}
	})

	exec(`INSERT INTO sales_channels (id, org_id, name, provider) VALUES ($1, $2, 'Shop channel', 'stripe')`, s.channel, orgID)
	exec(`INSERT INTO payment_provider_configs (org_id, provider, mode, secrets, status, is_active, verification_status, verified_at)
	      VALUES ($1, 'stripe', 'test', '{"api_key":"sk_test_shopbot","webhook_secret":"whsec_shopbot"}'::jsonb, 'configured', true, 'ok', now())`, orgID)
	exec(`INSERT INTO shop_settings (org_id, currency, shipping_fee_minor, shipping_label, country_iso2, contact_email, sales_channel_id, enabled)
	      VALUES ($1, 'EUR', 500, 'Courier', 'ES', 'shop@example.test', $2, false)`, orgID, s.channel)

	pi := func() uuid.UUID {
		ref := "cs_test_shopbot_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		row, err := gen.New(pool).InsertPaymentIntent(ctx, nil, orgID, "stripe", &ref, 4500, "EUR", "succeeded", nil, nil)
		if err != nil {
			t.Fatalf("payment intent: %v", err)
		}
		exec(`UPDATE payment_intents SET provider_charge_ref = $2 WHERE id = $1`, row.ID, "pi_shopbot_"+uuid.NewString()[:8])
		return row.ID
	}
	s.paidPI, s.reviewPI = pi(), pi()

	// Tokens must be unique 64-hex strings: derive each from the order id.
	token := func(id uuid.UUID) string {
		return strings.ReplaceAll(id.String(), "-", "") + strings.ReplaceAll(id.String(), "-", "")
	}
	insert := func(id uuid.UUID, status string, piID *uuid.UUID, reviewReason *string, name string) int64 {
		var num int64
		if err := pool.QueryRow(ctx, `INSERT INTO shop_orders (id, org_id, order_token, status, buyer_name, buyer_email, buyer_phone,
			ship_name, ship_address1, ship_city, ship_postal, ship_country, delivery_kind, currency, subtotal, shipping_fee, total,
			payment_intent_id, provider, paid_at, review_reason, expires_at, fulfillment_step)
			VALUES ($1, $2, $3, $4, $5, $6, '+34 600 111 222', $5, $7, 'Barcelona', '08001', 'ES', 'shipping', 'EUR', 4000, 500, 4500,
			$8, 'stripe', CASE WHEN $8::uuid IS NULL THEN NULL ELSE now() END, $9, now() + interval '1 day',
			CASE WHEN $4::text = 'paid' THEN 'waiting' END)
			RETURNING number`,
			id, orgID, token(id), status, name, strings.ToLower(strings.ReplaceAll(name, " ", "."))+"@example.test",
			s.buyerAddress, piID, reviewReason).Scan(&num); err != nil {
			t.Fatalf("seed order %s: %v", status, err)
		}
		exec(`INSERT INTO shop_order_items (order_id, product_title, variant_title, qty, unit_price_minor, total_minor, ordinal)
		      VALUES ($1, 'Red wine', '0.75 l', 2, 2000, 4000, 1)`, id)
		return num
	}
	late := "late_payment_no_stock"
	s.paidNum = insert(s.paid, "paid", &s.paidPI, nil, s.buyerName)
	s.pendingNum = insert(s.pending, "pending_payment", nil, nil, "Boris Unpaid")
	s.reviewNum = insert(s.review, "manual_review", &s.reviewPI, &late, "Clara Review")
	return s
}

func (s *shopBotSeed) orderStatus(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) (status string, refunded int64, tracking, note string) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT status, refunded_minor, tracking_note, staff_note FROM shop_orders WHERE id = $1`, id).Scan(&status, &refunded, &tracking, &note); err != nil {
		t.Fatal(err)
	}
	return status, refunded, tracking, note
}

func shopRefundRows(t *testing.T, pool *pgxpool.Pool, org uuid.UUID) (keys []string, amounts []int64) {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT COALESCE(idempotency_key, ''), amount FROM refunds WHERE org_id = $1 AND state = 'succeeded' ORDER BY created_at`, org)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var a int64
		if err := rows.Scan(&k, &a); err != nil {
			t.Fatal(err)
		}
		keys, amounts = append(keys, k), append(amounts, a)
	}
	return keys, amounts
}

func TestBotE2E_Shop_OwnerWorksTheOrders(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool) // no module
	stripe := newStubStripe(t)
	stripe.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"re_shop_%s","status":"succeeded"}`, uuid.NewString()[:8])
	}
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) {
		c.StripeAPIBaseURL = stripe.baseURL()
		c.EventCenterPublicURL = "https://events.example.test"
	})
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedShopBotOrg(t, pool, f.orgID)
	connectShopModuleForTest(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	linkECBotUser(t, f, srv, ecManagerTG, "manager")
	linkECBotUser(t, other, srv, ecOutsiderTG, "owner")

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	defer func() { stop() }()
	d := ecDriver{t, tg, ecOwnerTG}
	ctx := context.Background()
	paid, pending, review := seed.paid.String(), seed.pending.String(), seed.review.String()

	// ── the shop button: the owner and the manager have it, the module-less
	// organization does not, and a stale press there is told so
	d.say("/start", "Что будем делать?")
	if _, _, kb := tg.lastOf(ecOwnerTG); !hasPrefixData(kb, "sh:home") {
		t.Fatalf("the owner has no shop button: %v", kb)
	}
	ecDriver{t, tg, ecManagerTG}.say("/start", "Что будем делать?")
	if _, _, kb := tg.lastOf(ecManagerTG); !hasPrefixData(kb, "sh:home") {
		t.Fatalf("the manager has no shop button: %v", kb)
	}
	od := ecDriver{t, tg, ecOutsiderTG}
	od.say("/start", "Что будем делать?")
	if _, _, kb := tg.lastOf(ecOutsiderTG); hasPrefixData(kb, "sh:") {
		t.Fatalf("an organization without the module has a shop button: %v", kb)
	}
	od.press("sh:home", "Магазин не подключён")

	// ── the shop screen: counts and the storefront
	home := d.press("sh:home", "К отправке:")
	for _, want := range []string{"Витрина: 🔴 закрыта", "К отправке: 1", "На проверке: 1", "Ждут оплаты: 1"} {
		if !strings.Contains(home, want) {
			t.Errorf("shop screen lacks %q:\n%s", want, home)
		}
	}

	// ── the list: the paid order is the one to ship
	list := d.press("sh:ol", "К отправке")
	if !strings.Contains(list, "всего: 1") {
		t.Errorf("list:\n%s", list)
	}
	var st struct {
		Tab string      `json:"tab"`
		IDs []uuid.UUID `json:"ids"`
	}
	readDialog(t, pool, ecOwnerTG, "shop", &st)
	if st.Tab != "to_ship" || len(st.IDs) != 1 || st.IDs[0] != seed.paid {
		t.Fatalf("stored list = %+v", st)
	}
	card := d.press("sh:o:0", fmt.Sprintf("Заказ №%d", seed.paidNum))
	for _, want := range []string{"Ana Lopez", "ana.lopez@example.test", "+34 600 111 222", "Calle Mayor 1", "08001 Barcelona", "Red wine — 0.75 l × 2", "Итого: 45 EUR", "stripe"} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q:\n%s", want, card)
		}
	}
	// the other tabs
	d.press("sh:b", "К отправке")
	if txt := d.press("sh:t:u", "Ждут оплаты"); !strings.Contains(txt, "всего: 1") {
		t.Errorf("unpaid tab:\n%s", txt)
	}
	if txt := d.press("sh:t:r", "На проверке"); !strings.Contains(txt, "всего: 1") {
		t.Errorf("review tab:\n%s", txt)
	}
	// a typed text on the list is a search (the e-mail never reaches a URL)
	d.say("clara.review", "Поиск: clara.review")
	readDialog(t, pool, ecOwnerTG, "shop", &st)
	if len(st.IDs) != 1 || st.IDs[0] != seed.review {
		t.Errorf("the search found %v, want the order in review", st.IDs)
	}
	d.say("nobody-here", "ничего не найдено")
	d.press("sh:x", "На проверке")

	// ── the courier text comes as a message of its own, in a code block
	d.press("sh:v:"+paid, fmt.Sprintf("Заказ №%d", seed.paidNum))
	courier := d.press("sh:cr:"+paid, "Текст для курьера")
	if !strings.Contains(courier, "<pre>") || !strings.Contains(courier, "Calle Mayor 1") {
		t.Errorf("courier text:\n%s", courier)
	}

	// ── a note
	d.press("sh:nt:"+paid, "напишите заметку")
	d.say("pack carefully", "Заметка сохранена")
	if _, _, _, note := seed.orderStatus(t, pool, seed.paid); note != "pack carefully" {
		t.Errorf("staff note = %q", note)
	}

	// ── shipping with a tracking number
	d.press("sh:sh:"+paid, "номер отправления")
	d.say("TRACK-777", "передан курьеру")
	if status, _, tracking, _ := seed.orderStatus(t, pool, seed.paid); status != "shipped" || tracking != "TRACK-777" {
		t.Fatalf("after shipping: status=%s tracking=%q", status, tracking)
	}

	// ── the letter once more: the first one is queued, the second is refused
	d.press("sh:rm:"+paid, "какое письмо")
	d.press("sh:rs:s:"+paid, "поставлено в очередь")
	d.press("sh:rs:s:"+paid, "меньше минуты назад")

	// ── the unpaid order: a wrong word cancels nothing, the right one does
	d.press("sh:v:"+pending, fmt.Sprintf("Заказ №%d", seed.pendingNum))
	d.press("sh:cx:"+pending, "ОТМЕНИТЬ ЗАКАЗ")
	d.say("нет", "ничего не отменено")
	if status, _, _, _ := seed.orderStatus(t, pool, seed.pending); status != "pending_payment" {
		t.Fatalf("a wrong word changed the order to %s", status)
	}
	d.say("отменить заказ", "отменён")
	if status, _, _, _ := seed.orderStatus(t, pool, seed.pending); status != "cancelled" {
		t.Fatalf("status after the right word = %s", status)
	}

	// ── a part of the money back, by a typed word
	d.press("sh:v:"+paid, fmt.Sprintf("Заказ №%d", seed.paidNum))
	d.press("sh:f:s:"+paid, "Возврат · заказ №")
	d.press("sh:f:part", "Напишите сумму")
	d.say("999", "Это не сумма")
	d.say("10,50", "причину")
	word := d.say("damaged bottle", "ВЕРНУТЬ")
	if !strings.Contains(word, "10,50 EUR") || !strings.Contains(word, "damaged bottle") {
		t.Errorf("confirmation:\n%s", word)
	}
	d.say("нет", "ничего не отправлено")
	if keys, _ := shopRefundRows(t, pool, f.orgID); len(keys) != 0 {
		t.Fatalf("a wrong word refunded %d time(s)", len(keys))
	}
	var key1 struct {
		Key string `json:"key"`
	}
	readDialog(t, pool, ecOwnerTG, "shop_refund", &key1)
	if !strings.HasPrefix(key1.Key, "tg-") {
		t.Fatalf("dialog key = %q", key1.Key)
	}
	d.say("вернуть", "деньги вернулись: 10,50 EUR")
	keys, amounts := shopRefundRows(t, pool, f.orgID)
	if len(keys) != 1 || amounts[0] != 1050 || keys[0] != key1.Key {
		t.Fatalf("refund rows = %v %v; want one of 1050 with the dialog's key %q", keys, amounts, key1.Key)
	}
	if status, refunded, _, _ := seed.orderStatus(t, pool, seed.paid); status != "partially_refunded" || refunded != 1050 {
		t.Fatalf("after the part refund: %s, %d", status, refunded)
	}
	if n := countDialogs(t, pool, ecOwnerTG, "shop_refund"); n != 0 {
		t.Errorf("the finished refund left its dialog (%d)", n)
	}

	// ── then the rest: a new operation with a new key
	d.press("sh:v:"+paid, "Возвращён частично")
	d.press("sh:f:s:"+paid, "Возврат · заказ №")
	d.press("sh:f:all", "причину")
	d.press("sh:f:sk", "ВЕРНУТЬ")
	var key2 struct {
		Key string `json:"key"`
	}
	readDialog(t, pool, ecOwnerTG, "shop_refund", &key2)
	if key2.Key == "" || key2.Key == key1.Key {
		t.Fatalf("the second operation must have its own key: %q vs %q", key2.Key, key1.Key)
	}
	d.say("refund", "деньги вернулись: 34,50 EUR")
	keys, amounts = shopRefundRows(t, pool, f.orgID)
	if len(keys) != 2 || amounts[1] != 3450 || keys[1] != key2.Key {
		t.Fatalf("refund rows = %v %v", keys, amounts)
	}
	if status, refunded, _, _ := seed.orderStatus(t, pool, seed.paid); status != "refunded" || refunded != 4500 {
		t.Fatalf("after the whole refund: %s, %d", status, refunded)
	}
	// nothing is left to refund: the card no longer offers it
	if txt := d.press("sh:v:"+paid, "Возвращён"); strings.Contains(txt, "undefined") {
		t.Errorf("card:\n%s", txt)
	}
	if _, _, kb := tg.lastOf(ecOwnerTG); hasPrefixData(kb, "sh:f:s:") {
		t.Errorf("a refunded order still offers a refund: %v", kb)
	}

	// ── an order in review: refund and cancel it, by the word
	rc := d.press("sh:v:"+review, fmt.Sprintf("Заказ №%d", seed.reviewNum))
	if !strings.Contains(rc, "бронь закончилась") {
		t.Errorf("review card does not explain:\n%s", rc)
	}
	d.press("sh:rv:r:"+review, "Вернуть деньги и отменить заказ")
	d.say("нет", "ничего не отправлено")
	d.say("вернуть", "деньги вернулись")
	if status, refunded, _, _ := seed.orderStatus(t, pool, seed.review); status != "refunded" || refunded != 4500 {
		t.Fatalf("review order after refund_and_cancel: %s, %d", status, refunded)
	}

	// ── the storefront: blocked without a product, then open and close
	d.press("sh:home", "К отправке:")
	blocked := d.press("sh:sf:o", "Витрину пока нельзя открыть")
	if !strings.Contains(blocked, "нет ни одного активного товара") {
		t.Errorf("blockers:\n%s", blocked)
	}
	if on := storefrontOn(t, pool, f.orgID); on {
		t.Fatal("the storefront opened with no product")
	}
	var variant uuid.UUID
	product := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO shop_products (id, org_id, slug, title, status) VALUES ($1, $2, $3, 'Wine', 'active')`,
		product, f.orgID, "wine-"+product.String()[:8]); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO shop_variants (product_id, price_minor, stock) VALUES ($1, 2000, 5) RETURNING id`, product).Scan(&variant); err != nil {
		t.Fatal(err)
	}
	d.press("sh:sf:o", "Открыть витрину?")
	d.press("sh:sf:oy", "Витрина открыта")
	if !storefrontOn(t, pool, f.orgID) {
		t.Fatal("the storefront did not open")
	}
	d.press("sh:sf:c", "Неоплаченные заказы останутся")
	d.press("sh:sf:cy", "Витрина закрыта")
	if storefrontOn(t, pool, f.orgID) {
		t.Fatal("the storefront did not close")
	}

	// ── the settings are shown, not edited
	settings := d.press("sh:st", "Настройки магазина")
	for _, want := range []string{"Валюта: EUR", "Courier", "ES", "shop@example.test", "Shop channel (stripe)"} {
		if !strings.Contains(settings, want) {
			t.Errorf("settings lack %q:\n%s", want, settings)
		}
	}

	// ── "Open in browser" carries the target to the order
	d.press("sh:v:"+paid, "Заказ №")
	d.press("sh:w:o:"+paid, "Вот ваша ссылка")
	id, _, _ := tg.lastOf(ecOwnerTG)
	urls := tg.urlsOf(id)
	if len(urls) != 1 {
		t.Fatalf("link buttons = %v", urls)
	}
	wantNext := "&next=" + url.QueryEscape("/o/"+f.orgID.String()+"/shop/orders/"+paid)
	if !strings.HasPrefix(urls[0], "https://events.example.test/login/link#code=") || !strings.HasSuffix(urls[0], wantNext) {
		t.Errorf("link = %s, want the target %s", urls[0], wantNext)
	}
}

func storefrontOn(t *testing.T, pool *pgxpool.Pool, org uuid.UUID) bool {
	t.Helper()
	var on bool
	if err := pool.QueryRow(context.Background(), `SELECT enabled FROM shop_settings WHERE org_id = $1`, org).Scan(&on); err != nil {
		t.Fatal(err)
	}
	return on
}

func countDialogs(t *testing.T, pool *pgxpool.Pool, tgID int64, kind string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = $2`, tgID, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// readDialog decodes the state of a stored dialog of the account.
func readDialog(t *testing.T, pool *pgxpool.Pool, tgID int64, kind string, into any) {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT state FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = $2`, tgID, kind).Scan(&raw); err != nil {
		t.Fatalf("the %s dialog must be stored for %d: %v", kind, tgID, err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode %s dialog: %v", kind, err)
	}
}

// A shop refund waiting for its word survives a bot restart (it lives in
// bot_dialogs), the menu asks before dropping it, and leaving drops it.
func TestBotE2E_Shop_RefundDialogSurvivesRestartAndMenuAsks(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	stripe := newStubStripe(t)
	stripe.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"re_shop_%s","status":"succeeded"}`, uuid.NewString()[:8])
	}
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) { c.StripeAPIBaseURL = stripe.baseURL() })
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedShopBotOrg(t, pool, f.orgID)
	connectShopModuleForTest(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	defer func() { stop() }()
	d := ecDriver{t, tg, ecOwnerTG}
	paid := seed.paid.String()

	d.press("sh:f:s:"+paid, "Возврат · заказ №")
	d.press("sh:f:all", "причину")
	d.press("sh:f:sk", "ВЕРНУТЬ")
	var k1 struct {
		Key string `json:"key"`
	}
	readDialog(t, pool, ecOwnerTG, "shop_refund", &k1)

	// the menu asks first; "back" returns to the same step with the same key
	ask := d.say("/menu", "ждёт вашего слова")
	if !strings.Contains(ask, fmt.Sprint(seed.paidNum)) {
		t.Errorf("the question does not name the order:\n%s", ask)
	}
	d.press("sh:f:cur", "ВЕРНУТЬ")

	// a restart loses nothing
	stop()
	stop = startDialogTestBot(t, pool, api, tg)
	var k2 struct {
		Key string `json:"key"`
	}
	readDialog(t, pool, ecOwnerTG, "shop_refund", &k2)
	if k2.Key != k1.Key {
		t.Fatalf("the key changed over a restart: %q -> %q", k1.Key, k2.Key)
	}

	// leaving through the menu drops the unsent refund and sends nothing
	d.say("/menu", "ждёт вашего слова")
	d.press("menu:leave", "Что будем делать?")
	if n := countDialogs(t, pool, ecOwnerTG, "shop_refund"); n != 0 {
		t.Errorf("leaving left the refund dialog (%d)", n)
	}
	if keys, _ := shopRefundRows(t, pool, f.orgID); len(keys) != 0 {
		t.Errorf("leaving refunded %d time(s)", len(keys))
	}

	// the word after the dialog is gone refunds nothing
	d.say("вернуть", "")
	if keys, _ := shopRefundRows(t, pool, f.orgID); len(keys) != 0 {
		t.Errorf("a stray word refunded %d time(s)", len(keys))
	}
}

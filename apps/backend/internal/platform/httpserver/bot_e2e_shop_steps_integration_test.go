//go:build integration

package httpserver

// End-to-end proof of the delivery steps in the bot's "Shop" section (spec 61
// section 7b, stage M5) through the real router, the real bot and the real
// shop service. The owner packs an order, hands it to the courier with a
// tracking number, marks it delivered (the buyer's letter is queued once), and
// fails another delivery with a reason from the picker, hands it over again
// without a number, and fails it again with a reason typed as text. The
// history rows (who, from where, tracking, reason), the status and the step
// are read from the database; the tabs "To ship" (waiting and packed) and "Not
// delivered" list the right orders.

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

type shopStepEvent struct {
	Step, From, Tracking, Reason, Via, Actor string
	TelegramID                               *int64
}

func shopStepEvents(t *testing.T, pool *pgxpool.Pool, order uuid.UUID) []shopStepEvent {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT step, COALESCE(from_step, ''), tracking, reason, via, actor_name, actor_telegram_id
		   FROM shop_order_events WHERE order_id = $1 ORDER BY created_at, id`, order)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []shopStepEvent
	for rows.Next() {
		var e shopStepEvent
		if err := rows.Scan(&e.Step, &e.From, &e.Tracking, &e.Reason, &e.Via, &e.Actor, &e.TelegramID); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func shopOrderStep(t *testing.T, pool *pgxpool.Pool, order uuid.UUID) (step, status, tracking string) {
	t.Helper()
	var s *string
	if err := pool.QueryRow(context.Background(),
		`SELECT fulfillment_step, status, tracking_note FROM shop_orders WHERE id = $1`, order).Scan(&s, &status, &tracking); err != nil {
		t.Fatal(err)
	}
	if s != nil {
		step = *s
	}
	return step, status, tracking
}

func deliveredLetters(t *testing.T, pool *pgxpool.Pool, org, order uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM worker_jobs WHERE job_type = 'shop.order_email'
		   AND payload->>'org_id' = $1 AND payload->>'order_id' = $2 AND payload->>'kind' = 'delivered'`,
		org.String(), order.String()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBotE2E_Shop_DeliverySteps(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) { c.EventCenterPublicURL = "https://events.example.test" })
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedShopBotOrg(t, pool, f.orgID)
	connectShopModuleForTest(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	ctx := context.Background()

	// A second paid order, waiting to be packed (the seed's one is the first).
	paid2 := uuid.New()
	tok := strings.ReplaceAll(paid2.String(), "-", "")
	var num2 int64
	if err := pool.QueryRow(ctx, `INSERT INTO shop_orders (id, org_id, order_token, status, buyer_name, buyer_email, buyer_phone,
		ship_name, ship_address1, ship_city, ship_postal, ship_country, delivery_kind, currency, subtotal, shipping_fee, total,
		provider, paid_at, fulfillment_step, expires_at)
		VALUES ($1, $2, $3, 'paid', 'Dana Second', 'dana.second@example.test', '+34 600 333 444', 'Dana Second', 'Calle Sol 2', 'Madrid', '28001', 'ES',
		'shipping', 'EUR', 4000, 500, 4500, 'stripe', now(), 'waiting', now() + interval '1 day') RETURNING number`,
		paid2, f.orgID, tok+tok).Scan(&num2); err != nil {
		t.Fatalf("seed the second order: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO shop_order_items (order_id, product_title, variant_title, qty, unit_price_minor, total_minor, ordinal)
		VALUES ($1, 'Red wine', '0.75 l', 2, 2000, 4000, 1)`, paid2); err != nil {
		t.Fatal(err)
	}

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	defer func() { stop() }()
	d := ecDriver{t, tg, ecOwnerTG}
	paid, p2 := seed.paid.String(), paid2.String()

	// ── the counts and the tab: both paid orders wait to be packed
	if home := d.press("sh:home", "К отправке:"); !strings.Contains(home, "К отправке: 2") {
		t.Errorf("shop screen:\n%s", home)
	}
	if list := d.press("sh:ol", "К отправке"); !strings.Contains(list, "всего: 2") {
		t.Errorf("list:\n%s", list)
	}
	var st struct {
		Tab string      `json:"tab"`
		IDs []uuid.UUID `json:"ids"`
	}
	readDialog(t, pool, ecOwnerTG, "shop", &st)
	if st.Tab != "to_ship" || len(st.IDs) != 2 || st.IDs[0] != paid2 || st.IDs[1] != seed.paid {
		t.Fatalf("stored list = %+v, want the newer order first", st)
	}

	// ── the card names the step and offers the next ones
	card := d.press("sh:v:"+paid, fmt.Sprintf("Заказ №%d", seed.paidNum))
	if !strings.Contains(card, "Доставка:") || !strings.Contains(card, "Ждёт сборки") {
		t.Errorf("card:\n%s", card)
	}
	_, _, kb := tg.lastOf(ecOwnerTG)
	for _, want := range []string{"sh:ps:p:", "sh:sh:", "sh:ps:f:"} {
		if !hasPrefixData(kb, want) {
			t.Errorf("waiting card lacks %q: %v", want, kb)
		}
	}
	if hasPrefixData(kb, "sh:ps:d:") {
		t.Errorf("a waiting order cannot be delivered yet: %v", kb)
	}

	// ── packed
	if txt := d.press("sh:ps:p:"+paid, "Собран"); !strings.Contains(txt, "Собран") {
		t.Errorf("after packing:\n%s", txt)
	}
	if step, status, _ := shopOrderStep(t, pool, seed.paid); step != "packed" || status != "paid" {
		t.Fatalf("after packing: step=%s status=%s", step, status)
	}

	// ── handed to the courier with a tracking number: the old "ship" press
	d.press("sh:sh:"+paid, "номер отправления")
	d.say("TRACK-42", "передан курьеру")
	if step, status, tracking := shopOrderStep(t, pool, seed.paid); step != "handed_over" || status != "shipped" || tracking != "TRACK-42" {
		t.Fatalf("after the hand-over: step=%s status=%s tracking=%q", step, status, tracking)
	}
	_, _, kb = tg.lastOf(ecOwnerTG)
	if !hasPrefixData(kb, "sh:ps:d:") || hasPrefixData(kb, "sh:ps:p:") {
		t.Errorf("a handed-over card offers delivered, not packed: %v", kb)
	}

	// ── delivered: the buyer's letter is queued, once
	if txt := d.press("sh:ps:d:"+paid, "Доставлен"); !strings.Contains(txt, "Доставка:") {
		t.Errorf("after delivery:\n%s", txt)
	}
	if step, status, _ := shopOrderStep(t, pool, seed.paid); step != "delivered" || status != "shipped" {
		t.Fatalf("after delivery: step=%s status=%s", step, status)
	}
	if n := deliveredLetters(t, pool, f.orgID, seed.paid); n != 1 {
		t.Errorf("delivered letters queued = %d, want 1", n)
	}
	_, _, kb = tg.lastOf(ecOwnerTG)
	if hasPrefixData(kb, "sh:ps:") || hasPrefixData(kb, "sh:sh:") {
		t.Errorf("a delivered order has no step buttons: %v", kb)
	}

	// ── the history: three rows, each from the bot, with the person's name
	ev := shopStepEvents(t, pool, seed.paid)
	if len(ev) != 3 {
		t.Fatalf("history = %+v", ev)
	}
	wantSteps := []struct{ step, from, tracking string }{{"packed", "waiting", ""}, {"handed_over", "packed", "TRACK-42"}, {"delivered", "handed_over", ""}}
	for i, w := range wantSteps {
		e := ev[i]
		if e.Step != w.step || e.From != w.from || e.Tracking != w.tracking || e.Via != "telegram_bot" || e.Actor == "" {
			t.Errorf("history[%d] = %+v, want %+v from the bot with a name", i, e, w)
		}
	}

	// ── a failed delivery with a reason from the picker
	pick := d.press("sh:ps:f:"+p2, "почему не доставлен")
	if !strings.Contains(pick, fmt.Sprint(num2)) {
		t.Errorf("picker:\n%s", pick)
	}
	_, _, kb = tg.lastOf(ecOwnerTG)
	for _, want := range []string{"sh:fr:n:", "sh:fr:h:", "sh:fr:a:", "sh:fr:r:", "sh:fr:o:"} {
		if !hasPrefixData(kb, want) {
			t.Errorf("the reason picker lacks %q: %v", want, kb)
		}
	}
	if txt := d.press("sh:fr:h:"+p2, "причина: Нет дома"); !strings.Contains(txt, "Не доставлен") {
		t.Errorf("after the failed delivery:\n%s", txt)
	}
	if step, status, _ := shopOrderStep(t, pool, paid2); step != "failed" || status != "paid" {
		t.Fatalf("after failing: step=%s status=%s", step, status)
	}
	if ev := shopStepEvents(t, pool, paid2); len(ev) != 1 || ev[0].Reason != "not_home" || ev[0].Via != "telegram_bot" {
		t.Fatalf("history of the failed order = %+v", ev)
	}

	// ── the tab "Not delivered" lists it and "To ship" no longer does
	d.press("sh:ol", "К отправке")
	if txt := d.press("sh:t:f", "Не доставлены"); !strings.Contains(txt, "всего: 1") {
		t.Errorf("failed tab:\n%s", txt)
	}
	readDialog(t, pool, ecOwnerTG, "shop", &st)
	if st.Tab != "failed" || len(st.IDs) != 1 || st.IDs[0] != paid2 {
		t.Fatalf("failed tab list = %+v", st)
	}
	if txt := d.press("sh:t:s", "К отправке"); !strings.Contains(txt, "всего: 0") {
		t.Errorf("to-ship tab:\n%s", txt)
	}

	// ── another try: handed over again without a number (the "No number" button)
	d.press("sh:v:"+p2, "причина: Нет дома")
	d.press("sh:sh:"+p2, "номер отправления")
	d.press("sh:sn:"+p2, "передан курьеру")
	if step, status, _ := shopOrderStep(t, pool, paid2); step != "handed_over" || status != "shipped" {
		t.Fatalf("after the second try: step=%s status=%s", step, status)
	}

	// ── failed again, now with a reason typed as text ("Other")
	d.press("sh:ps:f:"+p2, "почему не доставлен")
	d.press("sh:fr:o:"+p2, "напишите причину")
	d.say("gate code was wrong", "причина: gate code was wrong")
	if step, _, _ := shopOrderStep(t, pool, paid2); step != "failed" {
		t.Fatalf("after the typed reason: step=%s", step)
	}
	ev2 := shopStepEvents(t, pool, paid2)
	if len(ev2) != 3 || ev2[1].Step != "handed_over" || ev2[2].Step != "failed" || ev2[2].Reason != "gate code was wrong" || ev2[2].From != "handed_over" {
		t.Fatalf("history of the second order = %+v", ev2)
	}
	if n := countDialogs(t, pool, ecOwnerTG, "shop"); n != 1 {
		t.Errorf("shop dialogs = %d", n)
	}

	// ── a stale button is told so: delivering an order that is not handed over
	d.press("sh:ps:d:"+p2, "Заказ изменился")
	if step, _, _ := shopOrderStep(t, pool, paid2); step != "failed" {
		t.Errorf("a stale delivered press moved the order to %s", step)
	}
	if n := deliveredLetters(t, pool, f.orgID, paid2); n != 0 {
		t.Errorf("delivered letters queued for the failed order = %d", n)
	}
}

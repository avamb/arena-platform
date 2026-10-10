//go:build integration

package httpserver

// End-to-end proof of the bot's Orders screens (spec 35 EC-04 and EC-05)
// through the real router, the real bot and the stub Telegram: an
// organization with paid orders (one with two tickets, one of them with a
// stored EAN-13 credential and a door scan), an expired order whose payment
// failed, and two orders still waiting for payment. An owner and a manager
// both drive the list (tabs, paging, the search by barcode, order number,
// e-mail, phone and name, the scopes of an event and of a date), open the
// cards and cancel an unpaid order by typing the word; a wrong word cancels
// nothing. A user of ANOTHER organization gets "not found" for every id and
// cannot cancel anything. A bot restart keeps the dialog.
//
// The stub records message TEXT only, so what a button shows is verified
// through the dialog state in bot_dialogs (the ids of the rows on screen) and
// the buttons by the unit tests; every effect is checked in the database.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

// ordBotSeed is what the e2e put into the organization.
type ordBotSeed struct {
	suffix            string
	event1, session1  uuid.UUID
	event2, session2  uuid.UUID
	eventName1        string
	anna, boris       uuid.UUID // paid with two tickets / expired with a failed payment
	clara, karl       uuid.UUID // waiting for payment
	dmitri            uuid.UUID
	annaSID, borisSID int64
	annaCode          string // the stored EAN-13 of Anna's first ticket
	annaEmail         string
	borisEmail        string
}

// seedOrdersBotOrg fills the organization with eight orders. Newest first:
// Filler One..Three and Dmitri (event 2), Clara, Karl, Boris, Anna (event 1).
func seedOrdersBotOrg(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) *ordBotSeed {
	t.Helper()
	ctx := context.Background()
	s := &ordBotSeed{
		suffix: uuid.NewString()[:6],
		event1: uuid.New(), session1: uuid.New(), event2: uuid.New(), session2: uuid.New(),
		anna: uuid.New(), boris: uuid.New(), clara: uuid.New(), karl: uuid.New(), dmitri: uuid.New(),
	}
	s.eventName1 = "Orders Show " + s.suffix
	s.annaEmail = "anna-" + s.suffix + "@example.test"
	s.borisEmail = "boris-" + s.suffix + "@example.test"
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed %q: %v", sql, err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		sessions := `(SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`
		orders := `(SELECT id FROM orders WHERE org_id = $1)`
		tickets := `(SELECT id FROM tickets WHERE order_id IN ` + orders + `)`
		for _, sql := range []string{
			`DELETE FROM order_events WHERE order_id IN ` + orders,
			`DELETE FROM delivery_jobs WHERE ticket_id IN ` + tickets,
			`DELETE FROM barcodes WHERE ticket_id IN ` + tickets,
			`DELETE FROM ticket_credentials WHERE ticket_id IN ` + tickets,
			`DELETE FROM order_items WHERE order_id IN ` + orders,
			`DELETE FROM tickets WHERE order_id IN ` + orders,
			`DELETE FROM payment_intents WHERE org_id = $1`,
			`DELETE FROM orders WHERE org_id = $1`,
			`DELETE FROM checkout_sessions WHERE org_id = $1`,
			`DELETE FROM reservations WHERE org_id = $1`,
			`DELETE FROM session_seats WHERE session_id IN ` + sessions,
			`DELETE FROM inventory_ledger WHERE session_id IN ` + sessions,
			`DELETE FROM ticket_tiers WHERE session_id IN ` + sessions,
			`DELETE FROM sales_channels WHERE org_id = $1`,
			`DELETE FROM sessions WHERE id IN ` + sessions,
			`DELETE FROM events WHERE org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
			`DELETE FROM audit_events WHERE metadata->>'org_id' = $1::text`,
		} {
			if _, err := pool.Exec(c, sql, orgID); err != nil {
				t.Logf("orders bot cleanup: %s: %v", sql, err)
			}
		}
	})

	venue, channel, tier1 := uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, venue, orgID, "Orders Hall "+s.suffix)
	exec(`INSERT INTO sales_channels (id, org_id, name, settings) VALUES ($1, $2, $3, '{"hosted_page": {"enabled": true}}'::jsonb)`,
		channel, orgID, "Orders Channel "+s.suffix)
	start := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Hour)
	for _, ev := range []struct {
		event, session uuid.UUID
		name           string
	}{{s.event1, s.session1, s.eventName1}, {s.event2, s.session2, "Other Show " + s.suffix}} {
		exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, ev.event, orgID, ev.name)
		exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		      VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 100, 'scheduled', 'EUR', 'override')`, ev.session, ev.event, venue, start)
	}
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, unit_seq, is_open)
	      VALUES ($1, $2, 'Parterre', 'fixed', 2500, 'EUR', 100, 1, true)`, tier1, s.session1)

	// order inserts a reservation, a checkout session and the order itself.
	order := func(id uuid.UUID, event, session uuid.UUID, status, name, email, phone string, total int, ago time.Duration) {
		t.Helper()
		res, cs := uuid.New(), uuid.New()
		resState, csState := "converted", "completed"
		switch status {
		case "pending_payment":
			resState, csState = "active", "pricing_confirmed"
		case "expired":
			resState, csState = "expired", "pricing_confirmed"
		}
		exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at)
		      VALUES ($1, $2, $3, $4, 1, $5, now() + interval '1 hour')`, res, orgID, channel, session, resState)
		exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, $5)`, cs, orgID, channel, res, csState)
		var phoneArg any
		if phone != "" {
			phoneArg = phone
		}
		exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
		                          source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, buyer_phone, created_at)
		      VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', $8, 'EUR', $9, 0, 0, $9, $10, $11, $12, now() - $13::interval)`,
			id, orgID, channel, event, session, cs, res, status, total, name, email, phoneArg, fmt.Sprintf("%d seconds", int(ago.Seconds())))
		if status == "expired" {
			exec(`INSERT INTO payment_intents (checkout_session_id, org_id, provider, provider_payment_id, amount, currency, state,
			                                   failure_code, failure_message, failed_at)
			      VALUES ($1, $2, 'stripe', $3, $4, 'EUR', 'failed', 'card_declined', 'Your card was declined.', now())`,
				cs, orgID, "cs_test_ordbot_"+uuid.NewString(), total)
		}
	}

	// Event 2: Dmitri and three fillers, the newest four.
	order(s.dmitri, s.event2, s.session2, "paid", "Dmitri Other", "dmitri-"+s.suffix+"@example.test", "", 3000, 30*time.Minute)
	for i, name := range []string{"Filler One", "Filler Two", "Filler Three"} {
		order(uuid.New(), s.event2, s.session2, "paid", name, fmt.Sprintf("filler%d-%s@example.test", i, s.suffix), "", 1000, time.Duration(10+i)*time.Minute)
	}
	// Event 1.
	order(s.clara, s.event1, s.session1, "pending_payment", "Clara Zakharova", "clara-"+s.suffix+"@example.test", "", 2500, time.Hour)
	order(s.karl, s.event1, s.session1, "pending_payment", "Karl Brandtner", "karl-"+s.suffix+"@example.test", "", 2500, 90*time.Minute)
	order(s.boris, s.event1, s.session1, "expired", "Boris Petrov", s.borisEmail, "", 2500, 2*time.Hour)
	order(s.anna, s.event1, s.session1, "paid", "Anna Orderova", s.annaEmail, "+34 611 222 333", 5000, 3*time.Hour)

	// Anna's two tickets: the first with a stored credential, the second
	// legacy and already scanned at the door.
	var err error
	if s.annaCode, err = ean13.Random(); err != nil {
		t.Fatal(err)
	}
	legacyCode, err := ean13.Random()
	if err != nil {
		t.Fatal(err)
	}
	var cs uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT checkout_session_id FROM orders WHERE id = $1`, s.anna).Scan(&cs); err != nil {
		t.Fatal(err)
	}
	t1, t2 := uuid.New(), uuid.New()
	for i, tk := range []uuid.UUID{t1, t2} {
		sector, row, seat := any(nil), any(nil), any(nil)
		if i == 0 {
			sector, row, seat = "A", "3", "12"
		}
		exec(`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal, seat_sector, seat_row, seat_number)
		      VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, tk, cs, s.session1, tier1, s.annaEmail, s.anna, i, sector, row, seat)
		exec(`INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, ticket_id, unit_price, discount, charge, total)
		      VALUES ($1, $2, $3, 'ticket', $4, $5, 2500, 0, 0, 2500)`, uuid.New(), s.anna, i, tier1, tk)
		exec(`INSERT INTO delivery_jobs (ticket_id, recipient_email, status, sent_at) VALUES ($1, $2, 'sent', now() - interval '50 minutes')`, tk, s.annaEmail)
	}
	exec(`INSERT INTO ticket_credentials (ticket_id, type, payload) VALUES ($1, 'ean13', $2)`, t1, s.annaCode)
	exec(`INSERT INTO barcodes (authority_id, external_ref, ticket_id, status)
	      SELECT id, $2, $1, 'active' FROM barcode_authorities WHERE type = 'platform'`, t1, s.annaCode)
	exec(`INSERT INTO barcodes (authority_id, external_ref, ticket_id, status, scanned_at)
	      SELECT id, $2, $1, 'scanned', now() - interval '10 minutes' FROM barcode_authorities WHERE type = 'platform'`, t2, legacyCode)

	for _, p := range []struct {
		dst *int64
		id  uuid.UUID
	}{{&s.annaSID, s.anna}, {&s.borisSID, s.boris}} {
		if err := pool.QueryRow(ctx, `SELECT system_id FROM orders WHERE id = $1`, p.id).Scan(p.dst); err != nil {
			t.Fatalf("read back system_id: %v", err)
		}
	}
	return s
}

// ordDialogView is the Orders dialog as it is stored, with the rows on screen
// named by their buyers.
type ordDialogView struct {
	Step, Tab, Query, ScopeName string
	Page                        int
	Buyers                      []string // the orders on the page on screen, in order
	CardBuyer                   string   // the order whose card or cancel prompt is open
}

func readOrdersDialog(t *testing.T, pool *pgxpool.Pool, tg int64) ordDialogView {
	t.Helper()
	ctx := context.Background()
	var step string
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT step, state FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'orders'`, tg).Scan(&step, &raw); err != nil {
		t.Fatalf("the orders dialog must be stored for %d: %v", tg, err)
	}
	var st struct {
		Tab       string      `json:"tab"`
		Query     string      `json:"query"`
		Page      int         `json:"page"`
		ScopeName string      `json:"scope_name"`
		IDs       []uuid.UUID `json:"ids"`
		CardID    *uuid.UUID  `json:"card_id"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("decode orders dialog: %v", err)
	}
	buyer := func(id uuid.UUID) string {
		var name string
		if err := pool.QueryRow(ctx, `SELECT buyer_name FROM orders WHERE id = $1`, id).Scan(&name); err != nil {
			t.Fatalf("order %s of the stored page: %v", id, err)
		}
		return name
	}
	out := ordDialogView{Step: step, Tab: st.Tab, Query: st.Query, Page: st.Page, ScopeName: st.ScopeName}
	for _, id := range st.IDs {
		out.Buyers = append(out.Buyers, buyer(id))
	}
	if st.CardID != nil {
		out.CardBuyer = buyer(*st.CardID)
	}
	return out
}

func orderStatus(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(context.Background(), `SELECT status FROM orders WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

// ordScreens drives every Orders screen as one linked user. pending is the
// order this user cancels; pendingName is its buyer.
func ordScreens(t *testing.T, pool *pgxpool.Pool, s *ordBotSeed, d ecDriver, pending uuid.UUID, pendingName string) {
	t.Helper()
	buyers := func(label string, v ordDialogView, want string) {
		t.Helper()
		if got := strings.Join(v.Buyers, ", "); got != want {
			t.Errorf("%s: page shows [%s], want [%s]", label, got, want)
		}
	}
	state := func() ordDialogView { return readOrdersDialog(t, pool, d.id) }

	// The list: newest first, five a page, eight in all.
	text := d.press("or:new", "Последние · 8")
	if !strings.Contains(text, "страница 1 из 2") {
		t.Errorf("first page:\n%s", text)
	}
	v := state()
	buyers("recent 1", v, "Filler One, Filler Two, Filler Three, Dmitri Other, Clara Zakharova")
	if v.Step != "list" || v.Tab != "recent" || v.Page != 1 {
		t.Errorf("state = %+v", v)
	}
	d.press("or:p:2", "страница 2 из 2")
	buyers("recent 2", state(), "Karl Brandtner, Boris Petrov, Anna Orderova")

	// Tabs.
	d.press("or:t:p", "Оплаченные · 5")
	buyers("paid", state(), "Filler One, Filler Two, Filler Three, Dmitri Other, Anna Orderova")
	d.press("or:t:u", "Неоплаченные · 3")
	buyers("unpaid", state(), "Clara Zakharova, Karl Brandtner, Boris Petrov")
	d.press("or:t:r", "Последние · 8")

	// Search: whatever is typed goes to q as it is.
	for _, c := range []struct{ label, text, want string }{
		{"barcode", s.annaCode, "Anna Orderova"},
		{"order number with #", fmt.Sprintf("#%d", s.annaSID), "Anna Orderova"},
		{"bare order number", fmt.Sprint(s.borisSID), "Boris Petrov"},
		{"e-mail", strings.ToUpper(s.borisEmail), "Boris Petrov"},
		{"phone in another format", "+34611222333", "Anna Orderova"},
		{"name", pendingName, pendingName},
	} {
		got := d.say(c.text, "найдено 1")
		if !strings.Contains(got, "Поиск: «") {
			t.Errorf("%s: no search line:\n%s", c.label, got)
		}
		v := state()
		buyers("search "+c.label, v, c.want)
		if v.Query != c.text || v.Step != "list" || v.Page != 1 {
			t.Errorf("search %s: state = %+v", c.label, v)
		}
	}
	got := d.say("zzzqqqxxx", "заказов не нашлось")
	if !strings.Contains(got, "штрихкод, номер заказа, e-mail, телефон или имя") {
		t.Errorf("the empty search must say what can be typed:\n%s", got)
	}
	if v := state(); len(v.Buyers) != 0 || v.Query != "zzzqqqxxx" {
		t.Errorf("empty search state = %+v", v)
	}
	d.press("or:x", "Последние · 8")
	if v := state(); v.Query != "" || len(v.Buyers) != 5 {
		t.Errorf("cleared search state = %+v", v)
	}

	// Anna's card, reached by the barcode search.
	d.say(s.annaCode, "найдено 1")
	card := d.press("or:o:0", "Заказ №")
	for _, w := range []string{
		fmt.Sprintf("Заказ №%d", s.annaSID), "✅ Оплачен · 50 EUR",
		s.eventName1, "<b>Anna Orderova</b>", "✉ " + s.annaEmail,
		`href="tel:+34611222333"`, `href="https://wa.me/34611222333"`, "+34 611 222 333",
		"Билетов: 2", "Parterre · 25 EUR · <code>" + s.annaCode + "</code> · место A / 3 / 12 · действует",
		"✔ вошёл", "страницу ивента в Arena (Orders Channel", "Билеты отправлены покупателю",
	} {
		if !strings.Contains(card, w) {
			t.Errorf("Anna's card lacks %q:\n%s", w, card)
		}
	}
	if strings.Contains(card, "Оплата не прошла") || strings.Contains(card, "не оплачен") {
		t.Errorf("a paid card explains an unpaid reason:\n%s", card)
	}
	t.Logf("paid order card:\n%s", card)
	if v := state(); v.Step != "card" || v.CardBuyer != "Anna Orderova" {
		t.Errorf("card state = %+v", v)
	}
	// A typed text on a card is no search.
	d.say("абракадабра", "Я не понял")
	// Back returns to the list as it was.
	d.press("or:b", "Поиск: «"+s.annaCode+"»")

	// A paid order cannot be cancelled, whatever a stale button says.
	annaID := s.anna
	m := d.tg.mark()
	d.tg.push(e2eCallbackAs(d.id, "or:c:"+annaID.String()))
	d.tg.waitSince(t, m, "уже нельзя отменить")
	if got := orderStatus(t, pool, s.anna); got != "paid" {
		t.Fatalf("a paid order became %q", got)
	}
	d.press("or:b", "Поиск: «"+s.annaCode+"»")

	// The expired order: why, in plain words, with the provider's message.
	d.say(s.borisEmail, "найдено 1")
	card = d.press("or:o:0", "Заказ №")
	for _, w := range []string{"⌛ Истёк", "Оплата не прошла", "Сообщение платёжного провайдера: Your card was declined.", "<b>Boris Petrov</b>", "Оплата: stripe"} {
		if !strings.Contains(card, w) {
			t.Errorf("Boris's card lacks %q:\n%s", w, card)
		}
	}
	if strings.Contains(card, "Билетов:") || strings.Contains(card, "Билеты отправлены") || strings.Contains(card, "tel:") {
		t.Errorf("an unpaid card shows tickets or a phone link:\n%s", card)
	}
	t.Logf("expired order card:\n%s", card)
	// An expired order has no cancel prompt either.
	m = d.tg.mark()
	d.tg.push(e2eCallbackAs(d.id, "or:c:"+s.boris.String()))
	d.tg.waitSince(t, m, "уже нельзя отменить")
	d.press("or:b", "Поиск: «"+s.borisEmail+"»")

	// The order waiting for payment: the prompt, a wrong word, the right one.
	d.say(pendingName, "найдено 1")
	card = d.press("or:o:0", "Ждёт оплаты")
	if !strings.Contains(card, "Покупатель ещё не заплатил") {
		t.Errorf("pending card:\n%s", card)
	}
	prompt := d.press("or:c:"+pending.String(), "Последний шаг")
	if !strings.Contains(prompt, "пока ничего не отменено") || !strings.Contains(prompt, "ОТМЕНИТЬ") {
		t.Errorf("cancel prompt:\n%s", prompt)
	}
	if v := state(); v.Step != "cancel" || v.CardBuyer != pendingName {
		t.Errorf("prompt state = %+v", v)
	}
	if got := orderStatus(t, pool, pending); got != "pending_payment" {
		t.Fatalf("opening the prompt cancelled the order: %q", got)
	}
	d.say("нет, не надо", "не то слово")
	d.say("ОТМЕНА", "не то слово")
	if got := orderStatus(t, pool, pending); got != "pending_payment" {
		t.Fatalf("a wrong word changed the order to %q", got)
	}
	done := d.say("отменить", "Заказ отменён")
	if !strings.Contains(done, "✖ Отменён") {
		t.Errorf("the card after the cancellation:\n%s", done)
	}
	if got := orderStatus(t, pool, pending); got != "cancelled" {
		t.Fatalf("the typed word must cancel the order, status = %q", got)
	}
	var events int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM order_events WHERE order_id = $1 AND type = 'cancelled'`, pending).Scan(&events); err != nil || events != 1 {
		t.Errorf("cancelled events = %d (%v)", events, err)
	}
	if v := state(); v.Step != "card" {
		t.Errorf("after the cancellation the dialog is on the card: %+v", v)
	}
	// The word typed again on the card is just text.
	d.say("отменить", "Я не понял")

	// Scopes: an event, a date, and back to everything.
	text = d.press("or:e:"+s.event2.String(), "Только: Other Show")
	if !strings.Contains(text, "Последние · 4") {
		t.Errorf("event scope:\n%s", text)
	}
	buyers("event 2", state(), "Filler One, Filler Two, Filler Three, Dmitri Other")
	text = d.press("or:s:"+s.session1.String(), "Только: "+s.eventName1+" · ")
	if !strings.Contains(text, "Последние · 4") {
		t.Errorf("date scope:\n%s", text)
	}
	buyers("session 1", state(), "Clara Zakharova, Karl Brandtner, Boris Petrov, Anna Orderova")
	d.press("or:t:p", "Оплаченные · 1")
	buyers("session 1 paid", state(), "Anna Orderova")
	d.press("or:a", "Оплаченные · 5")
	if v := state(); v.ScopeName != "" {
		t.Errorf("the scope must be dropped: %+v", v)
	}
	d.press("or:t:r", "Последние · 8")

	// From the Sessions dialog of the event: the date's own orders, and Back
	// goes to the event card.
	d.press("ses:list:"+s.event1.String(), "Сеансы мероприятия")
	d.press("ses:o:0", "Здесь можно перенести")
	d.press("ses:or", "Только: "+s.eventName1)
	if v := state(); len(v.Buyers) != 4 {
		t.Errorf("orders of the date from the Sessions dialog: %+v", v)
	}
	d.press("ec:o:"+s.event1.String(), s.eventName1)
}

func TestBotE2E_OrdersScreens_OwnerAndManager(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedOrdersBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	linkECBotUser(t, f, srv, ecManagerTG, "manager")
	linkECBotUser(t, other, srv, ecOutsiderTG, "owner")

	tg := newStubTelegram(t)
	startDialogTestBot(t, pool, api, tg)

	// The owner and the manager see the very same screens.
	ordScreens(t, pool, seed, ecDriver{t, tg, ecOwnerTG}, seed.clara, "Clara Zakharova")
	ordScreens(t, pool, seed, ecDriver{t, tg, ecManagerTG}, seed.karl, "Karl Brandtner")

	// Opening another screen ended the dialogs.
	d := ecDriver{t, tg, ecOwnerTG}
	d.press("home", "Что будем делать")
	var left int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM bot_dialogs WHERE telegram_user_id = ANY($1) AND kind = 'orders'`, []int64{ecOwnerTG, ecManagerTG}).Scan(&left); err != nil || left != 0 {
		t.Errorf("orders dialogs left after leaving the screens: %d (%v)", left, err)
	}

	// Somebody from another organization: an empty list, and every id of the
	// first organization is "not found" — no name, no e-mail, no cancellation.
	out := ecDriver{t, tg, ecOutsiderTG}
	out.press("or:new", "Заказов пока нет")
	out.say(seed.annaEmail, "заказов не нашлось")
	for _, c := range []string{
		"or:v:" + seed.anna.String(),
		"or:c:" + seed.karl.String(),
		"or:c:" + seed.anna.String(),
		"or:e:" + seed.event1.String(),
		"or:s:" + seed.session1.String(),
	} {
		got := out.press(c, "Не найдено")
		for _, leak := range []string{"Anna", seed.annaEmail, "611 222", "Karl", "Orders Show"} {
			if strings.Contains(got, leak) {
				t.Errorf("%s leaked %q to another organization:\n%s", c, leak, got)
			}
		}
	}
	// The outsider typing the cancel word cancels nothing: their list is open, so it is only a search.
	out.say("отменить", "найдено 0")
	for id, want := range map[uuid.UUID]string{seed.anna: "paid", seed.karl: "cancelled", seed.boris: "expired"} {
		if got := orderStatus(t, pool, id); got != want {
			t.Errorf("order %s is %q after the outsider's presses, want %q", id, got, want)
		}
	}
}

// The tab, the search the person typed and the page are on disk: a bot
// restart in the middle changes nothing, and a screen that ran out is
// reported once.
func TestBotE2E_OrdersSurviveBotRestart(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedOrdersBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	defer func() { stop() }()
	d := ecDriver{t, tg, ecOwnerTG}

	d.press("or:new", "Последние · 8")
	d.press("or:t:u", "Неоплаченные · 3")
	d.say("petrov", "Поиск: «petrov»")

	stop = restartDialogTestBot(t, pool, api, tg, stop)

	// After the restart the list is still the unpaid tab, still filtered, and
	// a button of the old message still opens the right order.
	text := d.press("or:p:1", "Неоплаченные · ")
	if !strings.Contains(text, "Поиск: «petrov»") {
		t.Errorf("the search did not survive the restart:\n%s", text)
	}
	v := readOrdersDialog(t, pool, ecOwnerTG)
	if v.Tab != "unpaid" || v.Query != "petrov" || strings.Join(v.Buyers, ",") != "Boris Petrov" {
		t.Errorf("state after the restart = %+v", v)
	}
	d.press("or:o:0", fmt.Sprintf("Заказ №%d", seed.borisSID))

	// A cancel prompt survives a restart too: the word typed afterwards is
	// still taken for the word.
	d.press("or:v:"+seed.clara.String(), "Clara Zakharova")
	d.press("or:c:"+seed.clara.String(), "Последний шаг")
	stop = restartDialogTestBot(t, pool, api, tg, stop)
	d.say("не надо", "не то слово")
	if got := orderStatus(t, pool, seed.clara); got != "pending_payment" {
		t.Fatalf("a wrong word after the restart changed the order to %q", got)
	}
	d.say("CANCEL", "Заказ отменён")
	if got := orderStatus(t, pool, seed.clara); got != "cancelled" {
		t.Fatalf("the English word must cancel in any language, status = %q", got)
	}

	// The screen that ran out says so once; the next text is just a text.
	d.press("or:b", "Неоплаченные · ")
	if _, err := pool.Exec(context.Background(),
		`UPDATE bot_dialogs SET expires_at = now() - interval '1 minute' WHERE telegram_user_id = $1 AND kind = 'orders'`, ecOwnerTG); err != nil {
		t.Fatal(err)
	}
	d.say("petrov", "Этот диалог устарел")
	m := tg.mark()
	tg.push(e2eMessageAs(ecOwnerTG, "petrov"))
	if got := tg.waitSince(t, m, "Я не понял"); strings.Contains(got, "устарел") {
		t.Errorf("an expired dialog must be reported once:\n%s", got)
	}
}

// restartDialogTestBot restarts the bot between two steps of a test.
func restartDialogTestBot(t *testing.T, pool *pgxpool.Pool, api *httptest.Server, tg *stubTelegram, stop func()) func() {
	t.Helper()
	stop()
	time.Sleep(400 * time.Millisecond) // the stub's long poll
	return startDialogTestBot(t, pool, api, tg)
}

//go:build integration

package httpserver

// End-to-end proof of the bot's event-center screens of stage 1 (spec 35
// EC-02, EC-03, EC-06, EC-07, EC-08) through the real router, the real bot
// and the stub Telegram: an organization with events in every sales state
// and a paid order with tickets; an owner and a manager both drive the list
// (filters, state, paging, search), the event card, the summaries, the CSV
// exports and the notifications screen; a user of ANOTHER organization sees
// none of it (the API answers 404 and the bot says "not found"); and a bot
// restart in the middle of a search leaves the list where it was.
//
// The stub records message TEXT only, so what a button shows is verified
// through the dialog state in bot_dialogs (the ids of the rows on screen).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
)

const (
	ecOwnerTG    = int64(7811)
	ecManagerTG  = int64(7812)
	ecOutsiderTG = int64(7813)
)

// ecBotSeed is what the e2e put into the organization.
type ecBotSeed struct {
	suffix       string
	richEvent    uuid.UUID // two dates, a paid order, an invitation, a promo code
	richFuture   uuid.UUID // its future session
	richPast     uuid.UUID
	archiveEvent uuid.UUID
	byName       map[string]uuid.UUID
}

// seedECBotOrg fills the organization: eight running events (the rich one,
// five on sale, one upcoming, one sold out) and one archived — nine in all,
// so the running filter takes two pages.
func seedECBotOrg(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) *ecBotSeed {
	t.Helper()
	ctx := context.Background()
	q := gen.New(pool)
	s := &ecBotSeed{suffix: uuid.NewString()[:6], byName: map[string]uuid.UUID{}}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed %q: %v", sql, err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		sessions := `(SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`
		for _, sql := range []string{
			`DELETE FROM promo_code_redemptions WHERE promo_code_id IN (SELECT id FROM promo_codes WHERE org_id = $1)`,
			`DELETE FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`,
			`DELETE FROM tickets WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`,
			`DELETE FROM orders WHERE org_id = $1`,
			`DELETE FROM session_seats WHERE session_id IN ` + sessions,
			`DELETE FROM checkout_sessions WHERE org_id = $1`,
			`DELETE FROM reservations WHERE org_id = $1`,
			`DELETE FROM ticket_tiers WHERE session_id IN ` + sessions,
			`DELETE FROM promo_codes WHERE org_id = $1`,
			`DELETE FROM sales_channels WHERE org_id = $1`,
			`DELETE FROM inventory_ledger WHERE session_id IN ` + sessions,
			`DELETE FROM sessions WHERE id IN ` + sessions,
			`DELETE FROM events WHERE org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
		} {
			if _, err := pool.Exec(c, sql, orgID); err != nil {
				t.Logf("ec bot cleanup: %s: %v", sql, err)
			}
		}
	})

	venue, channel := uuid.New(), uuid.New()
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, venue, orgID, "ECBot Hall "+s.suffix)
	exec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`, channel, orgID, "ECBot Channel "+s.suffix)

	day := 24 * time.Hour
	base := time.Now().UTC().Add(30 * day).Truncate(time.Hour)
	past := time.Now().UTC().Add(-30 * day).Truncate(time.Hour)

	// places inserts the five GA places of a category (post-0101 shape).
	places := func(sessionID, tierID uuid.UUID, status string) {
		for n := 1; n <= 5; n++ {
			exec(`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
			      VALUES ($1, $2, '', '', '', $3, $4, 'ga_unit')`, sessionID, fmt.Sprintf("ga|t1|00000%d", n), tierID, status)
		}
	}
	// event inserts an event with one session, one category and five places.
	event := func(name string, start time.Time, windowStart *time.Time, placeStatus string) (eventID, sessionID, tierID uuid.UUID) {
		eventID, sessionID, tierID = uuid.New(), uuid.New(), uuid.New()
		full := "ECBot " + name + " " + s.suffix
		exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, eventID, orgID, full)
		exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		      VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 5, 'scheduled', 'EUR', 'override')`, sessionID, eventID, venue, start)
		exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, unit_seq, is_open, sale_window_start)
		      VALUES ($1, $2, 'Standing', 'fixed', 2500, 'EUR', 5, 1, true, $3)`, tierID, sessionID, windowStart)
		places(sessionID, tierID, placeStatus)
		s.byName[full] = eventID
		return eventID, sessionID, tierID
	}

	// The rich event: a future date with a paid order, an invitation and a
	// promo code, and a past date with nothing sold.
	var tierFuture uuid.UUID
	s.richEvent, s.richFuture, tierFuture = event("Swan Lake", base, nil, "available")
	_, s.richPast, _ = func() (uuid.UUID, uuid.UUID, uuid.UUID) {
		sessionID, tierID := uuid.New(), uuid.New()
		exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		      VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 5, 'scheduled', 'EUR', 'override')`, sessionID, s.richEvent, venue, past)
		exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, unit_seq, is_open)
		      VALUES ($1, $2, 'Standing', 'fixed', 2500, 'EUR', 5, 1, true)`, tierID, sessionID)
		places(sessionID, tierID, "available")
		return s.richEvent, sessionID, tierID
	}()

	// Five events on sale after it, then one upcoming, one sold out.
	for i := 2; i <= 6; i++ {
		event(fmt.Sprintf("Jazz Night %d", i), base.Add(time.Duration(i-1)*day), nil, "available")
	}
	opensLater := base.Add(6*day - time.Hour) // the sale has not opened yet
	event("Upcoming Gala", base.Add(6*day), &opensLater, "available")
	event("Soldout Show", base.Add(7*day), nil, "sold")
	s.archiveEvent, _, _ = event("Archived Opera", past, nil, "available")

	// The paid order (two tickets, promo code), the invitation, the door count.
	resPaid, csPaid, resCompl, csCompl := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	paidOrder, complOrder := uuid.New(), uuid.New()
	email := "ecbot-buyer-" + s.suffix + "@example.test"
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	      VALUES ($1, $2, $3, $4, 2, 'converted', now() + interval '1 hour', now())`, resPaid, orgID, channel, s.richFuture)
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	      VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, resCompl, orgID, channel, s.richFuture)
	exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`, csPaid, orgID, channel, resPaid)
	exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`, csCompl, orgID, channel, resCompl)
	exec(`UPDATE session_seats SET status = 'sold', reservation_id = $2 WHERE session_id = $1 AND seat_key = 'ga|t1|000001'`, s.richFuture, resPaid)
	exec(`UPDATE session_seats SET status = 'sold', reservation_id = $2 WHERE session_id = $1 AND seat_key = 'ga|t1|000002'`, s.richFuture, resPaid)
	exec(`UPDATE session_seats SET status = 'sold', reservation_id = $2 WHERE session_id = $1 AND seat_key = 'ga|t1|000003'`, s.richFuture, resCompl)
	promo, err := q.InsertPromoCode(ctx, orgID, "ECBOT"+strings.ToUpper(s.suffix), "fixed_amount", 500, []string{}, []string{}, "EUR", nil, nil, nil, nil, 0, "active")
	if err != nil {
		t.Fatalf("InsertPromoCode: %v", err)
	}
	exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                          source, status, currency, subtotal, discount, charge, total, promo_code_id, buyer_name, buyer_email)
	      VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 5000, 500, 200, 4700, $8, 'Buyer Name', $9)`,
		paidOrder, orgID, channel, s.richEvent, s.richFuture, csPaid, resPaid, promo.ID, email)
	exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                          source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email)
	      VALUES ($1, $2, $3, $4, $5, $6, $7, 'complimentary', 'paid', 'EUR', 2500, 2500, 0, 0, 'Guest', $8)`,
		complOrder, orgID, channel, s.richEvent, s.richFuture, csCompl, resCompl, email)
	tickets := []struct {
		order, cs uuid.UUID
		ordinal   int
		used      bool
		unit      int64
		discount  int64
		charge    int64
		total     int64
	}{
		{paidOrder, csPaid, 0, true, 2500, 250, 100, 2350},
		{paidOrder, csPaid, 1, false, 2500, 250, 100, 2350},
		{complOrder, csCompl, 0, false, 2500, 2500, 0, 0},
	}
	for _, tk := range tickets {
		ticketID := uuid.New()
		var usedAt *time.Time
		if tk.used {
			n := time.Now().UTC()
			usedAt = &n
		}
		exec(`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal, used_at)
		      VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, ticketID, tk.cs, s.richFuture, tierFuture, email, tk.order, tk.ordinal, usedAt)
		exec(`INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, ticket_id, unit_price, discount, charge, total)
		      VALUES ($1, $2, $3, 'ticket', $4, $5, $6, $7, $8, $9)`, uuid.New(), tk.order, tk.ordinal+1, tierFuture, ticketID, tk.unit, tk.discount, tk.charge, tk.total)
	}
	if err := q.InsertPromoCodeRedemption(ctx, promo.ID, nil, &resPaid, 500, 5000, &paidOrder, nil, &channel); err != nil {
		t.Fatalf("InsertPromoCodeRedemption: %v", err)
	}
	return s
}

// linkECBotUser binds a Telegram account to the organization as an owner or a
// manager through the real invitation routes, as the deep link would.
func linkECBotUser(t *testing.T, f *botInviteFixture, srv *Server, tgID int64, role string) {
	t.Helper()
	ctx := context.Background()
	_, _ = f.pool.Exec(ctx, `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, tgID)
	email := f.emails[0]
	if role != "owner" {
		email = f.newEmail(role)
	}
	if rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":%q,"locale":"ru"}`, email, role)); rec.Code != http.StatusCreated {
		t.Fatalf("invite %s: %d %s", role, rec.Code, rec.Body.String())
	}
	code, _ := f.queuedCode(email)
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d,"locale":"ru"}`, code, email, tgID)); rec.Code != http.StatusOK {
		t.Fatalf("accept %s: %d %s", role, rec.Code, rec.Body.String())
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, tgID)
	})
}

// ecListState is the events list's dialog as it is stored.
type ecListState struct {
	Step   string
	Filter string
	Query  string
	Page   int
	Names  []string // the events on the page on screen, in order
}

func readECListState(t *testing.T, pool *pgxpool.Pool, tg int64) ecListState {
	t.Helper()
	ctx := context.Background()
	var step string
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT step, state FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'events'`, tg).Scan(&step, &raw); err != nil {
		t.Fatalf("the events dialog must be stored for %d: %v", tg, err)
	}
	var st struct {
		Filter string      `json:"filter"`
		Query  string      `json:"query"`
		Page   int         `json:"page"`
		IDs    []uuid.UUID `json:"ids"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("decode events dialog: %v", err)
	}
	out := ecListState{Step: step, Filter: st.Filter, Query: st.Query, Page: st.Page}
	for _, id := range st.IDs {
		var name string
		if err := pool.QueryRow(ctx, `SELECT name FROM events WHERE id = $1`, id).Scan(&name); err != nil {
			t.Fatalf("event %s of the stored page: %v", id, err)
		}
		out.Names = append(out.Names, name)
	}
	return out
}

// ecShort strips the common prefix and the run suffix from a seeded name.
func ecShort(name, suffix string) string {
	return strings.TrimSuffix(strings.TrimPrefix(name, "ECBot "), " "+suffix)
}

func (s *ecBotSeed) shortNames(names []string) string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, ecShort(n, s.suffix))
	}
	return strings.Join(out, ", ")
}

// ecDriver presses buttons and types texts as one Telegram account.
type ecDriver struct {
	t  *testing.T
	tg *stubTelegram
	id int64
}

func (d ecDriver) press(data, expect string) string {
	d.t.Helper()
	m := d.tg.mark()
	d.tg.push(e2eCallbackAs(d.id, data))
	return d.tg.waitSince(d.t, m, expect)
}

func (d ecDriver) say(text, expect string) string {
	d.t.Helper()
	m := d.tg.mark()
	d.tg.push(e2eMessageAs(d.id, text))
	return d.tg.waitSince(d.t, m, expect)
}

// ecScreens drives every screen of the stage as one linked user.
func ecScreens(t *testing.T, pool *pgxpool.Pool, s *ecBotSeed, d ecDriver, notifExpect string) {
	t.Helper()
	want := func(label, got, wantShort string) {
		t.Helper()
		if got != wantShort {
			t.Errorf("%s: page shows [%s], want [%s]", label, got, wantShort)
		}
	}

	// Running is the default; five rows a page, soonest date first; the state
	// chips come from the server (verified through the rows on screen).
	text := d.press("el:new", "Идут · 8")
	if !strings.Contains(text, "Страница 1 из 2") {
		t.Errorf("first page of the list:\n%s", text)
	}
	st := readECListState(t, pool, d.id)
	want("page 1", s.shortNames(st.Names), "Swan Lake, Jazz Night 2, Jazz Night 3, Jazz Night 4, Jazz Night 5")
	if st.Filter != "run" || st.Step != "list" || st.Page != 1 {
		t.Errorf("state = %+v", st)
	}
	d.press("el:p:2", "Страница 2 из 2")
	st = readECListState(t, pool, d.id)
	want("page 2", s.shortNames(st.Names), "Jazz Night 6, Upcoming Gala, Soldout Show")

	// Archive.
	text = d.press("el:f:arc", "Архив · 1")
	if !strings.Contains(text, "Страница 1 из 1") {
		t.Errorf("archive page:\n%s", text)
	}
	st = readECListState(t, pool, d.id)
	want("archive", s.shortNames(st.Names), "Archived Opera")
	d.press("el:f:run", "Идут · 8")

	// Search: typed text filters the names already loaded, case-insensitively.
	text = d.say("JAZZ", "найдено 5")
	if !strings.Contains(text, "Поиск: «JAZZ»") {
		t.Errorf("search line:\n%s", text)
	}
	st = readECListState(t, pool, d.id)
	want("search jazz", s.shortNames(st.Names), "Jazz Night 2, Jazz Night 3, Jazz Night 4, Jazz Night 5, Jazz Night 6")
	d.say("swan lake", "найдено 1")
	d.say("zzz-nothing", "ничего не нашлось")
	if st = readECListState(t, pool, d.id); len(st.Names) != 0 || st.Query != "zzz-nothing" {
		t.Errorf("empty search state = %+v", st)
	}
	d.press("el:x", "Идут · 8")
	if st = readECListState(t, pool, d.id); st.Query != "" || len(st.Names) != 5 {
		t.Errorf("cleared search state = %+v", st)
	}

	// The card of the rich event: the whole event first, then the figures.
	card := d.press("el:o:0", "Все даты (2)")
	for _, w := range []string{
		"ECBot Swan Lake", "Продано 3 из 10, свободно 7", "Оплачено 47 EUR, заказов: 2",
		"Возвраты: 0 на 0 EUR, нетто 47 EUR", "Вошло: 1 из 3", "Пригласительных: 1",
		"использован 1 раз, скидка 5 EUR", "Категории:", "• Standing — 25 EUR — 3/",
	} {
		if !strings.Contains(card, w) {
			t.Errorf("event card lacks %q:\n%s", w, card)
		}
	}
	t.Logf("event card:\n%s", card)
	if st = readECListState(t, pool, d.id); st.Step != "card" {
		t.Errorf("a card is not a search: step = %q", st.Step)
	}
	// A typed text on a card is no search.
	d.say("абракадабра", "Я не понял")

	// The dates, expanded.
	exp := d.press("ec:o:"+s.richEvent.String()+":1", "Продано 3 из 5")
	if !strings.Contains(exp, "Ближайшая дата") && !strings.Contains(exp, "Все даты (2)") {
		t.Errorf("expanded card:\n%s", exp)
	}

	// Summaries: the event and one date.
	sum := d.press("ec:es:"+s.richEvent.String(), "Сводка: ECBot Swan Lake")
	for _, w := range []string{"Билеты: действуют 3", "вошли 1", "Заказы: оплачено 2", "Нетто: <b>47 EUR</b>", "скидки 30 EUR"} {
		if !strings.Contains(sum, w) {
			t.Errorf("event summary lacks %q:\n%s", w, sum)
		}
	}
	t.Logf("event summary:\n%s", sum)
	d.press("ec:ss:"+s.richFuture.String(), "Билеты: действуют 3")

	// CSV: a document with a caption, in the requesting chat only.
	before := d.tg.callCount("sendDocument")
	d.press("ec:cs:"+s.richFuture.String(), "[document sales_")
	d.tg.waitCall(d.t, "sendDocument", before)
	var salesName string
	d.tg.mu.Lock()
	for name := range d.tg.docs {
		if strings.HasPrefix(name, "sales_") && strings.HasSuffix(name, ".csv") {
			salesName = name
		}
	}
	d.tg.mu.Unlock()
	body, ok := d.tg.document(salesName)
	if !ok || !strings.HasPrefix(string(body), "\xef\xbb\xbf") || eventbot.CSVRows(body) != 3 || !strings.Contains(string(body), ";") {
		t.Fatalf("sales CSV %q: ok=%v rows=%d body:\n%s", salesName, ok, eventbot.CSVRows(body), body)
	}
	m := d.tg.mark()
	d.tg.push(e2eCallbackAs(d.id, "ec:ce:"+s.richEvent.String()))
	capText := d.tg.waitSince(d.t, m, "[document sales_")
	t.Logf("event CSV caption: %s", capText)
	if !strings.Contains(capText, "билетов: 3") || !strings.Contains(capText, "Все даты (2)") {
		t.Errorf("event CSV caption: %s", capText)
	}
	d.press("ec:cm:"+s.richFuture.String(), "категорий: 1")

	// The same two buttons sit on the card of a date in the Sessions dialog
	// (index 1 is the future date; the past one comes first).
	d.press("ses:list:"+s.richEvent.String(), "Сеансы мероприятия")
	d.press("ses:o:1", "Здесь можно перенести")
	d.press("ses:sm", "Билеты: действуют 3")
	d.press("ses:card", "Здесь можно перенести")
	before = d.tg.callCount("sendDocument")
	d.press("ses:csv", "[document sales_")
	d.tg.waitCall(d.t, "sendDocument", before)

	// Notifications.
	n := d.press("ec:nt", "@ArenaSoldOutSalesBot")
	if !strings.Contains(n, notifExpect) {
		t.Errorf("notifications text lacks %q:\n%s", notifExpect, n)
	}
}

func TestBotE2E_EventCenterScreens_OwnerAndManager(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedECBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	linkECBotUser(t, f, srv, ecManagerTG, "manager")
	linkECBotUser(t, other, srv, ecOutsiderTG, "owner")

	tg := newStubTelegram(t)
	startDialogTestBot(t, pool, api, tg)

	// The owner and the manager see the very same screens.
	ecScreens(t, pool, seed, ecDriver{t, tg, ecOwnerTG}, "Владельцы там администраторы")
	ecScreens(t, pool, seed, ecDriver{t, tg, ecManagerTG}, "решает только владелец")

	// Opening another screen (Sessions, Notifications) ended the list's
	// dialog, so a text typed there is not taken for a search.
	var left int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM bot_dialogs WHERE telegram_user_id = ANY($1) AND kind = 'events'`, []int64{ecOwnerTG, ecManagerTG}).Scan(&left); err != nil || left != 0 {
		t.Errorf("events dialogs left after leaving the list: %d (%v)", left, err)
	}

	// Somebody from another organization: an empty list, and every id of the
	// first organization is "not found" — no figures, no document.
	out := ecDriver{t, tg, ecOutsiderTG}
	out.press("el:new", "Ивентов пока нет")
	docs := tg.callCount("sendDocument")
	for _, c := range []struct{ data, needle string }{
		{"ec:es:" + seed.richEvent.String(), "Не найдено"},
		{"ec:ss:" + seed.richFuture.String(), "Не найдено"},
		{"ec:cs:" + seed.richFuture.String(), "Не найдено"},
		{"ec:ce:" + seed.richEvent.String(), "Не найдено"},
		{"ec:cm:" + seed.richFuture.String(), "Не найдено"},
	} {
		got := out.press(c.data, c.needle)
		if strings.Contains(got, "Swan Lake") || strings.Contains(got, "47 EUR") {
			t.Errorf("%s leaked the other organization's data:\n%s", c.data, got)
		}
	}
	// A forged card of a foreign event falls back to the (empty) list.
	out.press("ec:o:"+seed.richEvent.String(), "Ивентов пока нет")
	if got := tg.callCount("sendDocument"); got != docs {
		t.Errorf("the outsider received %d document(s)", got-docs)
	}
}

// The search the person typed, the filter and the page are on disk: a bot
// restart in the middle changes nothing, and a list that ran out is reported
// once.
func TestBotE2E_EventsListSurvivesBotRestart(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedECBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	d := ecDriver{t, tg, ecOwnerTG}

	d.press("el:new", "Идут · 8")
	d.press("el:f:arc", "Архив · 1")
	d.say("opera", "Поиск: «opera»")

	stop()
	time.Sleep(400 * time.Millisecond) // the stub's long poll
	startDialogTestBot(t, pool, api, tg)

	// After the restart the list is still the archive, still filtered, and a
	// button of the old message still opens the right event.
	text := d.press("el:p:1", "Архив · 1")
	if !strings.Contains(text, "Поиск: «opera»") {
		t.Errorf("the search did not survive the restart:\n%s", text)
	}
	st := readECListState(t, pool, ecOwnerTG)
	if st.Filter != "arc" || st.Query != "opera" || seed.shortNames(st.Names) != "Archived Opera" {
		t.Errorf("state after the restart = %+v", st)
	}
	d.press("el:o:0", "ECBot Archived Opera")

	// The list that ran out says so once; the next text is just a text.
	d.press("el:b", "Архив · 1")
	if _, err := pool.Exec(context.Background(),
		`UPDATE bot_dialogs SET expires_at = now() - interval '1 minute' WHERE telegram_user_id = $1 AND kind = 'events'`, ecOwnerTG); err != nil {
		t.Fatal(err)
	}
	d.say("jazz", "Этот диалог устарел")
	m := tg.mark()
	tg.push(e2eMessageAs(ecOwnerTG, "jazz"))
	got := tg.waitSince(t, m, "Я не понял")
	if strings.Contains(got, "устарел") {
		t.Errorf("an expired dialog must be reported once:\n%s", got)
	}
}

// A CSV above the server's row cap is refused politely: the person is told to
// narrow it to one date, and no document is sent.
func TestBotE2E_CSVTooManyRowsIsRefusedPolitely(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	srv.exportMaxRows = 1 // two tickets in the event are more than one file may hold
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedECBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecManagerTG, "manager")

	tg := newStubTelegram(t)
	startDialogTestBot(t, pool, api, tg)
	d := ecDriver{t, tg, ecManagerTG}
	docs := tg.callCount("sendDocument")
	got := d.press("ec:ce:"+seed.richEvent.String(), "Строк слишком много")
	if !strings.Contains(got, "по одной дате") {
		t.Errorf("the refusal must say what to do:\n%s", got)
	}
	d.press("ec:cs:"+seed.richFuture.String(), "слишком много строк")
	if got := tg.callCount("sendDocument"); got != docs {
		t.Errorf("%d document(s) were sent for a refused export", got-docs)
	}
}

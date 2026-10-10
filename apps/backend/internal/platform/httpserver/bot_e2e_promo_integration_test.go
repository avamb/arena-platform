//go:build integration

package httpserver

// End-to-end proof of the promo-code screens (spec 35 EC-11) through the real
// router, the real bot and the stub Telegram: the owner and the manager create
// a percent code and a fixed code step by step (a percent over 100, a taken
// name, a name only a DELETED code holds, a session in another currency and an
// amount with three decimals are all refused in plain words), pause and
// activate, turn a club code into a code for chosen sessions and back, read
// the usage list and the CSV after seeded paid orders with redemptions, delete
// with a wrong word and then the right one; a user of ANOTHER organization
// gets "not found" on every press and changes nothing; a bot restart in the
// middle of the creation loses nothing; a dialog that ran out is reported once.
//
// The stub records message TEXT only, so every effect is checked in the
// database; which buttons a screen shows is the unit tests' business
// (promo_test.go).

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

const (
	pmOwnerTG    = int64(7841)
	pmManagerTG  = int64(7842)
	pmOutsiderTG = int64(7843)
)

type pmSeed struct {
	pool    *pgxpool.Pool
	orgID   uuid.UUID
	suffix  string
	venue   uuid.UUID
	channel uuid.UUID
	eventA  uuid.UUID
	nameA   string
	sessA   []uuid.UUID // three EUR dates
	eventB  uuid.UUID
	nameB   string
	sessB   uuid.UUID // one CZK date
}

func (s *pmSeed) exec(sql string, args ...any) {
	if _, err := s.pool.Exec(context.Background(), sql, args...); err != nil {
		panic("promo seed " + sql + ": " + err.Error())
	}
}

// seedPromoOrg gives the organization two running events: A with three EUR
// dates (the soonest) and B with one CZK date.
func seedPromoOrg(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) *pmSeed {
	t.Helper()
	s := &pmSeed{pool: pool, orgID: orgID, suffix: uuid.NewString()[:6], venue: uuid.New(), channel: uuid.New(), eventA: uuid.New(), eventB: uuid.New()}
	s.nameA, s.nameB = "PM Swan "+s.suffix, "PM Czech "+s.suffix
	t.Cleanup(func() {
		c := context.Background()
		sessions := `(SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`
		for _, sql := range []string{
			`DELETE FROM bot_dialogs WHERE org_id = $1`,
			`DELETE FROM promo_code_redemptions WHERE promo_code_id IN (SELECT id FROM promo_codes WHERE org_id = $1)`,
			`DELETE FROM promo_codes WHERE org_id = $1`,
			`DELETE FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`,
			`DELETE FROM orders WHERE org_id = $1`,
			`DELETE FROM checkout_sessions WHERE org_id = $1`,
			`DELETE FROM reservations WHERE org_id = $1`,
			`DELETE FROM ticket_tiers WHERE session_id IN ` + sessions,
			`DELETE FROM sales_channels WHERE org_id = $1`,
			`DELETE FROM sessions WHERE id IN ` + sessions,
			`DELETE FROM events WHERE org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
			`DELETE FROM audit_events WHERE metadata->>'org_id' = $1::text`,
		} {
			if _, err := pool.Exec(c, sql, orgID); err != nil {
				t.Logf("promo bot cleanup: %s: %v", sql, err)
			}
		}
	})
	s.exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, s.venue, orgID, "PM Hall "+s.suffix)
	s.exec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`, s.channel, orgID, "PM Channel "+s.suffix)
	addEvent := func(id uuid.UUID, name, cur string, firstDay, dates int) []uuid.UUID {
		s.exec(`INSERT INTO events (id, org_id, name, status, visibility, slug) VALUES ($1, $2, $3, 'published', 'public', $4)`,
			id, orgID, name, "pm-"+uuid.NewString()[:8])
		var out []uuid.UUID
		for i := 0; i < dates; i++ {
			sid := uuid.New()
			out = append(out, sid)
			s.exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
			        VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 50, 'scheduled', $5, 'override')`,
				sid, id, s.venue, time.Now().UTC().Add(time.Duration(firstDay+i)*24*time.Hour).Truncate(time.Hour), cur)
			s.exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, unit_seq, is_open)
			        VALUES ($1, $2, 'Standing', 'fixed', 2500, $3, 50, 1, true)`, uuid.New(), sid, cur)
		}
		return out
	}
	s.sessA = addEvent(s.eventA, s.nameA, "EUR", 30, 3)
	s.sessB = addEvent(s.eventB, s.nameB, "CZK", 60, 1)[0]
	return s
}

// paidOrder inserts a paid order with a named buyer and returns it with its
// system number.
func (s *pmSeed) paidOrder(session uuid.UUID, event uuid.UUID, cur string, total int64, buyer string) (uuid.UUID, int64) {
	res, cs, order := uuid.New(), uuid.New(), uuid.New()
	s.exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	        VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, res, s.orgID, s.channel, session)
	s.exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`, cs, s.orgID, s.channel, res)
	s.exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                            source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, paid_at)
	        VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', $8, $9, 0, 0, $9, $10, $11, now())`,
		order, s.orgID, s.channel, event, session, cs, res, cur, total, buyer, "pm-"+uuid.NewString()[:8]+"@example.test")
	var num int64
	if err := s.pool.QueryRow(context.Background(), `SELECT system_id FROM orders WHERE id = $1`, order).Scan(&num); err != nil {
		panic(err)
	}
	return order, num
}

func (s *pmSeed) redeem(code, order uuid.UUID, discount, amount int64) {
	s.exec(`INSERT INTO promo_code_redemptions (promo_code_id, discount_amount, order_amount, order_id, channel_id)
	        VALUES ($1, $2, $3, $4, $5)`, code, discount, amount, order, s.channel)
}

// pmRow is a promo code as stored.
type pmRow struct {
	ID       uuid.UUID
	Type     string
	Value    int64
	Currency *string
	Sessions []string
	MaxUses  *int32
	PerBuyer *int32
	Until    *time.Time
	Status   string
	Deleted  bool
}

func pmCode(t *testing.T, pool *pgxpool.Pool, org uuid.UUID, code string) (pmRow, bool) {
	t.Helper()
	var r pmRow
	err := pool.QueryRow(context.Background(), `
		SELECT id, discount_type, discount_value, currency, applies_to_session_ids::text[], max_uses, max_uses_per_customer,
		       valid_until, status, deleted_at IS NOT NULL
		  FROM promo_codes WHERE org_id = $1 AND code = $2`, org, code).
		Scan(&r.ID, &r.Type, &r.Value, &r.Currency, &r.Sessions, &r.MaxUses, &r.PerBuyer, &r.Until, &r.Status, &r.Deleted)
	if err != nil {
		return pmRow{}, false
	}
	return r, true
}

func mustPmCode(t *testing.T, pool *pgxpool.Pool, org uuid.UUID, code string) pmRow {
	t.Helper()
	r, ok := pmCode(t, pool, org, code)
	if !ok {
		t.Fatalf("promo code %s is not stored", code)
	}
	return r
}

func pmDialogs(t *testing.T, pool *pgxpool.Pool, tg int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'promo'`, tg).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// startPromoTestBot is startDialogTestBot with the message limit switched off:
// this test types far faster than any person, and the limit (30 messages a
// minute, EC-01) is not what it is about.
func startPromoTestBot(t *testing.T, pool *pgxpool.Pool, api *httptest.Server, tg *stubTelegram) (stop func()) {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	bot, err := eventbot.New(eventbot.Options{
		Token:             "123:test-token",
		Queries:           gen.New(pool),
		Arena:             eventbot.NewArenaClient(api.URL, botTestServiceToken, api.Client()),
		Minter:            eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience),
		Texts:             eventbot.NewTexts(bundle),
		TelegramServerURL: tg.srv.URL,
		HTTPClient:        tg.srv.Client(),
		MessageRateLimit:  -1,
	})
	if err != nil {
		t.Fatalf("eventbot.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("bot.Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("bot did not stop after cancel")
		}
	}
	t.Cleanup(stop)
	return stop
}

// pmTicked counts the sessions ticked on the picker the person has open (the
// dialog's draft or its edit): the stub shows message text only, so the
// checkboxes are read from the stored state.
func pmTicked(t *testing.T, pool *pgxpool.Pool, tg int64) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM bot_dialogs d,
		       jsonb_array_elements(COALESCE(d.state #> '{draft,pick,sel}', d.state #> '{edit,pick,sel}', '[]'::jsonb)) e
		 WHERE d.telegram_user_id = $1 AND d.kind = 'promo' AND e = 'true'::jsonb`, tg).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func sameStrings(a []string, b ...uuid.UUID) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]bool{}
	for _, s := range a {
		set[s] = true
	}
	for _, id := range b {
		if !set[id.String()] {
			return false
		}
	}
	return true
}

func TestBotE2E_PromoCodes(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedPromoOrg(t, pool, f.orgID)
	foreign := seedPromoOrg(t, pool, other.orgID)
	_ = foreign
	linkECBotUser(t, f, srv, pmOwnerTG, "owner")
	linkECBotUser(t, f, srv, pmManagerTG, "manager")
	linkECBotUser(t, other, srv, pmOutsiderTG, "owner")

	// The truth about the roles: the owner and the manager hold all four.
	for _, role := range []string{"org_admin", "organizer"} {
		for _, perm := range []string{"promo.read", "promo.create", "promo.update", "promo.delete"} {
			var n int
			if err := pool.QueryRow(context.Background(), `
				SELECT count(*) FROM role_permissions rp
				  JOIN roles r ON r.id = rp.role_id AND r.org_id IS NULL
				  JOIN permissions p ON p.id = rp.permission_id
				 WHERE r.name = $1 AND p.name = $2`, role, perm).Scan(&n); err != nil || n != 1 {
				t.Fatalf("role %s must hold %s: n=%d err=%v", role, perm, n, err)
			}
		}
	}

	tg := newStubTelegram(t)
	stop := startPromoTestBot(t, pool, api, tg)
	owner := ecDriver{t, tg, pmOwnerTG}
	manager := ecDriver{t, tg, pmManagerTG}
	outsider := ecDriver{t, tg, pmOutsiderTG}
	data := func(kind string, id uuid.UUID) string { return "pm:" + kind + ":" + id.String() }

	// quick runs the shortest creation of a percent code for every session up to
	// the summary.
	quick := func(d ecDriver, code string) string {
		d.press("pm:n", "Напечатайте код")
		d.say(code, "Какая скидка?")
		d.press("pm:c:pct", "Сколько процентов")
		d.say("5", "Где действует код?")
		d.press("pm:c:all", "Сколько раз всего")
		d.press("pm:c:skip", "один покупатель")
		d.press("pm:c:skip", "До какого дня")
		d.press("pm:c:skip", "Включить код сразу")
		return d.press("pm:c:st:active", "Проверьте новый код")
	}
	ticked := func(want int, label string) {
		t.Helper()
		if got := pmTicked(t, pool, pmOwnerTG); got != want {
			t.Errorf("%s: %d sessions ticked, want %d", label, got, want)
		}
	}

	// A code that was created and deleted keeps its name: the API's own 409.
	seed.exec(`INSERT INTO promo_codes (org_id, code, discount_type, discount_value, deleted_at) VALUES ($1, 'HIDDEN', 'percent', 5, now())`, f.orgID)

	// ── an empty list ─────────────────────────────────────────────────────────
	text := owner.press("pm:l", "Кодов: 0")
	if !strings.Contains(text, "Промокодов пока нет") {
		t.Errorf("empty list:\n%s", text)
	}

	// ── a percent code for every session, the owner ───────────────────────────
	owner.press("pm:n", "Напечатайте код")
	owner.say("summer 25", "Допустимы только буквы")
	owner.say(strings.Repeat("a", 65), "длиннее 64")
	owner.say("summer25", "Какая скидка?")
	owner.say("пять", "Какая скидка?") // text where a button is expected
	owner.press("pm:c:pct", "Сколько процентов")
	owner.say("150", "от 1 до 100")
	owner.say("0", "от 1 до 100")
	owner.say("15%", "Где действует код?")
	owner.press("pm:c:all", "Сколько раз всего")
	owner.say("abc", "от 1 до 1000000")
	owner.say("100", "Сколько раз код может использовать один покупатель")
	owner.say("101", "общий лимит")
	owner.press("pm:c:skip", "До какого дня")
	owner.press("pm:c:d:2099-12-31", "Включить код сразу")
	summary := owner.press("pm:c:st:active", "Проверьте новый код")
	for _, w := range []string{"SUMMER25", "скидка 15%", "все сеансы, включая будущие", "всего 100 раз", "до 31.12.2099", "действует"} {
		if !strings.Contains(summary, w) {
			t.Errorf("summary lacks %q:\n%s", w, summary)
		}
	}
	if _, ok := pmCode(t, pool, f.orgID, "SUMMER25"); ok {
		t.Fatal("nothing is created before the Create press")
	}
	created := owner.press("pm:c:go", "создан")
	if !strings.Contains(created, "SUMMER25") {
		t.Errorf("created card:\n%s", created)
	}
	summer := mustPmCode(t, pool, f.orgID, "SUMMER25")
	wantUntil := time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC)
	if summer.Type != "percent" || summer.Value != 15 || summer.Currency != nil || len(summer.Sessions) != 0 || summer.Status != "active" ||
		summer.MaxUses == nil || *summer.MaxUses != 100 || summer.PerBuyer != nil || summer.Until == nil || !summer.Until.Equal(wantUntil) {
		t.Errorf("SUMMER25 as stored = %+v", summer)
	}
	if pmDialogs(t, pool, pmOwnerTG) != 1 { // the card keeps the dialog, the draft is gone
		t.Errorf("dialogs = %d", pmDialogs(t, pool, pmOwnerTG))
	}

	// ── names that are taken: in the list, and only in the database ───────────
	owner.press("pm:n", "Напечатайте код")
	owner.say("Summer25", "уже есть")
	owner.press("pm:c:cancel", "Новый код не создан")
	quick(owner, "hidden") // a deleted code is not on the list of taken names...
	again := owner.press("pm:c:go", "Удалённый код тоже сохраняет")
	if !strings.Contains(again, "Напечатайте код") {
		t.Errorf("the dialog must return to the code question:\n%s", again)
	}
	if h := mustPmCode(t, pool, f.orgID, "HIDDEN"); !h.Deleted {
		t.Fatal("the deleted code must stay deleted")
	}
	owner.say("hidden", "уже есть") // and is now on the list of taken names
	owner.press("pm:c:cancel", "Новый код не создан")
	if pmDialogs(t, pool, pmOwnerTG) != 1 {
		t.Errorf("after the cancel the list is the dialog")
	}
	// A press on the cancelled draft finds nothing behind it.
	owner.press("pm:c:pct", "Этот экран уже закрыт")

	// ── a fixed code from an event: one currency, so none is asked ────────────
	owner.press("pm:n:"+seed.eventA.String(), "Напечатайте код")
	owner.say("five", "Какая скидка?")
	owner.press("pm:c:fix", "Фиксированная скидка вычитается из всего")
	owner.say("0", "больше нуля")
	owner.say("5.555", "больше нуля")
	owner.say("1,000.5", "больше нуля")
	scope := owner.say("5,50", "Где действует код?")
	if !strings.Contains(scope, seed.nameA) {
		t.Errorf("the scope question must name the event:\n%s", scope)
	}
	owner.press("pm:c:pick", "Отметьте сеансы ивента")
	owner.press("pm:k:ok", "Отметьте хотя бы один сеанс")
	owner.press("pm:k:t:0", "Отметьте сеансы ивента")
	owner.press("pm:k:t:1", "Отметьте сеансы ивента")
	ticked(2, "FIVE")
	owner.press("pm:k:ok", "Сколько раз всего")
	owner.press("pm:c:skip", "один покупатель")
	owner.say("1", "До какого дня")
	owner.press("pm:c:skip", "Включить код сразу")
	fiveSummary := owner.press("pm:c:st:paused", "Проверьте новый код")
	for _, w := range []string{"FIVE", "5,50 EUR", "один раз", "Работает только на заказы в EUR", seed.nameA, "сеансов: 2", "1 на покупателя", "на паузе"} {
		if !strings.Contains(fiveSummary, w) {
			t.Errorf("fixed summary lacks %q:\n%s", w, fiveSummary)
		}
	}
	owner.press("pm:c:go", "создан")
	five := mustPmCode(t, pool, f.orgID, "FIVE")
	if five.Type != "fixed_amount" || five.Value != 550 || five.Currency == nil || *five.Currency != "EUR" || five.Status != "paused" ||
		!sameStrings(five.Sessions, seed.sessA[0], seed.sessA[1]) || five.MaxUses != nil || five.PerBuyer == nil || *five.PerBuyer != 1 || five.Until != nil {
		t.Errorf("FIVE as stored = %+v", five)
	}

	// Pause and activate (FIVE was created paused).
	owner.press(data("ac", five.ID), "снова действует")
	if mustPmCode(t, pool, f.orgID, "FIVE").Status != "active" {
		t.Fatal("activate must activate")
	}
	owner.press(data("ps", five.ID), "на паузе: покупатели")
	if mustPmCode(t, pool, f.orgID, "FIVE").Status != "paused" {
		t.Fatal("pause must pause")
	}

	// ── the manager: a percent code with a typed, confirmed date ──────────────
	manager.press("pm:n", "Напечатайте код")
	manager.say("mgr20", "Какая скидка?")
	manager.press("pm:c:pct", "Сколько процентов")
	manager.say("20", "Где действует код?")
	manager.press("pm:c:all", "Сколько раз всего")
	manager.press("pm:c:skip", "один покупатель")
	manager.press("pm:c:skip", "До какого дня")
	manager.say("31.12.2099", "Вы написали")
	manager.press("pm:c:dc:2099-12-31", "Включить код сразу")
	manager.press("pm:c:st:active", "Проверьте новый код")
	manager.press("pm:c:go", "создан")
	mgr := mustPmCode(t, pool, f.orgID, "MGR20")
	if mgr.Value != 20 || mgr.Until == nil || !mgr.Until.Equal(wantUntil) || mgr.MaxUses != nil || len(mgr.Sessions) != 0 {
		t.Errorf("MGR20 as stored = %+v", mgr)
	}

	// ── the manager: a fixed code from the menu, two currencies ───────────────
	manager.press("pm:n", "Напечатайте код")
	manager.say("mgrfix", "Какая скидка?")
	manager.press("pm:c:fix", "Фиксированная скидка вычитается из всего")
	cur := manager.say("10", "В какой валюте?")
	_ = cur
	manager.say("euros", "трёхбуквенный код")
	manager.press("pm:c:cur:CZK", "Где действует код?")
	manager.press("pm:c:pick", "Выберите ивент")
	manager.press("pm:k:ev:0", "Отметьте сеансы ивента") // event A: its dates sell in EUR
	manager.press("pm:k:t:0", "продаётся в другой валюте")
	if got := pmTicked(t, pool, pmManagerTG); got != 0 {
		t.Errorf("a session in another currency must not be ticked: %d", got)
	}
	manager.press("pm:k:chev", "Выберите ивент")
	manager.press("pm:k:ev:1", "Отметьте сеансы ивента") // event B: CZK
	manager.press("pm:k:t:0", "Отметьте сеансы ивента")
	manager.press("pm:k:ok", "Сколько раз всего")
	manager.press("pm:c:skip", "один покупатель")
	manager.press("pm:c:skip", "До какого дня")
	manager.press("pm:c:skip", "Включить код сразу")
	manager.press("pm:c:st:active", "Проверьте новый код")
	manager.press("pm:c:go", "создан")
	mf := mustPmCode(t, pool, f.orgID, "MGRFIX")
	if mf.Type != "fixed_amount" || mf.Value != 1000 || mf.Currency == nil || *mf.Currency != "CZK" || !sameStrings(mf.Sessions, seed.sessB) {
		t.Errorf("MGRFIX as stored = %+v", mf)
	}

	// ── the lists: the whole organization, and one event ──────────────────────
	all := owner.press("pm:l", "Кодов: 4")
	for _, code := range []string{"SUMMER25", "FIVE", "MGR20", "MGRFIX"} {
		if !strings.Contains(all, code) {
			t.Errorf("the full list lacks %s:\n%s", code, all)
		}
	}
	if strings.Contains(all, "HIDDEN") {
		t.Errorf("a deleted code is not listed:\n%s", all)
	}
	for _, w := range []string{"скидка 15%", "5,50 EUR на заказ", "всего 100 раз", "до 31.12.2099", "не использовался"} {
		if !strings.Contains(all, w) {
			t.Errorf("the list lacks %q:\n%s", w, all)
		}
	}
	forA := owner.press("pm:l:"+seed.eventA.String(), "Для ивента")
	for _, code := range []string{"SUMMER25", "FIVE", "MGR20"} { // club codes and the one naming A's sessions
		if !strings.Contains(forA, code) {
			t.Errorf("event A's list lacks %s:\n%s", code, forA)
		}
	}
	if strings.Contains(forA, "MGRFIX") || !strings.Contains(forA, "Кодов: 3") {
		t.Errorf("event A's list must not hold the code of event B:\n%s", forA)
	}
	forB := owner.press("pm:l:"+seed.eventB.String(), "Для ивента")
	if !strings.Contains(forB, "MGRFIX") || strings.Contains(forB, "FIVE") || !strings.Contains(forB, "Кодов: 3") {
		t.Errorf("event B's list:\n%s", forB)
	}

	// ── sessions: club code -> chosen sessions -> club code ───────────────────
	sess := owner.press(data("se", summer.ID), "Где должен действовать код?")
	if !strings.Contains(sess, "все сеансы, включая будущие") {
		t.Errorf("a club code says so:\n%s", sess)
	}
	owner.press(data("so", summer.ID), "Выберите ивент")
	owner.press("pm:k:ev:0", "Отметьте сеансы ивента")
	owner.press("pm:k:ok", "Отметьте хотя бы один сеанс") // an empty list would be a club code
	owner.press("pm:k:t:0", "Отметьте сеансы ивента")
	ticked(1, "SUMMER25 edit")
	saved := owner.press("pm:k:ok", "теперь действует только на отмеченные сеансы")
	if !strings.Contains(saved, "сеансов: 1") {
		t.Errorf("saved card:\n%s", saved)
	}
	if got := mustPmCode(t, pool, f.orgID, "SUMMER25"); !sameStrings(got.Sessions, seed.sessA[0]) {
		t.Errorf("SUMMER25 sessions = %v", got.Sessions)
	}
	// Add a date of the other event: the first stays.
	owner.press(data("so", summer.ID), "Выберите ивент")
	owner.press("pm:k:ev:1", "Отметьте сеансы ивента")
	owner.press("pm:k:t:0", "Отметьте сеансы ивента")
	owner.press("pm:k:ok", "теперь действует только на отмеченные сеансы")
	if got := mustPmCode(t, pool, f.orgID, "SUMMER25"); !sameStrings(got.Sessions, seed.sessA[0], seed.sessB) {
		t.Errorf("SUMMER25 sessions after the second event = %v", got.Sessions)
	}
	// Taking event A's date away leaves event B's: ending with nothing ticked is fine.
	owner.press(data("so", summer.ID), "Выберите ивент")
	owner.press("pm:k:ev:0", "Отметьте сеансы ивента")
	ticked(1, "A's date is ticked already")
	owner.press("pm:k:t:0", "Отметьте сеансы ивента") // untick it
	ticked(0, "A's date unticked")
	owner.press("pm:k:ok", "теперь действует только на отмеченные сеансы")
	if got := mustPmCode(t, pool, f.orgID, "SUMMER25"); !sameStrings(got.Sessions, seed.sessB) {
		t.Errorf("SUMMER25 sessions after removing A's = %v", got.Sessions)
	}
	// ... but never when it would empty the whole list (that is "all sessions").
	owner.press(data("so", summer.ID), "Выберите ивент")
	owner.press("pm:k:ev:1", "Отметьте сеансы ивента")
	owner.press("pm:k:t:0", "Отметьте сеансы ивента")
	ticked(0, "B's only date unticked")
	owner.press("pm:k:ok", "Отметьте хотя бы один сеанс")
	if got := mustPmCode(t, pool, f.orgID, "SUMMER25"); !sameStrings(got.Sessions, seed.sessB) {
		t.Errorf("a refused edit changes nothing: %v", got.Sessions)
	}
	// A fixed code in EUR cannot be given a CZK date.
	owner.press(data("so", five.ID), "Выберите ивент")
	owner.press("pm:k:ev:1", "Отметьте сеансы ивента")
	owner.press("pm:k:t:0", "продаётся в другой валюте")
	// Back to every session, including future ones: the empty list is stored.
	owner.press(data("sa", summer.ID), "теперь действует на все сеансы")
	if got := mustPmCode(t, pool, f.orgID, "SUMMER25"); len(got.Sessions) != 0 {
		t.Errorf("all sessions = an empty list, got %v", got.Sessions)
	}

	// ── usage: seeded paid orders with redemptions ────────────────────────────
	var firstNum int64
	for i := 0; i < 7; i++ {
		order, num := seed.paidOrder(seed.sessA[0], seed.eventA, "EUR", 2500, "Anna Promo")
		if i == 0 {
			firstNum = num
		}
		seed.redeem(summer.ID, order, 375, 2500)
	}
	// MGR20 was used in two currencies: its sum is not one amount.
	czOrder, _ := seed.paidOrder(seed.sessB, seed.eventB, "CZK", 50000, "Pavel Mixed")
	seed.redeem(mgr.ID, czOrder, 10000, 50000)
	euOrder, _ := seed.paidOrder(seed.sessA[1], seed.eventA, "EUR", 2500, "Eva Mixed")
	seed.redeem(mgr.ID, euOrder, 500, 2500)

	card := owner.press(data("v", summer.ID), "использован 7 раз")
	if !strings.Contains(card, "26,25 EUR") || strings.Contains(card, "example.test") {
		t.Errorf("SUMMER25 card must total the discounts in EUR and show no e-mail:\n%s", card)
	}
	mixed := owner.press(data("v", mgr.ID), "использован 2 раз")
	if !strings.Contains(mixed, "в разных валютах") || strings.Contains(mixed, "105") {
		t.Errorf("a code used in two currencies must not print a sum:\n%s", mixed)
	}
	usage := owner.press(data("u", summer.ID), "использование")
	for _, w := range []string{"Anna Promo", "3,75 EUR", "из 25 EUR", "Страница 1 из 2", "№"} {
		if !strings.Contains(usage, w) {
			t.Errorf("usage page 1 lacks %q:\n%s", w, usage)
		}
	}
	if strings.Contains(usage, "example.test") || strings.Contains(usage, "@") {
		t.Errorf("the usage list shows buyers' names only, no e-mail:\n%s", usage)
	}
	if strings.Count(usage, "Anna Promo") != 5 {
		t.Errorf("five rows a page:\n%s", usage)
	}
	page2 := owner.press("pm:up:"+summer.ID.String()+":2", "Страница 2 из 2")
	if strings.Count(page2, "Anna Promo") != 2 {
		t.Errorf("the second page holds the last two:\n%s", page2)
	}
	if !strings.Contains(owner.press("pm:up:"+summer.ID.String()+":1", "использование"), "№") || firstNum == 0 {
		t.Error("page 1 again")
	}
	m := tg.mark()
	tg.push(e2eCallbackAs(pmOwnerTG, data("csv", summer.ID)))
	tg.waitSince(t, m, "[document promo_summer25_")
	csvBody, _ := tg.document("promo_summer25_" + time.Now().UTC().Format("2006-01-02") + ".csv")
	if len(csvBody) == 0 {
		t.Fatal("the usage CSV never arrived as a document")
	}
	if !strings.Contains(string(csvBody), "Anna Promo") || strings.Count(string(csvBody), "\n") < 8 {
		t.Errorf("the CSV must hold the seven uses:\n%s", csvBody)
	}
	// A code nobody used has an empty usage list and nothing to export.
	owner.press(data("u", five.ID), "никто не пользовался")
	m = tg.mark()
	tg.push(e2eCallbackAs(pmOwnerTG, data("csv", five.ID)))
	tg.waitSince(t, m, "нечего")

	// ── delete: a wrong word, going away, then the word ───────────────────────
	ask := owner.press(data("del", five.ID), "Удалить код FIVE")
	if !strings.Contains(ask, "Использован раз: 0") || !strings.Contains(ask, "УДАЛИТЬ") || !strings.Contains(ask, "название останется занятым") {
		t.Errorf("delete question:\n%s", ask)
	}
	if mustPmCode(t, pool, f.orgID, "FIVE").Deleted {
		t.Fatal("the question deletes nothing")
	}
	owner.say("нет", "Это не то слово")
	if mustPmCode(t, pool, f.orgID, "FIVE").Deleted {
		t.Fatal("a wrong word deletes nothing")
	}
	owner.press("pm:l", "Кодов: 4") // leaving ends the question
	owner.say("УДАЛИТЬ", "Я не понял")
	if mustPmCode(t, pool, f.orgID, "FIVE").Deleted {
		t.Fatal("a word typed after leaving deletes nothing")
	}
	// A used code shows how often it was used.
	usedAsk := owner.press(data("del", summer.ID), "Использован раз: 7")
	if !strings.Contains(usedAsk, "SUMMER25") {
		t.Errorf("delete question of a used code:\n%s", usedAsk)
	}
	owner.press("pm:l", "Кодов: 4")
	owner.press(data("del", five.ID), "Удалить код FIVE")
	owner.say("УДАЛИТЬ", "удалён")
	if !mustPmCode(t, pool, f.orgID, "FIVE").Deleted {
		t.Fatal("the typed word must delete")
	}
	owner.press("pm:l", "Кодов: 3")
	// The name stays taken: the same name is refused by the server.
	quick(owner, "five")
	owner.press("pm:c:go", "Удалённый код тоже сохраняет")
	owner.press("pm:c:cancel", "Новый код не создан")
	// The manager deletes with the English word.
	manager.press(data("del", mgr.ID), "Удалить код MGR20")
	manager.say("delete", "удалён")
	if !mustPmCode(t, pool, f.orgID, "MGR20").Deleted {
		t.Fatal("the manager must be able to delete with the English word")
	}

	// ── another organization gets nothing, and paging in its own list ─────────
	for i := 0; i < 7; i++ {
		foreign.exec(`INSERT INTO promo_codes (org_id, code, discount_type, discount_value) VALUES ($1, $2, 'percent', 10)`,
			other.orgID, "OTHER"+string(rune('A'+i)))
	}
	for _, kind := range []string{"v", "ps", "ac", "se", "sa", "so", "u", "csv", "del"} {
		got := outsider.press(data(kind, summer.ID), "Не найдено")
		if strings.Contains(got, "SUMMER25") || strings.Contains(got, "Anna") {
			t.Errorf("%s leaked the other organization's code:\n%s", kind, got)
		}
	}
	outsider.press("pm:up:"+summer.ID.String()+":1", "Не найдено")
	outsider.press("pm:l:"+seed.eventA.String(), "Не найдено")
	outsider.press("pm:n:"+seed.eventA.String(), "Не найдено")
	outsider.say("УДАЛИТЬ", "Я не понял")
	if got := mustPmCode(t, pool, f.orgID, "SUMMER25"); got.Deleted || got.Status != "active" || len(got.Sessions) != 0 || got.Value != 15 {
		t.Errorf("a user of another organization changed the code: %+v", got)
	}
	list := outsider.press("pm:l", "Кодов: 7")
	if strings.Contains(list, "SUMMER25") || !strings.Contains(list, "страница 1 из 2") || !strings.Contains(list, "OTHERG") || strings.Contains(list, "OTHERA") {
		t.Errorf("the outsider's own list:\n%s", list)
	}
	p2 := outsider.press("pm:p:2", "страница 2 из 2")
	if !strings.Contains(p2, "OTHERA") || !strings.Contains(p2, "OTHERB") || strings.Contains(p2, "OTHERG") {
		t.Errorf("page 2:\n%s", p2)
	}
	outsider.press("pm:o:1", "OTHERA")   // the codes are listed newest first: page 2 is B, A
	outsider.press("pm:o:4", "Кодов: 7") // a row that is not on the page opens nothing

	// ── the dialog survives a bot restart; an expired one is reported once ────
	owner.press("pm:n", "Напечатайте код")
	owner.say("restartme", "Какая скидка?")
	owner.press("pm:c:pct", "Сколько процентов")
	stop()
	time.Sleep(400 * time.Millisecond) // the stub's long poll
	startPromoTestBot(t, pool, api, tg)
	owner.say("25", "Где действует код?")
	owner.press("pm:c:cancel", "Новый код не создан")

	owner.press("pm:n", "Напечатайте код")
	owner.say("lapse", "Какая скидка?")
	if _, err := pool.Exec(context.Background(),
		`UPDATE bot_dialogs SET expires_at = now() - interval '1 minute' WHERE telegram_user_id = $1 AND kind = 'promo'`, pmOwnerTG); err != nil {
		t.Fatal(err)
	}
	owner.say("lapse2", "Этот диалог устарел")
	mark := tg.mark()
	tg.push(e2eMessageAs(pmOwnerTG, "lapse3"))
	if got := tg.waitSince(t, mark, "Я не понял"); strings.Contains(got, "устарел") {
		t.Errorf("an expired dialog is reported once:\n%s", got)
	}
	if _, ok := pmCode(t, pool, f.orgID, "LAPSE2"); ok {
		t.Fatal("an expired dialog creates nothing")
	}
	_ = manager
}

//go:build integration

package httpserver

// End-to-end proof of the "Refer a friend" screens (spec 52 section 5.2 and
// 7.1) through the real router, the real bot and the stub Telegram: the owner
// turns the programme on step by step (a percent out of range, text where a
// number is expected and a friend count of zero are refused in plain words,
// nothing is stored before the rules screen's button), the manager pauses and
// resumes it, the owner changes it keeping stored values, reads the results
// and the cashback list (five a page, a state chip) after seeded rewards,
// ends it only through the question and its separate "Yes" (a stale "Yes" on
// the card ends nothing), and starts a new one over the ended one. An event
// whose channel cannot return a part of a payment is explained, and a forced
// turn-on there is refused by the server in plain words. A user of ANOTHER
// organization gets "not found" and changes nothing; a dialog ended by another
// press takes no more typed text.
//
// The stub records message TEXT only, so every effect is checked in the
// database.

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	rfOwnerTG    = int64(7871)
	rfManagerTG  = int64(7872)
	rfOutsiderTG = int64(7873)
)

type rfProgramRow struct {
	ID       uuid.UUID
	Status   string
	Friend   int
	Cashback int
	Cap      int
	Max      *int
	Terms    bool
}

// rfPrograms lists the programmes of an event, newest first.
func rfPrograms(t *testing.T, pool *pgxpool.Pool, event uuid.UUID) []rfProgramRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT id, status, friend_discount_pct, cashback_pct, cashback_cap_pct, max_friends, terms_accepted_at IS NOT NULL
		  FROM referral_programs WHERE event_id = $1 ORDER BY created_at DESC, id DESC`, event)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []rfProgramRow
	for rows.Next() {
		var r rfProgramRow
		var friend, cashback, capPct int16
		var max *int16
		if err := rows.Scan(&r.ID, &r.Status, &friend, &cashback, &capPct, &max, &r.Terms); err != nil {
			t.Fatal(err)
		}
		r.Friend, r.Cashback, r.Cap = int(friend), int(cashback), int(capPct)
		if max != nil {
			m := int(*max)
			r.Max = &m
		}
		out = append(out, r)
	}
	return out
}

func rfDialogs(t *testing.T, pool *pgxpool.Pool, tg int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'referral'`, tg).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBotE2E_Referral(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedPromoOrg(t, pool, f.orgID)
	_ = seedPromoOrg(t, pool, other.orgID)
	linkECBotUser(t, f, srv, rfOwnerTG, "owner")
	linkECBotUser(t, f, srv, rfManagerTG, "manager")
	linkECBotUser(t, other, srv, rfOutsiderTG, "owner")

	// Runs BEFORE the seed's own cleanup (cleanups run last-in first): the
	// rewards and personal codes hold foreign keys to the orders.
	t.Cleanup(func() {
		c := context.Background()
		for _, sql := range []string{
			`DELETE FROM referral_rewards WHERE org_id = $1`,
			`DELETE FROM promo_codes WHERE org_id = $1 AND origin = 'referral'`,
			`DELETE FROM referral_programs WHERE org_id = $1`,
			`DELETE FROM audit_events WHERE action LIKE 'v1.referral_program.%' AND metadata->>'org_id' = $1::text`,
		} {
			if _, err := pool.Exec(c, sql, f.orgID); err != nil {
				t.Logf("referral bot cleanup: %s: %v", sql, err)
			}
		}
	})

	tg := newStubTelegram(t)
	startPromoTestBot(t, pool, api, tg)
	owner := ecDriver{t, tg, rfOwnerTG}
	manager := ecDriver{t, tg, rfManagerTG}
	outsider := ecDriver{t, tg, rfOutsiderTG}
	ev := func(kind string, event uuid.UUID) string { return "rf:" + kind + ":" + event.String() }
	eventA, eventB := seed.eventA, seed.eventB
	one := func(want string, got []rfProgramRow) rfProgramRow {
		t.Helper()
		if len(got) == 0 {
			t.Fatalf("%s: no programme is stored", want)
		}
		return got[0]
	}

	// ── no programme yet: the card says so, the event card has the button ────
	text := owner.press(ev("o", eventA), "Приведи друга")
	if !strings.Contains(text, "программы пока нет") || strings.Contains(text, "Включена") {
		t.Errorf("card without a programme:\n%s", text)
	}

	// ── turn on, step by step ────────────────────────────────────────────────
	owner.press(ev("n", eventA), "Сколько процентов скидки получает друг")
	owner.say("150", "Напишите целое число от 1 до 100")
	owner.say("десять", "Напишите целое число от 1 до 100")
	owner.say("10", "Сколько процентов от платежа друга")
	owner.say("20 %", "самое большее")
	owner.press("rf:c:cap100", "Сколько друзей может воспользоваться")
	owner.say("0", "Напишите целое число от 1")
	owner.say("5", "Правила")
	if got := rfPrograms(t, pool, eventA); len(got) != 0 {
		t.Fatalf("nothing may be stored before the rules screen's button: %+v", got)
	}
	// Text on the rules screen is answered with a hint and turns nothing on.
	owner.say("да", "нажмите кнопку")
	if got := rfPrograms(t, pool, eventA); len(got) != 0 {
		t.Fatalf("a typed text turned the programme on: %+v", got)
	}
	text = owner.press("rf:c:go", "Программа включена")
	if !strings.Contains(text, "Включена") || !strings.Contains(text, "Друзей на один код: 5") {
		t.Errorf("card after turning on:\n%s", text)
	}
	p := one("turned on", rfPrograms(t, pool, eventA))
	if p.Status != "active" || p.Friend != 10 || p.Cashback != 20 || p.Cap != 100 || p.Max == nil || *p.Max != 5 || !p.Terms {
		t.Fatalf("stored programme = %+v", p)
	}

	// ── the manager pauses and resumes: one press each ───────────────────────
	manager.press(ev("o", eventA), "Включена")
	manager.press(ev("ps", eventA), "Поставлено на паузу")
	if got := one("paused", rfPrograms(t, pool, eventA)); got.Status != "paused" {
		t.Errorf("status after pause = %s", got.Status)
	}
	text = manager.press(ev("o", eventA), "На паузе")
	if !strings.Contains(text, "На паузе") {
		t.Errorf("paused card:\n%s", text)
	}
	manager.press(ev("rs", eventA), "Снова включено")
	if got := one("resumed", rfPrograms(t, pool, eventA)); got.Status != "active" {
		t.Errorf("status after resume = %s", got.Status)
	}

	// ── change: keep what is stored, new cashback, no limit ──────────────────
	text = owner.press(ev("n", eventA), "Изменение")
	if !strings.Contains(text, "Выданные коды их не меняют") {
		t.Errorf("edit dialog:\n%s", text)
	}
	owner.press("rf:c:keep", "Сколько процентов от платежа друга")
	owner.say("25", "самое большее")
	owner.press("rf:c:keep", "Сколько друзей может воспользоваться")
	owner.press("rf:c:nomax", "Проверьте и сохраните")
	if got := one("not saved yet", rfPrograms(t, pool, eventA)); got.Cashback != 20 {
		t.Fatalf("the programme changed before the Save press: %+v", got)
	}
	owner.press("rf:c:go", "Сохранено")
	p = one("changed", rfPrograms(t, pool, eventA))
	if p.Friend != 10 || p.Cashback != 25 || p.Cap != 100 || p.Max != nil || p.Status != "active" {
		t.Fatalf("changed programme = %+v", p)
	}
	if n := len(rfPrograms(t, pool, eventA)); n != 1 {
		t.Fatalf("a change must not start a second programme: %d rows", n)
	}

	// ── results and the cashback list, from seeded rewards ───────────────────
	referrer, _ := seed.paidOrder(seed.sessA[0], eventA, "EUR", 2500, "RF Referrer")
	code := uuid.New()
	seed.exec(`INSERT INTO promo_codes (id, org_id, code, discount_type, discount_value, origin, referral_program_id, referrer_order_id)
	           VALUES ($1, $2, $3, 'percent', 10, 'referral', $4, $5)`, code, f.orgID, "RF"+strings.ToUpper(seed.suffix), p.ID, referrer)
	type rw struct {
		minor    int64
		state    string
		reason   string
		reversal string
	}
	rewards := []rw{
		{300, "paid", "", ""}, {250, "paid", "", ""}, {200, "paid", "", ""}, {150, "paid", "", "refunded"},
		{100, "due", "", ""}, {50, "capped", "cap_reached", ""}, {70, "not_possible", "card_unavailable", ""},
	}
	for _, r := range rewards {
		friend, _ := seed.paidOrder(seed.sessA[1], eventA, "EUR", 2250, "RF Friend")
		seed.exec(`UPDATE orders SET subtotal = total + 250, discount = 250 WHERE id = $1`, friend)
		var reason, reversal any
		if r.reason != "" {
			reason = r.reason
		}
		if r.reversal != "" {
			reversal = r.reversal
		}
		seed.exec(`INSERT INTO referral_rewards (org_id, program_id, promo_code_id, referrer_order_id, friend_order_id,
		                                         amount_minor, currency, cashback_pct, cap_pct, state, reason, friend_reversal)
		           VALUES ($1, $2, $3, $4, $5, $6, 'EUR', 25, 100, $7, $8, $9)`,
			f.orgID, p.ID, code, referrer, friend, r.minor, r.state, reason, reversal)
	}
	text = owner.press(ev("o", eventA), "Итоги")
	for _, want := range []string{
		"Выдано кодов: 1", "Пришло друзей: 7", "EUR", "Скидка дана: 17,50 EUR", "Кэшбэк выплачен: 9 EUR",
		"Кэшбэк в пути: 1 EUR", "Кэшбэк не выплачен: 1,20 EUR", "Друзей, которые вернули деньги после выплаты кэшбэка: 1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("results lack %q:\n%s", want, text)
		}
	}

	text = owner.press(ev("rw", eventA), "Показано: Все")
	if got := strings.Count(text, "Друг №"); got != 5 || !strings.Contains(text, "страница 1") {
		t.Errorf("first page of the list has %d rows:\n%s", got, text)
	}
	text = owner.press("rf:rp:2", "страница 2")
	if got := strings.Count(text, "Друг №"); got != 2 {
		t.Errorf("second page of the list has %d rows:\n%s", got, text)
	}
	for _, leak := range []string{"@", "RF Friend", "RF Referrer"} {
		if strings.Contains(text, leak) {
			t.Errorf("the list shows buyer data (%q):\n%s", leak, text)
		}
	}
	// A page button the list does not hold redraws the page on screen.
	text = owner.press("rf:rp:9", "страница 2")
	if got := strings.Count(text, "Друг №"); got != 2 {
		t.Errorf("a stale page press changed the page:\n%s", text)
	}
	owner.press("rf:rp:1", "страница 1")
	text = owner.press("rf:rf:paid", "Показано: Выплачено")
	if got := strings.Count(text, "Друг №"); got != 4 || !strings.Contains(text, "вернул деньги после выплаты") {
		t.Errorf("the paid chip shows %d rows:\n%s", got, text)
	}
	text = owner.press("rf:rf:capped", "Показано: Срезано пределом")
	if got := strings.Count(text, "Друг №"); got != 1 || !strings.Contains(text, "Предел кэшбэка этого покупателя достигнут") {
		t.Errorf("the capped chip:\n%s", text)
	}

	// ── ending: the stale "Yes" ends nothing, the question does ─────────────
	owner.press(ev("o", eventA), "Итоги")
	text = owner.press(ev("ey", eventA), "Итоги") // a "Yes" of a question that is not on screen
	if got := one("stale yes", rfPrograms(t, pool, eventA)); got.Status != "active" {
		t.Fatalf("a stale Yes ended the programme: %+v (%s)", got, text)
	}
	text = owner.press(ev("en", eventA), "Это окончательно")
	if got := one("asked", rfPrograms(t, pool, eventA)); got.Status != "active" {
		t.Fatalf("the question ended the programme: %+v", got)
	}
	// "No" returns to the card, and the Yes of that question is no longer valid.
	owner.press(ev("o", eventA), "Итоги")
	owner.press(ev("ey", eventA), "Итоги")
	if got := one("no", rfPrograms(t, pool, eventA)); got.Status != "active" {
		t.Fatalf("Yes after going back ended the programme: %+v", got)
	}
	owner.press(ev("en", eventA), "Это окончательно")
	text = owner.press(ev("ey", eventA), "Программа завершена")
	if !strings.Contains(text, "Завершена") {
		t.Errorf("ended card:\n%s", text)
	}
	if got := one("ended", rfPrograms(t, pool, eventA)); got.Status != "ended" {
		t.Fatalf("status after ending = %s", got.Status)
	}

	// ── a new programme over the ended one: the terms are asked again ───────
	text = owner.press(ev("n", eventA), "Включение")
	if strings.Contains(text, "Изменение") {
		t.Errorf("an ended programme must start a new one:\n%s", text)
	}
	owner.say("5", "Сколько процентов от платежа друга")
	owner.say("5", "самое большее")
	owner.say("40", "Сколько друзей может воспользоваться")
	owner.press("rf:c:nomax", "Правила")
	owner.press("rf:c:go", "Программа включена")
	all := rfPrograms(t, pool, eventA)
	if len(all) != 2 || all[0].Status != "active" || all[0].Friend != 5 || all[0].Cap != 40 || all[1].Status != "ended" {
		t.Fatalf("programmes after starting again = %+v", all)
	}

	// ── a channel that cannot return a part of a payment ─────────────────────
	seed.exec(`UPDATE sales_channels SET provider = 'flitt' WHERE org_id = $1`, f.orgID)
	text = owner.press(ev("o", eventB), "Приведи друга")
	if !strings.Contains(text, "выплатить нельзя") || !strings.Contains(text, "только платёж целиком") {
		t.Errorf("card of an event whose channel cannot return a part:\n%s", text)
	}
	// A forced turn-on is refused by the server, in plain words.
	owner.press(ev("n", eventB), "Сколько процентов скидки получает друг")
	owner.say("10", "Сколько процентов от платежа друга")
	owner.say("10", "самое большее")
	owner.press("rf:c:cap100", "Сколько друзей может воспользоваться")
	owner.press("rf:c:nomax", "Правила")
	text = owner.press("rf:c:go", "Программу включить нельзя")
	if !strings.Contains(text, "только платёж целиком") {
		t.Errorf("provider refusal:\n%s", text)
	}
	if got := rfPrograms(t, pool, eventB); len(got) != 0 {
		t.Fatalf("a programme was stored for an unsupported channel: %+v", got)
	}

	// ── another organization: not found, nothing changes ─────────────────────
	for _, data := range []string{ev("o", eventA), ev("n", eventA), ev("ps", eventA), ev("en", eventA), ev("rw", eventA)} {
		text = outsider.press(data, "Не найдено")
		if strings.Contains(text, "Включена") {
			t.Errorf("%s showed another organization's programme:\n%s", data, text)
		}
	}
	if got := one("outsider", rfPrograms(t, pool, eventA)); got.Status != "active" || got.Friend != 5 {
		t.Fatalf("the programme changed after an outsider's presses: %+v", got)
	}

	// ── leaving the dialog: a number typed afterwards is nobody's answer ─────
	owner.press(ev("n", eventA), "Изменение")
	if rfDialogs(t, pool, rfOwnerTG) != 1 {
		t.Fatal("the dialog is not stored while its question is on screen")
	}
	owner.press("home", "Что будем делать")
	if n := rfDialogs(t, pool, rfOwnerTG); n != 0 {
		t.Fatalf("another screen did not end the dialog: %d rows", n)
	}
	m := tg.mark()
	tg.push(e2eMessageAs(rfOwnerTG, "77"))
	tg.waitSince(t, m, "")
	if got := one("typed after leaving", rfPrograms(t, pool, eventA)); got.Friend != 5 || got.Cashback != 5 {
		t.Fatalf("a number typed after leaving changed the programme: %+v", got)
	}
}

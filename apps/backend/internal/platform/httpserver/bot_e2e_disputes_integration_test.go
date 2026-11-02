//go:build integration

package httpserver

// End-to-end proof of the bot's "Disputes" screens (spec 51.3 section 9.2,
// DS-11) through the real router, the real bot and the dispute service. The
// dispute is an e-mail channel one (an AllPay payment entered by hand), so no
// Stripe is needed: the owner reads the list and the card, a wrong word sends
// nothing, the right word and the final button queue the letter to the
// provider and mark the dispute answered; the manager reads but is never
// offered, and never allowed, to answer; a user of another organization sees
// nothing. The stub Telegram records message text and button data, the
// effects are checked in the database.

import (
	"context"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/disputes"
)

func dpHas(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestBotE2E_Disputes_OwnerSubmitsManagerOnlyReads(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	linkECBotUser(t, f, srv, ecManagerTG, "manager")
	linkECBotUser(t, other, srv, ecOutsiderTG, "owner")
	fx := newPay03Order(t, pool, f.orgID, "allpay")
	ctx := context.Background()
	// Registered after the order fixture, so it runs BEFORE the fixture's own
	// clean-up (the dispute rows point at the order and the payment).
	t.Cleanup(func() {
		cleanupDisputes(pool, f.orgID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM worker_jobs WHERE job_type LIKE 'dispute.%'
			AND payload->>'dispute_id' NOT IN (SELECT id::text FROM payment_disputes)`)
	})

	svc := srv.disputeService()
	if svc == nil {
		t.Fatal("the dispute service is not wired")
	}
	d, err := svc.CreateManual(ctx, f.orgID, disputes.ManualInput{
		OrderID: fx.order, Reason: "fraudulent", RespondTo: "disputes@allpay.test",
	}, disputes.Actor{Label: "test"})
	if err != nil {
		t.Fatalf("CreateManual: %v", err)
	}
	if _, err := svc.Build(ctx, f.orgID, d.ID, disputes.Actor{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	var orderNum int64
	if err := pool.QueryRow(ctx, `SELECT system_id FROM orders WHERE id = $1`, fx.order).Scan(&orderNum); err != nil {
		t.Fatalf("order number: %v", err)
	}
	orderLabel := "№" + strconv.FormatInt(orderNum, 10)
	state := func() string {
		var s string
		if err := pool.QueryRow(ctx, `SELECT state FROM payment_disputes WHERE id = $1`, d.ID).Scan(&s); err != nil {
			t.Fatalf("dispute state: %v", err)
		}
		return s
	}
	jobs := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = 'dispute.send_email'
			AND payload->>'dispute_id' = $1`, d.ID.String()).Scan(&n); err != nil {
			t.Fatalf("jobs: %v", err)
		}
		return n
	}
	if got := state(); got != "draft_ready" && got != "needs_response" {
		t.Fatalf("a built manual dispute is %q", got)
	}
	before := jobs()

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	defer func() { stop() }()
	did := d.ID.String()

	// ── the manager: reads, never answers ──────────────────────────────────
	m := ecDriver{t, tg, ecManagerTG}
	list := m.press("dp:l:open:1", "Споры · Нужен ответ")
	if !strings.Contains(list, orderLabel) || !strings.Contains(list, "50 EUR") {
		t.Errorf("the manager's list lacks the dispute:\n%s", list)
	}
	card := m.press("dp:v:"+did, "Спор · заказ")
	if !strings.Contains(card, "Отвечает на спор только владелец") {
		t.Errorf("the manager's card does not say that only the owner answers:\n%s", card)
	}
	_, _, kb := tg.lastOf(ecManagerTG)
	if dpHas(kb, "dp:sub:"+did) || dpHas(kb, "dp:acc:"+did) || dpHas(kb, "dp:rb:"+did) || dpHas(kb, "dp:own:"+did) {
		t.Errorf("the manager is offered an answer button: %v", kb)
	}
	if !dpHas(kb, "dp:pk:"+did) {
		t.Errorf("the manager must be able to read the package: %v", kb)
	}
	// A forged press changes nothing.
	m.press("dp:sub:"+did, "только владелец")
	m.press("dp:rb:"+did, "только владелец")
	m.press("dp:owny:"+did, "только владелец")
	if state() != "draft_ready" && state() != "needs_response" {
		t.Fatalf("the manager changed the dispute: %q", state())
	}
	if jobs() != before {
		t.Fatalf("the manager queued a letter")
	}

	// ── another organization sees nothing ──────────────────────────────────
	o := ecDriver{t, tg, ecOutsiderTG}
	got := o.press("dp:l:open:1", "Споры · Нужен ответ")
	if strings.Contains(got, orderLabel) {
		t.Errorf("a foreign owner sees the dispute:\n%s", got)
	}
	got = o.press("dp:v:"+did, "Не найдено")
	if strings.Contains(got, orderLabel) {
		t.Errorf("a foreign owner opened the dispute:\n%s", got)
	}

	// ── the owner ──────────────────────────────────────────────────────────
	d1 := ecDriver{t, tg, ecOwnerTG}
	d1.say("/start", "Что будем делать?")
	if _, _, kb := tg.lastOf(ecOwnerTG); !dpHas(kb, "dp:l:open:1") {
		t.Errorf("the main menu has no Disputes row: %v", kb)
	}
	card = d1.press("dp:v:"+did, "Спор · заказ")
	for _, want := range []string{orderLabel, "50 EUR", "по Мадриду", "Файлы пакета"} {
		if !strings.Contains(card, want) {
			t.Errorf("the owner's card lacks %q:\n%s", want, card)
		}
	}
	_, _, kb = tg.lastOf(ecOwnerTG)
	for _, want := range []string{"dp:sub:" + did, "dp:acc:" + did, "dp:own:" + did, "dp:rb:" + did, "or:v:" + fx.order.String()} {
		if !dpHas(kb, want) {
			t.Errorf("the owner's card lacks the button %q: %v", want, kb)
		}
	}

	ask := d1.press("dp:sub:"+did, "ОТПРАВИТЬ")
	if !strings.Contains(ask, "письмо") {
		t.Errorf("the question does not say that a letter goes to the provider:\n%s", ask)
	}
	// A wrong word sends nothing and asks again.
	d1.say("да", "Текст не совпал")
	d1.say("принять", "Текст не совпал") // the accept word is not the submit word
	if jobs() != before || (state() != "draft_ready" && state() != "needs_response") {
		t.Fatalf("a wrong word sent the answer: state %q, jobs %d", state(), jobs())
	}
	// Going back ends the step: a word typed afterwards answers nothing.
	d1.press("dp:v:"+did, "Спор · заказ")
	if n := botDialogCount(t, pool, ecOwnerTG, "dispute"); n != 0 {
		t.Fatalf("the dispute dialog survived going back: %d", n)
	}
	d1.press("dp:sub:"+did, "ОТПРАВИТЬ")
	d1.say("Отправить", "Последняя проверка")
	if jobs() != before || (state() != "draft_ready" && state() != "needs_response") {
		t.Fatalf("the typed word alone sent the answer: state %q", state())
	}
	var key string
	if err := pool.QueryRow(ctx, `SELECT state->>'key' FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'dispute'`, ecOwnerTG).Scan(&key); err != nil || key == "" {
		t.Fatalf("the dispute dialog has no key: %v", err)
	}
	tag := strings.TrimPrefix(key, "dp-")[:12]
	// A button of another screen sends nothing.
	d1.press("dp:ok:aaaaaaaaaaaa", "от старого экрана")
	if jobs() != before {
		t.Fatalf("a stale button sent the answer")
	}
	done := d1.press("dp:ok:"+tag, "Готово. Письмо")
	if !strings.Contains(done, "Копия придёт") {
		t.Errorf("the result does not mention the copy to the owner:\n%s", done)
	}
	if got := state(); got != "submitted" {
		t.Fatalf("dispute state after the final press = %q, want submitted", got)
	}
	if got := jobs(); got != before+1 {
		t.Fatalf("send_email jobs = %d, want %d", got, before+1)
	}
	// The same button again, and a new try, send nothing more.
	d1.press("dp:ok:"+tag, "уже закрыто")
	d1.press("dp:sub:"+did, "больше не ждёт ответа")
	if got := jobs(); got != before+1 {
		t.Fatalf("a repeated press queued another letter: %d", got)
	}
}

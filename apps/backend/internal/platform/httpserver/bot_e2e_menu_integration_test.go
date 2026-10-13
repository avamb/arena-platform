//go:build integration

package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// botDialogCount counts the live dialogs of one kind of a Telegram account.
func botDialogCount(t *testing.T, pool *pgxpool.Pool, tg int64, kind string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = $2`, tg, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The "☰ Menu" button (owner decision 2026-10-13): /start puts a persistent
// reply keyboard with the one button under the message field, the button
// opens the main menu from inside a dialog and ends that dialog, and a
// refund that waits for its confirmation asks before it is dropped.
func TestBotE2E_MenuButtonAlwaysOpensTheMenu(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	stripe := newStubStripe(t)
	stripe.refundHandler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"re_menu_e2e","status":"succeeded"}`)
	}
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) { c.StripeAPIBaseURL = stripe.baseURL() })
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedOrdersBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	seedRefundableOrder(t, pool, f.orgID, seed)

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	defer func() { stop() }()
	d := ecDriver{t, tg, ecOwnerTG}

	// ── /start: the FIRST message carries the persistent keyboard
	m := tg.mark()
	d.say("/start", "Что будем делать?")
	kb, text, ok := tg.replyKeyboardSince(m)
	if !ok {
		t.Fatalf("no message after /start carried a reply keyboard: %q", tg.sent[m:])
	}
	if tg.sent[m] != text {
		t.Errorf("the keyboard must come with the first message after /start, it came with %q (first: %q)", text, tg.sent[m])
	}
	if !kb.IsPersistent || !kb.ResizeKeyboard || len(kb.Keyboard) != 1 || len(kb.Keyboard[0]) != 1 || kb.Keyboard[0][0].Text != "☰ Меню" {
		t.Errorf("reply_markup = %+v, want one persistent, resized «☰ Меню» button", kb)
	}
	if !strings.Contains(text, "☰ Меню") {
		t.Errorf("the keyboard's message does not name the button: %q", text)
	}

	// ── inside a dialog: the team invite waits for an e-mail
	d.press("team:invite", "E-mail коллеги?")
	if botDialogCount(t, pool, ecOwnerTG, "team") != 1 {
		t.Fatal("the invite dialog must be open")
	}
	m = tg.mark()
	d.say("☰ Меню", "Что будем делать?")
	if botDialogCount(t, pool, ecOwnerTG, "team") != 0 {
		t.Error("the menu button must end the invite dialog")
	}
	if _, _, again := tg.replyKeyboardSince(m); again {
		t.Error("a chat that has the keyboard must not get it again")
	}
	// an e-mail typed now is nobody's answer: no invitation is sent
	invitations := func() int {
		var n int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM bot_invitations WHERE org_id = $1`, f.orgID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := invitations()
	d.say("colleague-"+seed.suffix+"@example.test", "Я не понял")
	if got := invitations(); got != before {
		t.Errorf("an e-mail typed after the menu created %d invitation(s)", got-before)
	}

	// ── a refund waiting for its word: the menu asks first
	d.press("or:v:"+seed.anna.String(), "Заказ №")
	d.press("or:f:s:"+seed.anna.String(), "Возврат · заказ №")
	d.press("or:f:t:1", "Возврат · заказ №")
	d.press("or:f:go", "Почему возвращаются деньги")
	d.press("or:f:r:1", "ВЕРНУТЬ")
	d.say("☰ Меню", "ещё не отправлен")
	if botDialogCount(t, pool, ecOwnerTG, "refund") != 1 {
		t.Fatal("the question must keep the refund")
	}
	// "back to the refund" shows the word question again
	d.press("or:f:cur", "ВЕРНУТЬ")
	d.say("вернуть", "Последняя проверка")
	// on the final screen the menu asks again; leaving drops the refund
	d.say("/menu", "ещё не отправлен")
	d.press("menu:leave", "Что будем делать?")
	if botDialogCount(t, pool, ecOwnerTG, "refund") != 0 {
		t.Error("leaving must end the refund dialog")
	}
	if n, _ := refundCount(t, srv, seed.anna); n != 0 {
		t.Errorf("leaving the refund created %d refund(s)", n)
	}

	// ── a stale press says where the menu is
	home, _, _ := tg.lastOf(ecOwnerTG)
	d.press("help", "Что умеет бот")
	answers := tg.callCount("answerCallbackQuery")
	tg.push(fmt.Sprintf(`"callback_query":{"id":"cb-stale-menu","from":{"id":%d,"is_bot":false,"first_name":"U","language_code":"ru"},"chat_instance":"x","data":"help","message":{"message_id":%d,"date":1700000000,"chat":{"id":%d,"type":"private"},"text":"menu"}}`,
		ecOwnerTG, home, ecOwnerTG))
	tg.waitCall(t, "answerCallbackQuery", answers)
	time.Sleep(300 * time.Millisecond)
	tg.mu.Lock()
	note := tg.answers[len(tg.answers)-1]
	tg.mu.Unlock()
	if note != "Это сообщение устарело. Нажмите ☰ Меню внизу." {
		t.Errorf("a stale press must point at the menu button: %q", note)
	}
}

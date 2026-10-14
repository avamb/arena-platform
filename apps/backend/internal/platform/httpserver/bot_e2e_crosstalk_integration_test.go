//go:build integration

package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// A command and a press of another screen end every open dialog, and a typed
// text reaches only the newest dialog that takes it: the word that cancels an
// order never reaches a session dialog left open earlier, and an e-mail
// question left open never takes the refund word.
func TestBotE2E_DialogsCloseOnCommandsAndForeignButtons(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	stripe := newStubStripe(t)
	stripe.refundHandler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"re_crosstalk_e2e","status":"succeeded"}`)
	}
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) { c.StripeAPIBaseURL = stripe.baseURL() })
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedOrdersBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	seedRefundableOrder(t, pool, f.orgID, seed)
	ctx := context.Background()
	// The session dialog asks for a contact first when the organization has
	// none; this test is about what happens after its last question.
	if _, err := pool.Exec(ctx, `UPDATE organizations SET contact_email = 'crosstalk@example.test' WHERE id = $1`, f.orgID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Only a regression writes these; they must not leak into the
		// generic worker_jobs drains of other tests.
		_, _ = pool.Exec(context.Background(), `DELETE FROM worker_jobs WHERE job_type = 'session.change_email'
		      AND payload->>'change_id' IN (SELECT id::text FROM session_changes WHERE session_id = $1)`, seed.session1)
		_, _ = pool.Exec(context.Background(), `DELETE FROM session_change_notices WHERE change_id IN (SELECT id FROM session_changes WHERE session_id = $1)`, seed.session1)
		_, _ = pool.Exec(context.Background(), `DELETE FROM session_changes WHERE session_id = $1`, seed.session1)
	})

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	defer func() { stop() }()
	d := ecDriver{t, tg, ecOwnerTG}
	sessionStatus := func() string {
		t.Helper()
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM sessions WHERE id = $1`, seed.session1).Scan(&status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	sessionChanges := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_changes WHERE session_id = $1`, seed.session1).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// ── «Sessions»: the session waits for its typed cancel word
	d.press("ses:list:"+seed.event1.String(), "Сеансы мероприятия")
	d.press("ses:o:0", "Здесь можно перенести")
	d.press("ses:cx", "Отмена сеанса")
	d.press("ses:nomsg", "Отмена сеанса")
	d.press("ses:go", "это действие нельзя отменить")
	if botDialogCount(t, pool, ecOwnerTG, "sessions") != 1 {
		t.Fatal("the session dialog must wait for the word")
	}

	// ── a command ends it
	d.say("/help", "Что умеет бот")
	if n := botDialogCount(t, pool, ecOwnerTG, "sessions"); n != 0 {
		t.Errorf("a command must end the session dialog, %d left", n)
	}

	// ── the main menu, the orders, an unpaid order and its cancel prompt
	d.press("home", "Что будем делать")
	d.press("or:new", "Последние · ")
	d.press("or:t:u", "Неоплаченные · ")
	d.press("or:v:"+seed.clara.String(), "Clara Zakharova")
	d.press("or:c:"+seed.clara.String(), "Последний шаг")

	// ── the same word now cancels the ORDER, never the session
	d.say("ОТМЕНИТЬ", "Заказ отменён")
	if got := orderStatus(t, pool, seed.clara); got != "cancelled" {
		t.Errorf("the order must be cancelled, status %q", got)
	}
	if got := sessionStatus(); got != "scheduled" {
		t.Fatalf("the order's cancel word cancelled the session: status %q", got)
	}
	if n := sessionChanges(); n != 0 {
		t.Fatalf("the order's cancel word wrote %d session change(s)", n)
	}

	// ── a session dialog left open by a press elsewhere ends too
	d.press("ses:list:"+seed.event1.String(), "Сеансы мероприятия")
	d.press("ses:o:0", "Здесь можно перенести")
	d.press("ses:cx", "Отмена сеанса")
	d.press("ses:nomsg", "Отмена сеанса")
	d.press("ses:go", "это действие нельзя отменить")
	d.press("or:new", "Последние · ")
	if n := botDialogCount(t, pool, ecOwnerTG, "sessions"); n != 0 {
		t.Errorf("a press of another screen must end the session dialog, %d left", n)
	}
	d.press("or:v:"+seed.karl.String(), "Karl Brandtner")
	d.press("or:c:"+seed.karl.String(), "Последний шаг")
	d.say("ОТМЕНИТЬ", "Заказ отменён")
	if got := sessionStatus(); got != "scheduled" || sessionChanges() != 0 {
		t.Fatalf("the session changed after a press elsewhere: status %q", got)
	}

	// ── an e-mail question left open never takes the refund word
	d.press("team:invite", "E-mail коллеги?")
	if botDialogCount(t, pool, ecOwnerTG, "team") != 1 {
		t.Fatal("the invite dialog must be open")
	}
	d.press("or:v:"+seed.anna.String(), "Заказ №")
	if n := botDialogCount(t, pool, ecOwnerTG, "team"); n != 0 {
		t.Errorf("a press of another screen must end the invite dialog, %d left", n)
	}
	d.press("or:f:s:"+seed.anna.String(), "Возврат · заказ №")
	d.press("or:f:t:1", "Возврат · заказ №")
	d.press("or:f:go", "Почему возвращаются деньги")
	d.press("or:f:r:1", "ВЕРНУТЬ")
	d.say("ВЕРНУТЬ", "Последняя проверка")
	// "Cancel" before anything was sent ends the refund and says so.
	d.press("or:f:no", "Ничего не возвращено")
	if n, _ := refundCount(t, srv, seed.anna); n != 0 {
		t.Errorf("no refund may be sent, got %d", n)
	}

	// ── the same, with a session dialog waiting for its word: the refund
	// word is the refund's
	d.press("ses:list:"+seed.event1.String(), "Сеансы мероприятия")
	d.press("ses:o:0", "Здесь можно перенести")
	d.press("ses:cx", "Отмена сеанса")
	d.press("ses:nomsg", "Отмена сеанса")
	d.press("ses:go", "это действие нельзя отменить")
	d.press("or:v:"+seed.anna.String(), "Заказ №")
	d.press("or:f:s:"+seed.anna.String(), "Возврат · заказ №")
	d.press("or:f:t:1", "Возврат · заказ №")
	d.press("or:f:go", "Почему возвращаются деньги")
	d.press("or:f:r:1", "ВЕРНУТЬ")
	d.say("ВЕРНУТЬ", "Последняя проверка")
	d.press("or:f:no", "Ничего не возвращено")
	if got := sessionStatus(); got != "scheduled" || sessionChanges() != 0 {
		t.Fatalf("the session changed during a refund: status %q", got)
	}

	// ── "Cancel" on a refund that may have started (it went to the server
	// and no answer came) keeps its key and never says nothing was refunded
	const key = "tg-crosstalk-maybe-started"
	state := fmt.Sprintf(`{"org_id":%q,"order_id":%q,"num":%d,"provider":"stripe","currency":"EUR","reason":"r","key":%q,"attempted":true,
	  "tickets":[{"id":%q,"refundable":2500,"amount":0,"sel":true}]}`,
		f.orgID, seed.anna, seed.annaSID, key, seed.anna)
	if _, err := pool.Exec(ctx, `INSERT INTO bot_dialogs (telegram_user_id, org_id, kind, step, state, expires_at)
	      VALUES ($1, $2, 'refund', 'final', $3::jsonb, now() + interval '30 minutes')
	      ON CONFLICT (telegram_user_id, kind) DO UPDATE SET step = EXCLUDED.step, state = EXCLUDED.state, expires_at = EXCLUDED.expires_at`,
		ecOwnerTG, f.orgID, state); err != nil {
		t.Fatal(err)
	}
	text := d.press("or:f:no", "мог уже начаться")
	if strings.Contains(text, "Ничего не возвращено") {
		t.Errorf("a refund that may have started must not be called not refunded:\n%s", text)
	}
	if got := refundDialogKey(t, pool, ecOwnerTG); got != key {
		t.Errorf("\"Cancel\" dropped the key of a refund that may have started: %q", got)
	}
	var parked bool
	if err := pool.QueryRow(ctx, `SELECT coalesce((state->>'parked')::boolean, false) FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'refund'`,
		ecOwnerTG).Scan(&parked); err != nil || !parked {
		t.Errorf("the refund must be parked: parked=%v err=%v", parked, err)
	}
	if n, _ := refundCount(t, srv, seed.anna); n != 0 {
		t.Errorf("no refund may be sent, got %d", n)
	}
}

// After a send that got no answer (the operation may have started), the
// buyer-letter toggle, the ticket ticks and "back" change nothing: the key
// stays, and the next "Yes" sends the SAME operation.
func TestBotE2E_RefundThatMayHaveStartedKeepsItsOperation(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	stripe := newStubStripe(t)
	stripe.refundHandler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"re_maybe_started_e2e","status":"succeeded"}`)
	}
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) { c.StripeAPIBaseURL = stripe.baseURL() })
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedOrdersBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	seedRefundableOrder(t, pool, f.orgID, seed)
	ctx := context.Background()

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	defer func() { stop() }()
	d := ecDriver{t, tg, ecOwnerTG}

	d.press("or:v:"+seed.anna.String(), "Заказ №")
	d.press("or:f:s:"+seed.anna.String(), "Возврат · заказ №")
	d.press("or:f:t:1", "Возврат · заказ №")
	d.press("or:f:go", "Почему возвращаются деньги")
	d.press("or:f:r:1", "ВЕРНУТЬ")
	d.say("вернуть", "Последняя проверка")
	key := refundDialogKey(t, pool, ecOwnerTG)
	if key == "" {
		t.Fatal("the final screen must hold a key")
	}
	// The send went out and no answer came: the state the bot keeps after a
	// timeout or a server error.
	if _, err := pool.Exec(ctx, `UPDATE bot_dialogs SET state = state || '{"attempted":true}'::jsonb
	      WHERE telegram_user_id = $1 AND kind = 'refund'`, ecOwnerTG); err != nil {
		t.Fatal(err)
	}
	attempted := func() bool {
		t.Helper()
		var a bool
		if err := pool.QueryRow(ctx, `SELECT coalesce((state->>'attempted')::boolean, false) FROM bot_dialogs
		      WHERE telegram_user_id = $1 AND kind = 'refund'`, ecOwnerTG).Scan(&a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	for _, press := range []string{"or:f:nt", "or:f:t:0", "or:f:all", "or:f:b", "or:f:go", "or:f:p"} {
		d.press(press, "мог уже начаться")
		if got := refundDialogKey(t, pool, ecOwnerTG); got != key || !attempted() {
			t.Fatalf("%s changed the operation: key %q -> %q, attempted=%v", press, key, got, attempted())
		}
	}
	if n, _ := refundCount(t, srv, seed.anna); n != 0 {
		t.Fatalf("a locked press sent %d refund(s)", n)
	}

	// "Yes" sends the same operation.
	sent := tg.mark()
	d.press("or:f:ok:"+strings.TrimPrefix(key, "tg-")[:12], "Отправляю возврат")
	tg.waitSince(t, sent, "Возврат запрошен")
	var batchKey string
	if err := pool.QueryRow(ctx, `SELECT idempotency_key FROM refund_batches WHERE order_id = $1`, seed.anna).Scan(&batchKey); err != nil {
		t.Fatalf("refund batch: %v", err)
	}
	if batchKey != key {
		t.Errorf("the refund went out with key %q, want the kept %q", batchKey, key)
	}
	if n, _ := refundCount(t, srv, seed.anna); n != 1 {
		t.Errorf("refunds = %d, want 1", n)
	}
}

// A refund dialog rewritten in the background (its answer arrived) is not
// the dialog the person is typing for: the cancel word typed for the session
// dialog opened meanwhile reaches that dialog. And a refund that may have
// started keeps its key when another order's refund is asked for.
func TestBotE2E_BackgroundRefundAnswerDoesNotTakeText(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServerCfg(t, pool, nil)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedOrdersBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE organizations SET contact_email = 'crosstalk@example.test' WHERE id = $1`, f.orgID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM worker_jobs WHERE job_type = 'session.change_email'
		      AND payload->>'change_id' IN (SELECT id::text FROM session_changes WHERE session_id = $1)`, seed.session1)
		_, _ = pool.Exec(c, `DELETE FROM session_change_notices WHERE change_id IN (SELECT id FROM session_changes WHERE session_id = $1)`, seed.session1)
		_, _ = pool.Exec(c, `DELETE FROM session_changes WHERE session_id = $1`, seed.session1)
	})

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	defer func() { stop() }()
	d := ecDriver{t, tg, ecOwnerTG}

	const key = "tg-background-answer-key"
	refundRow := func(touch string) {
		t.Helper()
		state := fmt.Sprintf(`{"org_id":%q,"order_id":%q,"num":%d,"provider":"stripe","currency":"EUR","reason":"r","key":%q,"attempted":true,
		  "tickets":[{"id":%q,"refundable":2500,"amount":0,"sel":true}]}`, f.orgID, seed.anna, seed.annaSID, key, uuid.New())
		if _, err := pool.Exec(ctx, `INSERT INTO bot_dialogs (telegram_user_id, org_id, kind, step, state, expires_at, updated_at)
		      VALUES ($1, $2, 'refund', 'final', $3::jsonb, now() + interval '30 minutes', `+touch+`)
		      ON CONFLICT (telegram_user_id, kind) DO UPDATE SET step = EXCLUDED.step, state = EXCLUDED.state,
		        expires_at = EXCLUDED.expires_at, updated_at = EXCLUDED.updated_at`, ecOwnerTG, f.orgID, state); err != nil {
			t.Fatal(err)
		}
	}

	// ── a refund that may have started holds its key; asking for another
	// order's refund does not overwrite it
	refundRow("now()")
	d.press("or:f:s:"+seed.boris.String(), fmt.Sprintf("Возврат по заказу №%d мог уже начаться", seed.annaSID))
	if got := refundDialogKey(t, pool, ecOwnerTG); got != key {
		t.Fatalf("another order's refund overwrote the kept operation: %q", got)
	}

	// ── the session dialog waits for its word; the refund dialog was
	// rewritten afterwards (as a background answer does) and stands at its
	// final screen, which takes buttons only
	d.press("ses:list:"+seed.event1.String(), "Сеансы мероприятия")
	d.press("ses:o:0", "Здесь можно перенести")
	d.press("ses:cx", "Отмена сеанса")
	d.press("ses:nomsg", "Отмена сеанса")
	d.press("ses:go", "это действие нельзя отменить")
	refundRow("now() + interval '5 seconds'")
	d.say("ОТМЕНИТЬ", "Сеанс отменён")
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM sessions WHERE id = $1`, seed.session1).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" {
		t.Fatalf("the word typed for the session dialog did not reach it: session %s", status)
	}
	if got := refundDialogKey(t, pool, ecOwnerTG); got != key {
		t.Errorf("the refund dialog changed: key %q", got)
	}
}

//go:build integration

package httpserver

// End-to-end proof of "Refund" on the bot's order card (spec 35 EC-21/22)
// through the real router, the real bot, the real refund engine and a stub
// Stripe: the owner picks a ticket, gives a reason and types the confirmation
// word — a wrong word refunds nothing, the right one returns the money and
// cancels only that ticket; a user of ANOTHER organization cannot reach the
// order and refunds nothing; an order of a seller's own site only gets an
// explanation. The stub Telegram records message TEXT only, so every effect
// is checked in the database.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

func refundCount(t *testing.T, srv *Server, order uuid.UUID) (n int, state string) {
	t.Helper()
	if err := srv.pgxPool.QueryRow(context.Background(),
		`SELECT count(*), COALESCE(max(state), '') FROM refunds WHERE order_id = $1`, order).Scan(&n, &state); err != nil {
		t.Fatal(err)
	}
	return n, state
}

// seedRefundableOrder makes Anna's seeded order refundable through a stub
// Stripe: a succeeded payment, the organization's payment config, the
// inventory row a ticket cancellation gives its place back to, and the
// cleanup of everything a refund leaves behind.
func seedRefundableOrder(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, seed *ordBotSeed) (tickets []uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tickets = resendTickets(t, pool, seed.anna)
	if len(tickets) != 2 {
		t.Fatalf("Anna's order has %d tickets, want 2", len(tickets))
	}
	// The orders seed scans one of Anna's tickets at the door; a scanned ticket
	// is never refundable, so the refund scenarios start from unscanned ones.
	if _, err := pool.Exec(ctx, `UPDATE barcodes SET status = 'active', scanned_at = NULL WHERE ticket_id = ANY($1)`, tickets); err != nil {
		t.Fatalf("unscan: %v", err)
	}
	var cs uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT checkout_session_id FROM orders WHERE id = $1`, seed.anna).Scan(&cs); err != nil {
		t.Fatal(err)
	}
	// Cancelling a refunded ticket gives its place back, which needs the ledger row.
	if _, err := pool.Exec(ctx, `INSERT INTO inventory_ledger (session_id, tier_id, capacity_total, capacity_sold)
		SELECT $1, id, 100, 2 FROM ticket_tiers WHERE session_id = $1 LIMIT 1`, seed.session1); err != nil {
		t.Fatalf("inventory ledger: %v", err)
	}
	ref := "cs_test_refundbot_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	pi, err := gen.New(pool).InsertPaymentIntent(ctx, &cs, orgID, "stripe", &ref, 5000, "EUR", "succeeded", nil, nil)
	if err != nil {
		t.Fatalf("payment intent: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE payment_intents SET provider_charge_ref = $2 WHERE id = $1`, pi.ID, "pi_refundbot_"+uuid.NewString()[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO payment_provider_configs (org_id, provider, mode, secrets, status, is_active)
		VALUES ($1, 'stripe', 'test', '{"api_key":"sk_test_refundbot","webhook_secret":"whsec_refundbot"}'::jsonb, 'configured', true)`, orgID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		for _, sql := range []string{
			`UPDATE tickets SET refund_id = NULL WHERE order_id = $1`,
			`DELETE FROM refunds WHERE order_id = $1`,
			`DELETE FROM refund_batches WHERE order_id = $1`,
			`DELETE FROM worker_jobs WHERE payload->>'ticket_id' = ANY($2)`,
			`DELETE FROM outbox_events WHERE aggregate_id = ANY($2)`,
		} {
			if strings.Contains(sql, "$2") {
				_, _ = pool.Exec(c, sql, seed.anna, []string{tickets[0].String(), tickets[1].String()})
			} else {
				_, _ = pool.Exec(c, sql, seed.anna)
			}
		}
		_, _ = pool.Exec(c, `DELETE FROM payment_provider_configs WHERE org_id = $1`, orgID)
	})

	return tickets
}

func TestBotE2E_Refund_OwnerReturnsOneTicket(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	stripe := newStubStripe(t)
	// hold, when set, keeps the provider call waiting until it is closed.
	var hold atomic.Pointer[chan struct{}]
	stripe.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		if h := hold.Load(); h != nil {
			<-*h
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"re_bot_e2e","status":"succeeded"}`)
	}
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) { c.StripeAPIBaseURL = stripe.baseURL() })
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedOrdersBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	linkECBotUser(t, other, srv, ecOutsiderTG, "owner")

	tickets := seedRefundableOrder(t, pool, f.orgID, seed)
	ctx := context.Background()

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	defer func() { stop() }()
	d := ecDriver{t, tg, ecOwnerTG}

	// ── the card offers the refund, and the options are the server's
	d.press("or:v:"+seed.anna.String(), "Заказ №")
	pick := d.press("or:f:s:"+seed.anna.String(), "Возврат · заказ №")
	if !strings.Contains(pick, "Stripe") {
		t.Errorf("the pick screen names no provider:\n%s", pick)
	}
	// deselect the first ticket: only the second is returned
	d.press("or:f:t:0", "Возврат · заказ №")
	reason := d.press("or:f:go", "Почему возвращаются деньги")
	_ = reason
	word := d.press("or:f:r:1", "ВЕРНУТЬ")
	if !strings.Contains(word, "Покупатель попросил") || !strings.Contains(word, "Отменить возврат нельзя") {
		t.Errorf("the confirmation lacks the reason or the warning:\n%s", word)
	}

	// ── a wrong word refunds nothing
	d.say("да", "Это не слово подтверждения")
	if n, _ := refundCount(t, srv, seed.anna); n != 0 {
		t.Fatalf("a wrong word created %d refund(s)", n)
	}
	// ── the right word only opens the final check: no money moves yet
	final := d.say("вернуть", "Последняя проверка")
	if !strings.Contains(final, "25 EUR") || !strings.Contains(final, "№") {
		t.Errorf("the final screen does not state the amount:\n%s", final)
	}
	// Owner decision 13.10 (R2-8): the final screen says the tickets become
	// invalid, also for a part of the amount.
	if !strings.Contains(final, "перестанут действовать") {
		t.Errorf("the final screen does not say the tickets become invalid:\n%s", final)
	}
	// R2-2: a "yes" button of another screen (another key) sends nothing.
	d.press("or:f:ok:000000000000", "от старого экрана")
	if n, _ := refundCount(t, srv, seed.anna); n != 0 {
		t.Fatalf("a stale yes button created %d refund(s)", n)
	}
	if n, _ := refundCount(t, srv, seed.anna); n != 0 {
		t.Fatalf("the typed word alone created %d refund(s); only the button may", n)
	}
	// ── "Cancel" ends it without a refund
	d.press("or:f:no", "Ничего не возвращено")
	if n, _ := refundCount(t, srv, seed.anna); n != 0 {
		t.Fatalf("cancel created %d refund(s)", n)
	}
	// ── again from the start, and this time the button sends it
	d.press("or:f:s:"+seed.anna.String(), "Возврат · заказ №")
	d.press("or:f:t:0", "Возврат · заказ №")
	d.press("or:f:go", "Почему возвращаются деньги")
	d.press("or:f:r:1", "ВЕРНУТЬ")
	d.say("вернуть", "Последняя проверка")
	var key string
	if err := pool.QueryRow(ctx, `SELECT state->>'key' FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'refund'`, ecOwnerTG).Scan(&key); err != nil {
		t.Fatalf("dialog key: %v", err)
	}
	tag := strings.TrimPrefix(key, "tg-")[:12]
	// R2-1: while the provider takes its time the bot keeps answering.
	held := make(chan struct{})
	hold.Store(&held)
	sentMark := tg.mark()
	d.press("or:f:ok:"+tag, "Отправляю возврат")
	d.press("home", "")
	if n, _ := refundCount(t, srv, seed.anna); n != 1 {
		t.Fatalf("refunds while the provider waits = %d; want the one being sent", n)
	}
	d.press("or:f:s:"+seed.anna.String(), "ещё отправляется")
	close(held)
	done := tg.waitSince(t, sentMark, "Возврат запрошен")
	if !strings.Contains(done, "возвращено") {
		t.Errorf("the result does not say the money is back:\n%s", done)
	}
	if n, state := refundCount(t, srv, seed.anna); n != 1 || state != "succeeded" {
		t.Fatalf("refunds = %d (%s); want 1 succeeded", n, state)
	}
	var amount int64
	if err := pool.QueryRow(ctx, `SELECT amount FROM refunds WHERE order_id = $1`, seed.anna).Scan(&amount); err != nil || amount != 2500 {
		t.Fatalf("refund amount = %d (%v); want 2500", amount, err)
	}
	statuses := map[uuid.UUID]string{}
	for _, tk := range tickets {
		var st string
		if err := pool.QueryRow(ctx, `SELECT status FROM tickets WHERE id = $1`, tk).Scan(&st); err != nil {
			t.Fatal(err)
		}
		statuses[tk] = st
	}
	if statuses[tickets[0]] != "active" || statuses[tickets[1]] != "cancelled" {
		t.Fatalf("ticket statuses = %v; want the first active and the second cancelled", statuses)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'refund'`, ecOwnerTG).Scan(&left); err != nil || left != 0 {
		t.Errorf("the dialog must end after the refund (%d rows, %v)", left, err)
	}

	// ── a ticket already used at the gate is never offered again
	if _, err := pool.Exec(ctx, `UPDATE tickets SET used_at = now() WHERE id = $1`, tickets[0]); err != nil {
		t.Fatal(err)
	}
	used := d.press("or:f:s:"+seed.anna.String(), "уже использованы на входе")
	if !strings.Contains(used, "возвращать здесь нечего") {
		t.Errorf("the screen for a used ticket does not explain itself:\n%s", used)
	}
	if n, _ := refundCount(t, srv, seed.anna); n != 1 {
		t.Fatalf("opening the refund of a used ticket changed the refunds: %d", n)
	}

	// ── a user of another organization reaches nothing
	o := ecDriver{t, tg, ecOutsiderTG}
	m := tg.mark()
	tg.push(e2eCallbackAs(ecOutsiderTG, "or:f:s:"+seed.anna.String()))
	tg.waitSince(t, m, "")
	if n, _ := refundCount(t, srv, seed.anna); n != 1 {
		t.Fatalf("a foreign user changed the refunds: %d", n)
	}
	_ = o
}

// Round 4 review D-F4: a refund whose send was cut off by a restart is not
// left behind a "sending" message without buttons. The new process writes to
// the person with a button that opens the final check again; confirming it
// replays the same operation, so the money goes back once.
func TestBotE2E_Refund_SendLostToARestartIsOfferedAgain(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	stripe := newStubStripe(t)
	stripe.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"re_bot_e2e_lost","status":"succeeded"}`)
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
	d := ecDriver{t, tg, ecOwnerTG}
	d.press("or:v:"+seed.anna.String(), "Заказ №")
	d.press("or:f:s:"+seed.anna.String(), "Возврат · заказ №")
	d.press("or:f:t:0", "Возврат · заказ №")
	d.press("or:f:go", "Почему возвращаются деньги")
	d.press("or:f:r:1", "ВЕРНУТЬ")
	d.say("вернуть", "Последняя проверка")
	var key string
	if err := pool.QueryRow(ctx, `SELECT state->>'key' FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'refund'`, ecOwnerTG).Scan(&key); err != nil {
		t.Fatalf("dialog key: %v", err)
	}
	tag := strings.TrimPrefix(key, "tg-")[:12]
	stop()
	// The earlier process had pressed "Yes" and was cut off mid-send.
	if _, err := pool.Exec(ctx, `UPDATE bot_dialogs SET step = 'sending',
		state = state || jsonb_build_object('run_id', 'an-earlier-process', 'sending_at', now())
		WHERE telegram_user_id = $1 AND kind = 'refund'`, ecOwnerTG); err != nil {
		t.Fatal(err)
	}
	mark := tg.mark()
	stop = startDialogTestBot(t, pool, api, tg)
	defer func() { stop() }()
	notice := tg.waitSince(t, mark, "Бот перезапустился, пока отправлялся возврат по заказу")
	if !strings.Contains(notice, "дважды деньги не вернутся") {
		t.Errorf("the restart notice does not say the money is safe:\n%s", notice)
	}
	final := d.press("or:f:b", "ответ потерялся")
	if !strings.Contains(final, "Последняя проверка") {
		t.Errorf("the recovery button does not open the final check:\n%s", final)
	}
	d.press("or:f:ok:"+tag, "Возврат запрошен")
	if n, state := refundCount(t, srv, seed.anna); n != 1 || state != "succeeded" {
		t.Fatalf("refunds = %d (%s); want exactly 1 succeeded", n, state)
	}
}

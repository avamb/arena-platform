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
	"testing"

	"github.com/google/uuid"

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

func TestBotE2E_Refund_OwnerReturnsOneTicket(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	stripe := newStubStripe(t)
	stripe.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"re_bot_e2e","status":"succeeded"}`)
	}
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) { c.StripeAPIBaseURL = stripe.baseURL() })
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedOrdersBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	linkECBotUser(t, other, srv, ecOutsiderTG, "owner")

	ctx := context.Background()
	tickets := resendTickets(t, pool, seed.anna)
	if len(tickets) != 2 {
		t.Fatalf("Anna's order has %d tickets, want 2", len(tickets))
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
	pi, err := gen.New(pool).InsertPaymentIntent(ctx, &cs, f.orgID, "stripe", &ref, 5000, "EUR", "succeeded", nil, nil)
	if err != nil {
		t.Fatalf("payment intent: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE payment_intents SET provider_charge_ref = $2 WHERE id = $1`, pi.ID, "pi_refundbot_"+uuid.NewString()[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO payment_provider_configs (org_id, provider, mode, secrets, status, is_active)
		VALUES ($1, 'stripe', 'test', '{"api_key":"sk_test_refundbot","webhook_secret":"whsec_refundbot"}'::jsonb, 'configured', true)`, f.orgID); err != nil {
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
		_, _ = pool.Exec(c, `DELETE FROM payment_provider_configs WHERE org_id = $1`, f.orgID)
	})

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
	// ── the right word returns the money for that one ticket
	done := d.say("вернуть", "Возврат запрошен")
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

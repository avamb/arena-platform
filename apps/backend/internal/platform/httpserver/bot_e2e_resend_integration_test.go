//go:build integration

package httpserver

// End-to-end proof of "Resend tickets" on the bot's order card (spec 35
// EC-13) through the real router, the real bot and the stub Telegram: an owner
// and a manager resend the tickets of a paid order to the order's own address
// and to a one-time address typed in chat; a refunded ticket is skipped; an
// order sold through a seller's own site is refused and nothing is queued; a
// user of ANOTHER organization gets "not found" and queues nothing; a bot
// restart in the middle of the dialog loses nothing; a dialog that ran out is
// reported once.
//
// The stub records message TEXT only, so what a button shows is covered by the
// unit tests; every effect is checked in the database: the delivery jobs (the
// one-time address lives there), the worker payloads (its 24-hour deadline),
// the order (its buyer e-mail never changes) and the order history.

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

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// resendTickets returns the tickets of an order, in ordinal order.
func resendTickets(t *testing.T, pool *pgxpool.Pool, order uuid.UUID) []uuid.UUID {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT id FROM tickets WHERE order_id = $1 ORDER BY ordinal`, order)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

type resendJobView struct{ status, recipient string }

func e2eResendJob(t *testing.T, pool *pgxpool.Pool, ticket uuid.UUID) resendJobView {
	t.Helper()
	var v resendJobView
	var rcpt *string
	if err := pool.QueryRow(context.Background(), `SELECT status, recipient_email FROM delivery_jobs WHERE ticket_id = $1`, ticket).Scan(&v.status, &rcpt); err != nil {
		t.Fatalf("delivery job of %s: %v", ticket, err)
	}
	if rcpt != nil {
		v.recipient = *rcpt
	}
	return v
}

// resendWorkerPayloads returns the payloads of the ticket.deliver jobs queued for the tickets.
func resendWorkerPayloads(t *testing.T, pool *pgxpool.Pool, tickets []uuid.UUID) []map[string]any {
	t.Helper()
	ids := make([]string, len(tickets))
	for i, id := range tickets {
		ids[i] = id.String()
	}
	rows, err := pool.Query(context.Background(),
		`SELECT payload FROM worker_jobs WHERE job_type = 'ticket.deliver' AND payload->>'ticket_id' = ANY($1) ORDER BY created_at, id`, ids)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func resendDialogStep(t *testing.T, pool *pgxpool.Pool, tg int64) (string, bool) {
	t.Helper()
	var step string
	err := pool.QueryRow(context.Background(), `SELECT step FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'resend'`, tg).Scan(&step)
	return step, err == nil
}

// markSent parks the delivery jobs of the tickets as delivered, the state a
// real send leaves, so the next scenario starts from "the letter went out".
func markSent(t *testing.T, pool *pgxpool.Pool, tickets []uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE delivery_jobs SET status = 'sent', sent_at = now() WHERE ticket_id = ANY($1)`, tickets); err != nil {
		t.Fatal(err)
	}
}

// seedSiteOrder adds a paid one-ticket order of the organization on a channel
// with an active WordPress webhook (a seller's own site) and returns the order.
func seedSiteOrder(t *testing.T, pool *pgxpool.Pool, s *ordBotSeed, orgID uuid.UUID) (order, ticket uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	channel, res, cs := uuid.New(), uuid.New(), uuid.New()
	order, ticket = uuid.New(), uuid.New()
	var tier uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM ticket_tiers WHERE session_id = $1 LIMIT 1`, s.session1).Scan(&tier); err != nil {
		t.Fatalf("tier: %v", err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("site order seed %q: %v", sql, err)
		}
	}
	exec(`INSERT INTO sales_channels (id, org_id, name, settings) VALUES ($1, $2, $3, '{}'::jsonb)`, channel, orgID, "Resend site "+s.suffix)
	if _, err := gen.New(pool).CreateWPWebhookSubscriber(ctx, channel, "https://site.example.test/hook", "secret"); err != nil {
		t.Fatalf("CreateWPWebhookSubscriber: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM worker_jobs WHERE payload->>'ticket_id' = $1`, ticket.String())
		_, _ = pool.Exec(c, `DELETE FROM webhook_subscribers WHERE channel_id = $1`, channel)
		// the organization-wide cleanup of seedOrdersBotOrg removes the rest
	})
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	      VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, res, orgID, channel, s.session1)
	exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`, cs, orgID, channel, res)
	exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                          source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, paid_at, created_at)
	      VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 2500, 0, 0, 2500, 'Site Buyer', $8, now(), now() - interval '4 hours')`,
		order, orgID, channel, s.event1, s.session1, cs, res, "site-"+s.suffix+"@example.test")
	exec(`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal)
	      VALUES ($1, $2, $3, $4, $5, $6, 0)`, ticket, cs, s.session1, tier, "site-"+s.suffix+"@example.test", order)
	exec(`INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, ticket_id, unit_price, discount, charge, total)
	      VALUES ($1, $2, 0, 'ticket', $3, $4, 2500, 0, 0, 2500)`, uuid.New(), order, tier, ticket)
	exec(`INSERT INTO delivery_jobs (ticket_id, recipient_email, status, sent_at) VALUES ($1, $2, 'sent', now())`, ticket, "site-"+s.suffix+"@example.test")
	return order, ticket
}

func orderBuyerEmail(t *testing.T, pool *pgxpool.Pool, order uuid.UUID) string {
	t.Helper()
	var e string
	if err := pool.QueryRow(context.Background(), `SELECT buyer_email FROM orders WHERE id = $1`, order).Scan(&e); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestBotE2E_ResendTickets_OwnerAndManager(t *testing.T) {
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

	tickets := resendTickets(t, pool, seed.anna)
	if len(tickets) != 2 {
		t.Fatalf("Anna's order has %d tickets, want 2", len(tickets))
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM worker_jobs WHERE payload->>'ticket_id' = ANY($1)`,
			[]string{tickets[0].String(), tickets[1].String()})
	})
	siteOrder, siteTicket := seedSiteOrder(t, pool, seed, f.orgID)

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	defer func() { stop() }()

	annaMasked := strings.SplitN(seed.annaEmail, "@", 2)[0][:1] // "a"
	for _, who := range []struct {
		name string
		tgID int64
	}{{"owner", ecOwnerTG}, {"manager", ecManagerTG}} {
		d := ecDriver{t, tg, who.tgID}
		markSent(t, pool, tickets)
		before := len(resendWorkerPayloads(t, pool, tickets))

		// ── the order's own address ──────────────────────────────────────────
		d.press("or:v:"+seed.anna.String(), "Заказ №")
		choose := d.press("or:r:s:"+seed.anna.String(), "Выслать билеты")
		for _, w := range []string{"Билетов к отправке: 2", "Адрес заказа: " + annaMasked + "*"} {
			if !strings.Contains(choose, w) {
				t.Errorf("%s: the choice lacks %q:\n%s", who.name, w, choose)
			}
		}
		if strings.Contains(choose, seed.annaEmail) {
			t.Errorf("%s: the order's address must be masked:\n%s", who.name, choose)
		}
		if step, ok := resendDialogStep(t, pool, who.tgID); !ok || step != "choose" {
			t.Errorf("%s: dialog = %q %v, want choose", who.name, step, ok)
		}
		confirm := d.press("or:r:a", "Отправить билеты?")
		if strings.Contains(confirm, seed.annaEmail) || !strings.Contains(confirm, "адрес заказа") {
			t.Errorf("%s: the confirmation:\n%s", who.name, confirm)
		}
		// Nothing is queued before the last press.
		if got := e2eResendJob(t, pool, tickets[0]); got.status != "sent" {
			t.Fatalf("%s: a job moved before the confirmation: %+v", who.name, got)
		}
		done := d.press("or:r:go", "Готово")
		if !strings.Contains(done, "Билеты заказа №") || strings.Contains(done, seed.annaEmail) {
			t.Errorf("%s: the result:\n%s", who.name, done)
		}
		for _, tk := range tickets {
			if got := e2eResendJob(t, pool, tk); got.status != "pending" || got.recipient != seed.annaEmail {
				t.Errorf("%s: ticket %s job = %+v, want pending to the order's address", who.name, tk, got)
			}
		}
		if after := resendWorkerPayloads(t, pool, tickets); len(after) != before+2 {
			t.Errorf("%s: %d worker jobs, want %d", who.name, len(after), before+2)
		} else if _, has := after[len(after)-1]["recipient_expires_at"]; has {
			t.Errorf("%s: the order's own address carries no deadline: %v", who.name, after[len(after)-1])
		}
		if _, ok := resendDialogStep(t, pool, who.tgID); ok {
			t.Errorf("%s: the dialog must end after the send", who.name)
		}
		markSent(t, pool, tickets)

		// ── a one-time address ───────────────────────────────────────────────
		oneTime := "gift-" + who.name + "-" + seed.suffix + "@example.org"
		d.press("or:v:"+seed.anna.String(), "Заказ №")
		d.press("or:r:s:"+seed.anna.String(), "Выслать билеты")
		ask := d.press("or:r:o", "Другой адрес")
		for _, w := range []string{"один раз", "24 часа", "не изменится", "Ответственность"} {
			if !strings.Contains(ask, w) {
				t.Errorf("%s: the warning lacks %q:\n%s", who.name, w, ask)
			}
		}
		if step, _ := resendDialogStep(t, pool, who.tgID); step != "email" {
			t.Errorf("%s: dialog step = %q, want email", who.name, step)
		}
		d.say("not an e-mail", "Это не похоже на e-mail")
		if got := e2eResendJob(t, pool, tickets[0]); got.status != "sent" {
			t.Fatalf("%s: a bad address moved a job: %+v", who.name, got)
		}
		confirm = d.say(oneTime, "Отправить билеты?")
		if !strings.Contains(confirm, oneTime) || !strings.Contains(confirm, "24 часа") {
			t.Errorf("%s: the one-time confirmation:\n%s", who.name, confirm)
		}
		before = len(resendWorkerPayloads(t, pool, tickets))
		sentAt := time.Now().UTC()
		d.press("or:r:go", "Готово")
		for _, tk := range tickets {
			if got := e2eResendJob(t, pool, tk); got.status != "pending" || got.recipient != oneTime {
				t.Errorf("%s: ticket %s job = %+v, want pending to the one-time address", who.name, tk, got)
			}
		}
		after := resendWorkerPayloads(t, pool, tickets)
		if len(after) != before+2 {
			t.Fatalf("%s: %d worker jobs, want %d", who.name, len(after), before+2)
		}
		for _, p := range after[len(after)-2:] {
			exp, _ := p["recipient_expires_at"].(string)
			at, err := time.Parse(time.RFC3339, exp)
			if err != nil || at.Before(sentAt.Add(23*time.Hour+55*time.Minute)) || at.After(sentAt.Add(24*time.Hour+5*time.Minute)) {
				t.Errorf("%s: recipient_expires_at = %q (%v), want about 24 hours ahead", who.name, exp, err)
			}
			if strings.Contains(string(mustJSON(p)), oneTime) {
				t.Errorf("%s: the worker payload carries the address: %v", who.name, p)
			}
		}
		// The order keeps its own address everywhere.
		if got := orderBuyerEmail(t, pool, seed.anna); got != seed.annaEmail {
			t.Errorf("%s: buyer_email = %q, the one-time address must never reach the order", who.name, got)
		}
		var holders int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM tickets WHERE order_id = $1 AND holder_email <> $2`, seed.anna, seed.annaEmail).Scan(&holders); err != nil || holders != 0 {
			t.Errorf("%s: %d tickets changed holder (%v)", who.name, holders, err)
		}
		var history string
		if err := pool.QueryRow(context.Background(),
			`SELECT coalesce(string_agg(payload::text, ' '), '') FROM order_events WHERE order_id = $1 AND type = 'tickets_resent'`, seed.anna).Scan(&history); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(history, "@") {
			t.Errorf("%s: the order history carries an address: %s", who.name, history)
		}
		markSent(t, pool, tickets)
	}
	var events int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM order_events WHERE order_id = $1 AND type = 'tickets_resent'`, seed.anna).Scan(&events); err != nil || events != 4 {
		t.Errorf("history rows = %d (%v), want 4 (two resends by each of two people)", events, err)
	}

	// ── a refunded ticket is not sent ────────────────────────────────────────
	d := ecDriver{t, tg, ecOwnerTG}
	if _, err := pool.Exec(context.Background(), `UPDATE tickets SET status = 'cancelled', cancelled_at = now() WHERE id = $1`, tickets[1]); err != nil {
		t.Fatal(err)
	}
	markSent(t, pool, tickets)
	d.press("or:v:"+seed.anna.String(), "Заказ №")
	one := d.press("or:r:s:"+seed.anna.String(), "Билетов к отправке: 1")
	if !strings.Contains(one, "возвращённые и аннулированные не отправляются") {
		t.Errorf("the choice must say refunded tickets are not sent:\n%s", one)
	}
	d.press("or:r:a", "Отправить билеты?")
	d.press("or:r:go", "Готово")
	if got := e2eResendJob(t, pool, tickets[0]); got.status != "pending" {
		t.Errorf("the active ticket's job = %+v, want pending", got)
	}
	if got := e2eResendJob(t, pool, tickets[1]); got.status != "sent" {
		t.Errorf("the refunded ticket's job = %+v, must stay as it was", got)
	}
	// Nothing left to send: the dialog does not even open.
	if _, err := pool.Exec(context.Background(), `UPDATE tickets SET status = 'cancelled', cancelled_at = now() WHERE id = $1`, tickets[0]); err != nil {
		t.Fatal(err)
	}
	d.press("or:r:s:"+seed.anna.String(), "нет действующих билетов")
	if _, ok := resendDialogStep(t, pool, ecOwnerTG); ok {
		t.Error("a dialog was opened for an order with no active ticket")
	}

	// ── an order sold through a seller's own site ────────────────────────────
	d.press("or:v:"+siteOrder.String(), "Заказ №")
	d.press("or:r:s:"+siteOrder.String(), "Выслать билеты")
	d.press("or:r:a", "Отправить билеты?")
	var siteNum int64
	if err := pool.QueryRow(context.Background(), `SELECT system_id FROM orders WHERE id = $1`, siteOrder).Scan(&siteNum); err != nil {
		t.Fatal(err)
	}
	site := d.press("or:r:go", "сайт")
	if !strings.Contains(site, "письмо отправляет сайт") || !strings.Contains(site, "№"+fmt.Sprint(siteNum)) {
		t.Errorf("the seller-site answer must name the site and the order number:\n%s", site)
	}
	if got := e2eResendJob(t, pool, siteTicket); got.status != "sent" {
		t.Errorf("a seller-site order's job = %+v, nothing may be queued", got)
	}
	if n := len(resendWorkerPayloads(t, pool, []uuid.UUID{siteTicket})); n != 0 {
		t.Errorf("%d worker jobs queued for a seller-site order", n)
	}

	// ── somebody from ANOTHER organization ───────────────────────────────────
	out := ecDriver{t, tg, ecOutsiderTG}
	for _, c := range []string{"or:r:s:" + seed.anna.String(), "or:r:a", "or:r:o", "or:r:go", "or:r:b"} {
		got := out.press(c, "Не найдено")
		for _, leak := range []string{"Anna", seed.annaEmail, "Orders Show"} {
			if strings.Contains(got, leak) {
				t.Errorf("%s leaked %q to another organization:\n%s", c, leak, got)
			}
		}
	}
	out.say(seed.annaEmail, "Я не понял") // no dialog, so just a text
	if n := resendCount(t, pool, `SELECT count(*) FROM worker_jobs WHERE job_type = 'ticket.deliver' AND payload->>'ticket_id' = $1`, siteTicket.String()); n != 0 {
		t.Errorf("another organization queued %d jobs", n)
	}
}

func resendCount(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

// The dialog is on disk: a bot restart between two steps loses nothing, a
// screen that ran out is reported once, and any other press ends it.
func TestBotE2E_ResendTickets_SurvivesRestartAndExpires(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedOrdersBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	tickets := resendTickets(t, pool, seed.anna)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM worker_jobs WHERE payload->>'ticket_id' = ANY($1)`,
			[]string{tickets[0].String(), tickets[1].String()})
	})
	markSent(t, pool, tickets)

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	defer func() { stop() }()
	d := ecDriver{t, tg, ecOwnerTG}

	d.press("or:v:"+seed.anna.String(), "Заказ №")
	d.press("or:r:s:"+seed.anna.String(), "Выслать билеты")
	d.press("or:r:o", "Другой адрес")

	stop = restartDialogTestBot(t, pool, api, tg, stop)

	// The bot no longer remembers anything in memory, yet the typed address is
	// taken for the address, and the confirmation press still works after a
	// second restart.
	addr := "after-restart-" + seed.suffix + "@example.org"
	d.say(addr, "Отправить билеты?")
	stop = restartDialogTestBot(t, pool, api, tg, stop)
	d.press("or:r:go", "Готово")
	for _, tk := range tickets {
		if got := e2eResendJob(t, pool, tk); got.status != "pending" || got.recipient != addr {
			t.Errorf("ticket %s job = %+v, want pending to %s", tk, got, addr)
		}
	}
	markSent(t, pool, tickets)

	// A dialog left half-way is ended by any other press.
	d.press("or:r:s:"+seed.anna.String(), "Выслать билеты")
	d.press("or:r:o", "Другой адрес")
	d.press("home", "Что будем делать")
	if _, ok := resendDialogStep(t, pool, ecOwnerTG); ok {
		t.Error("the Home press must end the resend dialog")
	}
	// ...so an address typed afterwards goes nowhere near the order.
	m := tg.mark()
	tg.push(e2eMessageAs(ecOwnerTG, "stray-"+seed.suffix+"@example.org"))
	tg.waitSince(t, m, "Я не понял")
	if got := e2eResendJob(t, pool, tickets[0]); got.status != "sent" {
		t.Errorf("a stray address moved a job: %+v", got)
	}

	// A dialog that ran out is reported once: on the press, then it is gone.
	d.press("or:r:s:"+seed.anna.String(), "Выслать билеты")
	d.press("or:r:a", "Отправить билеты?")
	if _, err := pool.Exec(context.Background(),
		`UPDATE bot_dialogs SET expires_at = now() - interval '1 minute' WHERE telegram_user_id = $1 AND kind = 'resend'`, ecOwnerTG); err != nil {
		t.Fatal(err)
	}
	d.press("or:r:go", "Этот диалог устарел")
	if got := e2eResendJob(t, pool, tickets[0]); got.status != "sent" {
		t.Errorf("an expired dialog queued a job: %+v", got)
	}
	d.press("or:r:go", "Не найдено")
}

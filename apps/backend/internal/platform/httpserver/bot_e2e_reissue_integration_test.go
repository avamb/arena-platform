//go:build integration

package httpserver

// End-to-end proof of "Replace tickets" on the bot's order card (spec 63,
// stage R-4) through the real router, the real bot and the stub Telegram: the
// owner walks the whole path (the case question, the real address, the
// reason, the check screen, the typed word); "From the order's address"
// leads to the resend screen and changes no barcode; a wrong word changes
// nothing; the right word, typed after a bot restart, replaces every ticket
// (old tickets transferred, new active ones, one journal row with the new
// address, the letter queued to the new address only); a reissue that has not
// gone out yet shows its state and sends its letter again; the agent sees no
// button and a forged press writes nothing.

import (
	"context"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

const reissueAgentTG = int64(7881)

// botReissueCleanup removes what a reissue of the order wrote, before the
// orders seed deletes the order (t.Cleanup runs last-in first).
func botReissueCleanup(t *testing.T, pool *pgxpool.Pool, order uuid.UUID, emails []string) {
	t.Helper()
	t.Cleanup(func() {
		c := context.Background()
		ids := func(sql string, args ...any) []uuid.UUID {
			rows, err := pool.Query(c, sql, args...)
			if err != nil {
				t.Logf("bot reissue cleanup: %v", err)
				return nil
			}
			defer rows.Close()
			var out []uuid.UUID
			for rows.Next() {
				var id uuid.UUID
				if rows.Scan(&id) == nil {
					out = append(out, id)
				}
			}
			return out
		}
		orders := []uuid.UUID{order}
		newTickets := ids(`SELECT i.new_ticket_id FROM ticket_reissue_items i JOIN ticket_reissues r ON r.id = i.reissue_id
			WHERE r.order_id = ANY($1)`, orders)
		allTickets := ids(`SELECT id FROM tickets WHERE order_id = ANY($1)`, orders)
		customers := ids(`SELECT customer_id FROM customer_identities WHERE value_normalized = ANY($1)`, emails)
		ticketText := []string{order.String()}
		for _, id := range allTickets {
			ticketText = append(ticketText, id.String())
		}
		for _, s := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM worker_jobs WHERE job_type = 'reissue.provider_email'
			    AND payload->>'reissue_id' IN (SELECT id::text FROM ticket_reissues WHERE order_id = ANY($1))`, []any{orders}},
			{`DELETE FROM worker_jobs WHERE payload->>'ticket_id' = ANY($1)`, []any{ticketText}},
			{`DELETE FROM outbox_events WHERE aggregate_id = ANY($1)`, []any{ticketText}},
			{`DELETE FROM ticket_reissue_items WHERE reissue_id IN (SELECT id FROM ticket_reissues WHERE order_id = ANY($1))`, []any{orders}},
			{`DELETE FROM ticket_reissues WHERE order_id = ANY($1)`, []any{orders}},
			{`DELETE FROM order_items WHERE order_id = ANY($1)`, []any{orders}},
			{`DELETE FROM delivery_jobs WHERE ticket_id = ANY($1)`, []any{newTickets}},
			{`DELETE FROM barcodes WHERE ticket_id = ANY($1)`, []any{newTickets}},
			{`DELETE FROM ticket_credentials WHERE ticket_id = ANY($1)`, []any{newTickets}},
			{`DELETE FROM tickets WHERE id = ANY($1)`, []any{newTickets}},
			{`UPDATE orders SET customer_id = NULL WHERE id = ANY($1) AND customer_id = ANY($2)`, []any{orders, customers}},
			{`DELETE FROM customer_org_links WHERE customer_id = ANY($1)`, []any{customers}},
			{`DELETE FROM customer_identities WHERE customer_id = ANY($1)`, []any{customers}},
			{`DELETE FROM customers WHERE id = ANY($1)`, []any{customers}},
		} {
			if _, err := pool.Exec(c, s.sql, s.args...); err != nil {
				t.Logf("bot reissue cleanup: %s: %v", s.sql, err)
			}
		}
	})
}

func reissueRows(t *testing.T, pool *pgxpool.Pool, order uuid.UUID) int {
	t.Helper()
	return resendCount(t, pool, `SELECT count(*) FROM ticket_reissues WHERE order_id = $1`, order)
}

func reissueDialogStep(t *testing.T, pool *pgxpool.Pool, tg int64) (string, bool) {
	t.Helper()
	var step string
	err := pool.QueryRow(context.Background(), `SELECT step FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'reissue'`, tg).Scan(&step)
	return step, err == nil
}

func TestBotE2E_Reissue_OwnerReplacesEveryTicket(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	ctx := context.Background()
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedOrdersBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")

	// The agent: a member of the organization without ticket.update.
	q := gen.New(pool)
	agent, err := q.InsertUser(ctx, f.newEmail("agent"), "x", "ru")
	if err != nil {
		t.Fatalf("InsertUser agent: %v", err)
	}
	if _, err := q.InsertMembership(ctx, agent.ID, f.orgID, "agent"); err != nil {
		t.Fatalf("InsertMembership agent: %v", err)
	}
	if _, err := q.UpsertBotTelegramLink(ctx, reissueAgentTG, agent.ID, nil, "ru", &f.orgID); err != nil {
		t.Fatalf("link agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, reissueAgentTG)
	})

	newEmail := "real-anna-" + seed.suffix + "@example.org"
	botReissueCleanup(t, pool, seed.anna, []string{newEmail, seed.annaEmail})
	oldTickets := resendTickets(t, pool, seed.anna)
	if len(oldTickets) != 2 {
		t.Fatalf("Anna's order has %d tickets, want 2", len(oldTickets))
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM worker_jobs WHERE payload->>'ticket_id' = ANY($1)`,
			[]string{oldTickets[0].String(), oldTickets[1].String()})
		_, _ = pool.Exec(context.Background(), `DELETE FROM bot_dialogs WHERE org_id = $1`, f.orgID)
	})

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	defer func() { stop() }()
	d := ecDriver{t, tg, ecOwnerTG}

	// ── the card carries the button ──────────────────────────────────────────
	d.press("or:v:"+seed.anna.String(), "Заказ №")
	if _, _, data := tg.lastOf(ecOwnerTG); !hasPrefixData(data, "or:x:s:"+seed.anna.String()) {
		t.Fatalf("the owner's card lacks «Заменить билеты»: %v", data)
	}

	// ── "From the order's address" leads to the resend screen ────────────────
	caseText := d.press("or:x:s:"+seed.anna.String(), "С какого адреса пишет покупатель")
	for _, w := range []string{"Билетов к замене: 2", "Адрес заказа: a*"} {
		if !strings.Contains(caseText, w) {
			t.Errorf("the case question lacks %q:\n%s", w, caseText)
		}
	}
	if strings.Contains(caseText, seed.annaEmail) {
		t.Errorf("the order's address must be masked:\n%s", caseText)
	}
	d.press("or:x:fo", "Выслать билеты")
	if step, ok := resendDialogStep(t, pool, ecOwnerTG); !ok || step != "choose" {
		t.Errorf("the resend dialog = %q %v, want choose", step, ok)
	}
	if _, ok := reissueDialogStep(t, pool, ecOwnerTG); ok {
		t.Error("handing over to the resend must end the reissue dialog")
	}
	if n := reissueRows(t, pool, seed.anna); n != 0 {
		t.Fatalf("the resend route wrote %d reissues", n)
	}

	// ── "From another address": the whole path ───────────────────────────────
	d.press("or:x:s:"+seed.anna.String(), "С какого адреса пишет покупатель")
	ask := d.press("or:x:fn", "Верный адрес")
	for _, w := range []string{"Anna Orderova", "+34 611 222 333", "a*"} {
		if !strings.Contains(ask, w) {
			t.Errorf("the address question lacks %q for comparison:\n%s", w, ask)
		}
	}
	d.say("not an e-mail", "Это не похоже на e-mail")
	d.say(newEmail, "Причина замены")
	d.press("or:x:r:1", "Добавьте заметку")
	check := d.press("or:x:nn", "Проверьте замену")
	for _, w := range []string{"перестанут пускать на вход", "Отменить замену нельзя", "уже был вход", "Ошибка в адресе при покупке"} {
		if !strings.Contains(check, w) {
			t.Errorf("the check screen lacks %q:\n%s", w, check)
		}
	}
	if strings.Contains(check, newEmail) {
		t.Errorf("the check screen must mask the new address:\n%s", check)
	}
	if n := reissueRows(t, pool, seed.anna); n != 0 {
		t.Fatalf("%d reissues before the word", n)
	}
	// The confirm press only opens the word step.
	d.press("or:x:ok", "Последний шаг")
	if step, _ := reissueDialogStep(t, pool, ecOwnerTG); step != "word" {
		t.Fatalf("dialog step = %q, want word", step)
	}
	if n := reissueRows(t, pool, seed.anna); n != 0 {
		t.Fatalf("the confirm press wrote %d reissues", n)
	}

	// The dialog is on disk: the word typed after a restart is still its answer.
	stop = restartDialogTestBot(t, pool, api, tg, stop)

	// ── a wrong word changes nothing ─────────────────────────────────────────
	d.say("заменить", "Текст не совпал")
	if n := reissueRows(t, pool, seed.anna); n != 0 {
		t.Fatalf("a wrong word wrote %d reissues", n)
	}
	if n := resendCount(t, pool, `SELECT count(*) FROM tickets WHERE order_id = $1 AND status = 'active'`, seed.anna); n != 2 {
		t.Fatalf("a wrong word changed the tickets: %d active", n)
	}

	// ── the right word replaces every ticket ─────────────────────────────────
	done := d.say("ЗАМЕНИТЬ БИЛЕТЫ", "заменены")
	if n := reissueRows(t, pool, seed.anna); n != 1 {
		t.Fatalf("reissues = %d, want 1", n)
	}
	var newEmailRow, state string
	if err := pool.QueryRow(ctx, `SELECT new_email, state FROM ticket_reissues WHERE order_id = $1`, seed.anna).Scan(&newEmailRow, &state); err != nil {
		t.Fatal(err)
	}
	if newEmailRow != newEmail || state != "pending" {
		t.Errorf("journal row = %q %q, want %q pending", newEmailRow, state, newEmail)
	}
	if n := resendCount(t, pool, `SELECT count(*) FROM tickets WHERE id = ANY($1) AND status = 'transferred'`, oldTickets); n != 2 {
		t.Errorf("%d old tickets are transferred, want 2", n)
	}
	newTickets := resendCount(t, pool, `SELECT count(*) FROM tickets WHERE order_id = $1 AND status = 'active' AND NOT (id = ANY($2))`, seed.anna, oldTickets)
	if newTickets != 2 {
		t.Errorf("%d new active tickets, want 2", newTickets)
	}
	var numbers []int64
	rows, err := pool.Query(ctx, `SELECT t.system_ticket_id FROM ticket_reissue_items i JOIN tickets t ON t.id = i.new_ticket_id
		JOIN ticket_reissues r ON r.id = i.reissue_id WHERE r.order_id = $1`, seed.anna)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n int64
		if rows.Scan(&n) == nil {
			numbers = append(numbers, n)
		}
	}
	rows.Close()
	if len(numbers) != 2 {
		t.Fatalf("new ticket numbers = %v", numbers)
	}
	for _, n := range numbers {
		if !strings.Contains(done, "→ №"+strconv.FormatInt(n, 10)) {
			t.Errorf("the result lacks the new number %d:\n%s", n, done)
		}
	}
	if strings.Contains(done, newEmail) {
		t.Errorf("the result must mask the new address:\n%s", done)
	}
	if _, ok := reissueDialogStep(t, pool, ecOwnerTG); ok {
		t.Error("the dialog must end after the replacement")
	}
	// The letter goes to the new address only; the earlier one gets nothing.
	if n := resendCount(t, pool, `SELECT count(*) FROM delivery_jobs dj JOIN ticket_reissue_items i ON i.new_ticket_id = dj.ticket_id
		JOIN ticket_reissues r ON r.id = i.reissue_id WHERE r.order_id = $1 AND dj.recipient_email = $2`, seed.anna, newEmail); n != 2 {
		t.Errorf("%d delivery jobs to the new address, want 2", n)
	}
	if n := resendCount(t, pool, `SELECT count(*) FROM delivery_jobs WHERE ticket_id = ANY($1) AND status <> 'sent'`, oldTickets); n != 0 {
		t.Errorf("%d delivery jobs of the old tickets moved: nothing may go to the wrong address", n)
	}
	if got := orderBuyerEmail(t, pool, seed.anna); got != seed.annaEmail {
		t.Errorf("buyer_email = %q before the letter left, want the old one", got)
	}

	// ── the reissue that has not gone out: its state and a new letter ────────
	pending := d.press("or:x:s:"+seed.anna.String(), "ещё не завершена")
	if !strings.Contains(pending, "в очереди") {
		t.Errorf("the pending screen lacks its state:\n%s", pending)
	}
	if _, _, data := tg.lastOf(ecOwnerTG); !hasPrefixData(data, "or:x:rs") {
		t.Errorf("the pending screen lacks «Отправить письмо ещё раз»: %v", data)
	}
	d.press("or:x:rs", "снова в очереди")
	if n := reissueRows(t, pool, seed.anna); n != 1 {
		t.Errorf("sending the letter again wrote a reissue: %d rows", n)
	}

	// ── the agent sees no button, a forged press writes nothing ──────────────
	ag := ecDriver{t, tg, reissueAgentTG}
	ag.press("or:v:"+seed.anna.String(), "не включает")
	if _, _, data := tg.lastOf(reissueAgentTG); hasPrefixData(data, "or:x:") {
		t.Errorf("the agent got a reissue button: %v", data)
	}
	ag.press("or:x:s:"+seed.anna.String(), "не включает")
	ag.say("ЗАМЕНИТЬ БИЛЕТЫ", "Я не понял")
	if n := reissueRows(t, pool, seed.anna); n != 1 {
		t.Errorf("the agent's presses changed the journal: %d rows", n)
	}
}

// An invitation issued before spec 46 (a complimentary issuance without an
// order) is replaced from its card under «Earlier invitations»: no case
// question, the same reason, check, word and idempotency rules; the old ticket
// is transferred and a new active one of the same issuance gets an invitation
// letter.
func TestBotE2E_Reissue_EarlierInvitation(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	ctx := context.Background()
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedOrdersBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")

	guest := "guest-" + seed.suffix + "@example.test"
	newEmail := "guest-real-" + seed.suffix + "@example.org"
	issuance, ticket := uuid.New(), uuid.New()
	var tier uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM ticket_tiers WHERE session_id = $1 LIMIT 1`, seed.session1).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	for i, st := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO complimentary_issuances (id, org_id, session_id, tier_id, qty, recipients, batch_id, status)
		  VALUES ($1, $2, $3, $4, 1, ARRAY[$5::text], $6, 'issued')`, []any{issuance, f.orgID, seed.session1, tier, guest, "batch-" + seed.suffix}},
		{`INSERT INTO tickets (id, session_id, tier_id, holder_email, complimentary_issuance_id, ordinal)
		  VALUES ($1, $2, $3, $4, $5, 0)`, []any{ticket, seed.session1, tier, guest, issuance}},
	} {
		if _, err := pool.Exec(ctx, st.sql, st.args...); err != nil {
			t.Fatalf("invitation seed %d: %v", i, err)
		}
	}
	// Runs before the orders seed's cleanup (t.Cleanup is last-in first).
	t.Cleanup(func() {
		c := context.Background()
		var ids []uuid.UUID
		rows, _ := pool.Query(c, `SELECT id FROM tickets WHERE complimentary_issuance_id = $1`, issuance)
		for rows.Next() {
			var id uuid.UUID
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		texts := make([]string, 0, len(ids))
		for _, id := range ids {
			texts = append(texts, id.String())
		}
		for _, st := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM worker_jobs WHERE job_type = 'reissue.provider_email'
			    AND payload->>'reissue_id' IN (SELECT id::text FROM ticket_reissues WHERE complimentary_issuance_id = $1)`, []any{issuance}},
			{`DELETE FROM worker_jobs WHERE payload->>'ticket_id' = ANY($1)`, []any{texts}},
			{`DELETE FROM outbox_events WHERE aggregate_id = ANY($1)`, []any{texts}},
			{`DELETE FROM ticket_reissue_items WHERE reissue_id IN (SELECT id FROM ticket_reissues WHERE complimentary_issuance_id = $1)`, []any{issuance}},
			{`DELETE FROM ticket_reissues WHERE complimentary_issuance_id = $1`, []any{issuance}},
			{`DELETE FROM delivery_jobs WHERE ticket_id = ANY($1)`, []any{ids}},
			{`DELETE FROM barcodes WHERE ticket_id = ANY($1)`, []any{ids}},
			{`DELETE FROM ticket_credentials WHERE ticket_id = ANY($1)`, []any{ids}},
			{`DELETE FROM tickets WHERE id = ANY($1)`, []any{ids}},
			{`DELETE FROM complimentary_issuances WHERE id = $1`, []any{issuance}},
			{`DELETE FROM bot_dialogs WHERE org_id = $1`, []any{f.orgID}},
		} {
			if _, err := pool.Exec(c, st.sql, st.args...); err != nil {
				t.Logf("invitation cleanup: %s: %v", st.sql, err)
			}
		}
	})
	invRows := func() int {
		return resendCount(t, pool, `SELECT count(*) FROM ticket_reissues WHERE complimentary_issuance_id = $1`, issuance)
	}

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	defer func() { stop() }()
	d := ecDriver{t, tg, ecOwnerTG}

	// ── the earlier invitation's card carries the button ─────────────────────
	d.press("iv:lg", "Прежние приглашения")
	d.press("iv:lo:0", "Гость:")
	if _, _, data := tg.lastOf(ecOwnerTG); !hasPrefixData(data, "or:x:si:"+issuance.String()) {
		t.Fatalf("the card lacks «Заменить приглашение»: %v", data)
	}

	// ── no case question: the address first ──────────────────────────────────
	ask := d.press("or:x:si:"+issuance.String(), "Верный адрес гостя")
	if !strings.Contains(ask, "Билетов к замене: 1") || strings.Contains(ask, guest) || strings.Contains(ask, "заказ") {
		t.Errorf("the address question:\n%s", ask)
	}
	if step, _ := reissueDialogStep(t, pool, ecOwnerTG); step != "email" {
		t.Fatalf("dialog step = %q, want email", step)
	}
	d.say(newEmail, "Причина замены")
	d.press("or:x:r:3", "истории приглашения")
	check := d.press("or:x:nn", "Проверьте замену приглашения")
	if strings.Contains(check, newEmail) || strings.Contains(check, "Платёжная система") || strings.Contains(check, "№0") {
		t.Errorf("the check screen:\n%s", check)
	}
	d.press("or:x:ok", "Последний шаг")
	d.say("заменить приглашение", "Текст не совпал")
	if n := invRows(); n != 0 {
		t.Fatalf("a wrong word wrote %d reissues", n)
	}
	done := d.say("REISSUE TICKETS", "Приглашение заменено")
	if n := invRows(); n != 1 {
		t.Fatalf("reissues = %d, want 1", n)
	}
	if n := resendCount(t, pool, `SELECT count(*) FROM tickets WHERE id = $1 AND status = 'transferred'`, ticket); n != 1 {
		t.Error("the old invitation ticket is not transferred")
	}
	var newID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tickets WHERE complimentary_issuance_id = $1 AND status = 'active'
		AND order_id IS NULL AND checkout_session_id IS NULL AND id <> $2`, issuance, ticket).Scan(&newID); err != nil {
		t.Fatalf("no new active ticket of the same issuance: %v", err)
	}
	if n := resendCount(t, pool, `SELECT count(*) FROM worker_jobs WHERE job_type = 'ticket.deliver' AND payload->>'ticket_id' = $1
		AND payload->>'template' = 'invitation'`, newID.String()); n != 1 {
		t.Errorf("%d invitation letters queued for the new ticket, want 1", n)
	}
	if n := resendCount(t, pool, `SELECT count(*) FROM delivery_jobs WHERE ticket_id = $1 AND recipient_email = $2`, newID, newEmail); n != 1 {
		t.Errorf("the new ticket's letter does not go to the new address")
	}
	if n := resendCount(t, pool, `SELECT count(*) FROM worker_jobs WHERE payload->>'ticket_id' = $1`, ticket.String()); n != 0 {
		t.Errorf("%d jobs for the old ticket: nothing may go to the wrong address", n)
	}
	if strings.Contains(done, newEmail) || !strings.Contains(done, "→ №") {
		t.Errorf("the result:\n%s", done)
	}
	if _, ok := reissueDialogStep(t, pool, ecOwnerTG); ok {
		t.Error("the dialog must end after the replacement")
	}

	// ── the pending reissue: its state and a new letter ──────────────────────
	d.press("or:x:si:"+issuance.String(), "Замена приглашения")
	d.press("or:x:rs", "снова в очереди")
	if n := invRows(); n != 1 {
		t.Errorf("sending the letter again wrote a reissue: %d rows", n)
	}
}

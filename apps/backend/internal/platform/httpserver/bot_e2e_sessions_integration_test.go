//go:build integration

package httpserver

// End-to-end proof of the bot's "Sessions" screens: a linked manager opens an
// event that already has a paying buyer, MOVES its one session to another day
// (the same session — never a second one), is asked for the organizer's
// contact e-mail because the organization has none, sees the numbers, and
// confirms. The session row is rewritten in place, one journal row and one
// buyer letter job are written, and a later cancellation of the whole event
// does the same through the real router, with Telegram replaced by the stub.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

const e2eSessionsTelegramUser = 781

func e2eSessionsMessage(text string) string {
	return fmt.Sprintf(`"message":{"message_id":%d,"date":1700000000,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"Ses","language_code":"ru"},"text":%q}`,
		time.Now().UnixNano()%100000, e2eSessionsTelegramUser, e2eSessionsTelegramUser, text)
}

func e2eSessionsCallback(data string) string {
	return fmt.Sprintf(`"callback_query":{"id":"cb-%d","from":{"id":%d,"is_bot":false,"first_name":"Ses","language_code":"ru"},"chat_instance":"x","data":%q,"message":{"message_id":5,"date":1700000000,"chat":{"id":%d,"type":"private"},"text":"menu"}}`,
		time.Now().UnixNano()%100000, e2eSessionsTelegramUser, data, e2eSessionsTelegramUser)
}

func TestBotE2E_SessionsMoveKeepsTheSessionAndWritesToBuyers(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	managerEmail := f.newEmail("ses")
	if rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"manager","locale":"ru"}`, managerEmail)); rec.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body.String())
	}
	code, _ := f.queuedCode(managerEmail)
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d,"locale":"ru"}`, code, managerEmail, e2eSessionsTelegramUser)); rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}

	// One event, one session in 30 days (Madrid), one paid ticket.
	eventID, sessionID, venueID := uuid.New(), uuid.New(), uuid.New()
	chanID, resID, csID, orderID, ticketID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	buyer := "buyer-" + uuid.NewString()[:8] + "@example.com"
	start := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Hour)
	for i, step := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, []any{venueID, f.orgID, "Bot Move Hall"}},
		{`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'private')`, []any{eventID, f.orgID, "Bot Move Event"}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		  VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 100, 'scheduled', 'EUR', 'override')`, []any{sessionID, eventID, venueID, start}},
		{`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`, []any{chanID, f.orgID, "Bot Move Channel"}},
		{`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
		  VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, []any{resID, f.orgID, chanID, sessionID}},
		{`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`, []any{csID, f.orgID, chanID, resID}},
		{`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
		                      source, status, currency, subtotal, discount, charge, total, buyer_email)
		  VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 2500, 0, 0, 2500, $8)`,
			[]any{orderID, f.orgID, chanID, eventID, sessionID, csID, resID, buyer}},
		{`INSERT INTO tickets (id, checkout_session_id, session_id, holder_email, order_id) VALUES ($1, $2, $3, $4, $5)`,
			[]any{ticketID, csID, sessionID, buyer, orderID}},
	} {
		if _, err := pool.Exec(ctx, step.sql, step.args...); err != nil {
			t.Fatalf("seed step %d: %v", i, err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		for _, stmt := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, []any{int64(e2eSessionsTelegramUser)}},
			{`DELETE FROM worker_jobs WHERE job_type = 'session.change_email' AND payload->>'order_id' = $1`, []any{orderID.String()}},
			{`DELETE FROM session_changes WHERE org_id = $1`, []any{f.orgID}},
			{`DELETE FROM tickets WHERE id = $1`, []any{ticketID}},
			{`DELETE FROM orders WHERE id = $1`, []any{orderID}},
			{`DELETE FROM checkout_sessions WHERE id = $1`, []any{csID}},
			{`DELETE FROM reservations WHERE id = $1`, []any{resID}},
			{`DELETE FROM sales_channels WHERE id = $1`, []any{chanID}},
			{`DELETE FROM inventory_ledger WHERE session_id = $1`, []any{sessionID}},
			{`DELETE FROM sessions WHERE id = $1`, []any{sessionID}},
			{`DELETE FROM events WHERE id = $1`, []any{eventID}},
			{`DELETE FROM venues WHERE id = $1`, []any{venueID}},
			{`DELETE FROM audit_events WHERE metadata->>'org_id' = $1`, []any{f.orgID.String()}},
		} {
			if _, err := pool.Exec(c, stmt.sql, stmt.args...); err != nil {
				t.Logf("sessions e2e cleanup: %s: %v", stmt.sql, err)
			}
		}
	})

	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	tg := newStubTelegram(t)
	bot, err := eventbot.New(eventbot.Options{
		Token:             "123:test-token",
		Queries:           gen.New(pool),
		Arena:             eventbot.NewArenaClient(api.URL, botTestServiceToken, api.Client()),
		Minter:            eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience),
		Texts:             eventbot.NewTexts(bundle),
		TelegramServerURL: tg.srv.URL,
		HTTPClient:        tg.srv.Client(),
		TicketsBaseURL:    "https://tickets.test",
	})
	if err != nil {
		t.Fatalf("eventbot.New: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()

	say := func(text, expect string) string {
		t.Helper()
		m := tg.mark()
		tg.push(e2eSessionsMessage(text))
		return tg.waitSince(t, m, expect)
	}
	press := func(data, expect string) string {
		t.Helper()
		m := tg.mark()
		tg.push(e2eSessionsCallback(data))
		return tg.waitSince(t, m, expect)
	}
	sessionStart := func() time.Time {
		var s time.Time
		if err := pool.QueryRow(ctx, `SELECT start_at FROM sessions WHERE id = $1`, sessionID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// The event card offers the dialog; the dialog lists the one session.
	press("ses:list:"+eventID.String(), "Сеансы мероприятия")
	press("ses:o:0", "Здесь можно перенести")
	press("ses:mv", "На какую дату перенести")

	// The new day is typed, read back and confirmed (never taken on trust).
	newDay := start.AddDate(0, 0, 7).In(time.UTC)
	iso := newDay.Format("2006-01-02")
	say(fmt.Sprintf("%02d.%02d.%d", newDay.Day(), int(newDay.Month()), newDay.Year()), "Вы написали")
	press("ses:dc:"+iso, "Во сколько начало")
	// Nothing has moved yet: the dry run comes first.
	if !sessionStart().Equal(start) {
		t.Fatal("the session moved before the organizer confirmed")
	}

	// The organization has no contact, so the bot asks for the e-mail before
	// it shows the numbers. A typo is refused.
	say("21:30", "e-mail для связи")
	say("not-an-email", "не похоже")
	say("Promo@Example.com", "Телефон для связи")
	confirm := press("ses:nophone", "билетов 1, заказов 1")
	for _, want := range []string{"Контакт сохранён", "promo@example.com", "Перенос сеанса"} {
		if !strings.Contains(confirm, want) {
			t.Fatalf("confirmation lacks %q:\n%s", want, confirm)
		}
	}

	// The organizer edits the message, then confirms: one press writes.
	press("ses:msg", "Напишите, что прочитают")
	say("Перенесли на неделю, извините.", "Перенос сеанса")
	press("ses:go", "Сеанс перенесён")

	got := sessionStart()
	if got.Equal(start) {
		t.Fatal("the session did not move")
	}
	var sessions int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE event_id = $1 AND deleted_at IS NULL`, eventID).Scan(&sessions)
	if sessions != 1 {
		t.Fatalf("the event has %d sessions after the move, want exactly 1", sessions)
	}
	var kinds []string
	var message string
	var orders int
	if err := pool.QueryRow(ctx, `SELECT kinds, message, orders_total FROM session_changes WHERE session_id = $1`, sessionID).Scan(&kinds, &message, &orders); err != nil {
		t.Fatalf("journal row: %v", err)
	}
	if len(kinds) == 0 || message != "Перенесли на неделю, извините." || orders != 1 {
		t.Fatalf("journal = kinds %v message %q orders %d", kinds, message, orders)
	}
	var jobs int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = 'session.change_email' AND payload->>'order_id' = $1`, orderID.String()).Scan(&jobs)
	if jobs != 1 {
		t.Fatalf("queued buyer letters = %d, want 1", jobs)
	}
	var email string
	_ = pool.QueryRow(ctx, `SELECT contact_email FROM organizations WHERE id = $1`, f.orgID).Scan(&email)
	if email != "promo@example.com" {
		t.Fatalf("organization contact = %q", email)
	}

	// Cancelling the whole event (one session, so the button is the session's
	// own "Cancel") writes another journal row and another letter.
	press("ses:list:"+eventID.String(), "Сеансы мероприятия")
	press("ses:o:0", "Здесь можно перенести")
	press("ses:cx", "Отмена сеанса")
	press("ses:nomsg", "Отмена сеанса")

	// A cancellation is never one button: the press only asks for the word,
	// and a wrong word cancels nothing.
	press("ses:go", "Последний шаг")
	var status string
	_ = pool.QueryRow(ctx, `SELECT status FROM sessions WHERE id = $1`, sessionID).Scan(&status)
	if status == "cancelled" {
		t.Fatal("the session was cancelled by a button press alone")
	}
	say("да", "Это не то слово")
	_ = pool.QueryRow(ctx, `SELECT status FROM sessions WHERE id = $1`, sessionID).Scan(&status)
	if status == "cancelled" {
		t.Fatal("the session was cancelled by a wrong word")
	}
	// Back returns to the confirmation, which still needs the word afterwards.
	press("ses:confirm", "Отмена сеанса")
	press("ses:go", "Последний шаг")
	say("ОТМЕНИТЬ", "Сеанс отменён")
	_ = pool.QueryRow(ctx, `SELECT status FROM sessions WHERE id = $1`, sessionID).Scan(&status)
	if status != "cancelled" {
		t.Fatalf("session status = %q, want cancelled", status)
	}
	var changes int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM session_changes WHERE session_id = $1`, sessionID).Scan(&changes)
	if changes != 2 {
		t.Fatalf("journal rows = %d, want 2 (move + cancel)", changes)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("bot did not stop")
	}
}

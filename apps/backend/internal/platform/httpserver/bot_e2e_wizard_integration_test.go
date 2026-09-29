//go:build integration

package httpserver

// End-to-end proof of the "+ Event" wizard (spec 28 §10 step 3): a linked
// manager answers the bot's questions one by one and the event, its
// session, its venue and its category land in the database through the
// real arena-api router (event-bundle import as the linked user), with
// Telegram replaced by the in-process stub. Run against a migrated
// database (see bot_e2e_integration_test.go for the DSN recipe).

import (
	"context"
	"encoding/json"
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

const e2eWizardTelegramUser = 778

func e2eWizardMessage(text string) string {
	return fmt.Sprintf(`"message":{"message_id":%d,"date":1700000000,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"Wiz","language_code":"ru"},"text":%q}`,
		time.Now().UnixNano()%100000, e2eWizardTelegramUser, e2eWizardTelegramUser, text)
}

func e2eWizardCallback(data string) string {
	return fmt.Sprintf(`"callback_query":{"id":"cb-%d","from":{"id":%d,"is_bot":false,"first_name":"Wiz","language_code":"ru"},"chat_instance":"x","data":%q,"message":{"message_id":5,"date":1700000000,"chat":{"id":%d,"type":"private"},"text":"menu"}}`,
		time.Now().UnixNano()%100000, e2eWizardTelegramUser, data, e2eWizardTelegramUser)
}

// mark returns how many messages the bot has sent so far; waitSince looks
// only at messages sent after that mark, so a repeated screen header
// ("Шаг 2 из 6") cannot satisfy a later wait.
func (s *stubTelegram) mark() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func (s *stubTelegram) waitSince(t *testing.T, mark int, needle string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for _, m := range s.sent[mark:] {
			if strings.Contains(m, needle) {
				s.mu.Unlock()
				return m
			}
		}
		s.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Fatalf("bot never said %q after message %d; it said:\n%s", needle, mark, strings.Join(s.sent[mark:], "\n---\n"))
	return ""
}

func TestBotE2E_WizardCreatesAnEvent(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := gen.New(pool)

	// Everything the wizard creates hangs off the organization; sweep it in
	// FK order before the fixture removes the organization itself.
	t.Cleanup(func() {
		c := context.Background()
		exec := func(label, sql string, args ...any) {
			if _, err := pool.Exec(c, sql, args...); err != nil {
				t.Logf("wizard cleanup: %s: %v", label, err)
			}
		}
		exec("links", `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, int64(e2eWizardTelegramUser))
		exec("compat_map_tiers", `DELETE FROM compatibility_id_map WHERE platform_id IN (
		          SELECT tt.id FROM ticket_tiers tt JOIN sessions s ON s.id = tt.session_id
		          JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`, f.orgID)
		exec("compat_map_sessions", `DELETE FROM compatibility_id_map WHERE platform_id IN (
		          SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`, f.orgID)
		exec("compat_map_events", `DELETE FROM compatibility_id_map WHERE platform_id IN (SELECT id FROM events WHERE org_id = $1)`, f.orgID)
		exec("compat_map_venues", `DELETE FROM compatibility_id_map WHERE platform_id IN (SELECT id FROM venues WHERE org_id = $1)`, f.orgID)
		exec("session_external_refs", `DELETE FROM session_external_refs WHERE org_id = $1`, f.orgID)
		exec("session_seats", `DELETE FROM session_seats WHERE session_id IN (
		          SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`, f.orgID)
		exec("inventory_ledger", `DELETE FROM inventory_ledger WHERE session_id IN (
		          SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`, f.orgID)
		exec("ticket_tiers", `DELETE FROM ticket_tiers WHERE session_id IN (
		          SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`, f.orgID)
		exec("sessions", `DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1)`, f.orgID)
		exec("event_promoters", `DELETE FROM event_promoters WHERE org_id = $1`, f.orgID)
		exec("events", `DELETE FROM events WHERE org_id = $1`, f.orgID)
		exec("org_promoters", `DELETE FROM org_promoters WHERE org_id = $1`, f.orgID)
		exec("venues", `DELETE FROM venues WHERE org_id = $1`, f.orgID)
		exec("audit", `DELETE FROM audit_events WHERE metadata->>'org_id' = $1`, f.orgID.String())
	})

	// The manager is invited and bound through the REST accept route (the
	// Telegram side of that is proven by TestBotE2E_InvitationToMyEvents).
	managerEmail := f.newEmail("wiz")
	if rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"manager","locale":"ru"}`, managerEmail)); rec.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body.String())
	}
	code, _ := f.queuedCode(managerEmail)
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d,"locale":"ru"}`, code, managerEmail, e2eWizardTelegramUser)); rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}

	// Estonia and Tallinn come from migration 0006; Estonia's zone is known
	// to the wizard, so a new venue there needs no timezone question.
	var countryID, cityID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2 = 'EE'`).Scan(&countryID); err != nil {
		t.Fatalf("country EE: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM cities WHERE slug = 'tallinn'`).Scan(&cityID); err != nil {
		t.Fatalf("city tallinn: %v", err)
	}

	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	tg := newStubTelegram(t)
	bot, err := eventbot.New(eventbot.Options{
		Token:             "123:test-token",
		Queries:           q,
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
		tg.push(e2eWizardMessage(text))
		return tg.waitSince(t, m, expect)
	}
	press := func(data, expect string) string {
		t.Helper()
		m := tg.mark()
		tg.push(e2eWizardCallback(data))
		return tg.waitSince(t, m, expect)
	}
	venueName := "Bot hall " + uuid.NewString()[:6]
	eventName := "Бот-концерт " + uuid.NewString()[:6]

	// Step 1 — the event.
	press("wz:new", "Как называется")
	say(strings.Repeat("Ы", 201), "до 200 знаков") // an over-long name is refused, with a plain hint
	say(eventName, "Возраст")
	press("wz:age:16+", "От чьего имени")
	press("wz:prom:org", "афишу")
	press("wz:skip", "Дата сеанса 1")

	// Step 2 — when and where; the venue is created through the API.
	say("32.13.2027", "Не понял дату")
	say("15.12.2027", "Время начала")
	press("wz:default", "В какой стране")
	press("wz:country:"+countryID.String(), "В каком городе")
	press("wz:city:"+cityID.String(), "Где проходит")
	press("wz:venue:new", "Название площадки")
	say(venueName, "Адрес площадки")
	say("Vabaduse väljak 1", "Сколько мест в зале")
	say("120", "Сколько мест продаём") // zone guessed: straight to the session capacity
	press("wz:keep", "Сеанс 1: 15.12.2027 20:00")
	press("wz:next", "Билеты одинаковые")

	// Step 3 — one category with a price change.
	press("wz:mode:single", "Как назвать билет")
	press("wz:default", "Цена билета")
	say("25", "Цена меняется")
	press("wz:yes", "С какой даты")
	say("01.12.2027", "Новая цена с 01.12.2027")
	say("30", "Добавить ещё одно")
	press("wz:done", "Описание ивента")

	// Step 4 — extra; the organization has no sales channel, so none is asked.
	press("wz:skip", "Валюта цен")
	press("wz:cur:EUR", "Открыть продажи сразу")
	summary := press("wz:pub:now", "Проверьте и опубликуйте")
	for _, want := range []string{eventName, venueName, "15.12.2027 20:00", "25 EUR", "120 мест"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lacks %q:\n%s", want, summary)
		}
	}

	// The draft is on disk between questions.
	if row, err := q.GetBotDraft(ctx, e2eWizardTelegramUser, f.orgID); err != nil || row.Step != "summary" {
		t.Fatalf("draft before publish: %+v %v", row, err)
	}

	// Publish: the bot posts the event-bundle as the manager.
	saved := press("wz:publish", "Готово!")
	if !strings.Contains(saved, eventName) || !strings.Contains(saved, "https://tickets.test/") {
		t.Fatalf("saved message: %s", saved)
	}

	var (
		eventID    uuid.UUID
		evName     string
		evStatus   string
		age        *string
		sessionCur string
		venueAddr  *string
		startsAt   time.Time
		capacity   int32
		tierPrice  int64
		tierCur    string
		windows    int
		venueTZ    string
		venueCap   *int32
	)
	if err := pool.QueryRow(ctx, `SELECT e.id, e.name, e.status, e.age_rating FROM events e WHERE e.org_id = $1`, f.orgID).Scan(&eventID, &evName, &evStatus, &age); err != nil {
		t.Fatalf("event row: %v", err)
	}
	if evName != eventName || evStatus != "published" || age == nil || *age != "16+" {
		t.Fatalf("event = %s %s %v", evName, evStatus, age)
	}
	if err := pool.QueryRow(ctx, `SELECT s.start_at, s.currency, s.capacity_total, v.timezone, v.capacity_default, v.address_line1
	        FROM sessions s JOIN venues v ON v.id = s.venue_id WHERE s.event_id = $1`, eventID).Scan(&startsAt, &sessionCur, &capacity, &venueTZ, &venueCap, &venueAddr); err != nil {
		t.Fatalf("session row: %v", err)
	}
	tallinn, _ := time.LoadLocation("Europe/Tallinn")
	if sessionCur != "EUR" || venueTZ != "Europe/Tallinn" || capacity != 120 || venueCap == nil || *venueCap != 120 || venueAddr == nil || *venueAddr != "Vabaduse väljak 1" {
		t.Fatalf("session = %v %s cap=%d venue=%s %v %v", startsAt, sessionCur, capacity, venueTZ, venueCap, venueAddr)
	}
	if got := startsAt.In(tallinn).Format("2006-01-02 15:04"); got != "2027-12-15 20:00" { // allow:timeformat: test fixture comparison
		t.Fatalf("starts_at = %s", got)
	}
	if err := pool.QueryRow(ctx, `SELECT tt.price_amount, tt.currency, (SELECT count(*) FROM ticket_tier_prices w WHERE w.tier_id = tt.id)
	        FROM ticket_tiers tt JOIN sessions s ON s.id = tt.session_id WHERE s.event_id = $1`, eventID).Scan(&tierPrice, &tierCur, &windows); err != nil {
		t.Fatalf("tier row: %v", err)
	}
	if tierPrice != 2500 || tierCur != "EUR" || windows != 1 {
		t.Fatalf("tier = %d %s windows=%d", tierPrice, tierCur, windows)
	}

	// The draft is gone, the answers are remembered for next time.
	if _, err := q.GetBotDraft(ctx, e2eWizardTelegramUser, f.orgID); err == nil {
		t.Fatal("draft must be deleted after publishing")
	}
	link, err := q.GetBotTelegramLink(ctx, e2eWizardTelegramUser)
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	var def eventbot.Defaults
	if err := json.Unmarshal(link.Defaults, &def); err != nil || def.VenueName != venueName || def.Age != "16+" || def.Currency != "EUR" || def.Capacity != 120 {
		t.Fatalf("defaults = %s (%v)", string(link.Defaults), err)
	}

	// The event list is no longer empty (the names are buttons, which the
	// stub does not record; the rows were verified above).
	press("events:1", "Страница 1 из 1")

	// ── Step 4: "Edit" reads the event back from arena, renames it and
	// re-prices the category; a change made elsewhere meanwhile is noticed.
	renamed := eventName + " (ред.)"
	intro := press("wz:edit:"+eventID.String(), "Правим")
	for _, want := range []string{eventName, venueName, "15.12.2027 20:00", "25 EUR", "120 мест"} {
		if !strings.Contains(intro, want) {
			t.Errorf("edit summary lacks %q:\n%s", want, intro)
		}
	}
	press("wz:edit:event", "Как называется")
	say(renamed, "Возраст")
	press("wz:age:18+", "От чьего имени")
	press("wz:prom:org", "афишу")
	press("wz:skip", "Проверьте и опубликуйте")
	press("wz:edit:tickets", "Как назвать билет")
	press("wz:default", "Цена билета")
	say("27,50", "Цена меняется")
	press("wz:no", "Проверьте и опубликуйте")
	// The bot compares updated_at as the API prints it - whole seconds - so
	// a change inside the same second as the load is invisible. On a fast
	// machine the whole edit above fits in one second, which made a plain
	// now() pass unnoticed (CI, 2026-09-29); move it a clear minute ahead.
	if _, err := pool.Exec(ctx, `UPDATE events SET updated_at = now() + interval '1 minute' WHERE id = $1`, eventID); err != nil {
		t.Fatal(err)
	}
	press("wz:publish", "меняли в другом месте")
	press("wz:publish:force", "Сохранено")
	var (
		nameAfter, ageAfter string
		sessionsAfter       int
		tiersAfter          int
		priceAfter          int64
		windowsAfter        int
	)
	if err := pool.QueryRow(ctx, `SELECT e.name, e.age_rating,
	        (SELECT count(*) FROM sessions s WHERE s.event_id = e.id),
	        (SELECT count(*) FROM ticket_tiers tt JOIN sessions s ON s.id = tt.session_id WHERE s.event_id = e.id AND tt.deleted_at IS NULL),
	        (SELECT tt.price_amount FROM ticket_tiers tt JOIN sessions s ON s.id = tt.session_id WHERE s.event_id = e.id LIMIT 1),
	        (SELECT count(*) FROM ticket_tier_prices w JOIN ticket_tiers tt ON tt.id = w.tier_id JOIN sessions s ON s.id = tt.session_id WHERE s.event_id = e.id)
	        FROM events e WHERE e.id = $1`, eventID).Scan(&nameAfter, &ageAfter, &sessionsAfter, &tiersAfter, &priceAfter, &windowsAfter); err != nil {
		t.Fatalf("event after edit: %v", err)
	}
	if nameAfter != renamed || ageAfter != "18+" || sessionsAfter != 1 || tiersAfter != 1 || priceAfter != 2750 || windowsAfter != 0 {
		t.Fatalf("after edit: name=%q age=%q sessions=%d tiers=%d price=%d windows=%d", nameAfter, ageAfter, sessionsAfter, tiersAfter, priceAfter, windowsAfter)
	}
	if _, err := q.GetBotDraft(ctx, e2eWizardTelegramUser, f.orgID); err == nil {
		t.Fatal("edit draft must be deleted after saving")
	}

	// ── "Repeat as new": the tickets travel, the dates are asked, the
	// remembered venue is one button away, and a second event appears.
	press("wz:copy:"+eventID.String(), "Копия")
	say("20.12.2027", "Время начала")
	press("wz:default", "В какой стране")
	press("wz:keep", "В каком городе")
	press("wz:keep", "Где проходит")
	press("wz:keep", "Сколько мест продаём")
	press("wz:keep", "Ещё сеанс")
	copySummary := press("wz:next", "Проверьте и опубликуйте")
	if !strings.Contains(copySummary, "20.12.2027 20:00") || !strings.Contains(copySummary, "27,50 EUR") || !strings.Contains(copySummary, renamed) {
		t.Fatalf("copy summary:\n%s", copySummary)
	}
	press("wz:publish", "Готово!")
	var events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE org_id = $1 AND name = $2`, f.orgID, renamed).Scan(&events); err != nil || events != 2 {
		t.Fatalf("events after copy = %d (%v), want 2", events, err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("bot.Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bot did not stop after cancel")
	}
}

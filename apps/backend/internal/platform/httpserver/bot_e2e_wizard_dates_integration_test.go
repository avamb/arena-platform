//go:build integration

package httpserver

// U-5 (2026-10-15), the live case through the real router and the real
// import: an event made one date per event-bundle call, whose two dates sell
// DIFFERENT categories. Editing it in the bot used to send the first date's
// categories (with the first date's tier ids) for every date: the import
// refused the second date with 422 import.invalid_category, so "Publish
// changes" failed for any such event, and the operator could not give a new
// date its own categories. Now every date carries its own categories, a new
// date asks "the same as the date before, or new ones?", and only the dates
// that changed are posted again.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

const e2eDatesTelegramUser = 795

func e2eDatesMessage(text string) string {
	return fmt.Sprintf(`"message":{"message_id":%d,"date":1700000000,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"Dates","language_code":"ru"},"text":%q}`,
		time.Now().UnixNano()%100000, e2eDatesTelegramUser, e2eDatesTelegramUser, text)
}

func e2eDatesCallback(data string) string {
	return fmt.Sprintf(`"callback_query":{"id":"cb-%d","from":{"id":%d,"is_bot":false,"first_name":"Dates","language_code":"ru"},"chat_instance":"x","data":%q,"message":{"message_id":5,"date":%d,"chat":{"id":%d,"type":"private"},"text":"menu"}}`,
		time.Now().UnixNano()%100000, e2eDatesTelegramUser, data, e2ePressDate(), e2eDatesTelegramUser)
}

func TestBotE2E_WizardPerDateCategories(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	srv.cfg.AppPublicURL = api.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := gen.New(pool)

	t.Cleanup(func() {
		c := context.Background()
		exec := func(label, sql string, args ...any) {
			if _, err := pool.Exec(c, sql, args...); err != nil {
				t.Logf("dates cleanup: %s: %v", label, err)
			}
		}
		exec("links", `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, int64(e2eDatesTelegramUser))
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
		exec("venues", `DELETE FROM venues WHERE org_id = $1`, f.orgID)
		exec("audit", `DELETE FROM audit_events WHERE metadata->>'org_id' = $1`, f.orgID.String())
	})

	managerEmail := f.newEmail("dates")
	if rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"manager","locale":"ru"}`, managerEmail)); rec.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body.String())
	}
	code, _ := f.queuedCode(managerEmail)
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d,"locale":"ru"}`, code, managerEmail, e2eDatesTelegramUser)); rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	var managerID, countryID, cityID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, managerEmail).Scan(&managerID); err != nil {
		t.Fatalf("manager: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2 = 'EE'`).Scan(&countryID); err != nil {
		t.Fatalf("country EE: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM cities WHERE slug = 'tallinn'`).Scan(&cityID); err != nil {
		t.Fatalf("city tallinn: %v", err)
	}

	// The event exists before the bot sees it: two dates, one import each,
	// with different categories — exactly how the live event was made.
	jwt, err := eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience).Mint(managerID)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	client := eventbot.NewArenaClient(api.URL, botTestServiceToken, api.Client())
	venue, err := client.CreateVenue(ctx, jwt, f.orgID, eventbot.VenueCreate{
		CountryID: countryID.String(), CountryISO2: "EE", CityID: cityID.String(),
		Name: "Kids hall " + uuid.NewString()[:6], Capacity: 14, Timezone: "Europe/Tallinn",
	})
	if err != nil {
		t.Fatalf("create venue: %v", err)
	}
	minusOne := -1
	noWindows := []bil24compat.ImportPriceWindow{}
	cat := func(name string, price float64, places int32) bil24compat.ImportSessionCategory {
		return bil24compat.ImportSessionCategory{CategoryPriceName: name, Price: price, Availability: places, NextCategoryIndex: &minusOne, PriceSchedule: &noWindows}
	}
	eventName := "Детский спектакль " + uuid.NewString()[:6]
	suffix := uuid.NewString()[:8]
	date := func(ref, eventID, day string, cats ...bil24compat.ImportSessionCategory) bil24compat.ImportSessionRequest {
		return bil24compat.ImportSessionRequest{
			Source: bil24compat.SourceArena, ExternalRef: ref + suffix,
			Action:       bil24compat.ImportSessionAction{ArenaEventID: eventID, ActionName: eventName, Age: "0+"},
			ActionEvent:  bil24compat.ImportSessionActionEvent{Day: day, Time: "18:00", Currency: "EUR"},
			Venue:        bil24compat.ImportSessionVenue{ArenaVenueID: venue.ID},
			CategoryList: cats, Publish: true,
		}
	}
	res1, err := client.ImportEventBundle(ctx, jwt, f.orgID, date("u5-1-", "", "15.12.2099",
		cat("Ребёнок 4–6 лет + взрослый", 50, 11), cat("Дополнительный детский", 35, 3)))
	if err != nil {
		t.Fatalf("import date 1: %v", err)
	}
	res2, err := client.ImportEventBundle(ctx, jwt, f.orgID, date("u5-2-", res1.EventID, "16.12.2099",
		cat("Ребёнок от 6 лет + взрослый", 50, 11), cat("Дополнительный детский", 35, 3)))
	if err != nil {
		t.Fatalf("import date 2: %v", err)
	}
	if res2.EventID != res1.EventID {
		t.Fatalf("date 2 joined another event: %s vs %s", res2.EventID, res1.EventID)
	}
	eventID := uuid.MustParse(res1.EventID)
	s1, s2 := uuid.MustParse(res1.SessionID), uuid.MustParse(res2.SessionID)

	// snapshot is everything about a date the edit must not touch: its
	// categories as rows and its seat version (a re-import bumps it).
	snapshot := func(session uuid.UUID) string {
		t.Helper()
		var tiers string
		var version int64
		if err := pool.QueryRow(ctx, `SELECT coalesce(string_agg(id::text || ':' || name || ':' || price_amount || ':' || coalesce(capacity, -1)
		        || ':' || is_open || ':' || (deleted_at IS NULL) || ':' || updated_at, '|' ORDER BY sort_order, id), '')
		        FROM ticket_tiers WHERE session_id = $1`, session).Scan(&tiers); err != nil {
			t.Fatalf("tiers of %s: %v", session, err)
		}
		if err := pool.QueryRow(ctx, `SELECT seat_status_version FROM sessions WHERE id = $1`, session).Scan(&version); err != nil {
			t.Fatalf("session %s: %v", session, err)
		}
		return fmt.Sprintf("%s#%d", tiers, version)
	}
	before1, before2 := snapshot(s1), snapshot(s2)

	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	tg := newStubTelegram(t)
	tg.restartGap()
	bot, err := eventbot.New(eventbot.Options{
		Token:             "123:test-token",
		Queries:           q,
		Arena:             client,
		Minter:            eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience),
		Texts:             eventbot.NewTexts(bundle),
		TelegramServerURL: tg.srv.URL,
		HTTPClient:        tg.srv.Client(),
		TicketsBaseURL:    "https://tickets.test",
		MessageRateLimit:  -1,
	})
	if err != nil {
		t.Fatalf("eventbot.New: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()

	say := func(text, expect string) string {
		t.Helper()
		m := tg.mark()
		tg.push(e2eDatesMessage(text))
		return tg.waitSince(t, m, expect)
	}
	press := func(data, expect string) string {
		t.Helper()
		tg.waitButton(e2eDatesTelegramUser, data, 2*time.Second)
		m := tg.mark()
		tg.push(e2eDatesCallback(data))
		return tg.waitSince(t, m, expect)
	}
	sayDate := func(text, iso, expect string) string {
		t.Helper()
		say(text, "Вы написали")
		return press("wz:dc:"+iso, expect)
	}

	// ── The live case: "Another date" with its own two categories.
	card := press("wz:edit:"+eventID.String(), "Правим")
	for _, want := range []string{"У сеансов разные категории", "Ребёнок 4–6 лет + взрослый", "Ребёнок от 6 лет + взрослый"} {
		if !strings.Contains(card, want) {
			t.Errorf("edit card lacks %q:\n%s", want, card)
		}
	}
	press("wz:e:date", "Дата сеанса 3")
	sayDate("18.12.2099", "2099-12-18", "Время начала")
	say("18:00", "Продажа билетов закроется в начале")
	press("wz:se:0", "Во сколько открывается вход")
	press("wz:none", "Сеанс 3 там же")
	ask := press("wz:same", "Категории сеанса 3")
	if !strings.Contains(ask, "16.12.2099 18:00") {
		t.Errorf("the categories question does not name the date before:\n%s", ask)
	}
	press("wz:st:own", "Название категории 1")
	say("Ребёнок 4–9 лет + взрослый", "Цена категории")
	say("50", "Сколько мест в категории")
	say("9", "Добавить ещё")
	press("wz:more", "Название категории 2")
	say("Дополнительный детский", "Цена категории")
	say("35", "Сколько мест в категории")
	say("3", "Добавить ещё")
	// U-5b: the new date is summed up before "one more date?".
	summary := press("wz:done", "Всё верно")
	for _, want := range []string{"Сеанс 3", "18.12.2099 18:00", "Ребёнок 4–9 лет + взрослый"} {
		if !strings.Contains(summary, want) {
			t.Errorf("new date summary lacks %q:\n%s", want, summary)
		}
	}
	press("wz:sc:next", "ещё один сеанс к этому мероприятию")
	press("wz:next", "Правка мероприятия")
	// "Publish changes" shows what goes to the site first; the dates nobody
	// touched are named as unchanged.
	preview := press("wz:publish", "Что уйдёт на сайт")
	for _, want := range []string{"Новая дата 18.12.2099 18:00", "Не меняются: 15.12.2099 18:00, 16.12.2099 18:00"} {
		if !strings.Contains(preview, want) {
			t.Errorf("publish preview lacks %q:\n%s", want, preview)
		}
	}
	press("wz:publish:go", "Сохранено")

	var s3 uuid.UUID
	var cap3 int32
	if err := pool.QueryRow(ctx, `SELECT id, capacity_total FROM sessions WHERE event_id = $1 AND id <> ALL($2) AND deleted_at IS NULL`,
		eventID, []uuid.UUID{s1, s2}).Scan(&s3, &cap3); err != nil {
		t.Fatalf("date 3: %v", err)
	}
	var tiers3 string
	if err := pool.QueryRow(ctx, `SELECT string_agg(name || ':' || price_amount || ':' || coalesce(capacity, -1) || ':' || is_open, '|' ORDER BY sort_order)
	        FROM ticket_tiers WHERE session_id = $1 AND deleted_at IS NULL`, s3).Scan(&tiers3); err != nil {
		t.Fatalf("date 3 tiers: %v", err)
	}
	if want := "Ребёнок 4–9 лет + взрослый:5000:9:true|Дополнительный детский:3500:3:true"; tiers3 != want || cap3 != 12 {
		t.Fatalf("date 3: tiers %q capacity %d, want %q and 12", tiers3, cap3, want)
	}
	if got := snapshot(s1); got != before1 {
		t.Fatalf("date 1 was touched:\nbefore %s\nafter  %s", before1, got)
	}
	if got := snapshot(s2); got != before2 {
		t.Fatalf("date 2 was touched:\nbefore %s\nafter  %s", before2, got)
	}

	// ── U-5b, the live case of 2026-10-15: a date added by mistake is taken
	// back on the card, and so is a changed age. Nothing is left to publish
	// and nothing reaches arena.
	before3 := snapshot(s3)
	lastButtons := func() []string {
		t.Helper()
		_, _, data := tg.lastOf(e2eDatesTelegramUser)
		return data
	}
	hasButton := func(data []string, want string) bool {
		for _, d := range data {
			if d == want {
				return true
			}
		}
		return false
	}
	press("wz:edit:"+eventID.String(), "Правим")
	press("wz:e:date", "Дата сеанса 4")
	sayDate("19.12.2099", "2099-12-19", "Время начала")
	say("18:00", "Продажа билетов закроется в начале")
	press("wz:se:0", "Во сколько открывается вход")
	press("wz:none", "Сеанс 4 там же")
	press("wz:same", "Категории сеанса 4")
	if s := press("wz:st:same", "Всё верно"); !strings.Contains(s, "как на сеансе 18.12.2099 18:00") {
		t.Errorf("copied date summary:\n%s", s)
	}
	press("wz:sc:next", "ещё один сеанс к этому мероприятию")
	card = press("wz:next", "Правка мероприятия")
	if !strings.Contains(card, "Новая дата 19.12.2099 18:00") || !hasButton(lastButtons(), "wz:e:undo:date:4") || !hasButton(lastButtons(), "wz:publish") {
		t.Fatalf("card with a new date: buttons %v\n%s", lastButtons(), card)
	}
	press("wz:e:undo:date:4", "Отменено: новая дата 19.12.2099 18:00")
	if data := lastButtons(); hasButton(data, "wz:publish") || hasPrefixData(data, "wz:e:undo:") {
		t.Fatalf("the date is taken back, yet the card offers %v", data)
	}
	press("wz:e:age", "Сейчас")
	press("wz:age:6+", "Возраст: 0+ → 6+")
	if data := lastButtons(); !hasButton(data, "wz:e:undo:age") || !hasButton(data, "wz:publish") {
		t.Fatalf("card after an age change offers %v", data)
	}
	press("wz:e:undo:age", "Отменено: Возраст")
	if data := lastButtons(); hasButton(data, "wz:publish") || hasPrefixData(data, "wz:e:undo:") {
		t.Fatalf("the age is taken back, yet the card offers %v", data)
	}
	var datesNow int
	var ageNow string
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM sessions WHERE event_id = $1 AND deleted_at IS NULL), coalesce(age_rating, '')
	        FROM events WHERE id = $1`, eventID).Scan(&datesNow, &ageNow); err != nil {
		t.Fatalf("event after undo: %v", err)
	}
	if datesNow != 3 || ageNow != "0+" {
		t.Fatalf("undo reached arena: %d dates, age %q", datesNow, ageNow)
	}
	for i, pair := range [][2]string{{before1, snapshot(s1)}, {before2, snapshot(s2)}, {before3, snapshot(s3)}} {
		if pair[0] != pair[1] {
			t.Fatalf("date %d was touched by an undone edit:\nbefore %s\nafter  %s", i+1, pair[0], pair[1])
		}
	}

	// ── Changing date 1's categories leaves date 2 alone.
	press("wz:edit:"+eventID.String(), "Правим")
	press("wz:e:tickets", "Выберите сеанс")
	press("wz:td:1", "Название категории 1")
	say("Ребёнок 4–7 лет + взрослый", "Цена категории")
	say("50", "Сколько мест в категории")
	say("11", "Добавить ещё")
	press("wz:more", "Название категории 2")
	say("Дополнительный детский", "Цена категории")
	say("40", "Сколько мест в категории")
	say("3", "Добавить ещё")
	press("wz:done", "Правка мероприятия")
	// A renamed category is a new one: the preview says the old one closes.
	preview = press("wz:publish", "Что уйдёт на сайт")
	for _, want := range []string{"категория «Ребёнок 4–6 лет + взрослый» будет закрыта", "будет создана «Ребёнок 4–7 лет + взрослый»",
		"Не меняются: 16.12.2099 18:00, 18.12.2099 18:00"} {
		if !strings.Contains(preview, want) {
			t.Errorf("rename preview lacks %q:\n%s", want, preview)
		}
	}
	press("wz:publish:go", "Сохранено")

	if got := snapshot(s2); got != before2 {
		t.Fatalf("date 2 was touched by date 1's change:\nbefore %s\nafter  %s", before2, got)
	}
	var renamed, extraPrice int
	if err := pool.QueryRow(ctx, `SELECT
	        (SELECT count(*) FROM ticket_tiers WHERE session_id = $1 AND name = 'Ребёнок 4–7 лет + взрослый' AND deleted_at IS NULL),
	        (SELECT price_amount FROM ticket_tiers WHERE session_id = $1 AND name = 'Дополнительный детский' AND deleted_at IS NULL)`,
		s1).Scan(&renamed, &extraPrice); err != nil {
		t.Fatalf("date 1 after the change: %v", err)
	}
	if renamed != 1 || extraPrice != 4000 {
		t.Fatalf("date 1 after the change: renamed=%d extra price=%d", renamed, extraPrice)
	}
	var otherDates int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ticket_tiers WHERE session_id IN ($1, $2) AND name = 'Ребёнок 4–7 лет + взрослый'`, s2, s3).Scan(&otherDates); err != nil || otherDates != 0 {
		t.Fatalf("date 1's new category leaked to other dates: %d %v", otherDates, err)
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

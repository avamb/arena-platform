//go:build integration

package httpserver

// Review of PR #357 (2026-11-02): an edit that posts several dates moves the
// event's updated_at with every date it writes. When the second date failed,
// «Retry» used to compare the event with the mark the draft was LOADED with
// and reported the bot's own first date as "changed elsewhere". The import
// now answers event_updated_at and the bot keeps it as its mark, cut to the
// second like GET /v1/events/{id} prints it. This walks it through the real
// router and the real import; only the second post of a save is refused, by
// a proxy in front of the router.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

const e2ePartialTelegramUser = 797

func e2ePartialMessage(text string) string {
	return fmt.Sprintf(`"message":{"message_id":%d,"date":1700000000,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"Partial","language_code":"ru"},"text":%q}`,
		time.Now().UnixNano()%100000, e2ePartialTelegramUser, e2ePartialTelegramUser, text)
}

func e2ePartialCallback(data string) string {
	return fmt.Sprintf(`"callback_query":{"id":"cb-%d","from":{"id":%d,"is_bot":false,"first_name":"Partial","language_code":"ru"},"chat_instance":"x","data":%q,"message":{"message_id":5,"date":%d,"chat":{"id":%d,"type":"private"},"text":"menu"}}`,
		time.Now().UnixNano()%100000, e2ePartialTelegramUser, data, e2ePressDate(), e2ePartialTelegramUser)
}

// importFailer forwards every request to the real router, except that while
// armed it refuses the failAt-th event-bundle import with a 500.
type importFailer struct {
	next   http.Handler
	mu     sync.Mutex
	failAt int
	seen   int
}

func (f *importFailer) arm(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAt, f.seen = n, 0
}

func (f *importFailer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/imports/event-bundle") {
		f.mu.Lock()
		f.seen++
		refuse := f.failAt > 0 && f.seen == f.failAt
		f.mu.Unlock()
		if refuse {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"the second date failed"}}`))
			return
		}
	}
	f.next.ServeHTTP(w, r)
}

func TestBotE2E_WizardRetryAfterAPartialSave(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	failer := &importFailer{next: srv.router}
	proxy := httptest.NewServer(failer)
	defer proxy.Close()
	srv.cfg.AppPublicURL = api.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := gen.New(pool)

	t.Cleanup(func() {
		c := context.Background()
		exec := func(label, sql string, args ...any) {
			if _, err := pool.Exec(c, sql, args...); err != nil {
				t.Logf("partial cleanup: %s: %v", label, err)
			}
		}
		exec("links", `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, int64(e2ePartialTelegramUser))
		exec("drafts", `DELETE FROM bot_drafts WHERE telegram_user_id = $1`, int64(e2ePartialTelegramUser))
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

	managerEmail := f.newEmail("partial")
	if rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"manager","locale":"ru"}`, managerEmail)); rec.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body.String())
	}
	code, _ := f.queuedCode(managerEmail)
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d,"locale":"ru"}`, code, managerEmail, e2ePartialTelegramUser)); rec.Code != http.StatusOK {
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

	// The event exists before the bot sees it: one date, one category.
	jwt, err := eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience).Mint(managerID)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	direct := eventbot.NewArenaClient(api.URL, botTestServiceToken, api.Client())
	venue, err := direct.CreateVenue(ctx, jwt, f.orgID, eventbot.VenueCreate{
		CountryID: countryID.String(), CountryISO2: "EE", CityID: cityID.String(),
		Name: "Partial hall " + uuid.NewString()[:6], Capacity: 40, Timezone: "Europe/Tallinn",
	})
	if err != nil {
		t.Fatalf("create venue: %v", err)
	}
	minusOne := -1
	noWindows := []bil24compat.ImportPriceWindow{}
	eventName := "Частичное сохранение " + uuid.NewString()[:6]
	res, err := direct.ImportEventBundle(ctx, jwt, f.orgID, bil24compat.ImportSessionRequest{
		Source: bil24compat.SourceArena, ExternalRef: "partial-" + uuid.NewString()[:8],
		Action:      bil24compat.ImportSessionAction{ActionName: eventName, Age: "0+"},
		ActionEvent: bil24compat.ImportSessionActionEvent{Day: "15.12.2099", Time: "18:00", Currency: "EUR"},
		Venue:       bil24compat.ImportSessionVenue{ArenaVenueID: venue.ID},
		CategoryList: []bil24compat.ImportSessionCategory{{CategoryPriceName: "Вход", Price: 20, Availability: 40,
			NextCategoryIndex: &minusOne, PriceSchedule: &noWindows}},
		Publish: true,
	})
	if err != nil {
		t.Fatalf("import the event: %v", err)
	}
	eventID := uuid.MustParse(res.EventID)

	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	tg := newStubTelegram(t)
	tg.restartGap()
	bot, err := eventbot.New(eventbot.Options{
		Token:             "123:test-token",
		Queries:           q,
		Arena:             eventbot.NewArenaClient(proxy.URL, botTestServiceToken, proxy.Client()),
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
		tg.push(e2ePartialMessage(text))
		return tg.waitSince(t, m, expect)
	}
	press := func(data, expect string) string {
		t.Helper()
		tg.waitButton(e2ePartialTelegramUser, data, 2*time.Second)
		m := tg.mark()
		tg.push(e2ePartialCallback(data))
		return tg.waitSince(t, m, expect)
	}
	addDate := func(n int, day, iso string) {
		t.Helper()
		press("wz:e:date", fmt.Sprintf("Дата сеанса %d", n))
		say(day, "Вы написали")
		press("wz:dc:"+iso, "Время начала")
		say("18:00", "Продажа билетов закроется в начале")
		press("wz:se:0", "Во сколько открывается вход")
		press("wz:none", fmt.Sprintf("Сеанс %d там же", n))
		press("wz:same", fmt.Sprintf("Категории сеанса %d", n))
		press("wz:st:same", "Всё верно")
		press("wz:sc:next", "ещё один сеанс к этому мероприятию")
		press("wz:next", "Правка мероприятия")
	}
	// partialSave changes the age, adds two dates and saves with the second
	// post refused: the first one is on the site, the rest is not.
	partialSave := func(age string, first int, days [2][2]string) {
		t.Helper()
		press("wz:edit:"+eventID.String(), "Правим")
		press("wz:e:age", "Сейчас")
		press("wz:age:"+age, "Возраст")
		addDate(first, days[0][0], days[0][1])
		addDate(first+1, days[1][0], days[1][1])
		press("wz:publish", "Что уйдёт на сайт")
		failer.arm(2)
		press("wz:publish:go", "не сохранён")
		failer.arm(0)
	}
	dates := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE event_id = $1 AND deleted_at IS NULL`, eventID).Scan(&n); err != nil {
			t.Fatalf("count dates: %v", err)
		}
		return n
	}

	// ── 1: the bot's own first post is no edit made elsewhere. «Retry» sends
	// what is left and says nothing about a change made elsewhere.
	partialSave("6+", 2, [2][2]string{{"17.12.2099", "2099-12-17"}, {"18.12.2099", "2099-12-18"}})
	if got := dates(); got != 2 {
		t.Fatalf("after the partial save: %d dates, want 2 (one written, one refused)", got)
	}
	m := tg.mark()
	tg.waitButton(e2ePartialTelegramUser, "wz:retry", 2*time.Second)
	tg.push(e2ePartialCallback("wz:retry"))
	tg.waitSince(t, m, "Сохранено")
	tg.mu.Lock()
	since := append([]string(nil), tg.sent[m:]...)
	tg.mu.Unlock()
	for _, msg := range since {
		if strings.Contains(msg, "меняли в другом месте") {
			t.Fatalf("«Retry» reported the bot's own write as an edit made elsewhere:\n%s", msg)
		}
	}
	if got := dates(); got != 3 {
		t.Fatalf("after «Retry»: %d dates, want 3", got)
	}

	// ── 2: an edit made by somebody else AFTER a partial save is still caught.
	partialSave("12+", 4, [2][2]string{{"19.12.2099", "2099-12-19"}, {"20.12.2099", "2099-12-20"}})
	const outside = "Переименовали между попытками"
	if _, err := pool.Exec(ctx, `UPDATE events SET name = $2, updated_at = now() + interval '1 minute' WHERE id = $1`, eventID, outside); err != nil {
		t.Fatalf("outside edit: %v", err)
	}
	warning := press("wz:retry", "меняли в другом месте")
	if !strings.Contains(warning, outside) {
		t.Errorf("the warning does not name the outside rename:\n%s", warning)
	}
	if strings.Contains(warning, "Возраст") {
		t.Errorf("the warning lists the age the bot itself already sent:\n%s", warning)
	}
	_, _, data := tg.lastOf(e2ePartialTelegramUser)
	if !hasData(data, "wz:publish:force") || !hasData(data, "wz:cancel") {
		t.Errorf("the warning offers %v, want «Save anyway» and «Cancel»", data)
	}
	var name string
	if err := pool.QueryRow(ctx, `SELECT name FROM events WHERE id = $1`, eventID).Scan(&name); err != nil {
		t.Fatalf("event name: %v", err)
	}
	if name != outside || dates() != 4 {
		t.Fatalf("the refused retry wrote: name %q, %d dates", name, dates())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("bot did not stop")
	}
}

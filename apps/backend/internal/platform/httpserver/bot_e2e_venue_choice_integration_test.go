//go:build integration

package httpserver

// End-to-end proof of the venue choice in the Telegram bot (spec 64 section
// 2.1.1): the wizard picks a country (quick pick, regions, a typed name), a
// city and a venue from the venue directory, and a country outside the arena
// reference is created together with its first venue (and with an approved
// organizer application). The real router, the real bot, only Telegram
// stubbed. Run against a FRESH migrated and seeded database (recipe in
// docs/agents/testing-integration.md):
//
//	DATABASE_URL=... go test -tags integration ./apps/backend/internal/platform/httpserver/ -run BotE2E_VenueChoice -p 1

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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// The countries of these tests are outside the 0127 seed. Each test owns its own
// so a rerun, or a neighbour, never sees a country another test made.
const (
	vcWizardUser    = int64(8201)
	vcNewCountryUsr = int64(8202)
	vcTypedUser     = int64(8203)
	vcOnboardUser   = int64(8204)
	vcWizardCountry = "LK" // Sri Lanka: made by a venue
	vcOnboardISO    = "NP" // Nepal: made by an approved application
)

// vcRig is the bot of one test: a manager of a fresh organization, linked to a
// Telegram account, talking to the real API through the stub Telegram.
type vcRig struct {
	t      *testing.T
	f      *botInviteFixture
	srv    *Server
	api    *httptest.Server
	tg     *stubTelegram
	user   int64
	jwt    string
	client *eventbot.ArenaClient
	cancel context.CancelFunc
	done   chan error
}

func (r *vcRig) say(text, expect string) string {
	r.t.Helper()
	m := r.tg.mark()
	r.tg.push(e2eMessageAs(r.user, text))
	return r.tg.waitSince(r.t, m, expect)
}

func (r *vcRig) press(data, expect string) string {
	r.t.Helper()
	r.tg.waitButton(r.user, data, 2*time.Second)
	m := r.tg.mark()
	r.tg.push(e2eCallbackAs(r.user, data))
	return r.tg.waitSince(r.t, m, expect)
}

func (r *vcRig) sayDate(text, iso, expect string) string {
	r.t.Helper()
	r.say(text, "Вы написали")
	return r.press("wz:dc:"+iso, expect)
}

// hasButton reports whether the newest keyboard of the chat carries the data.
func (r *vcRig) hasButton(data string) bool {
	_, _, keys := r.tg.lastOf(r.user)
	return hasData(keys, data)
}

// pageTo presses the next pages of a country list until the button is on screen.
func (r *vcRig) pageTo(pagePrefix, button string) {
	r.t.Helper()
	for page := 1; page <= 12; page++ {
		if r.tg.waitButton(r.user, button, 400*time.Millisecond) {
			return
		}
		r.tg.push(e2eCallbackAs(r.user, fmt.Sprintf("%s%d", pagePrefix, page+1)))
		time.Sleep(300 * time.Millisecond)
	}
	r.t.Fatalf("the button %q never appeared in the pages of the list", button)
}

func newVCRig(t *testing.T, user int64, label string, countries ...string) *vcRig {
	t.Helper()
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	// The test server speaks English only unless told otherwise; the world list
	// names the countries in Russian for a Russian bot, as production does.
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) { c.ActiveLocales = []string{"en", "ru"} })
	api := httptest.NewServer(srv.router)
	t.Cleanup(api.Close)
	srv.cfg.AppPublicURL = api.URL
	ctx, cancel := context.WithCancel(context.Background())
	q := gen.New(pool)

	// Everything a test makes hangs off the organization; sweep it in FK order,
	// then the countries and cities a venue created. The fixture removes the
	// organization itself last.
	t.Cleanup(func() {
		c := context.Background()
		exec := func(what, sql string, args ...any) {
			if _, err := pool.Exec(c, sql, args...); err != nil {
				t.Logf("venue choice cleanup: %s: %v", what, err)
			}
		}
		exec("links", `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, user)
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
		exec("notify_jobs", `DELETE FROM worker_jobs WHERE job_type = 'venue_plan.notify' AND (payload->>'venue_id' IN (SELECT id::text FROM venues WHERE org_id = $1) OR payload->>'country_iso2' = ANY($2))`, f.orgID, countries)
		exec("notify_log", `DELETE FROM venue_plan_notify_log WHERE subject_id IN (SELECT id FROM venues WHERE org_id = $1) OR subject_id IN (SELECT id FROM countries WHERE iso2 = ANY($2))`, f.orgID, countries)
		exec("venues", `DELETE FROM venues WHERE org_id = $1`, f.orgID)
		exec("country audit", `DELETE FROM audit_events WHERE resource_type = 'country' AND resource_id IN (SELECT id::text FROM countries WHERE iso2 = ANY($1))`, countries)
		exec("city names", `DELETE FROM i18n_text WHERE namespace = 'geo.cities' AND key IN (SELECT slug FROM cities WHERE country_id IN (SELECT id FROM countries WHERE iso2 = ANY($1)))`, countries)
		exec("cities", `DELETE FROM cities WHERE country_id IN (SELECT id FROM countries WHERE iso2 = ANY($1))`, countries)
		exec("country names", `DELETE FROM i18n_text WHERE namespace = 'geo.countries' AND key = ANY($1)`, countries)
		exec("countries", `DELETE FROM countries WHERE iso2 = ANY($1)`, countries)
		exec("audit", `DELETE FROM audit_events WHERE metadata->>'org_id' = $1`, f.orgID.String())
	})

	managerEmail := f.newEmail(label)
	if rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"manager","locale":"ru"}`, managerEmail)); rec.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body.String())
	}
	code, _ := f.queuedCode(managerEmail)
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d,"locale":"ru"}`, code, managerEmail, user)); rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	var managerID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, managerEmail).Scan(&managerID); err != nil {
		t.Fatalf("manager: %v", err)
	}
	minter := eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience)
	jwt, err := minter.Mint(managerID)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	client := eventbot.NewArenaClient(api.URL, botTestServiceToken, api.Client())

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
		Minter:            minter,
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
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Log("bot did not stop after cancel")
		}
	})
	return &vcRig{t: t, f: f, srv: srv, api: api, tg: tg, user: user, jwt: jwt, client: client, cancel: cancel, done: done}
}

// head walks the event's name, age, promoter and poster, then the first date's
// day, start time, sales end and doors: the next question is the country.
func (r *vcRig) head(eventName string) {
	r.t.Helper()
	r.press("wz:new", "Как называется")
	r.say(eventName, "Возраст")
	r.press("wz:age:16+", "Под каким брендом")
	r.press("wz:prom:org", "афишу")
	r.press("wz:skip", "Дата сеанса 1")
	r.sayDate("15.12.2099", "2099-12-15", "Время начала")
	r.press("wz:default", "Продажа билетов закроется в начале")
	r.press("wz:se:60", "Во сколько открывается вход")
	r.press("wz:dr:30", "В какой части мира")
}

// finish walks the ticket, extra and publish questions of a one-category event
// and publishes it.
func (r *vcRig) finish() {
	r.t.Helper()
	r.press("wz:next", "Билеты одинаковые")
	r.press("wz:mode:single", "Как назвать билет")
	r.press("wz:default", "Цена билета")
	r.say("25", "Цена меняется")
	r.press("wz:no", "Описание мероприятия")
	r.press("wz:skip", "Валюта цен")
	r.press("wz:cur:EUR", "Открыть продажи сразу")
	r.press("wz:pub:now", "Проверьте и опубликуйте")
	r.press("wz:publish", "Готово!")
}

func (r *vcRig) count(sql string, args ...any) int {
	r.t.Helper()
	return r.f.count(sql, args...)
}

// (a) quick pick -> an existing country -> a city from the directory -> a venue
// from the directory; the event is saved at that venue.
func TestBotE2E_VenueChoice_QuickPickCityVenueFromDirectory(t *testing.T) {
	r := newVCRig(t, vcWizardUser, "vc-dir")
	ctx := context.Background()
	pool := r.f.pool

	var countryID, cityID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2 = 'EE'`).Scan(&countryID); err != nil {
		t.Fatalf("country EE: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM cities WHERE slug = 'tallinn'`).Scan(&cityID); err != nil {
		t.Fatalf("city tallinn: %v", err)
	}
	// The organization already has a venue in Estonia: Estonia is in the quick pick,
	// Tallinn leads the city list with its venue count.
	venueName := "Directory hall " + uuid.NewString()[:6]
	venue, err := r.client.CreateVenue(ctx, r.jwt, r.f.orgID, eventbot.VenueCreate{
		CountryID: countryID.String(), CountryISO2: "EE", CityID: cityID.String(),
		Name: venueName, Capacity: 90, Timezone: "Europe/Tallinn",
	})
	if err != nil {
		t.Fatalf("create venue: %v", err)
	}
	eventName := "Каталог площадок " + uuid.NewString()[:6]

	r.head(eventName)
	if !r.hasButton("wz:country:EE") || !r.hasButton("wz:region:europe") {
		_, _, keys := r.tg.lastOf(r.user)
		t.Fatalf("the quick pick must hold Estonia above the regions: %v", keys)
	}
	r.press("wz:country:EE", "В каком городе")
	if !r.hasButton("wz:city:" + cityID.String()) {
		_, _, keys := r.tg.lastOf(r.user)
		t.Fatalf("the directory's city is missing: %v", keys)
	}
	r.press("wz:city:"+cityID.String(), "Где проходит")
	if !r.hasButton("wz:venue:"+venue.ID) || !r.hasButton("wz:venue:new") {
		_, _, keys := r.tg.lastOf(r.user)
		t.Fatalf("the directory's venue is missing: %v", keys)
	}
	// Typed text searches the city's venues by name.
	r.say("directory", "в названии")
	if !r.hasButton("wz:venue:"+venue.ID) || !r.hasButton("wz:q:clear") {
		_, _, keys := r.tg.lastOf(r.user)
		t.Fatalf("the search must find the venue and offer to show all: %v", keys)
	}
	r.press("wz:venue:"+venue.ID, "нет схемы зала")
	r.press("wz:plan:skip", "Сколько мест продаём")
	r.press("wz:keep", "ещё один сеанс")
	r.finish()

	var sessionVenue uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT s.venue_id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1 AND e.name = $2`, r.f.orgID, eventName).Scan(&sessionVenue); err != nil {
		t.Fatalf("session row: %v", err)
	}
	if sessionVenue.String() != venue.ID {
		t.Fatalf("the event is at venue %s, want %s", sessionVenue, venue.ID)
	}
	if n := r.count(`SELECT count(*) FROM venues WHERE org_id = $1`, r.f.orgID); n != 1 {
		t.Fatalf("the organization has %d venues, want the one it had", n)
	}
}

// (b) a region -> a country outside the reference -> a typed city -> a new venue:
// the country, the city and the venue exist after it, the session is saved
// there, and exactly one country_created job was queued.
func TestBotE2E_VenueChoice_CountryOutsideReferenceNewVenue(t *testing.T) {
	r := newVCRig(t, vcNewCountryUsr, "vc-new", vcWizardCountry)
	ctx := context.Background()
	pool := r.f.pool
	if n := r.count(`SELECT count(*) FROM countries WHERE iso2 = $1`, vcWizardCountry); n != 0 {
		t.Fatalf("%s is in the reference of this database already (%d): pick another country", vcWizardCountry, n)
	}
	eventName := "Новая страна " + uuid.NewString()[:6]
	venueName := "Colombo hall " + uuid.NewString()[:6]

	r.head(eventName)
	r.press("wz:region:asia", "В какой стране")
	r.pageTo("wz:page:", "wz:country:"+vcWizardCountry)
	r.press("wz:country:"+vcWizardCountry, "на платформе ещё нет")
	r.say("Colombo", "пока нет площадок")
	// The city only waits in the draft: nothing was created yet.
	if n := r.count(`SELECT count(*) FROM countries WHERE iso2 = $1`, vcWizardCountry); n != 0 {
		t.Fatalf("a country was created before the venue: %d rows", n)
	}
	r.press("wz:venue:new", "Название площадки")
	r.say(venueName, "Адрес площадки")
	r.press("wz:skip", "Сколько мест продаём")
	r.say("150", "Часовой пояс")
	r.press("wz:tz:Asia/Colombo", "нет схемы зала")
	r.press("wz:plan:skip", "Сеанс 1: 15.12.2099 20:00")
	r.finish()

	var countryID uuid.UUID
	var currency, region string
	if err := pool.QueryRow(ctx, `SELECT id, btrim(currency), region FROM countries WHERE iso2 = $1`, vcWizardCountry).Scan(&countryID, &currency, &region); err != nil {
		t.Fatalf("the country row: %v", err)
	}
	if currency != "LKR" || region != "asia" {
		t.Fatalf("country row = %s %s", currency, region)
	}
	var venueID, venueCity, sessionVenue uuid.UUID
	var tz string
	if err := pool.QueryRow(ctx, `SELECT v.id, v.city_id, v.timezone FROM venues v WHERE v.org_id = $1 AND v.name = $2`, r.f.orgID, venueName).Scan(&venueID, &venueCity, &tz); err != nil {
		t.Fatalf("the venue row: %v", err)
	}
	if n := r.count(`SELECT count(*) FROM cities WHERE id = $1 AND country_id = $2`, venueCity, countryID); n != 1 {
		t.Fatalf("the venue's city is not a city of the new country")
	}
	if n := r.count(`SELECT count(*) FROM cities WHERE country_id = $1`, countryID); n != 1 {
		t.Fatalf("the new country has %d cities, want 1", n)
	}
	if err := pool.QueryRow(ctx, `SELECT s.venue_id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1 AND e.name = $2`, r.f.orgID, eventName).Scan(&sessionVenue); err != nil || sessionVenue != venueID {
		t.Fatalf("the session is at venue %s (%v), want %s", sessionVenue, err, venueID)
	}
	if tz != "Asia/Colombo" {
		t.Fatalf("venue time zone = %s", tz)
	}
	if n := r.count(`SELECT count(*) FROM worker_jobs WHERE job_type = 'venue_plan.notify' AND payload->>'kind' = 'country_created' AND payload->>'country_iso2' = $1`, vcWizardCountry); n != 1 {
		t.Fatalf("country_created jobs = %d, want exactly 1", n)
	}
}

// (c) a typed country name offers buttons, a name nobody has says so and keeps
// the screen, and picking a match goes on to the city.
func TestBotE2E_VenueChoice_TypedCountryNameOffersMatches(t *testing.T) {
	r := newVCRig(t, vcTypedUser, "vc-typed")
	r.head("Поиск страны " + uuid.NewString()[:6])
	matches := r.say("эстон", "Нашлись такие страны")
	_ = matches
	if !r.hasButton("wz:country:EE") {
		_, _, keys := r.tg.lastOf(r.user)
		t.Fatalf("Estonia must be offered for \"эстон\": %v", keys)
	}
	r.say("Atlantis", "в списке нет")
	// The screen is kept: a region still opens, a typed English name works.
	r.say("Estonia", "Нашлись такие страны")
	r.press("wz:country:EE", "В каком городе")
}

// (d) the organizer application through the bot: the applicant picks a country
// outside the reference by region, the application stores its code, and an
// approval by a superadmin creates the country row.
func TestBotE2E_VenueChoice_OnboardingCountryByRegion(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) {
		c.OnboardingEnabled = true
		c.OnboardingSiteURL = "https://site.bot.test"
		c.ActiveLocales = []string{"en", "ru"}
	})
	api := httptest.NewServer(srv.router)
	defer api.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if n := func() int {
		var n int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM countries WHERE iso2 = $1`, vcOnboardISO).Scan(&n)
		return n
	}(); n != 0 {
		t.Fatalf("%s is in the reference of this database already: pick another country", vcOnboardISO)
	}

	tgUser := vcOnboardUser
	email := fmt.Sprintf("onb-geo-%d@arena-integration.test", time.Now().UnixNano())
	superEmail := "super-" + email
	t.Cleanup(func() {
		bg := context.Background()
		var orgID *uuid.UUID
		_ = pool.QueryRow(bg, `SELECT org_id FROM onboarding_applications WHERE applicant_email = $1 AND org_id IS NOT NULL`, email).Scan(&orgID)
		_, _ = pool.Exec(bg, `DELETE FROM worker_jobs WHERE payload->>'email' = $1`, email)
		_, _ = pool.Exec(bg, `DELETE FROM worker_jobs WHERE job_type = 'onboarding.notify' AND payload->>'application_id' IN (SELECT id::text FROM onboarding_applications WHERE applicant_email = $1)`, email)
		_, _ = pool.Exec(bg, `DELETE FROM worker_jobs WHERE job_type = 'venue_plan.notify' AND payload->>'country_iso2' = $1`, vcOnboardISO)
		_, _ = pool.Exec(bg, `DELETE FROM venue_plan_notify_log WHERE subject_id IN (SELECT id FROM countries WHERE iso2 = $1)`, vcOnboardISO)
		_, _ = pool.Exec(bg, `DELETE FROM onboarding_applications WHERE applicant_email = $1`, email)
		if orgID != nil {
			_, _ = pool.Exec(bg, `DELETE FROM audit_events WHERE metadata->>'org_id' = $1`, orgID.String())
			_, _ = pool.Exec(bg, `DELETE FROM memberships WHERE org_id = $1`, *orgID)
			_, _ = pool.Exec(bg, `DELETE FROM sales_channels WHERE org_id = $1`, *orgID)
			_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, *orgID)
		}
		_, _ = pool.Exec(bg, `DELETE FROM audit_events WHERE resource_type = 'country' AND resource_id IN (SELECT id::text FROM countries WHERE iso2 = $1)`, vcOnboardISO)
		_, _ = pool.Exec(bg, `DELETE FROM i18n_text WHERE namespace = 'geo.countries' AND key = $1`, vcOnboardISO)
		_, _ = pool.Exec(bg, `DELETE FROM countries WHERE iso2 = $1`, vcOnboardISO)
		_, _ = pool.Exec(bg, `DELETE FROM password_reset_tokens WHERE user_id IN (SELECT id FROM users WHERE email = ANY($1))`, []string{email, superEmail})
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE email = ANY($1)`, []string{email, superEmail})
	})

	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	tg := newStubTelegram(t)
	tg.restartGap()
	bot, err := eventbot.New(eventbot.Options{
		Token:             "123:test-token",
		Queries:           gen.New(pool),
		Arena:             eventbot.NewArenaClient(api.URL, botTestServiceToken, api.Client()),
		Minter:            eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience),
		Texts:             eventbot.NewTexts(bundle),
		TelegramServerURL: tg.srv.URL,
		HTTPClient:        tg.srv.Client(),
		SelfOnboarding:    true,
		NoticeEvery:       200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("eventbot.New: %v", err)
	}
	go func() { _ = bot.Run(ctx) }()

	say := func(update, needle string) string {
		t.Helper()
		m := tg.mark()
		tg.push(update)
		return tg.waitSince(t, m, needle)
	}
	say(e2eMessageAs(tgUser, "/start apply"), "Как вас зовут")
	say(e2eMessageAs(tgUser, "Ана"), "фамилия")
	say(e2eMessageAs(tgUser, "Перес"), "Поделитесь номером")
	say(e2eContactAs(tgUser, tgUser, "34600111333"), "Ваш e-mail")
	say(e2eMessageAs(tgUser, email), "6-значный код")

	var appID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM onboarding_applications WHERE applicant_email = $1`, email).Scan(&appID); err != nil {
		t.Fatalf("application row: %v", err)
	}
	prefill := `{"telegram_user_id":8204,"answers":{"legal_name":"Geo Events Ltd","tax_id":"ESB1234567","tax_id_scheme":"vat",
"address_line1":"Calle 1","address_postal_code":"28001","address_city":"Kathmandu","events_per_year":"1-5",
"tickets_per_year":"<500","payment_provider":"stripe"}}`
	if code, body := onbDo(t, api.Client(), http.MethodPut, api.URL+"/v1/bot/onboarding/applications/"+appID+"/answers", botTestServiceToken, prefill, nil); code != http.StatusOK {
		t.Fatalf("prefill: %d %s", code, body)
	}
	var code string
	if err := pool.QueryRow(ctx, `SELECT payload->>'code' FROM worker_jobs WHERE job_type = 'onboarding.email' AND payload->>'email' = $1 ORDER BY created_at DESC LIMIT 1`, email).Scan(&code); err != nil || len(code) != 6 {
		t.Fatalf("queued code %q: %v", code, err)
	}
	say(e2eMessageAs(tgUser, code), "готово на")
	say(e2eMessageAs(tgUser, "Geo Events"), "Страна")

	// The country question: the quick pick and the regions, no 250-button list.
	pressCountry := func(data, expect string) string {
		t.Helper()
		tg.waitButton(tgUser, data, 2*time.Second)
		return say(e2eCallbackAs(tgUser, data), expect)
	}
	_, _, keys := tg.lastOf(tgUser)
	if !hasData(keys, "onb:cr:country:asia") || len(keys) > 20 {
		t.Fatalf("the country question must offer the regions, not every country: %v", keys)
	}
	// A typed name offers its matches, and "back" returns to the regions.
	say(e2eMessageAs(tgUser, "непал"), "Нашлись такие страны")
	if _, _, keys = tg.lastOf(tgUser); !hasData(keys, "onb:c:country:NP") {
		t.Fatalf("Nepal must be offered for \"непал\": %v", keys)
	}
	say(e2eMessageAs(tgUser, "Atlantis"), "в списке нет")
	pressCountry("onb:cr:country:asia", "В какой стране")
	for page := 1; page <= 12; page++ {
		if tg.waitButton(tgUser, "onb:c:country:"+vcOnboardISO, 400*time.Millisecond) {
			break
		}
		tg.push(e2eCallbackAs(tgUser, fmt.Sprintf("onb:cp:country:asia:%d", page+1)))
		time.Sleep(300 * time.Millisecond)
	}
	pressCountry("onb:c:country:"+vcOnboardISO, "Какие мероприятия")

	var stored string
	if err := pool.QueryRow(ctx, `SELECT answers->>'country' FROM onboarding_applications WHERE id = $1::uuid`, appID).Scan(&stored); err != nil || stored != vcOnboardISO {
		t.Fatalf("the application stores country %q (%v), want %s", stored, err, vcOnboardISO)
	}
	// Nothing is created before the approval.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM countries WHERE iso2 = $1`, vcOnboardISO).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a country exists before the approval: %d (%v)", n, err)
	}

	edits := tg.callCount("editMessageReplyMarkup")
	tg.push(e2eCallbackAs(tgUser, "onb:m:event_types:0"))
	tg.waitCall(t, "editMessageReplyMarkup", edits)
	say(e2eCallbackAs(tgUser, "onb:md:event_types"), "Рассадка")
	say(e2eCallbackAs(tgUser, "onb:s:seating:0"), "Последний шаг")
	for _, key := range []string{"accept_terms", "accept_privacy", "confirm_authority"} {
		before := tg.callCount("editMessageReplyMarkup")
		tg.push(e2eCallbackAs(tgUser, "onb:ct:"+key))
		tg.waitCall(t, "editMessageReplyMarkup", before)
	}
	say(e2eCallbackAs(tgUser, "onb:consent"), "Проверьте заявку")
	say(e2eCallbackAs(tgUser, "onb:submit"), "Заявка отправлена")
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM onboarding_applications WHERE id = $1::uuid`, appID).Scan(&status); err != nil || status != "pending_approval" {
		t.Fatalf("after submit: status %q err %v", status, err)
	}

	// The superadmin approves through the admin route; the country row appears
	// in the approval's transaction, and one administrators' notice is queued.
	const password = "Test1234!"
	client := api.Client()
	registerUser(t, client, api.URL, superEmail, password)
	var superID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, superEmail).Scan(&superID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, superID); err != nil {
		t.Fatal(err)
	}
	superToken := loginUser(t, client, api.URL, superEmail, password)
	reason := map[string]string{"X-Admin-Reason": "venue choice e2e"}
	if c, body := onbDo(t, client, http.MethodPost, api.URL+"/v1/admin/onboarding/applications/"+appID+"/approve", superToken, "", reason); c != http.StatusOK || !strings.Contains(body, "org_id") {
		t.Fatalf("approve: %d %s", c, body)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM countries WHERE iso2 = $1`, vcOnboardISO).Scan(&n); err != nil || n != 1 {
		t.Fatalf("after the approval the %s row count = %d (%v), want 1", vcOnboardISO, n, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = 'venue_plan.notify' AND payload->>'kind' = 'country_created' AND payload->>'country_iso2' = $1`, vcOnboardISO).Scan(&n); err != nil || n != 1 {
		t.Fatalf("country_created jobs after the approval = %d (%v), want 1", n, err)
	}
}

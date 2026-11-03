//go:build integration

package httpserver

// POST /v1/organizations/{org_id}/event-page-facts (spec 59): the organizer's
// own event page is read through the real router, its picture is stored as an
// unattached media object of THIS organization, a ticketing platform's page
// and another organization are refused, and the Telegram bot's "Fill in from
// a link" reaches the same route. Run on a migrated database:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci?sslmode=disable \
//	  go test -tags integration ./apps/backend/internal/platform/httpserver/ -run EventPage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

const eventPageTelegramUser = 781

// eventPageSite serves an organizer's event page with a JSON-LD Event and its
// 4:5 poster.
func eventPageSite(t *testing.T) *httptest.Server {
	t.Helper()
	poster := e2eWizardPoster(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cherry-orchard":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<!doctype html><html lang="ru"><head><title>Вишнёвый сад</title>
<script type="application/ld+json">{"@context":"https://schema.org","@type":"TheaterEvent",
"name":"Вишнёвый сад","description":"Комедия Чехова в двух действиях.",
"startDate":"2099-11-20T19:00:00+01:00","typicalAgeRange":"12-",
"image":{"@type":"ImageObject","url":"/poster.png"},
"location":{"@type":"Place","name":"Teatro Kolybel","address":{"@type":"PostalAddress","addressLocality":"Madrid","addressCountry":"ES"}},
"offers":[{"@type":"Offer","name":"Партер","price":"25","priceCurrency":"EUR"},{"@type":"Offer","name":"Балкон","price":"18.50","priceCurrency":"EUR"}]}</script>
</head><body><h1>Вишнёвый сад</h1></body></html>`)
		case "/poster.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(poster)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func postEventPageFacts(t *testing.T, srv *Server, token string, org uuid.UUID, pageURL string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"url": pageURL})
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+org.String()+"/event-page-facts", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestEventPageFacts_JSONLDPageThroughTheRouter(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	posterFactsSweep(t, pool, f.orgID, other.orgID)
	srv := posterFactsServer(t, pool, nil, 50)
	site := eventPageSite(t)
	token := f.posterToken(srv, f.ownerID)

	rec, out := postEventPageFacts(t, srv, token, f.orgID, site.URL+"/cherry-orchard")
	if rec.Code != http.StatusOK {
		t.Fatalf("read: %d %s", rec.Code, rec.Body.String())
	}
	facts := out["facts"].(map[string]any)
	for k, want := range map[string]any{
		"name": "Вишнёвый сад", "date": "2099-11-20", "time": "19:00", "venue_name": "Teatro Kolybel",
		"city": "Madrid", "country": "ES", "age": "12+", "description": "Комедия Чехова в двух действиях.",
	} {
		if facts[k] != want {
			t.Errorf("%s = %v, want %v", k, facts[k], want)
		}
	}
	cats := facts["categories"].([]any)
	if len(cats) != 2 || cats[1].(map[string]any)["price_minor"].(float64) != 1850 {
		t.Errorf("categories = %v", cats)
	}
	img := facts["image"].(map[string]any)
	mediaID := img["media_id"].(string)
	var owner, ownerType string
	var width int32
	if err := pool.QueryRow(context.Background(),
		`SELECT org_id::text, owner_type, width FROM media_objects WHERE id = $1 AND owner_id IS NULL`, mediaID).Scan(&owner, &ownerType, &width); err != nil {
		t.Fatalf("stored picture: %v", err)
	}
	if owner != f.orgID.String() || ownerType != "event_poster" || width != 1000 || img["fits_poster"] != true {
		t.Errorf("picture = %s %s %d %v", owner, ownerType, width, img)
	}

	// The same page read again answers the same stored picture.
	if rec, again := postEventPageFacts(t, srv, token, f.orgID, site.URL+"/cherry-orchard"); rec.Code != http.StatusOK ||
		again["facts"].(map[string]any)["image"].(map[string]any)["media_id"] != mediaID {
		t.Errorf("second read: %d %v", rec.Code, again["facts"])
	}

	// A ticketing platform's page is refused before anything is fetched.
	rec, out = postEventPageFacts(t, srv, token, f.orgID, "https://www.ticketmaster.es/event/123")
	if rec.Code != http.StatusUnprocessableEntity || eventPageErrCode(out) != "event_page.ticketing_platform" {
		t.Errorf("ticketmaster: %d %v", rec.Code, out)
	}
	// Another organization's owner cannot read pages in this one.
	rec, _ = postEventPageFacts(t, srv, other.posterToken(srv, other.ownerID), f.orgID, site.URL+"/cherry-orchard")
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Errorf("foreign organization: %d %s", rec.Code, rec.Body.String())
	}
}

func eventPageErrCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

// The bot's "Fill in from a link": the page's facts come back as hints and
// pressing the name hint answers the name question.
func TestBotE2E_PageLinkHintsComeFromTheAPI(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	posterFactsSweep(t, pool, f.orgID)
	srv := posterFactsServer(t, pool, nil, 50)
	site := eventPageSite(t)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bot_drafts WHERE telegram_user_id = $1`, int64(eventPageTelegramUser))
		_, _ = pool.Exec(context.Background(), `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, int64(eventPageTelegramUser))
	})

	managerEmail := f.newEmail("page")
	if rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"manager","locale":"ru"}`, managerEmail)); rec.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body.String())
	}
	code, _ := f.queuedCode(managerEmail)
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d,"locale":"ru"}`, code, managerEmail, eventPageTelegramUser)); rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}

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
		TicketsBaseURL:    "https://tickets.test",
	})
	if err != nil {
		t.Fatalf("eventbot.New: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()

	msg := func(text string) string {
		return fmt.Sprintf(`"message":{"message_id":%d,"date":1700000000,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"Wiz","language_code":"ru"},"text":%q}`,
			len(text)*7+1, eventPageTelegramUser, eventPageTelegramUser, text)
	}
	cb := func(data string) string {
		return fmt.Sprintf(`"callback_query":{"id":"cb-%s","from":{"id":%d,"is_bot":false,"first_name":"Wiz","language_code":"ru"},"chat_instance":"x","data":%q,"message":{"message_id":5,"date":%d,"chat":{"id":%d,"type":"private"},"text":"menu"}}`,
			uuid.NewString()[:8], eventPageTelegramUser, data, e2ePressDate(), eventPageTelegramUser)
	}
	press := func(data, expect string) {
		t.Helper()
		tg.waitButton(eventPageTelegramUser, data, 2*time.Second)
		m := tg.mark()
		tg.push(cb(data))
		tg.waitSince(t, m, expect)
	}

	press("wz:new", "Заполнить по ссылке")
	press("wz:link", "ссылку на страницу")
	m := tg.mark()
	tg.push(msg(site.URL + "/cherry-orchard"))
	note := tg.waitSince(t, m, "На странице нашёл")
	for _, want := range []string{"Вишнёвый сад", "20.11.2099 19:00", "Teatro Kolybel", "Партер", "1000×1250"} {
		if !strings.Contains(note, want) {
			t.Errorf("the page note lacks %q:\n%s", want, note)
		}
	}
	// Back at the name question; the name is a button, not yet the answer.
	press("wz:hint", "Возраст")
	var name, source string
	if err := pool.QueryRow(context.Background(),
		`SELECT state->'event'->>'name', state->'hints'->>'source' FROM bot_drafts WHERE telegram_user_id = $1`,
		int64(eventPageTelegramUser)).Scan(&name, &source); err != nil {
		t.Fatalf("draft: %v", err)
	}
	if name != "Вишнёвый сад" || source != "page" {
		t.Errorf("draft name %q, hints source %q", name, source)
	}
	cancel()
	<-done
}

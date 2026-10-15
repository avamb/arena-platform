//go:build integration

package httpserver

// POST /v1/organizations/{org_id}/media/{media_id}/poster-facts (task 3-3):
// the model key lives on arena-api, so the route reads a poster of THIS
// organization through the real router and the real posterread reader against
// a stub Messages API, and the Telegram bot reaches the same route. Run on a
// migrated database:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci?sslmode=disable \
//	  go test -tags integration ./apps/backend/internal/platform/httpserver/ -run PosterFacts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

const posterFactsTelegramUser = 779

// stubModel answers every Messages API call with a fixed poster reading and
// counts the calls and the keys it was given.
type stubModel struct {
	srv   *httptest.Server
	calls atomic.Int32
	key   atomic.Value // last x-api-key
}

func newStubModel(t *testing.T) *stubModel {
	t.Helper()
	m := &stubModel{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.calls.Add(1)
		m.key.Store(r.Header.Get("x-api-key"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","stop_reason":"tool_use","content":[
			{"type":"tool_use","id":"tu_1","name":"poster_facts","input":{
				"name":"Stub Poster Concert","date":"2099-12-15","time":"20:00","venue_name":"Stub Hall","city":"Tallinn",
				"age":"16+","categories":[{"name":"Stalls","price":25,"currency":"eur"}]}}]}`))
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func posterFactsServer(t *testing.T, pool *pgxpool.Pool, model *stubModel, perDay int) *Server {
	t.Helper()
	return buildBotIntegrationServerCfg(t, pool, func(c *config.Config) {
		if model != nil {
			c.PosterLLMAPIKey = "sk-ant-test-stub"
			c.PosterLLMModel = "claude-sonnet-5"
			c.PosterLLMBaseURL = model.srv.URL
		}
		c.PosterReadsPerOrgPerDay = perDay
	})
}

func (f *botInviteFixture) posterToken(srv *Server, user uuid.UUID) string {
	f.t.Helper()
	tok, err := eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience).Mint(user)
	if err != nil {
		f.t.Fatalf("mint: %v", err)
	}
	return tok
}

func posterFactsSweep(t *testing.T, pool *pgxpool.Pool, orgs ...uuid.UUID) {
	t.Helper()
	t.Cleanup(func() {
		for _, org := range orgs {
			_, _ = pool.Exec(context.Background(), `DELETE FROM media_objects WHERE org_id = $1`, org)
			_, _ = pool.Exec(context.Background(), `DELETE FROM audit_events WHERE metadata->>'org_id' = $1`, org.String())
		}
	})
}

func uploadPosterAs(t *testing.T, srv *Server, token string, org uuid.UUID, data []byte) string {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("owner_type", "event_poster")
	_ = mw.WriteField("org_id", org.String())
	_ = mw.WriteField("content_type", "image/png")
	part, _ := mw.CreateFormFile("file", "poster.png")
	_, _ = part.Write(data)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/media", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		MediaObject struct {
			ID string `json:"id"`
		} `json:"media_object"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.MediaObject.ID == "" {
		t.Fatalf("upload answer: %s", rec.Body.String())
	}
	return env.MediaObject.ID
}

func readPosterAs(srv *Server, token string, org uuid.UUID, mediaID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+org.String()+"/media/"+mediaID+"/poster-facts", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec
}

func posterErrCode(rec *httptest.ResponseRecorder) string {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return env.Error.Code
}

func TestPosterFacts_RouteReadsOwnPosterAndKeepsOrganizationsApart(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	posterFactsSweep(t, pool, f.orgID, other.orgID)
	model := newStubModel(t)
	srv := posterFactsServer(t, pool, model, 2)
	img := e2eWizardPoster(t)

	owner := f.posterToken(srv, f.ownerID)
	mediaID := uploadPosterAs(t, srv, owner, f.orgID, img)

	// An agent of the same organization lacks event.update.
	q := gen.New(pool)
	agentEmail := f.newEmail("agent")
	agent, err := q.InsertUser(context.Background(), agentEmail, "x", "en")
	if err != nil {
		t.Fatalf("InsertUser agent: %v", err)
	}
	if _, err := q.InsertMembership(context.Background(), agent.ID, f.orgID, "agent"); err != nil {
		t.Fatalf("InsertMembership agent: %v", err)
	}
	if rec := readPosterAs(srv, f.posterToken(srv, agent.ID), f.orgID, mediaID); rec.Code != http.StatusForbidden {
		t.Errorf("agent: %d %s", rec.Code, rec.Body.String())
	}
	// No token at all.
	if rec := readPosterAs(srv, "", f.orgID, mediaID); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", rec.Code)
	}

	// Another organization's owner, whichever organization the path names.
	foreign := other.posterToken(srv, other.ownerID)
	if rec := readPosterAs(srv, foreign, f.orgID, mediaID); rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Errorf("foreign owner on the victim's path: %d %s, want 403 or 404", rec.Code, rec.Body.String())
	}
	if rec := readPosterAs(srv, foreign, other.orgID, mediaID); rec.Code != http.StatusNotFound || posterErrCode(rec) != "media.not_found" {
		t.Errorf("foreign owner on own path: %d %s", rec.Code, rec.Body.String())
	}
	if n := model.calls.Load(); n != 0 {
		t.Fatalf("the model was called %d times for refused requests", n)
	}

	// The owner reads it, twice, and the third read is over the daily cap.
	for i := 1; i <= 2; i++ {
		rec := readPosterAs(srv, owner, f.orgID, mediaID)
		if rec.Code != http.StatusOK {
			t.Fatalf("read %d: %d %s", i, rec.Code, rec.Body.String())
		}
		var out struct {
			Facts struct {
				Name       string `json:"name"`
				Date       string `json:"date"`
				Categories []struct {
					PriceMinor int64  `json:"price_minor"`
					Currency   string `json:"currency"`
				} `json:"categories"`
			} `json:"facts"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.Facts.Name != "Stub Poster Concert" || out.Facts.Date != "2099-12-15" ||
			len(out.Facts.Categories) != 1 || out.Facts.Categories[0].PriceMinor != 2500 || out.Facts.Categories[0].Currency != "EUR" {
			t.Fatalf("facts = %s", rec.Body.String())
		}
	}
	if key, _ := model.key.Load().(string); key != "sk-ant-test-stub" {
		t.Errorf("the model saw key %q", key)
	}
	if rec := readPosterAs(srv, owner, f.orgID, mediaID); rec.Code != http.StatusTooManyRequests || posterErrCode(rec) != "media.poster_reads_exhausted" {
		t.Errorf("third read: %d %s", rec.Code, rec.Body.String())
	}
	if n := model.calls.Load(); n != 2 {
		t.Errorf("the model was called %d times, want 2", n)
	}
	// Another organization has its own counter.
	otherMedia := uploadPosterAs(t, srv, foreign, other.orgID, img)
	if rec := readPosterAs(srv, foreign, other.orgID, otherMedia); rec.Code != http.StatusOK {
		t.Errorf("another organization shares the cap: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPosterFacts_WithoutAModelKeyTheRouteAnswers503(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	posterFactsSweep(t, pool, f.orgID)
	srv := posterFactsServer(t, pool, nil, 50)
	owner := f.posterToken(srv, f.ownerID)
	mediaID := uploadPosterAs(t, srv, owner, f.orgID, e2eWizardPoster(t))
	rec := readPosterAs(srv, owner, f.orgID, mediaID)
	if rec.Code != http.StatusServiceUnavailable || posterErrCode(rec) != "media.poster_reader_not_configured" {
		t.Fatalf("no key: %d %s", rec.Code, rec.Body.String())
	}
}

// The bot holds no model key: it uploads the poster, asks the API for the
// facts and offers them as hints; the stub model proves the call came through
// the API.
func TestBotE2E_PosterHintsComeFromTheAPI(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	posterFactsSweep(t, pool, f.orgID)
	model := newStubModel(t)
	srv := posterFactsServer(t, pool, model, 50)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, int64(posterFactsTelegramUser))
	})

	managerEmail := f.newEmail("hints")
	if rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"manager","locale":"ru"}`, managerEmail)); rec.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body.String())
	}
	code, _ := f.queuedCode(managerEmail)
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d,"locale":"ru"}`, code, managerEmail, posterFactsTelegramUser)); rec.Code != http.StatusOK {
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
			len(text)*7+1, posterFactsTelegramUser, posterFactsTelegramUser, text)
	}
	cb := func(data string) string {
		return fmt.Sprintf(`"callback_query":{"id":"cb-%s","from":{"id":%d,"is_bot":false,"first_name":"Wiz","language_code":"ru"},"chat_instance":"x","data":%q,"message":{"message_id":5,"date":%d,"chat":{"id":%d,"type":"private"},"text":"menu"}}`,
			uuid.NewString()[:8], posterFactsTelegramUser, data, e2ePressDate(), posterFactsTelegramUser)
	}
	press := func(data, expect string) {
		t.Helper()
		tg.waitButton(posterFactsTelegramUser, data, 2*time.Second)
		m := tg.mark()
		tg.push(cb(data))
		tg.waitSince(t, m, expect)
	}

	press("wz:new", "Как называется")
	m := tg.mark()
	tg.push(msg("Hints event"))
	tg.waitSince(t, m, "Возраст")
	press("wz:age:16+", "От чьего имени")
	press("wz:prom:org", "афишу")

	tg.mu.Lock()
	tg.poster = e2eWizardPoster(t)
	size := len(tg.poster)
	tg.mu.Unlock()
	m = tg.mark()
	tg.push(fmt.Sprintf(`"message":{"message_id":%d,"date":1700000000,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"Wiz","language_code":"ru"},"document":{"file_id":"poster-file","file_unique_id":"poster-u","file_name":"poster.png","mime_type":"image/png","file_size":%d}}`,
		4242, posterFactsTelegramUser, posterFactsTelegramUser, size))
	note := tg.waitSince(t, m, "На афише прочитал")
	for _, want := range []string{"Stub Poster Concert", "15.12.2099"} {
		if !strings.Contains(note, want) {
			t.Errorf("the hints note lacks %q:\n%s", want, note)
		}
	}
	if n := model.calls.Load(); n != 1 {
		t.Errorf("the model was called %d times, want 1", n)
	}
	cancel()
	<-done
}

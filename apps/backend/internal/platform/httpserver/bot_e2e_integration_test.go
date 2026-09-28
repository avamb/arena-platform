//go:build integration

package httpserver

// End-to-end proof of the bot's first live scenario (spec 28 §10 step 2):
// an invitation is created, the person opens the deep link in Telegram,
// confirms their e-mail, the account is bound, and "My events" answers with
// the organization's real (here: empty) list — the whole chain through a
// real arena-api router and the real bot process, with only Telegram
// replaced by an in-process stub of its Bot API. Run against a migrated
// database:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci?sslmode=disable \
//	  go test -tags integration ./apps/backend/internal/platform/httpserver/ -run BotE2E

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// stubTelegram is just enough of the Bot API for the scenario: getUpdates
// hands out the updates the test queues, sendMessage/editMessageText
// record what the bot said.
type stubTelegram struct {
	t       *testing.T
	srv     *httptest.Server
	updates chan json.RawMessage
	mu      sync.Mutex
	sent    []string
	nextID  int64
	msgSeq  int
}

func newStubTelegram(t *testing.T) *stubTelegram {
	t.Helper()
	s := &stubTelegram{t: t, updates: make(chan json.RawMessage, 16)}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stubTelegram) handle(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	// The library posts JSON for flat params and multipart/form-data as
	// soon as a param carries a nested struct (reply_markup), so both must
	// be understood.
	field := func(name string) string {
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			_ = r.ParseMultipartForm(1 << 20)
			return r.FormValue(name)
		}
		return ""
	}
	var body []byte
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		body, _ = io.ReadAll(r.Body)
	}
	if method != "getUpdates" {
		s.t.Logf("telegram stub: %s", method)
	}
	w.Header().Set("Content-Type", "application/json")
	switch method {
	case "getMe":
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Bot","username":"ArenaEventsCentrBot"}}`))
	case "getUpdates":
		select {
		case upd := <-s.updates:
			s.mu.Lock()
			s.nextID++
			id := s.nextID
			s.mu.Unlock()
			_, _ = fmt.Fprintf(w, `{"ok":true,"result":[{"update_id":%d,%s}]}`, id, string(upd))
		case <-time.After(200 * time.Millisecond):
			_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
		}
	case "sendMessage", "editMessageText":
		var p struct {
			Text string `json:"text"`
		}
		if text := field("text"); text != "" {
			p.Text = text
		} else {
			_ = json.Unmarshal(body, &p)
		}
		s.mu.Lock()
		s.sent = append(s.sent, p.Text)
		s.msgSeq++
		seq := s.msgSeq
		s.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":1700000000,"chat":{"id":777,"type":"private"},"text":%q}}`, seq, p.Text)
	default:
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}
}

func (s *stubTelegram) push(update string) { s.updates <- json.RawMessage(update) }

// waitFor blocks until a message the bot sent contains needle.
func (s *stubTelegram) waitFor(t *testing.T, needle string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for _, m := range s.sent {
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
	t.Fatalf("bot never said %q; it said:\n%s", needle, strings.Join(s.sent, "\n---\n"))
	return ""
}

const e2eTelegramUser = `"from":{"id":777,"is_bot":false,"first_name":"Test","language_code":"ru"}`

func e2eMessage(text string) string {
	return fmt.Sprintf(`"message":{"message_id":%d,"date":1700000000,"chat":{"id":777,"type":"private"},%s,"text":%q}`,
		time.Now().UnixNano()%100000, e2eTelegramUser, text)
}

func e2eCallback(data string) string {
	return fmt.Sprintf(`"callback_query":{"id":"cb-%d",%s,"chat_instance":"x","data":%q,"message":{"message_id":5,"date":1700000000,"chat":{"id":777,"type":"private"},"text":"menu"}}`,
		time.Now().UnixNano()%100000, e2eTelegramUser, data)
}

func TestBotE2E_InvitationToMyEvents(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The owner invites a manager; the code travels by e-mail.
	managerEmail := f.newEmail("e2e")
	if rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"manager","locale":"ru"}`, managerEmail)); rec.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body.String())
	}
	code, _ := f.queuedCode(managerEmail)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bot_telegram_links WHERE telegram_user_id = 777`)
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
	})
	if err != nil {
		t.Fatalf("eventbot.New: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()

	// 1. A stranger says /start: not invited.
	tg.push(e2eMessage("/start"))
	tg.waitFor(t, "приглашение")

	// 2. The deep link: the bot asks for the e-mail, a typo is refused, the
	//    right address binds the account.
	tg.push(e2eMessage("/start inv_" + code))
	tg.waitFor(t, "адрес почты")
	tg.push(e2eMessage("not an email"))
	tg.waitFor(t, "не похоже")
	tg.push(e2eMessage(strings.ToUpper(managerEmail)))
	accepted := tg.waitFor(t, "Готово")
	if !strings.Contains(accepted, "менеджер") {
		t.Errorf("accepted message should name the role: %s", accepted)
	}
	tg.waitFor(t, "Что будем делать?")

	link, err := gen.New(pool).GetBotTelegramLink(ctx, 777)
	if err != nil || link.Locale != "ru" || link.CurrentOrgID == nil || *link.CurrentOrgID != f.orgID {
		t.Fatalf("link after accept: %+v %v", link, err)
	}

	// 3. "My events" goes through arena-api as the linked manager: the
	//    organization has no events yet, and the API said so with a 200.
	tg.push(e2eCallback("events:1"))
	tg.waitFor(t, "Ивентов пока нет")

	// 4. Language switch is remembered.
	tg.push(e2eCallback("lang:en"))
	tg.waitFor(t, "What would you like to do?")
	tg.push(e2eMessage("/events"))
	tg.waitFor(t, "No events yet")
	if link, err := gen.New(pool).GetBotTelegramLink(ctx, 777); err != nil || link.Locale != "en" {
		t.Fatalf("locale not stored: %+v %v", link, err)
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

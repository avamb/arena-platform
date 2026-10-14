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
	"bytes"
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

	"github.com/google/uuid"

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
	// sentIDs is the message id behind each entry of sent (0 for a
	// document).
	sentIDs []int
	nextID  int64
	msgSeq  int
	// calls counts every Bot API method the bot called, for the methods whose
	// effect leaves no text behind (a button toggle edits the markup only).
	calls map[string]int
	// poster is what getFile/the download path serve for any file id.
	poster []byte
	// docs holds the bytes of every document the bot sent, by file name (the
	// last one of a name wins).
	docs map[string][]byte
	// msgs is every message the bot sent, by id: its chat, its text and the
	// callback data of the buttons it still carries. A pressed callback is
	// delivered on the live message that carries its data (feed.go refuses a
	// press on any other one), as a real client would.
	msgs map[int]*stubMessage
	// retired counts the old messages that lost their keyboard.
	retired int
	// lastCall is when the bot last called a method other than getUpdates.
	lastCall time.Time
	// answers are the texts of the bot's answerCallbackQuery calls ("" when
	// it only stopped the spinner).
	answers []string
}

// stubMessage is one message the bot sent and its current keyboard.
type stubMessage struct {
	chat   int64
	text   string
	markup json.RawMessage // the reply_markup as sent; nil when it has none
	data   []string        // callback data of its buttons
	urls   []string        // addresses of its URL buttons
	// replyKB is a reply keyboard (the one under the message field) the
	// message carried, as sent; nil for an inline keyboard or none.
	replyKB json.RawMessage
}

// document returns the bytes of a document the bot sent, by file name.
func (s *stubTelegram) document(name string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.docs[name]
	return b, ok
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
		counted := method
		if (method == "editMessageReplyMarkup" || method == "editMessageText") && stubParams(body, field).retires() {
			// An old message losing its keyboard (feed.go) is not the
			// redraw a test waits for after a toggle.
			counted = "retire"
		}
		s.mu.Lock()
		if s.calls == nil {
			s.calls = map[string]int{}
		}
		s.calls[counted]++
		s.lastCall = time.Now()
		s.mu.Unlock()
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
	case "sendMessage", "editMessageText", "editMessageReplyMarkup":
		p := stubParams(body, field)
		s.mu.Lock()
		seq := p.MessageID
		switch {
		case method == "sendMessage":
			s.msgSeq++
			seq = s.msgSeq
			s.remember(seq, p)
			s.sent = append(s.sent, p.Text)
			s.sentIDs = append(s.sentIDs, seq)
		case p.retires():
			// An old message losing its keyboard (feed.go): no new text.
			if m := s.msgs[seq]; m != nil {
				m.markup, m.data = nil, nil
			}
			s.retired++
		case method == "editMessageReplyMarkup":
			// A tick redrawn in place: same text, new buttons.
			if m := s.msgs[seq]; m != nil {
				p.Text = m.text
				s.remember(seq, p)
			}
		default:
			s.remember(seq, p)
			s.sent = append(s.sent, p.Text)
			s.sentIDs = append(s.sentIDs, seq)
		}
		s.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":1700000000,"chat":{"id":%d,"type":"private"},"text":%q}}`, seq, p.chat(), p.Text)
	case "answerCallbackQuery":
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(body, &p)
		if p.Text == "" {
			p.Text = field("text")
		}
		s.mu.Lock()
		s.answers = append(s.answers, p.Text)
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	case "getFile":
		// Any file id resolves to the poster the test staged; the library
		// then downloads it from /file/bot<token>/<file_path> below.
		_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"poster-file","file_unique_id":"poster-u","file_size":1,"file_path":"documents/poster.png"}}`))
	case "poster.png":
		s.mu.Lock()
		data := s.poster
		s.mu.Unlock()
		if len(data) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	case "sendDocument":
		// A document arrives as multipart; it is recorded as a sent line
		// "[document <filename>] <caption>" so waitFor can see it.
		name, caption := "", field("caption")
		if r.MultipartForm != nil {
			if files := r.MultipartForm.File["document"]; len(files) > 0 {
				name = files[0].Filename
				if fh, err := files[0].Open(); err == nil {
					body, _ := io.ReadAll(fh)
					_ = fh.Close()
					// A .pdf must be a PDF; any other document (the CSV
					// exports) is kept whole so a test can read the file.
					if strings.HasSuffix(strings.ToLower(name), ".pdf") && !strings.HasPrefix(string(body), "%PDF-") {
						name += " (not a PDF)"
					}
					s.mu.Lock()
					if s.docs == nil {
						s.docs = map[string][]byte{}
					}
					s.docs[name] = body
					s.mu.Unlock()
				}
			}
		}
		s.mu.Lock()
		s.sent = append(s.sent, "[document "+name+"] "+caption)
		s.sentIDs = append(s.sentIDs, 0)
		s.msgSeq++
		seq := s.msgSeq
		s.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":1700000000,"chat":{"id":777,"type":"private"},"caption":%q}}`, seq, caption)
	default:
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}
}

// stubSent is what the bot passed to sendMessage / editMessageText /
// editMessageReplyMarkup.
type stubSent struct {
	ChatID      json.Number     `json:"chat_id"`
	MessageID   int             `json:"message_id"`
	Text        string          `json:"text"`
	ReplyMarkup json.RawMessage `json:"reply_markup"`
}

func (p stubSent) chat() int64 {
	n, err := p.ChatID.Int64()
	if err != nil || n == 0 {
		return 777
	}
	return n
}

// retires reports an editMessageText that only takes the keyboard away from
// an old message (feed.go echoes the pressed button under its text).
func (p stubSent) retires() bool {
	return strings.ReplaceAll(string(p.ReplyMarkup), " ", "") == `{"inline_keyboard":[]}`
}

func stubParams(body []byte, field func(string) string) stubSent {
	var p stubSent
	if len(body) > 0 {
		dec := json.NewDecoder(strings.NewReader(string(body)))
		dec.UseNumber()
		_ = dec.Decode(&p)
		return p
	}
	p.ChatID = json.Number(field("chat_id"))
	p.Text = field("text")
	_, _ = fmt.Sscanf(field("message_id"), "%d", &p.MessageID)
	if m := field("reply_markup"); m != "" {
		p.ReplyMarkup = json.RawMessage(m)
	}
	return p
}

// remember records a message's text and keyboard; s.mu must be held.
func (s *stubTelegram) remember(id int, p stubSent) {
	if s.msgs == nil {
		s.msgs = map[int]*stubMessage{}
	}
	m := &stubMessage{chat: p.chat(), text: p.Text}
	if bytes.Contains(p.ReplyMarkup, []byte(`"keyboard"`)) || bytes.Contains(p.ReplyMarkup, []byte(`"remove_keyboard"`)) {
		m.replyKB = p.ReplyMarkup
	}
	var kb struct {
		InlineKeyboard [][]struct {
			CallbackData string `json:"callback_data"`
			URL          string `json:"url"`
		} `json:"inline_keyboard"`
	}
	if len(p.ReplyMarkup) > 0 && json.Unmarshal(p.ReplyMarkup, &kb) == nil {
		for _, row := range kb.InlineKeyboard {
			for _, b := range row {
				if b.CallbackData != "" {
					m.data = append(m.data, b.CallbackData)
				}
				if b.URL != "" {
					m.urls = append(m.urls, b.URL)
				}
			}
		}
		if len(m.data) > 0 {
			m.markup = p.ReplyMarkup
		}
	}
	s.msgs[id] = m
}

// liveMessage is the newest message of the chat that still carries a button
// with this callback data, or failing that the newest one with any button;
// 0 when the chat has none. s.mu must be held.
func (s *stubTelegram) liveMessage(chat int64, data string) int {
	best, any := 0, 0
	for id, m := range s.msgs {
		if m.chat != chat || len(m.data) == 0 {
			continue
		}
		if id > any {
			any = id
		}
		for _, d := range m.data {
			if d == data && id > best {
				best = id
			}
		}
	}
	if best == 0 {
		return any
	}
	return best
}

// waitButton waits until a live message of the chat carries a button with
// this callback data, so a press is not pushed while the bot is still drawing
// the screen that has it (the press would then land on an older message and
// the feed would refuse it as stale). It reports false when no such button
// appeared in time: the test then presses anyway, as for a made-up press.
func (s *stubTelegram) waitButton(chat int64, data string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		s.mu.Lock()
		found := false
		for _, m := range s.msgs {
			if m.chat != chat {
				continue
			}
			for _, d := range m.data {
				if d == data {
					found = true
				}
			}
		}
		s.mu.Unlock()
		if found || time.Now().After(deadline) {
			return found
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// push queues an update. A callback built by e2eCallback names the
// placeholder message 5; it is delivered on the message a person would have
// pressed: the live one that carries the button (or the newest live one, for
// a press the test makes up), with that message's text and keyboard.
//
// The bot may still be finishing the previous answer (a second message, the
// old keyboard's removal) when a test pushes the next press, so push waits
// for the bot to fall quiet first: the press then lands where a person, who
// sees the finished screen, would press.
func (s *stubTelegram) push(update string) {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		quiet := time.Since(s.lastCall) > 120*time.Millisecond
		s.mu.Unlock()
		if quiet {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.updates <- json.RawMessage(s.aim(update))
}

func (s *stubTelegram) aim(update string) string {
	var u map[string]json.RawMessage
	if json.Unmarshal([]byte("{"+update+"}"), &u) != nil || u["callback_query"] == nil {
		return update
	}
	var cq map[string]json.RawMessage
	if json.Unmarshal(u["callback_query"], &cq) != nil {
		return update
	}
	var data string
	_ = json.Unmarshal(cq["data"], &data)
	var msg struct {
		MessageID int `json:"message_id"`
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	}
	if json.Unmarshal(cq["message"], &msg) != nil || msg.MessageID != 5 {
		return update // a test that names its message keeps it
	}
	s.mu.Lock()
	id := s.liveMessage(msg.Chat.ID, data)
	var target *stubMessage
	if id != 0 {
		target = s.msgs[id]
	}
	s.mu.Unlock()
	if target == nil {
		return update
	}
	m := map[string]any{
		"message_id": id, "date": 1700000000, "text": target.text,
		"chat": map[string]any{"id": msg.Chat.ID, "type": "private"},
	}
	if target.markup != nil {
		m["reply_markup"] = target.markup
	}
	raw, _ := json.Marshal(m)
	cq["message"] = raw
	cqRaw, _ := json.Marshal(cq)
	return `"callback_query":` + string(cqRaw)
}

// retiredCount is how many old messages lost their keyboard so far.
func (s *stubTelegram) retiredCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retired
}

// lastOf is the newest message the bot sent to a chat: its id, text and the
// callback data of its buttons now.
func (s *stubTelegram) lastOf(chat int64) (int, string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	best := 0
	for id, m := range s.msgs {
		if m.chat == chat && id > best {
			best = id
		}
	}
	if best == 0 {
		return 0, "", nil
	}
	m := s.msgs[best]
	return best, m.text, append([]string(nil), m.data...)
}

// hasPrefixData reports whether a keyboard carries a button whose callback
// data starts with prefix.
func hasPrefixData(data []string, prefix string) bool {
	for _, d := range data {
		if strings.HasPrefix(d, prefix) {
			return true
		}
	}
	return false
}

// keyboardOf is the callback data of a message's buttons now.
// urlsOf returns the addresses of a message's URL buttons.
func (s *stubTelegram) urlsOf(id int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.msgs[id]; m != nil {
		return append([]string(nil), m.urls...)
	}
	return nil
}

func (s *stubTelegram) keyboardOf(id int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.msgs[id]; m != nil {
		return append([]string(nil), m.data...)
	}
	return nil
}

// labelsOf is the text of a message's buttons now.
func (s *stubTelegram) labelsOf(id int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.msgs[id]
	if m == nil || m.markup == nil {
		return nil
	}
	var kb struct {
		Rows [][]struct {
			Text string `json:"text"`
		} `json:"inline_keyboard"`
	}
	_ = json.Unmarshal(m.markup, &kb)
	var out []string
	for _, row := range kb.Rows {
		for _, b := range row {
			out = append(out, b.Text)
		}
	}
	return out
}

// callCount is how many times the bot has called a Bot API method so far.
func (s *stubTelegram) callCount(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[method]
}

// waitCall blocks until the bot has called method more than `before` times.
func (s *stubTelegram) waitCall(t *testing.T, method string, before int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if s.callCount(method) > before {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("bot never called %s after %d earlier calls", method, before)
}

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

// e2eMessageAs / e2eCallbackAs are the same updates from another Telegram
// account (its private chat id equals its user id, as in Telegram).
func e2eMessageAs(userID int64, text string) string {
	return fmt.Sprintf(`"message":{"message_id":%d,"date":1700000000,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"U","language_code":"ru"},"text":%q}`,
		time.Now().UnixNano()%100000, userID, userID, text)
}

func e2eCallbackAs(userID int64, data string) string {
	return fmt.Sprintf(`"callback_query":{"id":"cb-%d","from":{"id":%d,"is_bot":false,"first_name":"U","language_code":"ru"},"chat_instance":"x","data":%q,"message":{"message_id":5,"date":1700000000,"chat":{"id":%d,"type":"private"},"text":"menu"}}`,
		time.Now().UnixNano()%100000, userID, data, userID)
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
	// The first message also says what to do next, naming the button below.
	if !strings.Contains(accepted, "Как это работает") || !strings.Contains(accepted, "+ Мероприятие") {
		t.Errorf("accepted message should give the steps: %s", accepted)
	}
	tg.waitFor(t, "Что будем делать?")

	link, err := gen.New(pool).GetBotTelegramLink(ctx, 777)
	if err != nil || link.Locale != "ru" || link.CurrentOrgID == nil || *link.CurrentOrgID != f.orgID {
		t.Fatalf("link after accept: %+v %v", link, err)
	}

	// 3. "My events" goes through arena-api as the linked manager: the
	//    organization has no events yet, and the API said so with a 200.
	tg.push(e2eCallback("events:1"))
	tg.waitFor(t, "Мероприятий пока нет")

	// 3b. The ticket-scanner screen is one press from the menu.
	tg.push(e2eCallback("scanner"))
	tg.waitFor(t, "Сканер билетов")

	// 4. Language switch is remembered.
	tg.push(e2eCallback("lang:en"))
	tg.waitFor(t, "What would you like to do?")
	tg.push(e2eMessage("/events"))
	tg.waitFor(t, "No events yet")
	if link, err := gen.New(pool).GetBotTelegramLink(ctx, 777); err != nil || link.Locale != "en" {
		t.Fatalf("locale not stored: %+v %v", link, err)
	}
	// The Russian menu text was already said in step 2, so wait for a NEW
	// one — otherwise the next push races the locale switch (CI 2026-09-29:
	// "team" answered in English, then "Теперь бот говорит по-русски").
	m0 := tg.mark()
	tg.push(e2eCallback("lang:ru"))
	tg.waitSince(t, m0, "Что будем делать?")

	// 5. Team (step 5): the manager is told the screen is the owner's; the
	//    owner — bound through the same invitation route — sees both, invites
	//    a colleague by e-mail and removes the manager.
	const ownerTG = int64(779)
	ownerEmail := f.emails[0]
	if rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"owner","locale":"ru"}`, ownerEmail)); rec.Code != http.StatusCreated {
		t.Fatalf("invite owner: %d %s", rec.Code, rec.Body.String())
	}
	ownerCode, _ := f.queuedCode(ownerEmail)
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d,"locale":"ru"}`, ownerCode, ownerEmail, ownerTG)); rec.Code != http.StatusOK {
		t.Fatalf("accept owner: %d %s", rec.Code, rec.Body.String())
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, ownerTG)
	})
	_, managerPayload := f.queuedCode(managerEmail)
	managerUserID := uuid.MustParse(managerPayload.UserID)

	m := tg.mark()
	tg.push(e2eCallback("team"))
	tg.waitSince(t, m, "только владелец")

	m = tg.mark()
	tg.push(e2eCallbackAs(ownerTG, "team"))
	teamScreen := tg.waitSince(t, m, "Сотрудники: ")
	if !strings.Contains(teamScreen, managerEmail) || !strings.Contains(teamScreen, ownerEmail) || !strings.Contains(teamScreen, "(это вы)") || !strings.Contains(teamScreen, "Telegram ✓") {
		t.Fatalf("team screen:\n%s", teamScreen)
	}
	newEmail := f.newEmail("colleague")
	m = tg.mark()
	tg.push(e2eCallbackAs(ownerTG, "team:invite"))
	tg.waitSince(t, m, "E-mail коллеги")
	m = tg.mark()
	tg.push(e2eMessageAs(ownerTG, "not-an-email"))
	tg.waitSince(t, m, "не похоже")
	m = tg.mark()
	tg.push(e2eMessageAs(ownerTG, strings.ToUpper(newEmail)))
	tg.waitSince(t, m, "Что может")
	m = tg.mark()
	tg.push(e2eCallbackAs(ownerTG, "team:role:manager"))
	invited := tg.waitSince(t, m, "Приглашение отправлено")
	if !strings.Contains(invited, newEmail) || !strings.Contains(invited, "менеджер") || !strings.Contains(invited, "приглашение отправлено") {
		t.Fatalf("after invite:\n%s", invited)
	}
	colleagueCode, _ := f.queuedCode(newEmail)
	if colleagueCode == "" {
		t.Fatal("no invitation e-mail queued for the colleague")
	}

	// The colleague opens the link in Telegram: they are connected, and the
	// owner who invited them is told in their own chat.
	const colleagueTG = int64(780)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, colleagueTG)
	})
	m = tg.mark()
	tg.push(e2eMessageAs(colleagueTG, "/start inv_"+colleagueCode))
	tg.waitSince(t, m, "адрес почты")
	m = tg.mark()
	tg.push(e2eMessageAs(colleagueTG, newEmail))
	tg.waitSince(t, m, "Готово")
	notice := tg.waitSince(t, m, "принял приглашение")
	if !strings.Contains(notice, newEmail) || !strings.Contains(notice, "менеджер") {
		t.Fatalf("the inviter's notice:\n%s", notice)
	}

	m = tg.mark()
	tg.push(e2eCallbackAs(ownerTG, "team:rm:"+managerUserID.String()))
	tg.waitSince(t, m, "Убрать "+managerEmail)
	m = tg.mark()
	tg.push(e2eCallbackAs(ownerTG, "team:rm:"+managerUserID.String()+":yes"))
	removed := tg.waitSince(t, m, "убран из организации")
	if strings.Contains(removed[strings.Index(removed, "Сотрудники:"):], managerEmail) {
		t.Fatalf("the removed manager is still listed:\n%s", removed)
	}
	var active int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE org_id = $1 AND status = 'active'`, f.orgID).Scan(&active); err != nil || active != 2 {
		t.Fatalf("active memberships = %d (%v), want owner + colleague", active, err)
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

// stubReplyKeyboard is a reply keyboard as the bot sent it.
type stubReplyKeyboard struct {
	Keyboard [][]struct {
		Text string `json:"text"`
	} `json:"keyboard"`
	IsPersistent   bool `json:"is_persistent"`
	ResizeKeyboard bool `json:"resize_keyboard"`
	RemoveKeyboard bool `json:"remove_keyboard"`
}

// replyKeyboardSince is the reply keyboard of the first message sent after
// mark that carried one, and that message's text; ok is false when none did.
func (s *stubTelegram) replyKeyboardSince(mark int) (kb stubReplyKeyboard, text string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := mark; i < len(s.sentIDs); i++ {
		m := s.msgs[s.sentIDs[i]]
		if m == nil || m.replyKB == nil {
			continue
		}
		if json.Unmarshal(m.replyKB, &kb) != nil {
			return kb, m.text, false
		}
		return kb, m.text, true
	}
	return kb, "", false
}

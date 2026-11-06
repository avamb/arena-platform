package salesbot

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

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

func TestParseConnect(t *testing.T) {
	cases := []struct {
		text, bot, code string
		ok              bool
	}{
		{"/connect 7KQ2M-9XA3P", "SalesBot", "7KQ2M-9XA3P", true},
		{"/connect@SalesBot 7KQ2M 9XA3P", "SalesBot", "7KQ2M9XA3P", true},
		{"/connect@salesbot abc", "SalesBot", "abc", true},
		{"/connect@OtherBot abc", "SalesBot", "", false},
		{"/connect", "SalesBot", "", true},
		{"/start", "SalesBot", "", false},
		{"hello /connect x", "SalesBot", "", false},
		{"", "SalesBot", "", false},
	}
	for _, c := range cases {
		code, ok := ParseConnect(c.text, c.bot)
		if code != c.code || ok != c.ok {
			t.Errorf("ParseConnect(%q) = %q %v, want %q %v", c.text, code, ok, c.code, c.ok)
		}
	}
}

func TestTelegramName(t *testing.T) {
	if got := telegramName(models.User{ID: 5, FirstName: "Ivan", LastName: "P"}); got != "Ivan P" {
		t.Errorf("%q", got)
	}
	if got := telegramName(models.User{ID: 5, Username: "iv"}); got != "@iv" {
		t.Errorf("%q", got)
	}
	if got := telegramName(models.User{ID: 5}); got != "Telegram 5" {
		t.Errorf("%q", got)
	}
}

// ── fakes ───────────────────────────────────────────────────────────────────

type fakeAPI struct {
	mu       sync.Mutex
	steps    []StepRequest
	cards    int
	removed  []string
	connects int
	migrated [][2]string
	answer   Answer
	status   int
}

func (f *fakeAPI) Step(_ context.Context, r StepRequest) (Answer, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, r)
	return f.answer, f.status, nil
}
func (f *fakeAPI) Card(context.Context, string, string) (Answer, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cards++
	return f.answer, f.status, nil
}
func (f *fakeAPI) Connect(context.Context, ConnectRequest) (ConnectAnswer, int, error) {
	f.mu.Lock()
	f.connects++
	f.mu.Unlock()
	return ConnectAnswer{}, http.StatusNotFound, nil
}
func (f *fakeAPI) Removed(_ context.Context, chat string) error {
	f.removed = append(f.removed, chat)
	return nil
}
func (f *fakeAPI) Migrated(_ context.Context, from, to string) error {
	f.migrated = append(f.migrated, [2]string{from, to})
	return nil
}

type memPrompts struct {
	mu sync.Mutex
	m  map[string]Prompt
}

func key(chat string, id int64) string { return fmt.Sprintf("%s/%d", chat, id) }

func (p *memPrompts) Save(_ context.Context, pr Prompt) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[key(pr.ChatID, pr.MessageID)] = pr
	return nil
}
func (p *memPrompts) Get(_ context.Context, chat string, id int64) (Prompt, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pr, ok := p.m[key(chat, id)]
	return pr, ok, nil
}
func (p *memPrompts) Clear(_ context.Context, chat string, order uuid.UUID) ([]int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []int64
	for k, pr := range p.m {
		if pr.ChatID == chat && pr.OrderID == order {
			out = append(out, pr.MessageID)
			delete(p.m, k)
		}
	}
	return out, nil
}

// tgCall is one Bot API call the bot made.
type tgCall struct {
	method string
	params map[string]string
}

type stubTG struct {
	mu    sync.Mutex
	calls []tgCall
	seq   int
}

func (s *stubTG) handle(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	params := map[string]string{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		_ = r.ParseMultipartForm(1 << 20)
		for k, v := range r.MultipartForm.Value {
			params[k] = v[0]
		}
	} else {
		body, _ := io.ReadAll(r.Body)
		var raw map[string]any
		_ = json.Unmarshal(body, &raw)
		for k, v := range raw {
			if s, ok := v.(string); ok {
				params[k] = s
			} else {
				b, _ := json.Marshal(v)
				params[k] = string(b)
			}
		}
	}
	s.mu.Lock()
	s.calls = append(s.calls, tgCall{method, params})
	s.seq++
	id := 900 + s.seq
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch method {
	case "getMe":
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Sales","username":"SalesBot"}}`))
	case "sendMessage", "editMessageText", "editMessageReplyMarkup":
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":1,"chat":{"id":-100,"type":"supergroup"},"text":"x"}}`, id)
	default:
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}
}

func (s *stubTG) of(method string) []tgCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []tgCall
	for _, c := range s.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

func newTestBot(t *testing.T, api *fakeAPI) (*Bot, *stubTG, *memPrompts) {
	t.Helper()
	tg := &stubTG{}
	srv := httptest.NewServer(http.HandlerFunc(tg.handle))
	t.Cleanup(srv.Close)
	pr := &memPrompts{m: map[string]Prompt{}}
	b, err := New(Options{Token: "tok", API: api, Prompts: pr, TelegramServerURL: srv.URL,
		Now: func() time.Time { return time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	return b, tg, pr
}

func strp(s string) *string { return &s }

func press(order uuid.UUID, op string) *models.Update {
	return &models.Update{CallbackQuery: &models.CallbackQuery{
		ID: "cb1", From: models.User{ID: 42, FirstName: "Ivan"}, Data: salesnotify.EncodeCardAction(op, "ru", order),
		Message: models.MaybeInaccessibleMessage{Type: models.MaybeInaccessibleMessageTypeMessage,
			Message: &models.Message{ID: 77, Chat: models.Chat{ID: -100, Type: "supergroup"}}},
	}}
}

// "Handed to courier" asks for the number; the reply that quotes the
// question sets the step with it, the card is edited and the question goes.
func TestHandedOver_AskThenReply(t *testing.T) {
	order := uuid.New()
	api := &fakeAPI{status: 200, answer: Answer{Outcome: OutcomeUnchanged, Step: strp(shop.StepPacked),
		Card: salesnotify.DeliveryCard{Text: "card", Rows: salesnotify.StepButtons("ru", shop.StepPacked, order)}}}
	b, tg, prompts := newTestBot(t, api)
	ctx := context.Background()

	b.handleUpdate(ctx, nil, press(order, salesnotify.OpHandedOver))
	if len(api.steps) != 0 || api.cards != 1 {
		t.Fatalf("a question press set a step: %+v", api.steps)
	}
	asks := tg.of("sendMessage")
	if len(asks) != 1 || !strings.Contains(asks[0].params["reply_markup"], "force_reply") || !strings.Contains(asks[0].params["text"], "Номер отправления") {
		t.Fatalf("question: %+v", asks)
	}
	if kb := tg.of("editMessageReplyMarkup"); len(kb) != 1 || !strings.Contains(kb[0].params["reply_markup"], "Без номера") {
		t.Fatalf("card buttons while waiting: %+v", kb)
	}
	var promptID int64
	for _, p := range prompts.m {
		promptID = p.MessageID
		if p.CardMessageID != 77 || p.Step != shop.StepHandedOver || p.OrderID != order {
			t.Fatalf("prompt %+v", p)
		}
	}

	// A reply to some other message is not an answer.
	b.handleUpdate(ctx, nil, &models.Update{Message: &models.Message{ID: 5, Text: "x", From: &models.User{ID: 43},
		Chat: models.Chat{ID: -100}, ReplyToMessage: &models.Message{ID: 1}}})
	if len(api.steps) != 0 {
		t.Fatal("an unrelated reply set a step")
	}

	api.answer = Answer{Outcome: OutcomeApplied, Step: strp(shop.StepHandedOver), Card: salesnotify.DeliveryCard{Text: "new card"}}
	b.handleUpdate(ctx, nil, &models.Update{Message: &models.Message{ID: 6, Text: " RR123 ", From: &models.User{ID: 43, FirstName: "Olga"},
		Chat: models.Chat{ID: -100}, ReplyToMessage: &models.Message{ID: int(promptID)}}})
	if len(api.steps) != 1 || api.steps[0].Step != shop.StepHandedOver || api.steps[0].Tracking == nil || *api.steps[0].Tracking != "RR123" ||
		api.steps[0].TelegramName != "Olga" || api.steps[0].ChatID != "-100" {
		t.Fatalf("step %+v", api.steps)
	}
	edits := tg.of("editMessageText")
	if len(edits) != 1 || edits[0].params["message_id"] != "77" || edits[0].params["text"] != "new card" {
		t.Fatalf("card edit %+v", edits)
	}
	if del := tg.of("deleteMessage"); len(del) != 1 || len(prompts.m) != 0 {
		t.Fatalf("question not removed: %+v %v", del, prompts.m)
	}
}

func TestExpiredQuestion(t *testing.T) {
	order := uuid.New()
	api := &fakeAPI{status: 200}
	b, tg, prompts := newTestBot(t, api)
	prompts.m[key("-100", 500)] = Prompt{ChatID: "-100", MessageID: 500, CardMessageID: 77, OrderID: order,
		Step: shop.StepFailed, Locale: "en", ExpiresAt: time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC)}
	b.handleUpdate(context.Background(), nil, &models.Update{Message: &models.Message{ID: 6, Text: "dog", From: &models.User{ID: 43},
		Chat: models.Chat{ID: -100}, ReplyToMessage: &models.Message{ID: 500}}})
	if len(api.steps) != 0 {
		t.Fatal("an expired question set a step")
	}
	if m := tg.of("sendMessage"); len(m) != 1 || !strings.Contains(m[0].params["text"], "expired") {
		t.Fatalf("answer %+v", m)
	}
}

func TestPress_DirectStepsAndRefusals(t *testing.T) {
	order := uuid.New()
	api := &fakeAPI{status: 200, answer: Answer{Outcome: OutcomeApplied, Step: strp(shop.StepPacked), Card: salesnotify.DeliveryCard{Text: "c"}}}
	b, tg, _ := newTestBot(t, api)
	ctx := context.Background()
	b.handleUpdate(ctx, nil, press(order, salesnotify.OpPacked))
	if len(api.steps) != 1 || api.steps[0].Step != shop.StepPacked || api.steps[0].TelegramUserID != 42 {
		t.Fatalf("step %+v", api.steps)
	}
	if a := tg.of("answerCallbackQuery"); len(a) != 1 || !strings.Contains(a[0].params["text"], "Собран") {
		t.Fatalf("toast %+v", a)
	}
	b.handleUpdate(ctx, nil, press(order, "rw"))
	if got := api.steps[1]; got.Step != shop.StepFailed || got.Reason != shop.ReasonWrongAddress {
		t.Fatalf("reason press %+v", got)
	}
	api.answer = Answer{Outcome: OutcomeClosed, Step: strp(shop.StepPacked), Card: salesnotify.DeliveryCard{Text: "closed", Rows: [][]salesnotify.Button{}}}
	b.handleUpdate(ctx, nil, press(order, salesnotify.OpDelivered))
	a := tg.of("answerCallbackQuery")
	if !strings.Contains(a[len(a)-1].params["text"], "не отправлять") {
		t.Fatalf("closed toast %+v", a[len(a)-1])
	}
	// A refunded order asks nothing.
	b.handleUpdate(ctx, nil, press(order, salesnotify.OpHandedOver))
	if n := len(tg.of("sendMessage")); n != 0 {
		t.Fatalf("a refunded order got a question (%d)", n)
	}
	api.status = http.StatusNotFound
	b.handleUpdate(ctx, nil, press(order, salesnotify.OpPacked))
	a = tg.of("answerCallbackQuery")
	if !strings.Contains(a[len(a)-1].params["text"], "не подключён") {
		t.Fatalf("not connected toast %+v", a[len(a)-1])
	}
}

func TestMyChatMember_RemovedIsReported(t *testing.T) {
	api := &fakeAPI{}
	b, _, _ := newTestBot(t, api)
	b.handleUpdate(context.Background(), nil, &models.Update{MyChatMember: &models.ChatMemberUpdated{
		Chat: models.Chat{ID: -100}, NewChatMember: models.ChatMember{Type: models.ChatMemberTypeBanned}}})
	b.handleUpdate(context.Background(), nil, &models.Update{MyChatMember: &models.ChatMemberUpdated{
		Chat: models.Chat{ID: -200}, NewChatMember: models.ChatMember{Type: models.ChatMemberTypeMember}}})
	if len(api.removed) != 1 || api.removed[0] != "-100" {
		t.Fatalf("removed %v", api.removed)
	}
}

func connectMsg(chatType models.ChatType) *models.Update {
	return &models.Update{Message: &models.Message{ID: 5, Text: "/connect ABC123", Chat: models.Chat{ID: 42, Type: chatType},
		From: &models.User{ID: 42, FirstName: "Ivan", LanguageCode: "ru"}}}
}

// /connect works in a group only: a private chat gets the plain refusal and
// the code is not spent (the API is never called).
func TestConnect_OnlyInGroups(t *testing.T) {
	api := &fakeAPI{}
	b, tg, _ := newTestBot(t, api)
	ctx := context.Background()
	b.handleUpdate(ctx, nil, connectMsg(models.ChatTypePrivate))
	b.handleUpdate(ctx, nil, connectMsg(models.ChatTypeChannel))
	if api.connects != 0 {
		t.Fatalf("the code was spent outside a group (%d connects)", api.connects)
	}
	sent := tg.of("sendMessage")
	if len(sent) != 2 || !strings.Contains(sent[0].params["text"], "Подключать можно только группу") {
		t.Fatalf("replies %+v", sent)
	}
	b.handleUpdate(ctx, nil, connectMsg(models.ChatTypeGroup))
	b.handleUpdate(ctx, nil, connectMsg(models.ChatTypeSupergroup))
	if api.connects != 2 {
		t.Fatalf("connects in groups = %d; want 2", api.connects)
	}
}

// The left update of the old group of a supergroup upgrade is not a removal,
// whichever side of the migration message it comes on.
func TestMyChatMember_LeftAfterMigrationIsIgnored(t *testing.T) {
	api := &fakeAPI{}
	b, _, _ := newTestBot(t, api)
	ctx := context.Background()
	left := &models.Update{MyChatMember: &models.ChatMemberUpdated{
		Chat: models.Chat{ID: -100}, NewChatMember: models.ChatMember{Type: models.ChatMemberTypeLeft}}}
	b.handleUpdate(ctx, nil, &models.Update{Message: &models.Message{Chat: models.Chat{ID: -100}, MigrateToChatID: -1000000000100}})
	b.handleUpdate(ctx, nil, left)
	if len(api.migrated) != 1 || api.migrated[0] != [2]string{"-100", "-1000000000100"} {
		t.Fatalf("migrated %v", api.migrated)
	}
	if len(api.removed) != 0 {
		t.Fatalf("the upgrade was reported as a removal: %v", api.removed)
	}
	// Another group that really lost the bot is still reported.
	b.handleUpdate(ctx, nil, &models.Update{MyChatMember: &models.ChatMemberUpdated{
		Chat: models.Chat{ID: -200}, NewChatMember: models.ChatMember{Type: models.ChatMemberTypeBanned}}})
	if len(api.removed) != 1 || api.removed[0] != "-200" {
		t.Fatalf("removed %v", api.removed)
	}
}

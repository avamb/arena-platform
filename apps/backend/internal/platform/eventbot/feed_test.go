package eventbot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// feedCall is one Bot API request the bot made.
type feedCall struct {
	method string
	msgID  int
	text   string
	markup string
}

// feedStub records the Bot API calls and numbers the sent messages from 100.
type feedStub struct {
	mu    sync.Mutex
	calls []feedCall
	next  int
}

func (s *feedStub) serve(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	var c feedCall
	c.method = method
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		_ = r.ParseMultipartForm(1 << 20)
		c.text = r.FormValue("text")
		c.markup = r.FormValue("reply_markup")
		_, _ = fmt.Sscanf(r.FormValue("message_id"), "%d", &c.msgID)
	} else {
		body, _ := io.ReadAll(r.Body)
		var p struct {
			MessageID   int             `json:"message_id"`
			Text        string          `json:"text"`
			ReplyMarkup json.RawMessage `json:"reply_markup"`
		}
		_ = json.Unmarshal(body, &p)
		c.msgID, c.text, c.markup = p.MessageID, p.Text, string(p.ReplyMarkup)
	}
	s.mu.Lock()
	if method == "sendMessage" {
		s.next++
		c.msgID = 100 + s.next
	}
	s.calls = append(s.calls, c)
	id := c.msgID
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch method {
	case "sendMessage", "editMessageText":
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":0,"chat":{"id":42,"type":"private"}}}`, id)
	default:
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	}
}

func (s *feedStub) all() []feedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]feedCall(nil), s.calls...)
}

func (s *feedStub) reset() {
	s.mu.Lock()
	s.calls = nil
	s.mu.Unlock()
}

func newFeedBot(t *testing.T) (*Bot, *feedStub) {
	t.Helper()
	stub := &feedStub{}
	srv := httptest.NewServer(http.HandlerFunc(stub.serve))
	t.Cleanup(srv.Close)
	tg, err := tgbot.New("123:test", tgbot.WithServerURL(srv.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	return &Bot{tg: tg, texts: NewTexts(nil), logger: slog.New(slog.NewTextHandler(io.Discard, nil)), feed: newFeedMemory()}, stub
}

func kbOf(data ...string) *models.InlineKeyboardMarkup {
	var rows [][]models.InlineKeyboardButton
	for _, d := range data {
		rows = append(rows, []models.InlineKeyboardButton{{Text: "btn " + d, CallbackData: d}})
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// press is a press on message msgID of chat 42 whose keyboard is kb.
func press(msgID int, text, data string, kb *models.InlineKeyboardMarkup) *models.CallbackQuery {
	return &models.CallbackQuery{
		ID:   "cb",
		From: models.User{ID: 42, LanguageCode: "ru"},
		Data: data,
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{
			ID: msgID, Text: text, Chat: models.Chat{ID: 42, Type: "private"}, ReplyMarkup: kb,
		}},
	}
}

// A press is answered by a NEW message at the bottom, and the pressed message
// keeps its text, gains a line naming the pressed button and loses its
// keyboard.
func TestFeed_PressPostsANewMessageAndRetiresThePressedOne(t *testing.T) {
	b, stub := newFeedBot(t)
	old := kbOf("x:refund", "home")
	b.feed.add(42, 10)
	cq := press(10, "Order card", "x:refund", old)
	ctx := withFeedAction(context.Background(), pressAction(cq))
	msg := 10
	b.reply(ctx, 42, &msg, "Pick the tickets", kbOf("x:t:0"))

	calls := stub.all()
	if len(calls) != 2 || calls[0].method != "sendMessage" || calls[0].text != "Pick the tickets" {
		t.Fatalf("want a new message first, got %+v", calls)
	}
	echo := calls[1]
	if echo.method != "editMessageText" || echo.msgID != 10 || echo.text != "Order card\n\n✔ btn x:refund" {
		t.Fatalf("the pressed message was not retired with its echo: %+v", echo)
	}
	if strings.ReplaceAll(echo.markup, " ", "") != `{"inline_keyboard":[]}` {
		t.Fatalf("the pressed message kept a keyboard: %q", echo.markup)
	}
	if b.feed.admit(42, 10) {
		t.Fatal("the pressed message is still live")
	}
	if !b.feed.admit(42, calls[0].msgID) {
		t.Fatal("the new message is not live")
	}
}

// A press on a message that is no longer live answers "out of date", stops
// the spinner, takes the keyboard away and does nothing else.
func TestFeed_AStalePressDoesNothing(t *testing.T) {
	b, stub := newFeedBot(t)
	b.feed.add(42, 11) // the current screen
	b.route = func(context.Context, *models.Update) { t.Fatal("a stale press reached a handler") }
	b.handleCallback(context.Background(), press(10, "Old final check", "or:f:ok:abc", kbOf("or:f:ok:abc")))

	calls := stub.all()
	if len(calls) != 2 {
		t.Fatalf("want an answer and a stripped keyboard, got %+v", calls)
	}
	if calls[0].method != "answerCallbackQuery" {
		t.Fatalf("the spinner was not stopped first: %+v", calls)
	}
	if calls[1].method != "editMessageReplyMarkup" || calls[1].msgID != 10 {
		t.Fatalf("the stale keyboard was not removed: %+v", calls[1])
	}
	for _, c := range calls {
		if c.method == "sendMessage" {
			t.Fatalf("a stale press sent a message: %+v", c)
		}
	}
	if !b.feed.admit(42, 11) {
		t.Fatal("the current screen stopped being live")
	}
}

// The second tap of a double tap is refused: the first one already moved the
// chat on.
func TestFeed_TheSecondTapOfADoubleTapIsRefused(t *testing.T) {
	b, stub := newFeedBot(t)
	b.feed.add(42, 10)
	cq := press(10, "Final check", "x:yes", kbOf("x:yes"))
	ctx := withFeedAction(context.Background(), pressAction(cq))
	msg := 10
	b.consumePress(ctx, 42)
	b.reply(ctx, 42, &msg, "Sending…", nil)
	stub.reset()
	if b.feed.admit(42, 10) {
		t.Fatal("the second tap on the same message would act again")
	}
}

// An answer without buttons (a note, an error) leaves the pressed screen
// usable: taking its keyboard would leave the person with no button at all.
func TestFeed_AnAnswerWithoutButtonsKeepsTheScreen(t *testing.T) {
	b, stub := newFeedBot(t)
	b.feed.add(42, 10)
	cq := press(10, "Event card", "ec:ce:x", kbOf("ec:ce:x", "home"))
	ctx := withFeedAction(context.Background(), pressAction(cq))
	msg := 10
	b.reply(ctx, 42, &msg, "Too many rows: export one date at a time.", nil)
	for _, c := range stub.all() {
		if c.method != "sendMessage" {
			t.Fatalf("the screen above was touched: %+v", c)
		}
	}
	if !b.feed.admit(42, 10) {
		t.Fatal("the screen above lost its buttons to a note")
	}
}

// Ticking a box in the list on screen and turning a page redraw the message
// in place; nothing is retired.
func TestFeed_TogglesAndPagesRedrawInPlace(t *testing.T) {
	b, stub := newFeedBot(t)
	b.feed.add(42, 10)
	for _, cq := range []*models.CallbackQuery{
		press(10, "Pick", "or:f:t:0", kbOf("or:f:t:0", "or:f:go")),
		press(10, "List", "el:p:2", &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "1/3", CallbackData: "noop"}, {Text: "Next ›", CallbackData: "el:p:2"}},
		}}),
		press(10, "Calendar", "wz:cal:2026-11", kbOf("wz:cal:2026-11")),
	} {
		stub.reset()
		ctx := withFeedAction(context.Background(), pressAction(cq))
		msg := 10
		b.reply(ctx, 42, &msg, "Redrawn", kbOf("or:f:t:0"))
		calls := stub.all()
		if len(calls) != 1 || calls[0].method != "editMessageText" || calls[0].msgID != 10 {
			t.Fatalf("%s: want one in-place edit, got %+v", cq.Data, calls)
		}
		if !b.feed.admit(42, 10) {
			t.Fatalf("%s: the redrawn message stopped being live", cq.Data)
		}
	}
}

// A typed answer whose reply carries buttons takes the buttons from the older
// screens, including a remembered one the reply was asked to edit.
func TestFeed_ATypedAnswerRetiresTheOlderScreens(t *testing.T) {
	b, stub := newFeedBot(t)
	b.feed.add(42, 10) // the screen that asked for the word
	b.feed.add(42, 12) // another live screen
	ctx := withFeedAction(context.Background(), textAction(42))
	remembered := 10
	b.reply(ctx, 42, &remembered, "Final check", kbOf("x:yes"))

	calls := stub.all()
	if calls[0].method != "sendMessage" {
		t.Fatalf("the answer was not a new message: %+v", calls)
	}
	stripped := map[int]bool{}
	for _, c := range calls[1:] {
		if c.method == "editMessageReplyMarkup" {
			stripped[c.msgID] = true
		}
	}
	if !stripped[10] || !stripped[12] {
		t.Fatalf("older screens kept their buttons: %+v", calls)
	}
	if b.feed.admit(42, 10) || b.feed.admit(42, 12) {
		t.Fatal("an older screen is still live")
	}
}

// A second reply of the same action redraws the message the first one sent
// instead of posting a third ("Saving…" then the result).
func TestFeed_ASecondReplyOfTheSameActionEditsItsOwnMessage(t *testing.T) {
	b, stub := newFeedBot(t)
	b.feed.add(42, 10)
	cq := press(10, "Summary", "wz:save", kbOf("wz:save"))
	ctx := withFeedAction(context.Background(), pressAction(cq))
	msg := 10
	b.reply(ctx, 42, &msg, "Saving…", nil)
	b.reply(ctx, 42, &msg, "Saved", kbOf("home"))
	sends, edits := 0, 0
	for _, c := range stub.all() {
		switch {
		case c.method == "sendMessage":
			sends++
		case c.method == "editMessageText" && c.text == "Saved":
			edits++
		}
	}
	if sends != 1 || edits != 1 {
		t.Fatalf("want one new message then an edit of it, got %+v", stub.all())
	}
}

// Background work (a refund answer, a reminder) posts a new message and
// leaves the screen the person is on alone.
func TestFeed_BackgroundWorkLeavesTheCurrentScreenAlone(t *testing.T) {
	b, stub := newFeedBot(t)
	b.feed.add(42, 10)
	ctx := detached(withFeedAction(context.Background(), textAction(42)))
	b.send(ctx, 42, "Reminder", kbOf("wz:resume"))
	for _, c := range stub.all() {
		if c.method != "sendMessage" {
			t.Fatalf("background work touched another message: %+v", c)
		}
	}
	if !b.feed.admit(42, 10) {
		t.Fatal("the current screen lost its buttons to a reminder")
	}
}

// After a restart the bot does not know the chat: the first press is
// accepted and becomes the chat's live message.
func TestFeed_AnUnknownChatAcceptsThePress(t *testing.T) {
	f := newFeedMemory()
	if !f.admit(42, 7) {
		t.Fatal("a press in a chat the bot does not know yet was refused")
	}
	if !f.admit(42, 7) || f.admit(42, 6) {
		t.Fatal("the pressed message should be the chat's only live one")
	}
	var none *feedMemory
	if !none.admit(42, 1) {
		t.Fatal("a bot without a feed memory must accept every press")
	}
}

// A keyboard of links only can do nothing, so it is never live.
func TestFeed_LinkOnlyKeyboardsAreNotLive(t *testing.T) {
	links := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "Open", URL: "https://example.org"}}}}
	if hasCallbacks(links) || !hasCallbacks(kbOf("home")) || hasCallbacks(nil) {
		t.Fatal("hasCallbacks misjudged a keyboard")
	}
}

func TestInPlacePress(t *testing.T) {
	pager := []models.InlineKeyboardButton{{Text: "‹ Back", CallbackData: "or:p:1"}, {Text: "2/4", CallbackData: "noop"}, {Text: "Next ›", CallbackData: "or:p:3"}}
	cases := []struct {
		data  string
		row   []models.InlineKeyboardButton
		label string
		want  bool
	}{
		{"or:f:t:3", nil, "☑ №1", true},
		{"or:f:nt", nil, "☑ Send", true},
		{"or:p:3", pager, "Next ›", true},
		{"noop", pager, "2/4", false},
		{"ses:cal:2026-12", nil, "›", true},
		{"or:f:go", nil, "Continue", false},
		{"or:f:ok:abc", nil, "Yes", false},
		{"home", nil, "Home", false},
	}
	for _, c := range cases {
		if got := inPlacePress(c.data, c.row, c.label); got != c.want {
			t.Errorf("inPlacePress(%q) = %v, want %v", c.data, got, c.want)
		}
	}
}

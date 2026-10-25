package eventbot

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// miniappTestBot is a bot whose arena-api is a stub answering every
// POST /v1/bot/web-login-codes with link (or status when it is not 201). The
// returned counter says how many codes were asked for.
func miniappTestBot(t *testing.T, enabled bool, status int, link string) (*Bot, *atomic.Int32, *[]map[string]any) {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	var calls atomic.Int32
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { // the event the editor mark reads (RT-12b): none, so no mark
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"events":[]}`))
			return
		}
		calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusCreated {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"code":"bot.web_login_superadmin","message":"no"}}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"url":"` + link + `","expires_at":"2026-10-25T12:00:00Z"}`))
	}))
	t.Cleanup(srv.Close)
	return &Bot{
		texts:         NewTexts(bundle),
		arena:         NewArenaClient(srv.URL, "svc", srv.Client()),
		logger:        slog.Default(),
		miniappEditor: enabled,
	}, &calls, &bodies
}

func flatten(kb *models.InlineKeyboardMarkup) []models.InlineKeyboardButton {
	var out []models.InlineKeyboardButton
	for _, row := range kb.InlineKeyboard {
		out = append(out, row...)
	}
	return out
}

func webAppButtons(kb *models.InlineKeyboardMarkup) []models.InlineKeyboardButton {
	var out []models.InlineKeyboardButton
	for _, btn := range flatten(kb) {
		if btn.WebApp != nil {
			out = append(out, btn)
		}
	}
	return out
}

const miniappTestLink = "https://events.example.test/tg/edit#code=abcDEF0123456789abcDEF0123456789abcDEF01234&kind=description&event=00000000-0000-0000-0000-000000000001"

// With the flag on, the screens carry an ordinary CALLBACK button, in every
// language, and nothing is asked of arena-api while a screen is drawn: no code
// exists before the organizer presses the button.
func TestMiniappRow_FlagOnShowsACallbackButtonAndAsksForNoCode(t *testing.T) {
	ev := uuid.NewString()
	b, calls, _ := miniappTestBot(t, true, http.StatusCreated, miniappTestLink)
	want := map[string]string{"en": "✏️ Edit in the editor", "ru": "✏️ Править в редакторе", "es": "✏️ Editar en el editor"}
	for _, loc := range []string{"en", "ru", "es"} {
		row := b.miniappRow(42, loc, tfKindDescription, ev)
		if len(row) != 1 || row[0].CallbackData != "tf:m" || row[0].WebApp != nil || row[0].URL != "" {
			t.Fatalf("%s: preview row = %+v", loc, row)
		}
		if row[0].Text != want[loc] {
			t.Errorf("%s: label %q, want %q", loc, row[0].Text, want[loc])
		}
		note := b.miniappRow(42, loc, tfKindEmailNote, ev)
		if len(note) != 1 || note[0].CallbackData != "tf:m:"+ev || len(note[0].CallbackData) > 64 || note[0].WebApp != nil {
			t.Fatalf("%s: note row = %+v", loc, note)
		}
		kb := b.tfKeyboard(loc, row)
		if got := webAppButtons(kb); len(got) != 0 {
			t.Errorf("%s: %d web_app buttons before the press", loc, len(got))
		}
		var data []string
		for _, btn := range flatten(kb) {
			if btn.CallbackData != "" {
				data = append(data, btn.CallbackData)
				if len(btn.CallbackData) > 64 {
					t.Errorf("callback %q is longer than 64 bytes", btn.CallbackData)
				}
			}
		}
		if strings.Join(data, ",") != "tf:s,tf:p,tf:a,tf:m,tf:c" {
			t.Errorf("%s: callbacks = %v", loc, data)
		}
		if last := flatten(kb); last[len(last)-1].CallbackData != "tf:c" {
			t.Errorf("%s: the cancel button is not last", loc)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("%d codes asked for while drawing screens, want none", calls.Load())
	}
}

// With the flag off (the default) nothing shows: the keyboard is the four
// buttons of before.
func TestMiniappRow_FlagOffShowsNothingAndAsksForNoCode(t *testing.T) {
	b, calls, _ := miniappTestBot(t, false, http.StatusCreated, miniappTestLink)
	if row := b.miniappRow(42, "en", tfKindDescription, uuid.NewString()); row != nil {
		t.Fatalf("row with the flag off: %+v", row)
	}
	if row := b.miniappRow(42, "en", tfKindEmailNote, uuid.NewString()); row != nil {
		t.Fatalf("note row with the flag off: %+v", row)
	}
	if calls.Load() != 0 {
		t.Errorf("%d code requests with the flag off", calls.Load())
	}
	kb := b.tfKeyboard("en", nil)
	if len(flatten(kb)) != 4 || len(webAppButtons(kb)) != 0 {
		t.Errorf("keyboard = %+v", kb)
	}
}

// Where a button cannot work there is none: a group chat, an event that does
// not exist yet, a bad event id, an unknown kind.
func TestMiniappRow_NoButtonWhereItCannotWork(t *testing.T) {
	ev := uuid.NewString()
	b, calls, _ := miniappTestBot(t, true, http.StatusCreated, miniappTestLink)
	if row := b.miniappRow(-100123, "en", tfKindDescription, ev); row != nil {
		t.Errorf("group chat: %+v", row)
	}
	if row := b.miniappRow(42, "en", tfKindDescription, ""); row != nil {
		t.Errorf("create wizard, no event yet: %+v", row)
	}
	if row := b.miniappRow(42, "en", tfKindDescription, "not-a-uuid"); row != nil {
		t.Errorf("bad event id: %+v", row)
	}
	if row := b.miniappRow(42, "en", tfKind("other"), ev); row != nil {
		t.Errorf("unknown kind: %+v", row)
	}
	if calls.Load() != 0 {
		t.Errorf("%d code requests for a screen with no button", calls.Load())
	}
}

// miniappPressBot is miniappTestBot with a Telegram stub behind it, so a press
// can be followed down to the Bot API calls.
func miniappPressBot(t *testing.T, enabled bool, status int, link string) (*Bot, *atomic.Int32, *feedStub) {
	t.Helper()
	b, calls, _ := miniappTestBot(t, enabled, status, link)
	stub := &feedStub{}
	srv := httptest.NewServer(http.HandlerFunc(stub.serve))
	t.Cleanup(srv.Close)
	tg, err := tgbot.New("123:test", tgbot.WithServerURL(srv.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	b.tg = tg
	b.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	feed := newFeedMemory()
	feed.boot -= 10
	b.feed = feed
	return b, calls, stub
}

// noteScreenPress is a press of the letter screen's button of event ev.
func noteScreenPress(ev string) *models.CallbackQuery {
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "Change", CallbackData: "tf:e:" + ev}},
		{{Text: "✏️ Edit in the editor", CallbackData: "tf:m:" + ev}},
		{{Text: "Back", CallbackData: "event:" + ev + ":1"}, {Text: "Home", CallbackData: "home"}},
	}}
	return press(10, "Text in the letter", "tf:m:"+ev, kb)
}

func ownerIdentity() *Identity {
	return &Identity{Current: &Membership{Role: membershipRoleOwner}}
}

func parseMarkup(t *testing.T, raw string) *models.InlineKeyboardMarkup {
	t.Helper()
	var kb models.InlineKeyboardMarkup
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &kb); err != nil {
			t.Fatal(err)
		}
	}
	return &kb
}

// The press draws the code, turns THAT button of THAT message into a web_app
// button with the fresh link, keeps every other button, and adds the note about
// the five minutes. A second press later draws a new code.
func TestMiniappPress_DrawsTheCodeAndReplacesTheButtonInPlace(t *testing.T) {
	ev := uuid.NewString()
	b, calls, stub := miniappPressBot(t, true, http.StatusCreated, miniappTestLink)
	from := &models.User{ID: 42}
	for n := 1; n <= 2; n++ {
		stub.reset()
		ctx := withFeedAction(context.Background(), pressAction(noteScreenPress(ev)))
		if !b.tfMiniappCallback(ctx, 42, 10, from, ownerIdentity(), "jwt", []string{"m", ev}) {
			t.Fatal("the press was not taken")
		}
		if int(calls.Load()) != n {
			t.Fatalf("press %d: %d codes asked for", n, calls.Load())
		}
		got := stub.all()
		if len(got) != 1 || got[0].method != "editMessageText" || got[0].msgID != 10 {
			t.Fatalf("press %d: Bot API calls %+v, want one edit of the pressed message", n, got)
		}
		if got[0].text != "Text in the letter\n\nThe link works for 5 minutes." {
			t.Errorf("text %q", got[0].text)
		}
		kb := parseMarkup(t, got[0].markup)
		apps := webAppButtons(kb)
		if len(apps) != 1 || apps[0].WebApp.URL != miniappTestLink || apps[0].Text != "✏️ Edit in the editor" {
			t.Fatalf("web_app buttons %+v", apps)
		}
		var data []string
		for _, btn := range flatten(kb) {
			if btn.CallbackData != "" {
				data = append(data, btn.CallbackData)
			}
		}
		if strings.Join(data, ",") != "tf:e:"+ev+",event:"+ev+":1,home" {
			t.Errorf("the other buttons changed: %v", data)
		}
		if len(kb.InlineKeyboard) != 3 {
			t.Errorf("rows %d, want the same 3", len(kb.InlineKeyboard))
		}
	}
}

// A refusal of arena-api, a link Telegram would not take, the flag turned off
// after the screen was drawn: the message keeps its button, the organizer gets
// one short line, and (flag off) no code is asked for.
func TestMiniappPress_RefusalsLeaveTheScreenAndSayWhy(t *testing.T) {
	ev := uuid.NewString()
	from := &models.User{ID: 42}
	for name, c := range map[string]struct {
		enabled bool
		status  int
		link    string
		asked   int32
	}{
		"superadmin or unlinked": {true, http.StatusForbidden, "", 1},
		"http link":              {true, http.StatusCreated, "http://events.example.test/tg/edit#code=x", 1},
		"link over the limit":    {true, http.StatusCreated, "https://events.example.test/tg/edit#code=" + strings.Repeat("a", tgButtonURLMax), 1},
		"flag turned off":        {false, http.StatusCreated, miniappTestLink, 0},
	} {
		b, calls, stub := miniappPressBot(t, c.enabled, c.status, c.link)
		ctx := withFeedAction(context.Background(), pressAction(noteScreenPress(ev)))
		b.tfMiniappCallback(ctx, 42, 10, from, ownerIdentity(), "jwt", []string{"m", ev})
		if calls.Load() != c.asked {
			t.Errorf("%s: %d codes asked for, want %d", name, calls.Load(), c.asked)
		}
		got := stub.all()
		if len(got) != 1 || got[0].method != "sendMessage" || !strings.Contains(got[0].text, "editor") || got[0].markup != "" {
			t.Errorf("%s: Bot API calls %+v, want one plain message", name, got)
		}
	}
}

// Only the people who may change event texts get a code, and a press whose
// keyboard is not in the update still gets the link, in a message of its own.
func TestMiniappPress_RightsAndAMessageWithoutItsKeyboard(t *testing.T) {
	ev := uuid.NewString()
	from := &models.User{ID: 42}

	b, calls, stub := miniappPressBot(t, true, http.StatusCreated, miniappTestLink)
	ctx := withFeedAction(context.Background(), pressAction(noteScreenPress(ev)))
	agent := &Identity{Current: &Membership{Role: "agent"}}
	b.tfMiniappCallback(ctx, 42, 10, from, agent, "jwt", []string{"m", ev})
	if calls.Load() != 0 {
		t.Errorf("an agent got a code")
	}
	for _, c := range stub.all() {
		if len(webAppButtons(parseMarkup(t, c.markup))) != 0 {
			t.Errorf("an agent got a web_app button: %+v", c)
		}
	}

	b, calls, stub = miniappPressBot(t, true, http.StatusCreated, miniappTestLink)
	bare := press(10, "Text in the letter", "tf:m:"+ev, nil)
	ctx = withFeedAction(context.Background(), pressAction(bare))
	b.tfMiniappCallback(ctx, 42, 10, from, ownerIdentity(), "jwt", []string{"m", ev})
	got := stub.all()
	if calls.Load() != 1 || len(got) != 1 || got[0].method != "sendMessage" || len(webAppButtons(parseMarkup(t, got[0].markup))) != 1 {
		t.Errorf("no keyboard in the update: %d codes, calls %+v", calls.Load(), got)
	}

	// Another press is none of this handler's business.
	if b.tfMiniappCallback(ctx, 42, 10, from, ownerIdentity(), "jwt", []string{"s"}) {
		t.Error("a press of another button was taken")
	}
}

// A text with no room for one more line keeps its text: only the keyboard
// changes, and the note is a message of its own.
func TestMiniappPress_LongTextOnlyChangesTheKeyboard(t *testing.T) {
	ev := uuid.NewString()
	b, _, stub := miniappPressBot(t, true, http.StatusCreated, miniappTestLink)
	cq := noteScreenPress(ev)
	cq.Message.Message.Text = strings.Repeat("я", tgTextLimit-5)
	ctx := withFeedAction(context.Background(), pressAction(cq))
	b.tfMiniappCallback(ctx, 42, 10, &models.User{ID: 42}, ownerIdentity(), "jwt", []string{"m", ev})
	got := stub.all()
	if len(got) != 2 || got[0].method != "editMessageReplyMarkup" || got[0].msgID != 10 || got[1].method != "sendMessage" {
		t.Fatalf("Bot API calls %+v", got)
	}
	if len(webAppButtons(parseMarkup(t, got[0].markup))) != 1 || !strings.Contains(got[1].text, "5") {
		t.Errorf("calls %+v", got)
	}
}

func TestWithWebAppButton(t *testing.T) {
	msg := &models.Message{ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "A", CallbackData: "a"}, {Text: "B", CallbackData: "tf:m"}},
	}}}
	kb, ok := withWebAppButton(msg, "tf:m", "https://e.test/x")
	if !ok || kb.InlineKeyboard[0][1].WebApp == nil || kb.InlineKeyboard[0][1].CallbackData != "" || kb.InlineKeyboard[0][1].Text != "B" || kb.InlineKeyboard[0][0].CallbackData != "a" {
		t.Fatalf("%+v %v", kb, ok)
	}
	if msg.ReplyMarkup.InlineKeyboard[0][1].CallbackData != "tf:m" {
		t.Error("the original keyboard was changed")
	}
	if _, ok := withWebAppButton(msg, "tf:m:zzz", "https://e.test/x"); ok {
		t.Error("a button that is not there was found")
	}
	if _, ok := withWebAppButton(nil, "tf:m", "https://e.test/x"); ok {
		t.Error("no message, a button was found")
	}
}

func TestMiniappURLOK(t *testing.T) {
	if !miniappURLOK(miniappTestLink) {
		t.Error("a normal link is refused")
	}
	if len(miniappTestLink) > 200 {
		t.Errorf("a link is %d bytes, far from the URL limit expected", len(miniappTestLink))
	}
	if miniappURLOK("http://events.example.test/tg/edit") || miniappURLOK("") {
		t.Error("a non-https address is accepted")
	}
	if !miniappURLOK("https://e.test/" + strings.Repeat("a", tgButtonURLMax-len("https://e.test/"))) {
		t.Error("a link exactly at the limit is refused")
	}
	if miniappURLOK("https://e.test/" + strings.Repeat("a", tgButtonURLMax)) {
		t.Error("a link over the limit is accepted")
	}
}

// The new label exists with text in every language the bot speaks and is in
// the keys the locale test checks.
func TestMiniappKeys_InTheKeyList(t *testing.T) {
	found := false
	for _, k := range MessageKeys {
		if k == "bot.tf.edit_btn" {
			found = true
		}
	}
	if !found {
		t.Fatal("bot.tf.edit_btn is not in MessageKeys")
	}
}

// The note and the refusal exist, with text, in every language of the bot.
func TestMiniappKeys_NoteAndRefusalInEveryLanguage(t *testing.T) {
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	texts := NewTexts(bundle)
	for _, key := range []string{"bot.tf.edit_note", "bot.tf.edit_failed"} {
		listed := false
		for _, k := range MessageKeys {
			listed = listed || k == key
		}
		if !listed {
			t.Errorf("%s is not in MessageKeys", key)
		}
		for _, loc := range []string{"en", "ru", "es"} {
			if got := texts.T(loc, key, nil); got == "" || got == key {
				t.Errorf("%s/%s resolved to %q", loc, key, got)
			}
		}
	}
	if !strings.Contains(texts.T("ru", "bot.tf.edit_note", nil), "5 минут") {
		t.Error("the Russian note does not say 5 минут")
	}
}

// The description preview's button is "tf:m": the dialog knows the kind and
// the event. A press with no open preview (it ran out, or it is another
// organization's) draws no code.
func TestMiniappPress_DescriptionPreviewReadsTheDialog(t *testing.T) {
	ev := uuid.NewString()
	org := uuid.New()
	id := &Identity{Current: &Membership{OrgID: org, Role: membershipRoleOwner}}
	from := &models.User{ID: 42}
	previewPress := func() *models.CallbackQuery {
		kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "Save", CallbackData: "tf:s"}},
			{{Text: "✏️ Edit in the editor", CallbackData: "tf:m"}},
			{{Text: "Cancel", CallbackData: "tf:c"}},
		}}
		return press(10, "Here is the text", "tf:m", kb)
	}

	b, calls, stub := miniappPressBot(t, true, http.StatusCreated, miniappTestLink)
	b.dialogs = newMemDialogStore(&testClock{t: time.Now()})
	ctx := withFeedAction(context.Background(), pressAction(previewPress()))
	if err := b.tfSave(ctx, 42, org, tfStepPreview, tfDialog{Kind: string(tfKindDescription), EventID: ev}); err != nil {
		t.Fatal(err)
	}
	if !b.tfMiniappCallback(ctx, 42, 10, from, id, "jwt", []string{"m"}) {
		t.Fatal("the press was not taken")
	}
	if calls.Load() != 1 {
		t.Fatalf("%d codes asked for", calls.Load())
	}
	got := stub.all()
	if len(got) != 1 || got[0].method != "editMessageText" || len(webAppButtons(parseMarkup(t, got[0].markup))) != 1 {
		t.Fatalf("Bot API calls %+v", got)
	}
	if kb := parseMarkup(t, got[0].markup); strings.Contains(got[0].markup, `"tf:m"`) || len(kb.InlineKeyboard) != 3 {
		t.Errorf("the keyboard after the press: %s", got[0].markup)
	}

	// No dialog: the person is told to type again, no code.
	b2, calls2, stub2 := miniappPressBot(t, true, http.StatusCreated, miniappTestLink)
	b2.dialogs = newMemDialogStore(&testClock{t: time.Now()})
	ctx2 := withFeedAction(context.Background(), pressAction(previewPress()))
	b2.tfMiniappCallback(ctx2, 42, 10, from, id, "jwt", []string{"m"})
	if calls2.Load() != 0 {
		t.Errorf("a press with no preview drew %d codes", calls2.Load())
	}
	for _, c := range stub2.all() {
		if len(webAppButtons(parseMarkup(t, c.markup))) != 0 {
			t.Errorf("a web_app button without a preview: %+v", c)
		}
	}

	// A preview of another organization, or one already past its step.
	b3, calls3, _ := miniappPressBot(t, true, http.StatusCreated, miniappTestLink)
	b3.dialogs = newMemDialogStore(&testClock{t: time.Now()})
	ctx3 := withFeedAction(context.Background(), pressAction(previewPress()))
	if err := b3.tfSave(ctx3, 42, uuid.New(), tfStepPreview, tfDialog{Kind: string(tfKindDescription), EventID: ev}); err != nil {
		t.Fatal(err)
	}
	b3.tfMiniappCallback(ctx3, 42, 10, from, id, "jwt", []string{"m"})
	if err := b3.tfSave(ctx3, 42, org, tfStepAwait, tfDialog{Kind: string(tfKindDescription), EventID: ev}); err != nil {
		t.Fatal(err)
	}
	b3.tfMiniappCallback(ctx3, 42, 10, from, id, "jwt", []string{"m"})
	if calls3.Load() != 0 {
		t.Errorf("a foreign or finished preview drew %d codes", calls3.Load())
	}
}

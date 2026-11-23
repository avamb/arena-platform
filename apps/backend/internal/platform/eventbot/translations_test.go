package eventbot

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// trStub is the translations API of arena: it lists what it holds, records the
// PUT and POST calls, and answers the machine route with machineStatus.
type trStub struct {
	mu            sync.Mutex
	machineOn     bool
	machineStatus int
	list          map[string]any
	calls         []trCall
}

type trCall struct {
	method, path string
	body         map[string]any
}

func (s *trStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet:
		s.list["machine_available"] = s.machineOn
		_ = json.NewEncoder(w).Encode(s.list)
	case strings.HasSuffix(r.URL.Path, "/machine"):
		s.calls = append(s.calls, trCall{r.Method, r.URL.Path, body})
		if s.machineStatus != 0 && s.machineStatus != http.StatusAccepted {
			w.WriteHeader(s.machineStatus)
			_, _ = w.Write([]byte(`{"error":{"code":"translation.not_configured","message":"off"}}`))
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"job_id":"j","locales":[]}`))
	default:
		s.calls = append(s.calls, trCall{r.Method, r.URL.Path, body})
		_, _ = w.Write([]byte(`{"locale":"uk","source":"manual","translation_stale":false,"updated_at":"2026-11-22T10:00:00Z"}`))
	}
}

func (s *trStub) recorded() []trCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]trCall(nil), s.calls...)
}

func newTrStub(machine bool) *trStub {
	return &trStub{machineOn: machine, list: map[string]any{
		"content_locale":    "ru",
		"source_hash":       "h",
		"supported_locales": []string{"de", "en", "ru", "uk"},
		"site_locales":      []string{"en"},
		"original":          map[string]any{"name": "Лебединое озеро", "description_html": "<p>Оригинал</p>"},
		"translations": []map[string]any{
			{"locale": "en", "name": "Swan Lake", "short_description": nil, "description_html": "<p>Old English</p>", "report_text_html": nil,
				"source": "machine", "translation_stale": true, "updated_at": "2026-11-20T10:00:00Z"},
			{"locale": "de", "name": nil, "short_description": "Kurz", "description_html": nil, "report_text_html": nil,
				"source": "manual", "translation_stale": false, "updated_at": "2026-11-20T10:00:00Z"},
		},
	}}
}

func trTestBot(t *testing.T, s *trStub) (*Bot, *feedStub) {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(s)
	t.Cleanup(api.Close)
	stub := &feedStub{}
	tgSrv := httptest.NewServer(http.HandlerFunc(stub.serve))
	t.Cleanup(tgSrv.Close)
	tg, err := tgbot.New("123:test", tgbot.WithServerURL(tgSrv.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	feed := newFeedMemory()
	feed.boot -= 10
	return &Bot{
		tg: tg, texts: NewTexts(bundle), arena: NewArenaClient(api.URL, "", nil), feed: feed,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		dialogs: newMemDialogStore(&testClock{t: time.Now()}),
	}, stub
}

func trOwner(org uuid.UUID) *Identity {
	return &Identity{Current: &Membership{OrgID: org, Role: membershipRoleOwner}}
}

// lastMarkup is the keyboard of the last message the bot sent or edited.
func lastCall(t *testing.T, stub *feedStub) (string, []string) {
	t.Helper()
	calls := stub.all()
	if len(calls) == 0 {
		t.Fatal("the bot sent nothing")
	}
	c := calls[len(calls)-1]
	for i := len(calls) - 1; i >= 0; i-- { // a press may end with a call that only retires the old keyboard
		if calls[i].text != "" {
			c = calls[i]
			break
		}
	}
	var kb models.InlineKeyboardMarkup
	if c.markup != "" {
		if err := json.Unmarshal([]byte(c.markup), &kb); err != nil {
			t.Fatal(err)
		}
	}
	var data []string
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			data = append(data, btn.CallbackData)
		}
	}
	return c.text, data
}

func trContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// The edit group offers "Translations" to the people who write the event texts
// and to nobody else.
func TestTranslations_ButtonOnTheEditGroup(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	ev := uuid.New()
	owner, manager, _, agent := cardTestIDs()
	want := "tr:l:" + ev.String()
	f := cardFacts{Status: "published", SalesState: "on_sale", Dates: 1, SingleLive: true}
	for name, id := range map[string]*Identity{"owner": owner, "manager": manager} {
		if got := flat(b.cardGroupRows("en", id, ev, cardGroupEdit, f)); !trContains(got, want) {
			t.Errorf("%s: no translations button in %v", name, got)
		}
	}
	if got := flat(b.cardGroupRows("en", agent, ev, cardGroupEdit, f)); trContains(got, want) {
		t.Errorf("agent sees the translations button: %v", got)
	}
}

// The list shows every language but the original's, with its state, and the
// machine button only when the platform has the model on.
func TestTranslations_ListScreenAndMachineButton(t *testing.T) {
	org, ev := uuid.New(), uuid.New()
	for _, machine := range []bool{true, false} {
		s := newTrStub(machine)
		b, stub := trTestBot(t, s)
		b.trListScreen(context.Background(), 42, nil, trOwner(org), "jwt", ev, "")
		text, data := lastCall(t, stub)
		for _, l := range []string{"de", "en", "uk"} {
			if !trContains(data, "tr:o:"+ev.String()+":"+l) {
				t.Errorf("machine=%v: no button for %s in %v", machine, l, data)
			}
		}
		if trContains(data, "tr:o:"+ev.String()+":ru") {
			t.Errorf("the language of the original is offered: %v", data)
		}
		if got := trContains(data, "tr:m:"+ev.String()); got != machine {
			t.Errorf("machine=%v: machine button present = %v", machine, got)
		}
		if !strings.Contains(text, "Лебединое озеро") && !strings.Contains(text, "Swan") {
			// The bot's own language is English here, the name comes from the original.
			if !strings.Contains(text, "Translations") {
				t.Errorf("list text = %q", text)
			}
		}
		if !strings.Contains(text, "Translations") {
			t.Errorf("list title missing: %q", text)
		}
	}
}

// A language is opened, the person sends the translation, and arena gets a PUT
// with description_html to that locale. The texts the row already had are kept.
func TestTranslations_TypedTextIsStoredAsTheDescription(t *testing.T) {
	org, ev := uuid.New(), uuid.New()
	s := newTrStub(true)
	b, stub := trTestBot(t, s)
	ctx := context.Background()
	id := trOwner(org)
	from := &models.User{ID: 42}

	b.trPress(ctx, 42, 10, from, id, "jwt", []string{"o", ev.String(), "en"})
	text, _ := lastCall(t, stub)
	if !strings.Contains(text, "Old English") {
		t.Errorf("the current translation is not shown: %q", text)
	}
	var dlg trDialog
	if step, found, _, err := b.dialogs.Load(ctx, 42, trDialogKind, &dlg); err != nil || !found || step != trStepAsk || dlg.Locale != "en" || dlg.EventID != ev {
		t.Fatalf("dialog = %+v step=%q found=%v err=%v", dlg, step, found, err)
	}

	b.trTyped(ctx, 42, from, id, "jwt", dlg, "Hello **world**\n\nSecond paragraph")
	calls := s.recorded()
	if len(calls) != 1 {
		t.Fatalf("%d calls: %+v", len(calls), calls)
	}
	c := calls[0]
	if c.method != http.MethodPut || c.path != "/v1/organizations/"+org.String()+"/events/"+ev.String()+"/translations/en" {
		t.Errorf("call = %s %s", c.method, c.path)
	}
	html, _ := c.body["description_html"].(string)
	if !strings.Contains(html, "Hello") || !strings.Contains(html, "<p>") || !strings.Contains(html, "Second paragraph") {
		t.Errorf("description_html = %q", html)
	}
	if c.body["name"] != "Swan Lake" {
		t.Errorf("the name of the row is not kept: %v", c.body)
	}
	if _, found, _, _ := b.dialogs.Load(ctx, 42, trDialogKind, nil); found {
		t.Error("the dialog stays open after the save")
	}
}

// A language with no translation shows the original and asks for the text.
func TestTranslations_LanguageWithoutTranslationShowsTheOriginal(t *testing.T) {
	org, ev := uuid.New(), uuid.New()
	s := newTrStub(true)
	b, stub := trTestBot(t, s)
	b.trPress(context.Background(), 42, 10, &models.User{ID: 42}, trOwner(org), "jwt", []string{"o", ev.String(), "uk"})
	text, data := lastCall(t, stub)
	if !strings.Contains(text, "Оригинал") || !trContains(data, "tr:l:"+ev.String()) {
		t.Errorf("text = %q, buttons = %v", text, data)
	}
}

// A text the bot will not send (empty after trimming) keeps the dialog open and
// calls nothing.
func TestTranslations_RefusedTextKeepsTheDialog(t *testing.T) {
	org, ev := uuid.New(), uuid.New()
	s := newTrStub(true)
	b, _ := trTestBot(t, s)
	ctx := context.Background()
	from := &models.User{ID: 42}
	b.trPress(ctx, 42, 10, from, trOwner(org), "jwt", []string{"o", ev.String(), "uk"})
	var dlg trDialog
	_, _, _, _ = b.dialogs.Load(ctx, 42, trDialogKind, &dlg)
	b.trTyped(ctx, 42, from, trOwner(org), "jwt", dlg, "   ")
	if n := len(s.recorded()); n != 0 {
		t.Errorf("%d calls for an empty text", n)
	}
	if _, found, _, _ := b.dialogs.Load(ctx, 42, trDialogKind, nil); !found {
		t.Error("the dialog ended on a refused text")
	}
}

// The machine asks for the languages that have no translation at all: a manual
// or an older machine text is never in the request.
func TestTranslations_MachineAsksOnlyForMissingLanguages(t *testing.T) {
	org, ev := uuid.New(), uuid.New()
	s := newTrStub(true)
	b, _ := trTestBot(t, s)
	b.trPress(context.Background(), 42, 10, &models.User{ID: 42}, trOwner(org), "jwt", []string{"m", ev.String()})
	calls := s.recorded()
	if len(calls) != 1 || calls[0].method != http.MethodPost || !strings.HasSuffix(calls[0].path, "/translations/machine") {
		t.Fatalf("calls = %+v", calls)
	}
	got, _ := calls[0].body["locales"].([]any)
	if len(got) != 1 || got[0] != "uk" {
		t.Errorf("locales = %v, want [uk]", got)
	}
}

// With the model off, the press asks for nothing and the person is told so. A
// 503 from the route is the same answer, not an error.
func TestTranslations_MachineOffAnd503(t *testing.T) {
	org, ev := uuid.New(), uuid.New()
	// The list says it is off: no call at all.
	off := newTrStub(false)
	b, stub := trTestBot(t, off)
	b.trPress(context.Background(), 42, 10, &models.User{ID: 42}, trOwner(org), "jwt", []string{"m", ev.String()})
	if n := len(off.recorded()); n != 0 {
		t.Errorf("%d machine calls with the model off", n)
	}
	text, data := lastCall(t, stub)
	if !strings.Contains(text, "off") || trContains(data, "tr:m:"+ev.String()) {
		t.Errorf("text = %q, buttons = %v", text, data)
	}
	// The list says it is on, the route answers 503.
	on := newTrStub(true)
	on.machineStatus = http.StatusServiceUnavailable
	b2, stub2 := trTestBot(t, on)
	b2.trPress(context.Background(), 42, 10, &models.User{ID: 42}, trOwner(org), "jwt", []string{"m", ev.String()})
	text2, _ := lastCall(t, stub2)
	if !strings.Contains(text2, "off") {
		t.Errorf("503 text = %q", text2)
	}
}

// A role that may not change texts gets the refusal and no call.
func TestTranslations_NoRightsNoCall(t *testing.T) {
	ev := uuid.New()
	s := newTrStub(true)
	b, _ := trTestBot(t, s)
	agent := &Identity{Current: &Membership{OrgID: uuid.New(), Role: "agent"}}
	b.trPress(context.Background(), 42, 10, &models.User{ID: 42}, agent, "jwt", []string{"m", ev.String()})
	if n := len(s.recorded()); n != 0 {
		t.Errorf("%d calls for a role without rights", n)
	}
}

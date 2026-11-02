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

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// conflictFixture is a bot whose arena-api serves the ONE event an edit draft
// changes, as somebody else may have left it, or fails to read it. Every
// other request is a write and is only counted: none may happen here.
type conflictFixture struct {
	b      *Bot
	stub   *feedStub
	r      *wizardRun
	d      *Draft
	mu     sync.Mutex
	ev     openapi.EventItem
	fail   bool
	writes int
}

// newConflictFixture loads an event into an edit draft, changes its age and
// opens the preview of «Publish changes». The event arena serves is, until a
// test changes it, exactly what the draft loaded.
func newConflictFixture(t *testing.T) *conflictFixture {
	t.Helper()
	r, d := loadedWith(t, artDescription, "art")
	f := &conflictFixture{r: r, d: d, stub: &feedStub{}}
	f.ev = editorSaved(d, artDescription)
	stamp, err := time.Parse(time.RFC3339Nano, d.Event.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	f.ev.UpdatedAt = stamp
	if d.Event.PosterMediaID != "" {
		p := uuid.MustParse(d.Event.PosterMediaID)
		f.ev.PosterMediaId = &p
	}

	r.press("e:age", stEvAge)
	r.press("age:18+", stEditMenu)
	r.press("publish", stPreview)

	arena := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if req.Method != http.MethodGet {
			f.writes++
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"code":"internal","message":"no writes here"}}`)
			return
		}
		if f.fail || !strings.HasPrefix(req.URL.Path, "/v1/events/") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"code":"internal","message":"x"}}`)
			return
		}
		_ = json.NewEncoder(w).Encode(openapi.EventEnvelope{Event: f.ev})
	}))
	t.Cleanup(arena.Close)
	tgSrv := httptest.NewServer(http.HandlerFunc(f.stub.serve))
	t.Cleanup(tgSrv.Close)
	tg, err := tgbot.New("123:test", tgbot.WithServerURL(tgSrv.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	f.b = &Bot{
		tg:     tg,
		texts:  r.w.texts,
		wizard: r.w,
		arena:  NewArenaClient(arena.URL, "svc", arena.Client()),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return f
}

// changedElsewhere makes the event as somebody else saved it a minute later.
func (f *conflictFixture) changedElsewhere(edit func(ev *openapi.EventItem)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	edit(&f.ev)
	f.ev.UpdatedAt = f.ev.UpdatedAt.Add(time.Minute)
}

func (f *conflictFixture) last(t *testing.T) feedCall {
	t.Helper()
	calls := f.stub.all()
	if len(calls) == 0 {
		t.Fatal("the bot sent nothing")
	}
	return calls[len(calls)-1]
}

// Somebody renamed the event in the web event center while the manager edited
// the age: the PREVIEW says so, names the change, still shows what the draft
// would send, and offers «Save anyway» instead of its own «Publish».
func TestConflict_PreviewSaysTheEventWasChangedElsewhere(t *testing.T) {
	f := newConflictFixture(t)
	old := f.d.Event.Name
	f.changedElsewhere(func(ev *openapi.EventItem) { ev.Name = "Переименовали в вебе" })

	f.b.wizardRender(context.Background(), 42, nil, f.r.ws, f.d, "")

	got := f.last(t)
	for _, want := range []string{
		"меняли в другом месте",
		"Название: «" + old + "» → «Переименовали в вебе»",
		"Что уйдёт на сайт",
		"Возраст: 16+ → 18+",
		"«Всё равно сохранить» запишет вашу версию",
	} {
		if !strings.Contains(got.text, want) {
			t.Errorf("the preview lacks %q:\n%s", want, got.text)
		}
	}
	if strings.Contains(got.text, "Нажмите «Опубликовать»") {
		t.Errorf("the preview still points at «Publish»:\n%s", got.text)
	}
	for _, want := range []string{"wz:publish:force", "wz:cancel", "wz:e:home"} {
		if !strings.Contains(got.markup, want) {
			t.Errorf("the buttons lack %q: %s", want, got.markup)
		}
	}
	if strings.Contains(got.markup, "wz:publish:go") {
		t.Errorf("the preview kept its «Publish» button: %s", got.markup)
	}
	if f.writes != 0 {
		t.Errorf("the preview wrote %d times", f.writes)
	}
}

// Only updated_at moved (dates or prices changed): the preview still warns and
// says that the parts it can compare are as loaded.
func TestConflict_PreviewNamesAnUnseenChange(t *testing.T) {
	f := newConflictFixture(t)
	f.changedElsewhere(func(*openapi.EventItem) {})

	f.b.wizardRender(context.Background(), 42, nil, f.r.ws, f.d, "")

	got := f.last(t).text
	if !strings.Contains(got, "меняли в другом месте") || !strings.Contains(got, "например даты или цены") {
		t.Fatalf("the preview:\n%s", got)
	}
}

// Nothing changed elsewhere: the preview is the usual one.
func TestConflict_PreviewUnchanged(t *testing.T) {
	f := newConflictFixture(t)

	f.b.wizardRender(context.Background(), 42, nil, f.r.ws, f.d, "")

	got := f.last(t)
	if strings.Contains(got.text, "меняли в другом месте") || strings.Contains(got.text, "Не удалось проверить") {
		t.Fatalf("a warning on an unchanged event:\n%s", got.text)
	}
	if !strings.Contains(got.markup, "wz:publish:go") {
		t.Fatalf("the preview lost «Publish»: %s", got.markup)
	}
}

// The mini app saved the description: the sync adopts it with the new
// updated_at, and the preview does not take the organizer's own save for a
// change made elsewhere.
func TestConflict_PreviewAfterAMiniAppSave(t *testing.T) {
	f := newConflictFixture(t)
	f.changedElsewhere(func(ev *openapi.EventItem) {
		s := "<p>Из редактора</p>"
		ev.Description = &s
	})
	f.mu.Lock()
	ev := f.ev
	f.mu.Unlock()
	if !f.d.adoptEditorDescription(ev) {
		t.Fatal("the editor's description was not adopted")
	}

	f.b.wizardRender(context.Background(), 42, nil, f.r.ws, f.d, "")

	if got := f.last(t).text; strings.Contains(got, "меняли в другом месте") {
		t.Fatalf("the preview warned about the organizer's own save:\n%s", got)
	}
}

// The event cannot be read when the preview opens: the preview is drawn with a
// note, because the save checks again.
func TestConflict_PreviewCheckFailedIsANote(t *testing.T) {
	f := newConflictFixture(t)
	f.fail = true

	f.b.wizardRender(context.Background(), 42, nil, f.r.ws, f.d, "")

	got := f.last(t)
	if !strings.Contains(got.text, "Не удалось проверить") || !strings.Contains(got.text, "Что уйдёт на сайт") {
		t.Fatalf("the preview:\n%s", got.text)
	}
	if !strings.Contains(got.markup, "wz:publish:go") {
		t.Fatalf("the preview lost «Publish»: %s", got.markup)
	}
}

// The check cannot read the event when the save starts: nothing is saved, and
// the person may retry or save anyway.
func TestConflict_SaveRefusedWhenTheCheckFails(t *testing.T) {
	f := newConflictFixture(t)
	f.fail = true

	f.b.wizardSave(context.Background(), 42, nil, &models.User{ID: 42}, f.r.ws, f.d, uuid.NewString(), false)

	got := f.last(t)
	if !strings.Contains(got.text, "ничего не сохранили") || strings.Contains(got.text, "Сохраняю") {
		t.Fatalf("the answer:\n%s", got.text)
	}
	for _, want := range []string{"wz:retry", "wz:publish:force", "wz:cancel"} {
		if !strings.Contains(got.markup, want) {
			t.Errorf("the buttons lack %q: %s", want, got.markup)
		}
	}
	if f.writes != 0 {
		t.Fatalf("the save went on: %d writes", f.writes)
	}
}

// A change made between the preview and the press is still caught by the save,
// with what changed.
func TestConflict_SaveRefusedWhenChangedAfterThePreview(t *testing.T) {
	f := newConflictFixture(t)
	f.changedElsewhere(func(ev *openapi.EventItem) { ev.AgeRating = strp("12+") })

	f.b.wizardSave(context.Background(), 42, nil, &models.User{ID: 42}, f.r.ws, f.d, uuid.NewString(), false)

	got := f.last(t)
	if !strings.Contains(got.text, "меняли в другом месте") || !strings.Contains(got.text, "Возраст: 16+ → 12+") {
		t.Fatalf("the answer:\n%s", got.text)
	}
	if !strings.Contains(got.markup, "wz:publish:force") || !strings.Contains(got.markup, "wz:cancel") {
		t.Fatalf("the buttons: %s", got.markup)
	}
	if f.writes != 0 {
		t.Fatalf("the save went on: %d writes", f.writes)
	}
}

// Each part the event carries is named when it alone differs.
func TestElsewhereChanges_EachPart(t *testing.T) {
	_, d := loadedWith(t, artDescription, "art")
	base := editorSaved(d, artDescription)
	if got := elsewhereChanges(base, d.Loaded.Event); len(got) != 0 {
		t.Fatalf("the loaded event differs from itself: %v", got)
	}
	p := uuid.New()
	other := "<p>Другое</p>"
	for want, edit := range map[string]func(ev *openapi.EventItem){
		"name":        func(ev *openapi.EventItem) { ev.Name = "Другое" },
		"description": func(ev *openapi.EventItem) { ev.Description = &other },
		"poster":      func(ev *openapi.EventItem) { ev.PosterMediaId = &p },
		"age":         func(ev *openapi.EventItem) { ev.AgeRating = strp("0+") },
		"promoter":    func(ev *openapi.EventItem) { ev.PromoterId = &p },
	} {
		ev := base
		edit(&ev)
		if got := elsewhereChanges(ev, d.Loaded.Event); len(got) != 1 || got[0] != want {
			t.Errorf("%s: got %v", want, got)
		}
	}
}

// The new texts render in every language of the bot.
func TestConflict_TextsInEveryLanguage(t *testing.T) {
	f := newConflictFixture(t)
	f.changedElsewhere(func(ev *openapi.EventItem) { ev.Name = "X" })
	for _, loc := range SupportedLocales {
		ws := f.r.ws
		ws.Locale = loc
		f.mu.Lock()
		ev := f.ev
		f.mu.Unlock()
		text := f.b.conflictPreviewText(ws, f.d, ev, "")
		if strings.Contains(text, "bot.wz.") || strings.Contains(text, "<no value>") {
			t.Errorf("%s: a key or an empty value:\n%s", loc, text)
		}
		failed := f.b.texts.T(loc, "bot.wz.edit_check_failed", map[string]any{"Name": "X"})
		if strings.Contains(failed, "bot.wz.") || !strings.Contains(failed, "X") {
			t.Errorf("%s: check failed text %q", loc, failed)
		}
	}
}

// Coming back to a preview reconciles the draft with a mini app save first,
// because the preview checks for a change made elsewhere when it is drawn.
func TestEditorSyncData_ResumeOfThePreview(t *testing.T) {
	_, d := loadedWith(t, artDescription, "art")
	d.Step = stPreview
	if !editorSyncData(d, "resume") {
		t.Error("resume on the preview does not sync")
	}
	d.Step = stEditMenu
	if editorSyncData(d, "resume") {
		t.Error("resume on the card syncs")
	}
}

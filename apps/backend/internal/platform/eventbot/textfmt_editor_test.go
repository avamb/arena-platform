package eventbot

import (
	"context"
	"fmt"
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

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

// editorFixture is a bot whose arena-api serves ONE event and whose texts the
// test can change, as the mini app editor does.
type editorFixture struct {
	b      *Bot
	stub   *feedStub
	org    uuid.UUID
	event  uuid.UUID
	id     *Identity
	from   *models.User
	mu     sync.Mutex
	desc   string
	descFm string
	note   string
	fail   bool
}

func (f *editorFixture) set(desc, note string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.desc, f.descFm, f.note = desc, "art", note
}

func newEditorFixture(t *testing.T) *editorFixture {
	t.Helper()
	f := &editorFixture{org: uuid.New(), event: uuid.New(), from: &models.User{ID: 42}}
	f.set("<p>Old description</p>", "<p>Old note</p>")
	arena := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case f.fail:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"code":"internal","message":"x"}}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/events"):
			note := "null"
			if f.note != "" {
				note = fmt.Sprintf("%q", f.note)
			}
			_, _ = fmt.Fprintf(w, `{"events":[{"id":%q,"org_id":%q,"name":"Concert","description":%q,"description_format":%q,"email_note":%s,"email_note_format":"art"}]}`,
				f.event, f.org, f.desc, f.descFm, note)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/web-login-codes"):
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"url":"https://events.example.test/tg/edit#code=abc&kind=description&event=x","expires_at":"2026-10-25T12:00:00Z"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"x","message":"x"}}`)
		}
	}))
	t.Cleanup(arena.Close)
	f.stub = &feedStub{}
	tgSrv := httptest.NewServer(http.HandlerFunc(f.stub.serve))
	t.Cleanup(tgSrv.Close)
	tg, err := tgbot.New("123:test", tgbot.WithServerURL(tgSrv.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	f.b = &Bot{
		tg:            tg,
		texts:         NewTexts(bundle),
		arena:         NewArenaClient(arena.URL, "svc", arena.Client()),
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		miniappEditor: true,
		dialogs:       newMemDialogStore(&testClock{t: time.Now()}),
	}
	f.id = &Identity{Link: gen.BotTelegramLinkRow{Locale: "ru"}, Current: &Membership{OrgID: f.org, Role: membershipRoleOwner}}
	return f
}

const editorNoteRU = "Изменения из редактора сохранены."

func (f *editorFixture) sentText() string {
	var all []string
	for _, c := range f.stub.all() {
		all = append(all, c.text)
	}
	return strings.Join(all, "\n---\n")
}

// The press under a preview keeps what the event holds; a press that follows
// when nothing was saved goes on as it did, and a press after the editor saved
// says so ONCE, draws the saved text and does not carry the press out.
func TestEditorSave_PreviewSaysItOnceAndShowsTheSavedText(t *testing.T) {
	f := newEditorFixture(t)
	ctx := context.Background()
	ev := f.event.String()
	if err := f.b.tfSave(ctx, 42, f.org, tfStepPreview, tfDialog{Kind: string(tfKindDescription), EventID: ev, Text: "typed", Art: "<p>typed</p>", Applied: true}); err != nil {
		t.Fatal(err)
	}
	pressed := press(10, "Here is the text", "tf:m", nil)
	if !f.b.tfMiniappCallback(withFeedAction(ctx, pressAction(pressed)), 42, 10, f.from, f.id, "jwt", []string{"m"}) {
		t.Fatal("the press was not taken")
	}
	dlg, step, found, _ := f.b.tfLoad(ctx, 42)
	if !found || step != tfStepPreview || !dlg.Editor || dlg.Seen != editorFingerprint("<p>Old description</p>") || dlg.Art != "<p>typed</p>" {
		t.Fatalf("after the press: %+v step %q", dlg, step)
	}

	// Nothing was saved in the editor: the press goes on (not taken here).
	f.stub.reset()
	if f.b.tfEditorSavedPreview(ctx, 42, 10, f.from, f.id, "jwt", dlg) {
		t.Fatal("a press with no save in the editor was taken")
	}
	if strings.Contains(f.sentText(), editorNoteRU) {
		t.Fatal("the note was said without a save")
	}

	// The editor saved.
	f.set("<p>From the <strong>editor</strong></p>", "")
	f.stub.reset()
	if !f.b.tfEditorSavedPreview(ctx, 42, 10, f.from, f.id, "jwt", dlg) {
		t.Fatal("the press after a save was not taken")
	}
	calls := f.stub.all()
	if len(calls) < 2 || !strings.Contains(calls[0].text, editorNoteRU) {
		t.Fatalf("the pressed message did not say the note first: %+v", calls)
	}
	if !strings.Contains(f.sentText(), "editor") || !strings.Contains(calls[len(calls)-1].markup, "tf:s") {
		t.Fatalf("the saved text and its buttons were not drawn: %+v", calls)
	}
	dlg, step, _, _ = f.b.tfLoad(ctx, 42)
	if step != tfStepPreview || dlg.Editor || dlg.Seen != "" || dlg.Art != "<p>From the <strong>editor</strong></p>" || !dlg.Applied || dlg.Text != richtext.ToLegacy(dlg.Art) {
		t.Fatalf("dialog after the note: %+v step %q", dlg, step)
	}
	// ONCE: the next press belongs to the organizer again.
	if f.b.tfEditorSavedPreview(ctx, 42, 10, f.from, f.id, "jwt", dlg) {
		t.Fatal("the note was said twice")
	}
}

// A letter text deleted in the editor leaves nothing to preview: the screen of
// the letter text comes back, with the note.
func TestEditorSave_PreviewOfALetterTextDeletedInTheEditor(t *testing.T) {
	f := newEditorFixture(t)
	ctx := context.Background()
	ev := f.event.String()
	if err := f.b.tfSave(ctx, 42, f.org, tfStepPreview, tfDialog{Kind: string(tfKindEmailNote), EventID: ev, Text: "typed", Art: "<p>typed</p>", Applied: true}); err != nil {
		t.Fatal(err)
	}
	f.b.tfMiniappCallback(withFeedAction(ctx, pressAction(press(10, "Here", "tf:m", nil))), 42, 10, f.from, f.id, "jwt", []string{"m"})
	dlg, _, _, _ := f.b.tfLoad(ctx, 42)
	if !dlg.Editor {
		t.Fatalf("no mark: %+v", dlg)
	}
	f.set("<p>Old description</p>", "")
	f.stub.reset()
	if !f.b.tfEditorSavedPreview(ctx, 42, 10, f.from, f.id, "jwt", dlg) {
		t.Fatal("not taken")
	}
	if _, _, found, _ := f.b.tfLoad(ctx, 42); found {
		t.Fatal("the dialog stayed after the text was deleted")
	}
	out := f.sentText()
	if !strings.Contains(out, editorNoteRU) || !strings.Contains(out, "Текст в письме") {
		t.Fatalf("screen: %s", out)
	}
}

// The letter screen has no dialog: the press opens one of the step "editor",
// the next press says it once and draws the saved text.
func TestEditorSave_LetterScreen(t *testing.T) {
	f := newEditorFixture(t)
	ctx := context.Background()
	ev := f.event.String()
	if !f.b.tfMiniappCallback(withFeedAction(ctx, pressAction(noteScreenPress(ev))), 42, 10, f.from, f.id, "jwt", []string{"m", ev}) {
		t.Fatal("the press was not taken")
	}
	dlg, step, found, _ := f.b.tfLoad(ctx, 42)
	if !found || step != tfStepEditor || !dlg.Editor || dlg.EventID != ev || dlg.Kind != string(tfKindEmailNote) || dlg.Seen != editorFingerprint("<p>Old note</p>") {
		t.Fatalf("dialog after the press: %+v step %q", dlg, step)
	}
	if f.b.tfEditorSavedNote(ctx, 42, 10, f.from, f.id, "jwt", f.event) {
		t.Fatal("taken without a save")
	}
	// The editor saved a note: the next press (any button) says it once.
	f.set("<p>Old description</p>", "<p>New <strong>note</strong></p>")
	f.stub.reset()
	if !f.b.tfEditorSavedNote(ctx, 42, 10, f.from, f.id, "jwt", f.event) {
		t.Fatal("not taken after a save")
	}
	out := f.sentText()
	if !strings.Contains(out, editorNoteRU) || !strings.Contains(out, "note") || strings.Contains(out, "Old note") {
		t.Fatalf("screen: %s", out)
	}
	if _, _, found, _ := f.b.tfLoad(ctx, 42); found {
		t.Fatal("the marker stayed: the note would be said twice")
	}
	if f.b.tfEditorSavedNote(ctx, 42, 10, f.from, f.id, "jwt", f.event) {
		t.Fatal("said twice")
	}
}

// A press on an older letter screen never replaces the preview the organizer
// is working in.
func TestEditorSave_MarkKeepsAPreviewInProgress(t *testing.T) {
	f := newEditorFixture(t)
	ctx := context.Background()
	ev := f.event.String()
	if err := f.b.tfSave(ctx, 42, f.org, tfStepPreview, tfDialog{Kind: string(tfKindDescription), EventID: uuid.NewString(), Art: "<p>mine</p>"}); err != nil {
		t.Fatal(err)
	}
	f.b.tfMiniappCallback(withFeedAction(ctx, pressAction(noteScreenPress(ev))), 42, 10, f.from, f.id, "jwt", []string{"m", ev})
	dlg, step, _, _ := f.b.tfLoad(ctx, 42)
	if step != tfStepPreview || dlg.Art != "<p>mine</p>" || dlg.Editor {
		t.Fatalf("the preview was replaced: %+v step %q", dlg, step)
	}
}

// A typed text under the preview is not blocked: it is preceded by the note,
// once.
func TestEditorSave_TypedTextGetsTheNoteAndGoesOn(t *testing.T) {
	f := newEditorFixture(t)
	ctx := context.Background()
	dlg := tfDialog{Kind: string(tfKindDescription), EventID: f.event.String(), Editor: true, Seen: editorFingerprint("<p>Old description</p>")}
	if got := f.b.tfEditorSavedTyped(ctx, 42, f.id, "jwt", dlg); !got.Editor || len(f.stub.all()) != 0 {
		t.Fatalf("no save, yet: %+v %v", got, f.stub.all())
	}
	f.set("<p>Saved in the editor</p>", "")
	got := f.b.tfEditorSavedTyped(ctx, 42, f.id, "jwt", dlg)
	if got.Editor || got.Seen != "" {
		t.Fatalf("the marker stayed: %+v", got)
	}
	if calls := f.stub.all(); len(calls) != 1 || calls[0].method != "sendMessage" || !strings.Contains(calls[0].text, editorNoteRU) {
		t.Fatalf("calls: %+v", calls)
	}
}

// A failed read says nothing and blocks nothing; a read that fails at the press
// leaves no mark.
func TestEditorSave_FailedReadChangesNothing(t *testing.T) {
	f := newEditorFixture(t)
	ctx := context.Background()
	dlg := tfDialog{Kind: string(tfKindDescription), EventID: f.event.String(), Editor: true, Seen: editorFingerprint("<p>x</p>")}
	f.mu.Lock()
	f.fail = true
	f.mu.Unlock()
	if _, changed := f.b.tfEditorChanged(ctx, f.id, "jwt", dlg); changed {
		t.Fatal("a failed read counted as a save")
	}
	if err := f.b.tfSave(ctx, 42, f.org, tfStepPreview, tfDialog{Kind: string(tfKindDescription), EventID: f.event.String(), Art: "<p>a</p>"}); err != nil {
		t.Fatal(err)
	}
	f.b.tfEditorMark(ctx, f.from, f.id, "jwt", tfKindDescription, f.event.String(), true)
	if got, _, _, _ := f.b.tfLoad(ctx, 42); got.Editor {
		t.Fatalf("a mark without a read: %+v", got)
	}
}

// A description kept in the old notation and the same words written as ART are
// one text: the editor rewriting a legacy row without a word changed is no
// change.
func TestEditorSave_LegacyAndArtOfTheSameWordsAreOneText(t *testing.T) {
	f := newEditorFixture(t)
	f.mu.Lock()
	f.desc, f.descFm = "Hello **world**", "text"
	f.mu.Unlock()
	ctx := context.Background()
	ev, err := f.b.eventOf(ctx, "jwt", f.org, f.event)
	if err != nil {
		t.Fatal(err)
	}
	legacy := descriptionART(*ev)
	if legacy != "<p>Hello <strong>world</strong></p>" {
		t.Fatalf("legacy as ART: %q", legacy)
	}
	dlg := tfDialog{Kind: string(tfKindDescription), EventID: f.event.String(), Editor: true, Seen: editorFingerprint(legacy)}
	f.set("<p>Hello <strong>world</strong></p>", "")
	if _, changed := f.b.tfEditorChanged(ctx, f.id, "jwt", dlg); changed {
		t.Fatal("the same words as ART counted as a save")
	}
}

// The key exists with text in every language of the bot.
func TestEditorSave_NoteInEveryLanguage(t *testing.T) {
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	texts := NewTexts(bundle)
	listed := false
	for _, k := range MessageKeys {
		listed = listed || k == "bot.tf.editor_saved"
	}
	if !listed {
		t.Error("bot.tf.editor_saved is not in MessageKeys")
	}
	for _, loc := range SupportedLocales {
		if got := texts.T(loc, "bot.tf.editor_saved", nil); got == "" || got == "bot.tf.editor_saved" {
			t.Errorf("%s: %q", loc, got)
		}
	}
	if got := texts.T("ru", "bot.tf.editor_saved", nil); got != editorNoteRU {
		t.Errorf("ru = %q", got)
	}
}

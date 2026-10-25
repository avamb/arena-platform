package eventbot

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func editDraftAtDescription(eventID string) *Draft {
	d := NewDraft()
	d.Mode, d.Step = ModeEdit, stXDescription
	d.Event.EventID = eventID
	return d
}

// The description question of an EXISTING event gets the editor button above
// the navigation row; the create wizard, other questions, a group chat, the
// flag off and a platform superadmin get none.
func TestDescriptionQuestion_EditorButtonGates(t *testing.T) {
	ev := uuid.NewString()
	kb := func() *models.InlineKeyboardMarkup {
		return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "Skip", CallbackData: "wz:skip"}},
			{{Text: "Back", CallbackData: "wz:e:home"}},
		}}
	}
	b, calls, _ := miniappTestBot(t, true, http.StatusCreated, miniappTestLink)
	ws := WizSession{Locale: "ru"}

	got := b.withDescriptionEditor(42, ws, editDraftAtDescription(ev), kb())
	if len(got.InlineKeyboard) != 3 {
		t.Fatalf("rows = %d, want 3", len(got.InlineKeyboard))
	}
	btn := got.InlineKeyboard[1][0]
	if btn.CallbackData != "tf:md:"+ev || len(btn.CallbackData) > 64 || btn.WebApp != nil || btn.Text != "✏️ Править в редакторе" {
		t.Errorf("editor button = %+v", btn)
	}
	if got.InlineKeyboard[2][0].CallbackData != "wz:e:home" || got.InlineKeyboard[0][0].CallbackData != "wz:skip" {
		t.Errorf("other rows moved: %+v", got.InlineKeyboard)
	}
	if only := b.withDescriptionEditor(42, ws, editDraftAtDescription(ev), nil); only == nil || len(only.InlineKeyboard) != 1 {
		t.Errorf("a screen with no buttons: %+v", only)
	}

	same := func(name string, got *models.InlineKeyboardMarkup) {
		t.Helper()
		if len(got.InlineKeyboard) != 2 {
			t.Errorf("%s: a button was added: %+v", name, got.InlineKeyboard)
		}
	}
	create := editDraftAtDescription(ev)
	create.Mode = ModeCreate
	same("create wizard", b.withDescriptionEditor(42, ws, create, kb()))
	create.Event.EventID = ""
	same("create wizard, no event", b.withDescriptionEditor(42, ws, create, kb()))
	other := editDraftAtDescription(ev)
	other.Step = stXCurrency
	same("another question", b.withDescriptionEditor(42, ws, other, kb()))
	same("group chat", b.withDescriptionEditor(-100123, ws, editDraftAtDescription(ev), kb()))
	same("no event id", b.withDescriptionEditor(42, ws, editDraftAtDescription(""), kb()))
	same("bad event id", b.withDescriptionEditor(42, ws, editDraftAtDescription("x"), kb()))
	su := ws
	su.Superadmin = true
	same("superadmin", b.withDescriptionEditor(42, su, editDraftAtDescription(ev), kb()))
	off, _, _ := miniappTestBot(t, false, http.StatusCreated, miniappTestLink)
	same("flag off", off.withDescriptionEditor(42, ws, editDraftAtDescription(ev), kb()))
	if calls.Load() != 0 {
		t.Errorf("%d codes asked for while drawing", calls.Load())
	}
}

// wizardSession carries the platform role to the keyboard.
func TestWizardSession_CarriesSuperadmin(t *testing.T) {
	b := &Bot{}
	id := &Identity{Superadmin: true, Current: &Membership{}}
	if !b.wizardSession(id, "jwt").Superadmin {
		t.Error("the session lost the platform role")
	}
}

func descriptionQuestionPress(ev string) *models.CallbackQuery {
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "Skip", CallbackData: "wz:skip"}},
		{{Text: "✏️ Edit in the editor", CallbackData: "tf:md:" + ev}},
		{{Text: "Back", CallbackData: "wz:e:home"}},
	}}
	return press(10, "Description", "tf:md:"+ev, kb)
}

// The press draws a code for that event (kind description), turns that button
// into a web_app button and keeps the other buttons.
func TestMiniappPress_DescriptionQuestion(t *testing.T) {
	ev := uuid.NewString()
	b, calls, stub := miniappPressBot(t, true, http.StatusCreated, miniappTestLink)
	ctx := withFeedAction(context.Background(), pressAction(descriptionQuestionPress(ev)))
	if !b.tfMiniappCallback(ctx, 42, 10, &models.User{ID: 42}, ownerIdentity(), "jwt", []string{"md", ev}) {
		t.Fatal("the press was not taken")
	}
	if calls.Load() != 1 {
		t.Fatalf("%d codes asked for", calls.Load())
	}
	got := stub.all()
	if len(got) != 1 || got[0].method != "editMessageText" || got[0].msgID != 10 {
		t.Fatalf("Bot API calls %+v", got)
	}
	kb := parseMarkup(t, got[0].markup)
	apps := webAppButtons(kb)
	if len(apps) != 1 || apps[0].WebApp.URL != miniappTestLink {
		t.Fatalf("web_app buttons %+v", apps)
	}
	var data []string
	for _, btn := range flatten(kb) {
		if btn.CallbackData != "" {
			data = append(data, btn.CallbackData)
		}
	}
	if strings.Join(data, ",") != "wz:skip,wz:e:home" {
		t.Errorf("other buttons = %v", data)
	}
}

// Gates of the press: a role that may not change texts, a superadmin, the flag
// turned off, a malformed button. None of them asks for a code.
func TestMiniappPress_DescriptionQuestionRefusals(t *testing.T) {
	ev := uuid.NewString()
	from := &models.User{ID: 42}
	b, calls, _ := miniappPressBot(t, true, http.StatusCreated, miniappTestLink)
	ctx := withFeedAction(context.Background(), pressAction(descriptionQuestionPress(ev)))
	agent := &Identity{Current: &Membership{Role: "agent"}}
	if !b.tfMiniappCallback(ctx, 42, 10, from, agent, "jwt", []string{"md", ev}) || calls.Load() != 0 {
		t.Errorf("a role without rights: %d codes", calls.Load())
	}
	su := &Identity{Superadmin: true, Current: &Membership{Role: membershipRoleOwner}}
	if !b.tfMiniappCallback(ctx, 42, 10, from, su, "jwt", []string{"md", ev}) || calls.Load() != 0 {
		t.Errorf("superadmin: %d codes", calls.Load())
	}
	if !b.tfMiniappCallback(ctx, 42, 10, from, ownerIdentity(), "jwt", []string{"md"}) || calls.Load() != 0 {
		t.Errorf("a button without an event: %d codes", calls.Load())
	}
	if !b.tfMiniappCallback(ctx, 42, 10, from, ownerIdentity(), "jwt", []string{"md", "not-a-uuid"}) || calls.Load() != 0 {
		t.Errorf("a bad event id: %d codes", calls.Load())
	}
	off, offCalls, _ := miniappPressBot(t, false, http.StatusCreated, miniappTestLink)
	if !off.tfMiniappCallback(ctx, 42, 10, from, ownerIdentity(), "jwt", []string{"md", ev}) || offCalls.Load() != 0 {
		t.Errorf("flag off: %d codes", offCalls.Load())
	}
}

// editorSaved is the event as arena-api answers it after the mini app saved
// art as the description, with updated_at moved on.
func editorSaved(d *Draft, art string) openapi.EventItem {
	fm := openapi.EventItemDescriptionFormat("art")
	ev := openapi.EventItem{
		Id: uuid.MustParse(d.Event.EventID), Name: d.Event.Name, AgeRating: strp(d.Event.Age),
		Description: strp(art), DescriptionFormat: &fm,
		UpdatedAt: time.Date(2026, 10, 25, 9, 30, 0, 0, time.UTC),
	}
	if d.Event.PromoterID != "" {
		p := uuid.MustParse(d.Event.PromoterID)
		ev.PromoterId = &p
	}
	return ev
}

// The data-safety case: the organizer opens the editor from the description
// question, saves there, goes back to the card and publishes ANOTHER change.
// The old description is not written back, and their own save does not trip
// the «changed elsewhere» check.
func TestEditorDescription_SavedInTheMiniAppIsNeverWrittenBack(t *testing.T) {
	const saved = "<p>Новый текст из редактора</p>"
	r, d := loadedWith(t, artDescription, "art")
	r.press("e:desc", stXDescription)
	oldStamp := d.Event.UpdatedAt

	ev := editorSaved(d, saved)
	if !d.adoptEditorDescription(ev) {
		t.Fatal("the saved description was not adopted")
	}
	if d.Event.Description != saved || d.Loaded.Event.Description != saved || d.Event.DescriptionFormat != "art" {
		t.Fatalf("draft = %q / %q, loaded = %q", d.Event.Description, d.Event.DescriptionFormat, d.Loaded.Event.Description)
	}
	if d.descriptionChanged() {
		t.Error("the adopted description counts as the organizer's change")
	}
	for _, f := range d.Changed {
		if f == "description" {
			t.Errorf("description is marked changed: %v", d.Changed)
		}
	}
	want := ev.UpdatedAt.UTC().Format(time.RFC3339Nano)
	if d.Event.UpdatedAt != want || d.Loaded.Event.UpdatedAt != want || d.Event.UpdatedAt == oldStamp {
		t.Errorf("updated_at = %q / %q, want %q", d.Event.UpdatedAt, d.Loaded.Event.UpdatedAt, want)
	}

	// The organizer now changes something else and publishes.
	r.press("e:home", stEditMenu)
	addNewDate(r)
	io := &fakeSave{}
	if out := r.w.Save(context.Background(), io, r.ws, d, "d9"); !out.Done {
		t.Fatalf("save = %+v", out)
	}
	if len(io.posts) == 0 {
		t.Fatal("nothing was posted")
	}
	for i, p := range io.posts {
		if p.Action.Description != "" || p.Action.DescriptionFormat != "" {
			t.Fatalf("post %d sends a description: %q / %q", i, p.Action.Description, p.Action.DescriptionFormat)
		}
	}
}

// Nothing to adopt: the same description as loaded, another event, a
// translation shown, the same words in the old notation.
func TestEditorDescription_NothingToAdopt(t *testing.T) {
	_, d := loadedWith(t, artDescription, "art")
	if d.adoptEditorDescription(editorSaved(d, artDescription)) {
		t.Error("adopted the description the draft already holds")
	}
	other := editorSaved(d, "<p>x</p>")
	other.Id = uuid.New()
	if d.adoptEditorDescription(other) {
		t.Error("adopted the text of another event")
	}
	if d.Event.Description != artDescription {
		t.Errorf("the draft moved: %q", d.Event.Description)
	}

	// The list shows a translation, the canonical text is description_base: the
	// base is what is compared, so a translation is not a save.
	tr := editorSaved(d, "<p>English translation</p>")
	base, translated := artDescription, true
	tr.DescriptionBase, tr.DescriptionTranslated = &base, &translated
	if d.adoptEditorDescription(tr) {
		t.Error("a translation was taken for a save")
	}

	// A plain-text row the editor rewrote with the same words is no change.
	_, dp := loadedWith(t, "Просто текст", "")
	if dp.adoptEditorDescription(editorSaved(dp, "<p>Просто текст</p>")) {
		t.Error("the same words in another notation counted as a save")
	}
}

// A text the organizer typed into the draft is theirs: the sync leaves it and
// leaves updated_at alone, so the usual «changed elsewhere» check decides.
func TestEditorDescription_OrganizersOwnTextIsKept(t *testing.T) {
	r, d := loadedWith(t, artDescription, "art")
	r.press("e:desc", stXDescription)
	r.text("Мой новый текст", stEditMenu)
	stamp := d.Event.UpdatedAt
	if d.adoptEditorDescription(editorSaved(d, "<p>Из редактора</p>")) {
		t.Fatal("replaced the organizer's own text")
	}
	if d.Event.Description != "<p>Мой новый текст</p>" || d.Event.UpdatedAt != stamp {
		t.Errorf("draft = %q, updated_at %q", d.Event.Description, d.Event.UpdatedAt)
	}
}

// If the name (or another compared part) was changed elsewhere too, the
// description is adopted but updated_at is NOT refreshed: the warning stays.
func TestEditorDescription_OtherChangeKeepsTheWarning(t *testing.T) {
	_, d := loadedWith(t, artDescription, "art")
	stamp := d.Event.UpdatedAt
	ev := editorSaved(d, "<p>Из редактора</p>")
	ev.Name = "Переименовали в другом месте"
	if !d.adoptEditorDescription(ev) {
		t.Fatal("not adopted")
	}
	if d.Event.UpdatedAt != stamp || d.Loaded.Event.UpdatedAt != stamp {
		t.Errorf("updated_at moved to %q although the name changed elsewhere", d.Event.UpdatedAt)
	}
}

// A description deleted in the editor leaves the draft empty and unchanged.
func TestEditorDescription_DeletedInTheEditor(t *testing.T) {
	_, d := loadedWith(t, artDescription, "art")
	ev := editorSaved(d, "")
	ev.Description, ev.DescriptionFormat = nil, nil
	if !d.adoptEditorDescription(ev) {
		t.Fatal("not adopted")
	}
	if d.Event.Description != "" || d.Event.DescriptionFormat != "" || d.descriptionChanged() {
		t.Errorf("draft = %q / %q", d.Event.Description, d.Event.DescriptionFormat)
	}
}

// A create draft is never touched.
func TestEditorDescription_CreateDraftIgnored(t *testing.T) {
	d := NewDraft()
	d.Event.Description = "<p>x</p>"
	if d.adoptEditorDescription(openapi.EventItem{Id: uuid.New()}) {
		t.Error("adopted into a create draft")
	}
}

func TestEditorSyncData(t *testing.T) {
	_, d := loadedWith(t, artDescription, "art")
	for _, data := range []string{"e:desc", "publish", "publish:go", "publish:force", "retry"} {
		if !editorSyncData(d, data) {
			t.Errorf("%s: no sync", data)
		}
	}
	for _, data := range []string{"skip", "back", "e:name", "e:home"} {
		if editorSyncData(d, data) {
			t.Errorf("%s: synced", data)
		}
	}
	d.Step = stXDescription
	if !editorSyncData(d, "e:home") {
		t.Error("leaving the description question must sync")
	}
	if editorSyncData(NewDraft(), "publish") {
		t.Error("a create draft synced")
	}
}

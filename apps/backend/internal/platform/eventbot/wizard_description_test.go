package eventbot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// artDescription is a description as RT-02 stores it.
const artDescription = `<h3>Программа</h3><p>Концерт <strong>«Лебединое озеро»</strong> &amp; <em>антракт</em>.</p><ul><li>первое отделение</li><li>второе отделение</li></ul><p>Билеты: <a href="https://example.com/t">на сайте</a></p>`

// loadedWith loads an event whose description is stored as given, in the format
// the server reports ("" = the server said nothing).
func loadedWith(t *testing.T, description, format string) (*wizardRun, *Draft) {
	t.Helper()
	r, f := loadRun(t)
	s := f.sessions[0]
	f.tiers[s.Id] = []openapi.TicketTierItem{{Id: uuid.New(), SessionId: s.Id, Name: "Вход", PriceAmount: 50000, Currency: "CZK", Capacity: i32p(250)}}
	f.event.Description = &description
	if format != "" {
		fm := openapi.EventItemDescriptionFormat(format)
		f.event.DescriptionFormat = &fm
	}
	d, _, err := r.w.LoadEventDraft(context.Background(), f, r.ws, f.event.Id)
	if err != nil {
		t.Fatal(err)
	}
	r.d = d
	return r, d
}

// addNewDate adds one more date to the edited event.
func addNewDate(r *wizardRun) {
	r.t.Helper()
	r.press("e:date", stSDate)
	r.text("20.12.2026", stSTime)
	r.press("default", stSSame)
	r.press("same", stSTickets)
	r.press("st:same", stSConfirm)
	r.press("e:home", stEditMenu)
}

// Loading an event with an ART description and saving after a change of the
// date only sends the description back as it was read, byte for byte, with
// the format "art" (it used to go out as plain text and every tag was
// escaped).
func TestDescription_ARTLoadedAndOnlyTheDateChangedGoesBackUnchanged(t *testing.T) {
	r, d := loadedWith(t, artDescription, "art")
	if d.Event.Description != artDescription || d.Event.DescriptionFormat != "art" {
		t.Fatalf("the draft holds %q / %q", d.Event.Description, d.Event.DescriptionFormat)
	}
	addNewDate(r)
	io := &fakeSave{}
	if out := r.w.Save(context.Background(), io, r.ws, d, "d9"); !out.Done {
		t.Fatalf("save = %+v", out)
	}
	if len(io.posts) == 0 {
		t.Fatal("nothing was posted")
	}
	for i, p := range io.posts {
		if p.Action.Description != artDescription || p.Action.DescriptionFormat != "art" {
			t.Fatalf("post %d carries %q / %q", i, p.Action.Description, p.Action.DescriptionFormat)
		}
	}
	raw, _ := json.Marshal(io.posts[0])
	if !strings.Contains(string(raw), `"descriptionFormat":"art"`) {
		t.Fatalf("the wire body does not name the format: %s", raw)
	}
}

// The edit screen never shows the tags of ART, and a long one is cut by its
// visible text, never inside a tag.
func TestDescription_ScreenShowsNoTags(t *testing.T) {
	r, _ := loadedWith(t, artDescription, "art")
	r.press("e:desc", stXDescription)
	screen := render(t, r)
	for _, bad := range []string{"<p>", "</p>", "<ul>", "<li>", "<h3>", "<strong>", "&lt;p&gt;", "&lt;strong&gt;"} {
		if strings.Contains(screen.Text, bad) {
			t.Fatalf("the screen shows %q:\n%s", bad, screen.Text)
		}
	}
	if !strings.Contains(screen.Text, "Лебединое озеро") || !strings.Contains(screen.Text, "первое отделение") {
		t.Fatalf("the screen lacks the words:\n%s", screen.Text)
	}
	if !strings.Contains(screen.Text, `<a href="https://example.com/t">на сайте</a>`) || !strings.Contains(screen.Text, "<b>") {
		t.Fatalf("the screen is not the Telegram markup of the text:\n%s", screen.Text)
	}

	long := "<p>" + strings.Repeat("слово <strong>важное</strong> ", 60) + "</p><p>" + strings.Repeat("конец ", 40) + "</p>"
	r2, _ := loadedWith(t, long, "art")
	r2.press("e:desc", stXDescription)
	text := render(t, r2).Text
	for _, bad := range []string{"<p>", "</p>", "<strong>", "&lt;"} {
		if strings.Contains(text, bad) {
			t.Fatalf("the long screen shows %q:\n%s", bad, text)
		}
	}
	if !strings.Contains(text, "…") {
		t.Fatalf("the long text is not cut:\n%s", text)
	}
}

func TestDescriptionPreview_CutsTheVisibleTextNotTheMarkup(t *testing.T) {
	e := DraftEvent{Description: "<p>" + strings.Repeat("<strong>ab</strong>", 400) + "</p>", DescriptionFormat: "art"}
	got := descriptionPreview(e, 300)
	if strings.ContainsAny(got, "<>") {
		t.Fatalf("markup in the cut text: %q", got)
	}
	if n := len([]rune(got)); n > 300 {
		t.Fatalf("%d characters", n)
	}
	// The old text is escaped, never read as markup.
	old := DraftEvent{Description: "a <b>x</b> & **y**", DescriptionFormat: "text"}
	if got := descriptionPreview(old, 300); got != "a &lt;b&gt;x&lt;/b&gt; &amp; **y**" {
		t.Fatalf("old text preview %q", got)
	}
}

// An event whose stored text is of the old kind, or of an unknown kind, is
// not re-sent when the organizer leaves the description alone: the server
// keeps what it has, and a guessed format cannot turn tags into words.
func TestDescription_UntouchedOldOrUnknownTextIsNotResent(t *testing.T) {
	for _, tc := range []struct{ name, stored, format string }{
		{"old text", "Описание **из** Arena", "text"},
		{"unknown format holding tags", "<p>Что-то</p>", ""},
	} {
		r, d := loadedWith(t, tc.stored, tc.format)
		addNewDate(r)
		io := &fakeSave{}
		if out := r.w.Save(context.Background(), io, r.ws, d, "d9"); !out.Done {
			t.Fatalf("%s: save = %+v", tc.name, out)
		}
		for _, p := range io.posts {
			if p.Action.Description != "" || p.Action.DescriptionFormat != "" {
				t.Fatalf("%s: the description went out: %q / %q", tc.name, p.Action.Description, p.Action.DescriptionFormat)
			}
		}
	}
}

// What the organizer types is plain text: it replaces the stored one and goes
// out as the default format, never as ART.
func TestDescription_TypedTextIsPlainTextAndReplacesART(t *testing.T) {
	r, d := loadedWith(t, artDescription, "art")
	r.press("e:desc", stXDescription)
	r.text("Новый текст <b>без разметки</b>", stEditMenu)
	if d.Event.Description != "Новый текст <b>без разметки</b>" || d.Event.DescriptionFormat != "text" {
		t.Fatalf("draft = %q / %q", d.Event.Description, d.Event.DescriptionFormat)
	}
	if !d.fieldChanged("description") {
		t.Fatal("a typed description is a change")
	}
	addNewDate(r)
	io := &fakeSave{}
	if out := r.w.Save(context.Background(), io, r.ws, d, "d9"); !out.Done {
		t.Fatalf("save = %+v", out)
	}
	for _, p := range io.posts {
		if p.Action.Description != "Новый текст <b>без разметки</b>" || p.Action.DescriptionFormat != "" {
			t.Fatalf("typed text went out as %q / %q", p.Action.Description, p.Action.DescriptionFormat)
		}
	}
}

// Undo goes back to the loaded text AND its format.
func TestDescription_UndoRestoresTheTextAndItsFormat(t *testing.T) {
	r, d := loadedWith(t, artDescription, "art")
	r.press("e:desc", stXDescription)
	r.text("Новый текст", stEditMenu)
	r.press("e:undo:description", stEditMenu)
	if d.Event.Description != artDescription || d.Event.DescriptionFormat != "art" {
		t.Fatalf("after undo: %q / %q", d.Event.Description, d.Event.DescriptionFormat)
	}
}

// The text typed is cut at 4000 characters: visible characters of plain text.
func TestDescription_TypedTextLimitIs4000(t *testing.T) {
	d := &Draft{}
	d.setTypedDescription(strings.Repeat("я", 4100))
	if n := len([]rune(d.Event.Description)); n != 4000 {
		t.Fatalf("%d characters kept", n)
	}
}

// A draft stored before the format was kept has no description_format: it
// still reads, and an edit that did not touch the description does not
// re-send it.
func TestDescription_DraftWithoutFormatStillReads(t *testing.T) {
	_, d := loadedWith(t, artDescription, "art")
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	old := string(raw)
	for _, key := range []string{`"description_format":"art",`, `,"description_format":"art"`} {
		old = strings.ReplaceAll(old, key, "")
	}
	if strings.Contains(old, "description_format") {
		t.Fatalf("could not strip the format: %s", old)
	}
	var back Draft
	if err := json.Unmarshal([]byte(old), &back); err != nil {
		t.Fatal(err)
	}
	if back.Version != draftSchemaVersion || back.Event.Description != artDescription || back.Event.DescriptionFormat != "" {
		t.Fatalf("old draft = %q / %q (v%d)", back.Event.Description, back.Event.DescriptionFormat, back.Version)
	}
	if text, format := back.bundleDescription(); text != "" || format != "" {
		t.Fatalf("an old draft re-sent its description as %q / %q", text, format)
	}
}

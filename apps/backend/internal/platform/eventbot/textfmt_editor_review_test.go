package eventbot

// textfmt_editor_review_test.go - the RT-12b review: the canonical text (not the
// translation of the list) is what the bot fingerprints, a second press of
// «Edit in the editor» never overwrites a save nobody was told about, an
// expired dialog of the step "editor" does not swallow a typed line, and the
// fingerprint reads a text the way the server stores it.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

func strPtr(s string) *string { return &s }

func boolPtr(b bool) *bool { return &b }

// The list answers a Russian client with the translation as `description`; the
// bot reads the canonical text next to it, in the editor's fingerprint and in
// the wizard's loaded draft.
func TestEditorReview_TheCanonicalTextIsReadNotTheTranslation(t *testing.T) {
	art := openapi.EventItemDescriptionFormat("art")
	ev := openapi.EventItem{
		Description:           strPtr("<p>Русский перевод</p>"),
		DescriptionBase:       strPtr("<p>The original</p>"),
		DescriptionTranslated: boolPtr(true),
		DescriptionFormat:     &art,
	}
	if got := descriptionART(ev); got != "<p>The original</p>" {
		t.Fatalf("descriptionART = %q, want the canonical text", got)
	}
	if got := eventDescriptionBase(ev); got != "<p>The original</p>" {
		t.Fatalf("eventDescriptionBase = %q", got)
	}
	// An event whose only text is a translation has no canonical text.
	ev.DescriptionBase = nil
	if got := descriptionART(ev); got != "" {
		t.Fatalf("descriptionART of a translation-only event = %q, want empty", got)
	}
	// A server that predates the field: the description is the only text.
	old := openapi.EventItem{Description: strPtr("<p>Plain</p>"), DescriptionFormat: &art}
	if got := descriptionART(old); got != "<p>Plain</p>" {
		t.Fatalf("descriptionART for an old server = %q", got)
	}
}

// The fingerprint is of the text the server STORES: the same words in the old
// notation and as ART are one text, and ART is compared after the filter.
func TestEditorReview_TheFingerprintReadsATextAsTheServerStoresIt(t *testing.T) {
	text := openapi.EventItemDescriptionFormat("text")
	art := openapi.EventItemDescriptionFormat("art")
	legacy := openapi.EventItem{Description: strPtr("Tickets: https://example.com/a, **now**\n\n- one\n- two"), DescriptionFormat: &text}
	want, err := richtext.Normalize(*legacy.Description, richtext.FormatText, richtext.ProfileDescription)
	if err != nil {
		t.Fatal(err)
	}
	if got := descriptionART(legacy); got != want {
		t.Fatalf("a text row = %q, want what Normalize makes of it: %q", got, want)
	}
	// What the editor then saves is that ART: the same fingerprint, so no false "saved".
	saved := openapi.EventItem{Description: strPtr(want), DescriptionFormat: &art}
	if editorFingerprint(descriptionART(saved)) != editorFingerprint(descriptionART(legacy)) {
		t.Fatal("the same words as ART counted as another text")
	}
	// ART is sanitised before it is compared: a stored tag the filter drops is not a difference.
	dirty := openapi.EventItem{Description: strPtr(`<p onclick="x()">Hi</p>`), DescriptionFormat: &art}
	clean := openapi.EventItem{Description: strPtr(`<p>Hi</p>`), DescriptionFormat: &art}
	if editorFingerprint(descriptionART(dirty)) != editorFingerprint(descriptionART(clean)) {
		t.Fatal("an attribute the filter drops made a difference")
	}
}

// The editor saves the canonical text while the list shows a translation: the
// bot notices the save by the canonical text, and a change of the translation
// alone is no save.
func TestEditorReview_ASaveOfTheCanonicalTextIsSeenThroughATranslation(t *testing.T) {
	f := newEditorFixture(t)
	ctx := context.Background()
	f.mu.Lock()
	f.shown = "<p>Русский перевод</p>"
	f.mu.Unlock()
	ev := f.event.String()
	if err := f.b.tfSave(ctx, 42, f.org, tfStepPreview, tfDialog{Kind: string(tfKindDescription), EventID: ev, Art: "<p>typed</p>", Applied: true}); err != nil {
		t.Fatal(err)
	}
	f.b.tfMiniappCallback(withFeedAction(ctx, pressAction(press(10, "Here", "tf:m", nil))), 42, 10, f.from, f.id, "jwt", []string{"m"})
	dlg, _, _, _ := f.b.tfLoad(ctx, 42)
	if dlg.Seen != editorFingerprint("<p>Old description</p>") {
		t.Fatalf("the mark is of the translation, not of the canonical text: %+v", dlg)
	}
	// Somebody edits only the Russian translation: the list changes, the canonical text does not.
	f.mu.Lock()
	f.shown = "<p>Другой перевод</p>"
	f.mu.Unlock()
	if _, changed := f.b.tfEditorChanged(ctx, f.id, "jwt", dlg); changed {
		t.Fatal("a change of the translation counted as the editor's save")
	}
	f.set("<p>Saved in the editor</p>", "<p>Old note</p>")
	if art, changed := f.b.tfEditorChanged(ctx, f.id, "jwt", dlg); !changed || art != "<p>Saved in the editor</p>" {
		t.Fatalf("a save of the canonical text: %q %v", art, changed)
	}
}

// A second press of the button under the same preview, after the editor saved,
// says so first: the mark is not overwritten with the saved text, the editor is
// not opened over a save nobody was told about. A press after that opens it.
func TestEditorReview_ASecondPressUnderThePreviewAsksBeforeItOverwritesTheMark(t *testing.T) {
	f := newEditorFixture(t)
	ctx := context.Background()
	ev := f.event.String()
	if err := f.b.tfSave(ctx, 42, f.org, tfStepPreview, tfDialog{Kind: string(tfKindDescription), EventID: ev, Text: "typed", Art: "<p>typed</p>", Applied: true}); err != nil {
		t.Fatal(err)
	}
	pressM := func() bool {
		return f.b.tfMiniappCallback(withFeedAction(ctx, pressAction(press(10, "Here", "tf:m", nil))), 42, 10, f.from, f.id, "jwt", []string{"m"})
	}
	if !pressM() || f.links != 1 {
		t.Fatalf("the first press: links %d", f.links)
	}
	// The editor saved. The second press must not open the editor again.
	f.set("<p>From the <strong>editor</strong></p>", "")
	f.stub.reset()
	if !pressM() {
		t.Fatal("the second press was not taken")
	}
	if f.links != 1 {
		t.Fatalf("the editor was opened again over an unannounced save (links %d)", f.links)
	}
	if out := f.sentText(); !strings.Contains(out, editorNoteRU) {
		t.Fatalf("the second press did not say the editor saved:\n%s", out)
	}
	dlg, step, _, _ := f.b.tfLoad(ctx, 42)
	if step != tfStepPreview || dlg.Editor || dlg.Seen != "" || dlg.Art != "<p>From the <strong>editor</strong></p>" {
		t.Fatalf("dialog after the note: %+v step %q", dlg, step)
	}
	// The press after the note opens the editor, with a fresh mark of the saved text.
	if !pressM() || f.links != 2 {
		t.Fatalf("the third press: links %d", f.links)
	}
	dlg, _, _, _ = f.b.tfLoad(ctx, 42)
	if !dlg.Editor || dlg.Seen != editorFingerprint("<p>From the <strong>editor</strong></p>") {
		t.Fatalf("mark after the third press: %+v", dlg)
	}
}

// Without a save in between, a second press just opens the editor again and
// keeps the mark it had.
func TestEditorReview_ASecondPressWithNoSaveOpensTheEditorAgain(t *testing.T) {
	f := newEditorFixture(t)
	ctx := context.Background()
	ev := f.event.String()
	if err := f.b.tfSave(ctx, 42, f.org, tfStepPreview, tfDialog{Kind: string(tfKindDescription), EventID: ev, Art: "<p>typed</p>", Applied: true}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		if !f.b.tfMiniappCallback(withFeedAction(ctx, pressAction(press(10, "Here", "tf:m", nil))), 42, 10, f.from, f.id, "jwt", []string{"m"}) || f.links != i {
			t.Fatalf("press %d: links %d", i, f.links)
		}
	}
	if dlg, _, _, _ := f.b.tfLoad(ctx, 42); !dlg.Editor || dlg.Seen != editorFingerprint("<p>Old description</p>") {
		t.Fatalf("mark: %+v", dlg)
	}
}

// The same on the letter screen: its second press, after a save, says so and
// does not open the editor.
func TestEditorReview_ASecondPressOnTheLetterScreenAsksBeforeItOverwritesTheMark(t *testing.T) {
	f := newEditorFixture(t)
	ctx := context.Background()
	ev := f.event.String()
	pressM := func() bool {
		return f.b.tfMiniappCallback(withFeedAction(ctx, pressAction(noteScreenPress(ev))), 42, 10, f.from, f.id, "jwt", []string{"m", ev})
	}
	if !pressM() || f.links != 1 {
		t.Fatalf("the first press: links %d", f.links)
	}
	f.set("<p>Old description</p>", "<p>New <strong>note</strong></p>")
	f.stub.reset()
	if !pressM() {
		t.Fatal("the second press was not taken")
	}
	if f.links != 1 {
		t.Fatalf("the editor was opened again over an unannounced save (links %d)", f.links)
	}
	if out := f.sentText(); !strings.Contains(out, editorNoteRU) || !strings.Contains(out, "note") {
		t.Fatalf("the second press did not say it, or did not show the saved text:\n%s", out)
	}
	if _, _, found, _ := f.b.tfLoad(ctx, 42); found {
		t.Fatal("the mark stayed after the note")
	}
	if !pressM() || f.links != 2 {
		t.Fatalf("the third press: links %d", f.links)
	}
}

// A dialog of the step "editor" that lapsed was not waiting for typed text: the
// line the person types goes on to wherever it would have gone, and nobody is
// told "expired". A lapsed preview still is.
func TestEditorReview_AnExpiredEditorStepDoesNotSwallowATypedLine(t *testing.T) {
	f := newEditorFixture(t)
	ctx := context.Background()
	ev := f.event.String()
	f.b.tfMiniappCallback(withFeedAction(ctx, pressAction(noteScreenPress(ev))), 42, 10, f.from, f.id, "jwt", []string{"m", ev})
	if _, step, found, _ := f.b.tfLoad(ctx, 42); !found || step != tfStepEditor {
		t.Fatalf("no dialog of the step editor: %q %v", step, found)
	}
	f.clock.advance(dialogTTL + time.Minute)
	f.stub.reset()
	if f.b.tfText(ctx, 42, f.from, "Hello, what is on?") {
		t.Fatal("the lapsed editor step took a typed line")
	}
	if got := f.stub.all(); len(got) != 0 {
		t.Fatalf("something was said to a line of chat: %+v", got)
	}
	// The report is once: a second line is not "expired" either.
	if f.b.tfText(ctx, 42, f.from, "Again") {
		t.Fatal("taken the second time")
	}
}

// The wizard's check of «changed elsewhere» is not defeated by a name that
// changed too: only a draft whose other parts are as loaded takes the new stamp.
func TestEditorReview_SameOtherParts(t *testing.T) {
	poster := uuid.New()
	promoter := uuid.New()
	age := "12+"
	ev := openapi.EventItem{Name: "Concert", AgeRating: &age, PosterMediaId: &poster, PromoterId: &promoter}
	loaded := DraftEvent{Name: "Concert", Age: "12+", PosterMediaID: poster.String(), PromoterID: promoter.String()}
	if !sameOtherParts(ev, loaded) {
		t.Fatal("an event as loaded was not the same")
	}
	for name, mutate := range map[string]func(*DraftEvent){
		"name":     func(d *DraftEvent) { d.Name = "Concert, renamed" },
		"age":      func(d *DraftEvent) { d.Age = "16+" },
		"poster":   func(d *DraftEvent) { d.PosterMediaID = "" },
		"promoter": func(d *DraftEvent) { d.PromoterID = "" },
	} {
		changed := loaded
		mutate(&changed)
		if sameOtherParts(ev, changed) {
			t.Errorf("a different %s counted as the same", name)
		}
	}
}

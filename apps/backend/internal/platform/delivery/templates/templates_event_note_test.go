package templates

import (
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventnote"
)

// noteLabels are the "About the event" captions, one per locale. A new locale
// or a reworded caption fails here, never silently in a buyer's inbox.
var noteLabels = map[string]string{
	"en": "About the event",
	"ru": "О мероприятии",
	"cs": "O akci",
	"de": "Zur Veranstaltung",
	"es": "Sobre el evento",
	"fr": "À propos de l’événement",
	"he": "על האירוע",
}

func noteData(note string) Data {
	return Data{
		TicketID:       "123",
		RecipientEmail: "buyer@example.test",
		EventName:      "Swan Lake",
		SessionStart:   "2099-01-01 19:00 (CET)",
		Branding:       Branding{OrgName: "Org", LegalName: "Org s.r.o."},
		Change:         ChangeData{Date: true, OldStart: "2099-01-01 18:00", NewStart: "2099-01-01 19:00"},
		EventNote:      eventnote.Parse(note),
	}
}

// Both kinds, all seven locales: the block is printed in the HTML and the text
// part, the caption is the locale's own, a link is an anchor with
// rel="noopener", and everything else is escaped.
func TestEventNote_PrintedInTicketAndChangeLetters(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	const note = "Come 30 minutes early & bring <ID>.\nThe cloakroom is free.\n\nProgramme: https://example.com/programme."
	for _, kind := range []string{TemplateKindTicket, TemplateKindChange} {
		for loc, label := range noteLabels {
			out, err := r.Render(kind, loc, noteData(note))
			if err != nil {
				t.Fatalf("%s.%s: %v", kind, loc, err)
			}
			if !strings.Contains(out.HTMLBody, label) {
				t.Errorf("%s.%s: html lacks the caption %q", kind, loc, label)
			}
			if !strings.Contains(out.TextBody, label+":") {
				t.Errorf("%s.%s: text lacks the caption %q", kind, loc, label)
			}
			if !strings.Contains(out.HTMLBody, "Come 30 minutes early &amp; bring &lt;ID&gt;.") {
				t.Errorf("%s.%s: html does not carry the escaped first paragraph", kind, loc)
			}
			if !strings.Contains(out.HTMLBody, `<a href="https://example.com/programme" rel="noopener"`) {
				t.Errorf("%s.%s: html lacks the anchor with rel=noopener", kind, loc)
			}
			if !strings.Contains(out.HTMLBody, "The cloakroom is free.") {
				t.Errorf("%s.%s: the second line is missing", kind, loc)
			}
			if !strings.Contains(out.TextBody, "Come 30 minutes early & bring <ID>.\nThe cloakroom is free.") {
				t.Errorf("%s.%s: text part lacks the note as written", kind, loc)
			}
			if !strings.Contains(out.TextBody, "https://example.com/programme.") {
				t.Errorf("%s.%s: text part lacks the link", kind, loc)
			}
		}
	}
}

// No note, no block: the letter is what it was before EC-31.
func TestEventNote_AbsentPrintsNothing(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{TemplateKindTicket, TemplateKindChange} {
		for loc, label := range noteLabels {
			out, err := r.Render(kind, loc, noteData(""))
			if err != nil {
				t.Fatalf("%s.%s: %v", kind, loc, err)
			}
			if strings.Contains(out.HTMLBody, label) || strings.Contains(out.TextBody, label) {
				t.Errorf("%s.%s: caption printed without a note", kind, loc)
			}
			if strings.Contains(out.HTMLBody, "noopener") {
				t.Errorf("%s.%s: a link anchor appeared without a note", kind, loc)
			}
		}
	}
}

// A note that somehow holds markup (the filter lets only plain text in, this
// is the second line of defence) is still escaped by the HTML engine.
func TestEventNote_MarkupIsEscaped(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	b := eventnote.Block{
		Paragraphs: []eventnote.Paragraph{{{Text: `<script>alert(1)</script>`}, {Text: "click", URL: `javascript:alert(1)`}}},
		Plain:      "x",
	}
	d := noteData("")
	d.EventNote = b
	out, err := r.Render(TemplateKindTicket, "en", d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.HTMLBody, "<script>") {
		t.Fatal("markup reached the html body unescaped")
	}
	if strings.Contains(out.HTMLBody, `href="javascript:`) {
		t.Fatal("a javascript: URL reached an href")
	}
}

// The other kinds never print the block, even when the data carries one.
func TestEventNote_OtherKindsIgnoreIt(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{TemplateKindInvitation, TemplateKindCancel} {
		out, err := r.Render(kind, "en", noteData("Secret organizer words"))
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if strings.Contains(out.HTMLBody, "Secret organizer words") || strings.Contains(out.TextBody, "Secret organizer words") {
			t.Errorf("%s printed the event note", kind)
		}
	}
}

func TestEventNote_VersionCarriesTheBlock(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	plain := r.VersionFor(TemplateKindTicket, "ru", false)
	if plain != r.Version(TemplateKindTicket, "ru") {
		t.Fatalf("without a note the version is the file's: %q", plain)
	}
	with := r.VersionFor(TemplateKindTicket, "ru", true)
	if with != plain+NoteVersionSuffix || !strings.HasSuffix(with, "+note") {
		t.Fatalf("with a note: %q", with)
	}
	if r.VersionFor("nope", "en", true) != "" {
		t.Fatal("an unknown kind has no version, with or without a note")
	}
}

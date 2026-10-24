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

func noteData(note, format string) Data {
	return Data{
		TicketID:       "123",
		RecipientEmail: "buyer@example.test",
		EventName:      "Swan Lake",
		SessionStart:   "2099-01-01 19:00 (CET)",
		Branding:       Branding{OrgName: "Org", LegalName: "Org s.r.o."},
		Change:         ChangeData{Date: true, OldStart: "2099-01-01 18:00", NewStart: "2099-01-01 19:00"},
		EventNote:      eventnote.Build(note, format),
	}
}

// Both kinds, all seven locales: an ART note is printed as formatted HTML in
// the HTML part (never as escaped tags) and as plain text without tags in the
// text part, the caption is the locale's own and a link is an anchor with
// rel="noopener noreferrer".
func TestEventNote_ARTPrintedInTicketAndChangeLetters(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	const note = `<p>Come <strong>30 minutes</strong> early &amp; bring &lt;ID&gt;.<br>The cloakroom is free.</p><ul><li>Coat check</li></ul><p>Programme: <a href="https://example.com/programme">the programme</a></p>`
	for _, kind := range []string{TemplateKindTicket, TemplateKindChange} {
		for loc, label := range noteLabels {
			out, err := r.Render(kind, loc, noteData(note, "art"))
			if err != nil {
				t.Fatalf("%s.%s: %v", kind, loc, err)
			}
			if !strings.Contains(out.HTMLBody, label) {
				t.Errorf("%s.%s: html lacks the caption %q", kind, loc, label)
			}
			if !strings.Contains(out.TextBody, label+":") {
				t.Errorf("%s.%s: text lacks the caption %q", kind, loc, label)
			}
			for _, want := range []string{
				"Come <strong>30 minutes</strong> early &amp; bring &lt;ID&gt;.<br>The cloakroom is free.",
				"<li>Coat check</li>",
				`href="https://example.com/programme"`,
				`rel="noopener noreferrer"`,
			} {
				if !strings.Contains(out.HTMLBody, want) {
					t.Errorf("%s.%s: html lacks %q", kind, loc, want)
				}
			}
			if strings.Contains(out.HTMLBody, "&lt;p&gt;") || strings.Contains(out.HTMLBody, "&lt;strong&gt;") || strings.Contains(out.HTMLBody, "&lt;a ") {
				t.Errorf("%s.%s: the note markup was escaped into the letter", kind, loc)
			}
			if i := strings.Index(out.HTMLBody, "<li>Coat check</li>"); i < 0 || strings.Contains(out.HTMLBody[strings.LastIndex(out.HTMLBody[:i], "<div style=\"color:#46555f"):i], "pre-line") {
				t.Errorf("%s.%s: the note block keeps white-space:pre-line", kind, loc)
			}
			if strings.Contains(out.TextBody, "<p>") || strings.Contains(out.TextBody, "<strong>") || strings.Contains(out.TextBody, "<a ") || strings.Contains(out.TextBody, "<li>") {
				t.Errorf("%s.%s: the text part carries tags:\n%s", kind, loc, out.TextBody)
			}
			if !strings.Contains(out.TextBody, "Come 30 minutes early & bring <ID>.") || !strings.Contains(out.TextBody, "Coat check") || !strings.Contains(out.TextBody, "https://example.com/programme") {
				t.Errorf("%s.%s: text part lacks the note words:\n%s", kind, loc, out.TextBody)
			}
		}
	}
}

// A note stored before RT-02 (format text, or a job without a format) keeps
// its marks and prints bare https links as clickable anchors.
func TestEventNote_LegacyTextStillRendersWithLinks(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	const note = "Come **early** & bring <ID>.\nThe cloakroom is free.\n\nProgramme: https://example.com/programme."
	for _, format := range []string{"text", ""} {
		for _, kind := range []string{TemplateKindTicket, TemplateKindChange} {
			out, err := r.Render(kind, "en", noteData(note, format))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"Come <strong>early</strong> &amp; bring &lt;ID&gt;.", `<a style="color:#1a73e8;" href="https://example.com/programme"`, `rel="noopener noreferrer"`, "The cloakroom is free."} {
				if !strings.Contains(out.HTMLBody, want) {
					t.Errorf("%s format %q: html lacks %q", kind, format, want)
				}
			}
			if !strings.Contains(out.TextBody, "Come **early** & bring <ID>.\nThe cloakroom is free.") || !strings.Contains(out.TextBody, "https://example.com/programme.") {
				t.Errorf("%s format %q: text part lacks the legacy note:\n%s", kind, format, out.TextBody)
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
			out, err := r.Render(kind, loc, noteData("", "art"))
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

// A hostile ART note (the filter ran at write time, this is the second line
// of defence: Build sanitises again) never reaches the letter as markup.
func TestEventNote_HostileARTIsNeutralised(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	d := noteData(`<p onclick="x()">hi<script>alert(1)</script><a href="javascript:alert(1)">click</a></p>`, "art")
	out, err := r.Render(TemplateKindTicket, "en", d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.HTMLBody, "<script>") || strings.Contains(out.HTMLBody, "onclick") {
		t.Fatal("markup reached the html body")
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
		out, err := r.Render(kind, "en", noteData("Secret organizer words", "text"))
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

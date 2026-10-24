package catalogimport

import (
	"strings"
	"testing"
	"time"
)

func snapshotEvent(desc, venue, format string) Bil24SnapshotEvent {
	return Bil24SnapshotEvent{
		ExternalBil24ID: "b-1", Title: "T", StartsAt: time.Date(2026, 5, 1, 18, 0, 0, 0, time.UTC),
		Description: desc, VenueName: venue, DescriptionFormat: format,
	}
}

// Bil24 descriptions are HTML: art is the default.
func TestDescriptionART_DefaultIsHTML(t *testing.T) {
	e := snapshotEvent(`<p>Rock <b>night</b></p><script>x()</script>`, "", "")
	got, err := e.DescriptionART()
	if err != nil || got != "<p>Rock <strong>night</strong></p>" {
		t.Fatalf("default: %q %v", got, err)
	}
	e.DescriptionFormat = DescriptionFormatART
	if again, _ := e.DescriptionART(); again != got {
		t.Fatalf("explicit art: %q", again)
	}
}

func TestDescriptionART_TextFormatShowsTagsAsWritten(t *testing.T) {
	e := snapshotEvent("**Rock** <b>night</b>", "", DescriptionFormatText)
	got, err := e.DescriptionART()
	if err != nil || got != "<p><strong>Rock</strong> &lt;b&gt;night&lt;/b&gt;</p>" {
		t.Fatalf("text: %q %v", got, err)
	}
}

func TestDescriptionART_WordPressParagraphsAndVenue(t *testing.T) {
	e := snapshotEvent("First <i>one</i>.\n\nSecond,\nline two.", "Hall <A> & B", "")
	got, err := e.DescriptionART()
	want := "<p>First <em>one</em>.</p><p>Second,<br>line two.</p><p>(Venue: Hall &lt;A&gt; &amp; B)</p>"
	if err != nil || got != want {
		t.Fatalf("got %q %v, want %q", got, err, want)
	}
	// With <p> in the description the venue still becomes its own paragraph.
	e = snapshotEvent("<p>One</p>", "Main Hall", "")
	if got, err := e.DescriptionART(); err != nil || got != "<p>One</p><p>(Venue: Main Hall)</p>" {
		t.Fatalf("with <p>: %q %v", got, err)
	}
	// As text the venue is not escaped by hand: the normaliser does it.
	e = snapshotEvent("One", "A & B", DescriptionFormatText)
	if got, _ := e.DescriptionART(); !strings.Contains(got, "(Venue: A &amp; B)") {
		t.Fatalf("text venue: %q", got)
	}
}

func TestParseDescriptionFormat(t *testing.T) {
	for in, want := range map[string]string{"": "art", "art": "art", "text": "text"} {
		if got, err := ParseDescriptionFormat(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"html", "ART", "markdown"} {
		if _, err := ParseDescriptionFormat(in); err == nil {
			t.Errorf("%q must be refused", in)
		}
	}
}

func TestValidate_RefusesAnUnknownDescriptionFormat(t *testing.T) {
	e := snapshotEvent("x", "", "html")
	if err := e.Validate(); err == nil || !strings.Contains(err.Error(), "description") {
		t.Fatalf("Validate: %v", err)
	}
}

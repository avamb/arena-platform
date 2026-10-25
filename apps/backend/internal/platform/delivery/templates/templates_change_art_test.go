package templates

import (
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventnote"
)

// RT-11: the organizer's message of the change and cancel letters is ART read
// through richtext.Render (eventnote.BuildMessage), in all seven locales.
func TestRender_ChangeAndCancel_ArtMessageIsFormattedInHTMLAndPlainInText(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	block := eventnote.BuildMessage(
		`<p>Doors open at <strong>19:30</strong>.</p><ul><li>bring ID</li><li>no bags</li></ul><p><a href="https://example.com/info">https://example.com/info</a></p><script>alert(1)</script>`,
		"art",
	)
	if block.Empty() {
		t.Fatal("BuildMessage returned an empty block for a message")
	}
	data := changeTestData()
	data.Change.Message = block.Plain
	data.Change.MessageHTML = block.HTML
	for _, kind := range []string{TemplateKindChange, TemplateKindCancel} {
		for _, loc := range SupportedLocales {
			out, err := r.Render(kind, loc, data)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			for _, want := range []string{"<strong>19:30</strong>", "<li>bring ID</li>", `href="https://example.com/info"`, `rel="noopener noreferrer"`} {
				if !strings.Contains(out.HTMLBody, want) {
					t.Errorf("%s/%s: html lacks %q", kind, loc, want)
				}
			}
			if strings.Contains(out.HTMLBody, "<script") || strings.Contains(out.HTMLBody, "alert(1)") {
				t.Errorf("%s/%s: a script reached the letter", kind, loc)
			}
			if strings.Contains(out.HTMLBody, "white-space:pre-line") {
				t.Errorf("%s/%s: ART message printed as pre-line plain text", kind, loc)
			}
			if strings.Contains(out.HTMLBody, "&lt;p&gt;") || strings.Contains(out.TextBody, "<p>") || strings.Contains(out.TextBody, "<strong>") {
				t.Errorf("%s/%s: tags of the message are shown as text", kind, loc)
			}
			if !strings.Contains(out.TextBody, "Doors open at 19:30.") || !strings.Contains(out.TextBody, "• bring ID") {
				t.Errorf("%s/%s: plain text part lacks the message words:\n%s", kind, loc, out.TextBody)
			}
		}
	}
}

// An older plain message (MessageHTML empty) is still printed, escaped, as before.
func TestRender_ChangeAndCancel_PlainMessageFallsBackToPreLine(t *testing.T) {
	r, _ := New()
	data := changeTestData()
	data.Change.Message = "Line one\nLine <two>"
	for _, kind := range []string{TemplateKindChange, TemplateKindCancel} {
		for _, loc := range SupportedLocales {
			out, err := r.Render(kind, loc, data)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			if !strings.Contains(out.HTMLBody, "white-space:pre-line") || !strings.Contains(out.HTMLBody, "Line &lt;two&gt;") {
				t.Errorf("%s/%s: plain message not printed escaped with pre-line", kind, loc)
			}
		}
	}
}

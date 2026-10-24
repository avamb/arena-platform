package eventtext

import (
	"strings"
	"testing"
)

func TestParseFormat(t *testing.T) {
	for in, want := range map[string]string{"": "text", "text": "text", "art": "art"} {
		got, ok := ParseFormat(in)
		if !ok || got != want {
			t.Errorf("ParseFormat(%q) = %q, %v; want %q, true", in, got, ok, want)
		}
	}
	for _, in := range []string{"ART", "html", "Text", " art", "markdown"} {
		if got, ok := ParseFormat(in); ok {
			t.Errorf("ParseFormat(%q) = %q, true; want a refusal", in, got)
		}
	}
}

func TestNormalizeDescription_TextBecomesART(t *testing.T) {
	got, err := NormalizeDescription("**Big** night\n\n- one\n- two", "text")
	if err != nil {
		t.Fatal(err)
	}
	want := "<p><strong>Big</strong> night</p><ul><li>one</li><li>two</li></ul>"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// "text" is the default of an empty flag.
	if again, _ := NormalizeDescription("**Big** night\n\n- one\n- two", ""); again != want {
		t.Fatalf("empty format: %q", again)
	}
}

func TestNormalizeDescription_ARTIsSanitized(t *testing.T) {
	got, err := NormalizeDescription(`<h1>Title</h1><p onclick="x()">Hi <b>there</b></p><script>alert(1)</script>`, "art")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "script") || strings.Contains(got, "onclick") || strings.Contains(got, "<b>") {
		t.Fatalf("not sanitized: %q", got)
	}
	// The description profile keeps headings.
	if !strings.Contains(got, "<h3>Title</h3>") || !strings.Contains(got, "<strong>there</strong>") {
		t.Fatalf("lost allowed markup: %q", got)
	}
}

func TestNormalizeDescription_PlainTextNeverGuessedAsMarkup(t *testing.T) {
	got, err := NormalizeDescription("a < b and <3 and <b>not bold</b>", "text")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "<b>") || !strings.Contains(got, "&lt;b&gt;not bold&lt;/b&gt;") {
		t.Fatalf("a literal tag in plain text must stay visible text: %q", got)
	}
}

func TestNormalizeDescription_Reasons(t *testing.T) {
	cases := []struct {
		name, in, format, reason string
	}{
		{"visible limit", strings.Repeat("x", 4001), "text", "too_long"},
		{"html limit", strings.Repeat("x", 16001), "text", "html_too_long"},
		{"control char", "bad\x00byte", "text", "control_characters"},
		{"bidi override", "evil ‮ text", "text", "control_characters"},
		{"too many links", strings.Repeat(`<a href="https://a.test/">x</a> `, 21), "art", "too_many_links"},
		{"bad encoding", "bad \xff byte", "text", "bad_encoding"},
		{"unknown format", "x", "html", "invalid_format"},
	}
	for _, c := range cases {
		_, err := NormalizeDescription(c.in, c.format)
		if err == nil {
			t.Errorf("%s: no error", c.name)
			continue
		}
		if got := Reason(err); got != c.reason {
			t.Errorf("%s: reason %q, want %q", c.name, got, c.reason)
		}
	}
}

func TestNormalizeDescription_HebrewDirectionMarksSurvive(t *testing.T) {
	in := "שלום‏ עולם"
	got, err := NormalizeDescription(in, "text")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "‏") {
		t.Fatalf("the RLM was dropped: %q", got)
	}
}

func TestNormalizeNote(t *testing.T) {
	got, err := NormalizeNote("Come early\n\nSee https://example.com/x", "text")
	if err != nil {
		t.Fatal(err)
	}
	want := `<p>Come early</p><p>See <a href="https://example.com/x">https://example.com/x</a></p>`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if empty, err := NormalizeNote("   ", "text"); err != nil || empty != "" {
		t.Fatalf("a blank note means no note: %q, %v", empty, err)
	}
	// The letter-text profile has no headings and no mailto.
	art, err := NormalizeNote(`<h3>Head</h3><p><a href="mailto:a@b.test">mail</a></p>`, "art")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(art, "<h3>") || strings.Contains(art, "mailto") {
		t.Fatalf("the note profile let through %q", art)
	}
	for in, reason := range map[string]string{
		"go javascript:alert(1)":   "scheme_not_allowed",
		"visit http://example.com": "scheme_not_allowed",
		strings.Repeat("x", 2001):  "too_long",
		strings.Repeat("x", 8001):  "html_too_long",
		"bad\x00byte":              "control_characters",
	} {
		_, err := NormalizeNote(in, "text")
		if err == nil || Reason(err) != reason {
			t.Errorf("NormalizeNote(%.20q): %v, want reason %s", in, err, reason)
		}
	}
}

func TestUpgradeError(t *testing.T) {
	_, err := NormalizeDescription(strings.Repeat("x", 4001), "text")
	ue := &UpgradeError{Locale: "he", Err: err}
	if !strings.Contains(ue.Error(), "(he)") {
		t.Fatalf("locale missing: %s", ue.Error())
	}
	got, ok := AsUpgradeError(ue)
	if !ok || got.Locale != "he" || Reason(got.Err) != "too_long" {
		t.Fatalf("AsUpgradeError: %v %v", got, ok)
	}
	if _, ok := AsUpgradeError(err); ok {
		t.Fatal("a richtext error is not an upgrade error")
	}
}

package eventnote

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

func TestBuild_ARTNoteRendersFormattedHTML(t *testing.T) {
	note := `<p>Come <strong>early</strong>.</p><ul><li>coat check</li><li><a href="https://example.com/map">map</a></li></ul>`
	b := Build(note, richtext.FormatART)
	html := string(b.HTML)
	for _, want := range []string{"<strong>early</strong>", "<li>coat check</li>", `href="https://example.com/map"`, `rel="noopener noreferrer"`, `target="_blank"`, "<p style="} {
		if !strings.Contains(html, want) {
			t.Errorf("html misses %q: %s", want, html)
		}
	}
	if strings.Contains(html, "&lt;") {
		t.Errorf("the markup was escaped: %s", html)
	}
	if strings.ContainsAny(b.Plain, "<>") || !strings.Contains(b.Plain, "Come early.") || !strings.Contains(b.Plain, "coat check") {
		t.Errorf("plain part: %q", b.Plain)
	}
	if b.Empty() {
		t.Error("a note with text is empty")
	}
}

func TestBuild_LegacyTextKeepsClickableLinksAndMarks(t *testing.T) {
	for _, format := range []string{richtext.FormatText, ""} {
		b := Build("Doors **close** at 19:00\n\nMap: https://example.com/info, please.", format)
		html := string(b.HTML)
		if !strings.Contains(html, "<strong>close</strong>") || !strings.Contains(html, `<a style="color:#1a73e8;" href="https://example.com/info"`) {
			t.Errorf("format %q: %s", format, html)
		}
		if !strings.Contains(b.Plain, "Doors **close** at 19:00") {
			t.Errorf("format %q: legacy plain part is the stored text, got %q", format, b.Plain)
		}
	}
}

func TestBuild_LegacyTextNeverBecomesMarkup(t *testing.T) {
	b := Build("<script>alert(1)</script> and <b>x</b>", "text")
	if strings.Contains(string(b.HTML), "<script") || strings.Contains(string(b.HTML), "<b>") {
		t.Fatalf("markup got through: %s", b.HTML)
	}
}

func TestBuild_EmptyIsZero(t *testing.T) {
	for _, in := range []string{"", "  \n ", "<p></p>"} {
		if b := Build(in, "art"); !b.Empty() || b.Plain != "" || b.HTML != "" {
			t.Errorf("%q: not empty: %#v", in, b)
		}
	}
}

func TestBuild_HostileARTIsSanitisedAgain(t *testing.T) {
	b := Build(`<p onclick="x()">a<script>bad()</script><a href="javascript:alert(1)">b</a></p>`, "art")
	h := string(b.HTML)
	if strings.Contains(h, "script") || strings.Contains(h, "onclick") || strings.Contains(h, "javascript:") {
		t.Fatalf("hostile ART reached the letter: %s", h)
	}
}

// The table's CHECK is the HTML limit of the letter-text profile (spec 43,
// RT-02: migration 0168 widened the 2000 of 0165 to the 8000 characters of the
// stored HTML; the visible 2000 are checked by Go).
func TestLimitMatchesMigration(t *testing.T) {
	body, err := os.ReadFile("../../migrations/sql/0168_rich_text.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(body)
	if i := strings.Index(up, "-- +goose Down"); i >= 0 {
		up = up[:i]
	}
	m := regexp.MustCompile(`char_length\(note\)\s+BETWEEN\s+1\s+AND\s+(\d+)`).FindStringSubmatch(up)
	if m == nil {
		t.Fatal("note length CHECK not found in the Up block of 0168")
	}
	n, _ := strconv.Atoi(m[1])
	if n != richtext.ProfileEmailNote.MaxHTML {
		t.Fatalf("migration allows %d characters, richtext.ProfileEmailNote.MaxHTML is %d", n, richtext.ProfileEmailNote.MaxHTML)
	}
}

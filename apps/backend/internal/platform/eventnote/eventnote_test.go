package eventnote

import (
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

func TestNormalize_PlainTextPasses(t *testing.T) {
	got, err := Normalize("Come 30 minutes early.\r\nThe cloakroom is free.\r\n\r\n\r\n\r\nSee https://example.com/info.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "Come 30 minutes early.\nThe cloakroom is free.\n\nSee https://example.com/info."
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestNormalize_StripsHTMLTags(t *testing.T) {
	cases := map[string]string{
		"Hello <b>world</b>":                          "Hello world",
		`<a href="https://x.test">go</a>`:             "go",
		"<script>alert(1)</script>Bring ID":           "alert(1)Bring ID",
		"<<b>b>nested":                                "nested",
		"a < b and c > d":                             "a < b and c > d",
		"<img src=x onerror=alert(1)>text":            "text",
		`<a href="javascript:alert(1)">click</a> now`: "click now",
	}
	for in, want := range cases {
		got, err := Normalize(in)
		if err != nil {
			t.Errorf("%q: unexpected error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestNormalize_Refusals(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"nul", "a\x00b", ErrControlChars},
		{"escape", "a\x1bb", ErrControlChars},
		{"bidi override", "a\u202eb", ErrControlChars},
		{"line separator", "a\u2028b", ErrControlChars},
		{"bad utf8", "a\xffb", ErrBadEncoding},
		{"too long", strings.Repeat("я", MaxRunes+1), ErrTooLong},
		{"javascript", "see javascript:alert(1)", ErrScheme},
		{"data uri", "DATA:text/html;base64,AAAA", ErrScheme},
		{"vbscript", "vbscript:msgbox", ErrScheme},
		{"file", "file:///etc/passwd", ErrScheme},
		{"plain http", "visit http://example.com", ErrScheme},
		{"six links", strings.Repeat("https://a.test/x ", MaxLinks+1), ErrTooManyLinks},
		{"https without a host", "go to https:// now", ErrUnsafeLink},
		{"link with credentials", "https://user:pass@a.test/", ErrUnsafeLink},
		{"link too long", "https://a.test/" + strings.Repeat("a", MaxLinkLength), ErrUnsafeLink},
	}
	for _, c := range cases {
		_, err := Normalize(c.in)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: got %v want %v", c.name, err, c.want)
		}
		if err != nil && Reason(err) == "invalid" {
			t.Errorf("%s: Reason is the generic word", c.name)
		}
	}
}

func TestNormalize_NoFalsePositives(t *testing.T) {
	for _, in := range []string{
		"Metadata: bring a pass",
		"Time: 19:30, doors 19:00",
		"Update: the hall is warm",
		"data: none",
		"Profile: https://example.com/a?b=1&c=2#frag",
		"5 ссылок: https://a.test https://b.test https://c.test https://d.test https://e.test",
	} {
		if _, err := Normalize(in); err != nil {
			t.Errorf("%q: unexpected error %v", in, err)
		}
	}
}

func TestNormalize_LimitIsInCharactersNotBytes(t *testing.T) {
	if _, err := Normalize(strings.Repeat("я", MaxRunes)); err != nil {
		t.Fatalf("2000 Cyrillic characters must pass: %v", err)
	}
}

func TestNormalize_IsIdempotent(t *testing.T) {
	for _, in := range []string{
		"  hello \t world  \r\n\r\n\r\n\r\nnext  ",
		"<<b>b>x <i>y</i>",
		"link https://a.test/path, then.",
		"",
		"   ",
	} {
		once, err := Normalize(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		twice, err := Normalize(once)
		if err != nil || twice != once {
			t.Errorf("%q: not idempotent: %q -> %q (%v)", in, once, twice, err)
		}
	}
}

func TestNormalize_EmptyMeansNoNote(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\n", "<b></b>"} {
		got, err := Normalize(in)
		if err != nil || got != "" {
			t.Errorf("%q: got %q, %v", in, got, err)
		}
	}
}

func TestParse_ParagraphsAndLinks(t *testing.T) {
	b := Parse("First line.\nSecond line.\n\nRead https://example.com/info, please.\n\nhttps://a.test")
	if len(b.Paragraphs) != 3 {
		t.Fatalf("paragraphs: %d", len(b.Paragraphs))
	}
	p := b.Paragraphs[1]
	if len(p) != 3 || p[0].Text != "Read " || p[1].URL != "https://example.com/info" || p[2].Text != ", please." {
		t.Fatalf("segments: %#v", p)
	}
	if b.Paragraphs[2][0].URL != "https://a.test" {
		t.Fatalf("bare link: %#v", b.Paragraphs[2])
	}
	if !strings.Contains(b.Plain, "First line.\nSecond line.") {
		t.Fatalf("plain: %q", b.Plain)
	}
}

func TestParse_EmptyIsZero(t *testing.T) {
	if b := Parse("  \n "); !b.Empty() || b.Plain != "" {
		t.Fatalf("not empty: %#v", b)
	}
}

func TestParse_QuoteClosingLinkKeepsMultibyteText(t *testing.T) {
	b := Parse("«see https://a.test/x»")
	p := b.Paragraphs[0]
	if len(p) != 3 || p[1].URL != "https://a.test/x" || p[2].Text != "»" {
		t.Fatalf("segments: %#v", p)
	}
}

// The table's CHECK is the HTML limit of the letter-text profile (spec 43,
// RT-02: migration 0167 widened the 2000 of 0165 to the 8000 characters of the
// stored HTML; the visible 2000 are checked by Go).
func TestLimitMatchesMigration(t *testing.T) {
	body, err := os.ReadFile("../../migrations/sql/0167_rich_text.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(body)
	if i := strings.Index(up, "-- +goose Down"); i >= 0 {
		up = up[:i]
	}
	m := regexp.MustCompile(`char_length\(note\)\s+BETWEEN\s+1\s+AND\s+(\d+)`).FindStringSubmatch(up)
	if m == nil {
		t.Fatal("note length CHECK not found in the Up block of 0167")
	}
	n, _ := strconv.Atoi(m[1])
	if n != richtext.ProfileEmailNote.MaxHTML {
		t.Fatalf("migration allows %d characters, richtext.ProfileEmailNote.MaxHTML is %d", n, richtext.ProfileEmailNote.MaxHTML)
	}
	if richtext.ProfileEmailNote.MaxVisible != MaxRunes {
		t.Fatalf("richtext.ProfileEmailNote.MaxVisible is %d, eventnote.MaxRunes is %d", richtext.ProfileEmailNote.MaxVisible, MaxRunes)
	}
}

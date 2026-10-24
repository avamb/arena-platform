package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

func TestSingleMarkerLines(t *testing.T) {
	cases := []struct {
		name, in string
		want     []string
	}{
		{"lone dash", "- Hello!", []string{"- Hello!"}},
		{"lone star in a paragraph", "Intro\n* only one\nOutro", []string{"* only one"}},
		{"two lines are a list", "- one\n- two", nil},
		{"list and a stray line", "- one\n- two\n\ntext\n\n- alone", []string{"- alone"}},
		{"bold is not a marker", "**bold** text", nil},
		{"dash without space", "-5 degrees", nil},
		{"bullet", "• lone bullet", []string{"• lone bullet"}},
		{"crlf", "a\r\n- x\r\nb", []string{"- x"}},
		{"none", "plain text", nil},
	}
	for _, c := range cases {
		got := singleMarkerLines(c.in)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLiteralTags(t *testing.T) {
	got := literalTags("Use <b>bold</b> and <3 and a < b > c, <i>x</i>")
	if len(got) == 0 || got[0] != "<b>" {
		t.Fatalf("%q", got)
	}
	if len(literalTags("a < b and 3 > 2")) != 1 { // "< b and 3 >" is a span between a < and a >
		t.Fatalf("the span between < and > counts once")
	}
	if len(literalTags("no markup at all")) != 0 {
		t.Fatal("false positive")
	}
	if n := len(literalTags(strings.Repeat("<a>", 20))); n != 5 {
		t.Fatalf("at most 5 per text, got %d", n)
	}
}

func TestParseFlags(t *testing.T) {
	f, err := parseFlags(nil)
	if err != nil || f.apply || f.isRoll || f.limit != 0 || f.report != "" {
		t.Fatalf("defaults: %+v %v", f, err)
	}
	f, err = parseFlags([]string{"--apply", "--limit", "5", "--report", "r.md"})
	if err != nil || !f.apply || f.limit != 5 || f.report != "r.md" {
		t.Fatalf("apply: %+v %v", f, err)
	}
	if f, err = parseFlags([]string{"--dry-run=false"}); err != nil || !f.apply {
		t.Fatalf("--dry-run=false must apply: %+v %v", f, err)
	}
	if f, err = parseFlags([]string{"--dry-run=true"}); err != nil || f.apply {
		t.Fatalf("--dry-run=true: %+v %v", f, err)
	}
	if _, err = parseFlags([]string{"--apply", "--dry-run=true"}); err == nil {
		t.Fatal("--apply with --dry-run=true must be refused")
	}
	if _, err = parseFlags([]string{"--limit", "-1"}); err == nil {
		t.Fatal("a negative limit must be refused")
	}
	id := uuid.New()
	f, err = parseFlags([]string{"--rollback", id.String()})
	if err != nil || !f.isRoll || f.rollback != id || f.apply {
		t.Fatalf("rollback: %+v %v", f, err)
	}
	if _, err = parseFlags([]string{"--rollback", "not-a-uuid"}); err == nil {
		t.Fatal("a bad run id must be refused")
	}
}

func TestReportMarkdown(t *testing.T) {
	id := uuid.New()
	rep := &Report{RunID: id, Apply: true, EventsScanned: 2, NotesScanned: 1, ShowText: true}
	rep.Changed = append(rep.Changed, changedRow{Entity: entityDescriptionI18n, ID: "e1", Locale: "he", Before: "שלום\u200f **x**", After: "<p>שלום\u200f <strong>x</strong></p>"})
	rep.Skipped = append(rep.Skipped, skippedRow{Entity: entityDescription, ID: "e2", Reason: "too_long", Detail: "richtext: the visible text is too long"})
	rep.SingleMarkers = append(rep.SingleMarkers, noticeRow{Entity: entityDescription, ID: "e3", Detail: "- Hello!"})
	rep.LiteralTags = append(rep.LiteralTags, noticeRow{Entity: entityEmailNote, ID: "e4", Detail: "<b>"})
	md := rep.Markdown()
	for _, want := range []string{
		"APPLIED", id.String(), "locale=he", "Before:", "After:", "<p>שלום\u200f <strong>x</strong></p>",
		"**too_long**", "locale=base", "- Hello!", "`<b>`", "## Changed (1)", "## Left as they are (1)",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("report lacks %q:\n%s", want, md)
		}
	}
	dry := (&Report{RunID: id}).Markdown()
	if !strings.Contains(dry, "DRY RUN") || strings.Contains(dry, id.String()) || !strings.Contains(dry, "## Would be changed (0)") {
		t.Errorf("dry run header:\n%s", dry)
	}
	roll := (&Report{RunID: id, Apply: true, Rollback: true}).Markdown()
	if !strings.Contains(roll, "## Restored (0)") {
		t.Errorf("rollback header:\n%s", roll)
	}
}

func TestFenceOutgrowsBackticksInText(t *testing.T) {
	out := fence("a ``` b")
	if !strings.HasPrefix(out, "````\n") || !strings.HasSuffix(out, "\n````\n") {
		t.Fatalf("%q", out)
	}
	long := strings.Repeat("я", maxShown+10)
	if got := shorten(long); !strings.Contains(got, "[10 more characters]") {
		t.Fatalf("shorten: %.40s", got)
	}
}

func TestParseFlags_Org(t *testing.T) {
	id := uuid.New()
	f, err := parseFlags([]string{"--org", id.String(), "--apply"})
	if err != nil || f.org != id || !f.apply {
		t.Fatalf("%+v %v", f, err)
	}
	if f, err = parseFlags(nil); err != nil || f.org != uuid.Nil {
		t.Fatalf("default: %+v %v", f, err)
	}
	if _, err = parseFlags([]string{"--org", "acme"}); err == nil {
		t.Fatal("a bad organization id must be refused")
	}
}

func TestHasHTMLTags(t *testing.T) {
	for _, in := range []string{"<p>x</p>", "a <strong>b</strong>", "line<br>two", "line<br/>two", `see <a href="https://x.test">x</a>`, "<UL><li>x</li></UL>", "<h3>T</h3>", "<div class=\"a\">x</div>"} {
		if !hasHTMLTags(in) {
			t.Errorf("%q: tags not found", in)
		}
	}
	for _, in := range []string{"plain text", "a < b and 3 > 2", "Use <this> word", "mail <me@example.com>", "**bold** _it_", "<3 you", "<abbr>x</abbr>"} {
		if hasHTMLTags(in) {
			t.Errorf("%q: false positive", in)
		}
	}
}

func TestConvertText_LiteralTagsAreLeftAloneUnlessAsked(t *testing.T) {
	if _, err := convertText("<p>Concert <b>x</b></p>", richtext.ProfileDescription, false); err == nil || reasonOf(err) != reasonLiteralTags {
		t.Fatalf("html text: %v", err)
	}
	got, err := convertText("<p>Concert <b>x</b></p>", richtext.ProfileDescription, true)
	if err != nil || got != "<p>Concert <strong>x</strong></p>" {
		t.Fatalf("html as art: %q %v", got, err)
	}
	// A text without tags is the old text either way.
	got, err = convertText("**Big** night", richtext.ProfileDescription, true)
	if err != nil || got != "<p><strong>Big</strong> night</p>" {
		t.Fatalf("plain: %q %v", got, err)
	}
	if _, err := convertText("<p><script>x()</script></p>", richtext.ProfileDescription, true); err == nil {
		t.Fatal("a text that is empty after conversion must not be written")
	}
}

func TestParseFlags_NewFlags(t *testing.T) {
	id := uuid.New()
	f, err := parseFlags([]string{"--html-as-art", "--after", id.String(), "--limit", "10", "--show-text", "--report", "r.md"})
	if err != nil || !f.htmlAsART || f.after != id || f.limit != 10 || !f.showText {
		t.Fatalf("%+v %v", f, err)
	}
	for name, args := range map[string][]string{
		"show-text without a file":  {"--show-text"},
		"bad cursor":                {"--after", "xyz"},
		"rollback with limit":       {"--rollback", id.String(), "--limit", "5"},
		"rollback with after":       {"--rollback", id.String(), "--after", id.String()},
		"rollback with html-as-art": {"--rollback", id.String(), "--html-as-art"},
		"negative purge":            {"--purge-backup-older-than", "-3"},
		"purge with rollback":       {"--purge-backup-older-than", "30", "--rollback", id.String()},
		"purge with limit":          {"--purge-backup-older-than", "30", "--limit", "5"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
	f, err = parseFlags([]string{"--rollback", id.String(), "--org", id.String()})
	if err != nil || f.org != id || !f.isRoll {
		t.Fatalf("rollback with org: %+v %v", f, err)
	}
	f, err = parseFlags([]string{"--purge-backup-older-than", "30", "--apply"})
	if err != nil || f.purgeDays != 30 || !f.apply {
		t.Fatalf("purge: %+v %v", f, err)
	}
}

// By default the report has no text of an event or a note: ids, locales,
// lengths and reasons only. The texts are in it only when asked for.
func TestReportMarkdown_NoTextsByDefault(t *testing.T) {
	rep := &Report{RunID: uuid.New(), Apply: true}
	secret := "call +420 777 123 456 or <b>write</b> to boss@example.com"
	rep.changed(entityEmailNote, "e1", "", secret, "<p>"+secret+"</p>")
	rep.notices("e1", textItem{entity: entityEmailNote, old: "- " + secret})
	rep.Skipped = append(rep.Skipped, skippedRow{Entity: entityDescription, ID: "e2", Reason: reasonLiteralTags, Detail: "literal_tags"})
	md := rep.Markdown()
	for _, bad := range []string{"777", "boss@example.com", "<b>", "Before:", "After:"} {
		if strings.Contains(md, bad) {
			t.Fatalf("the default report holds %q:\n%s", bad, md)
		}
	}
	for _, want := range []string{"e1", "e2", "literal_tags", "characters", "## Changed (1)"} {
		if !strings.Contains(md, want) {
			t.Errorf("the default report lacks %q:\n%s", want, md)
		}
	}
	if len(rep.Changed[0].Before) != 0 || len(rep.Changed[0].After) != 0 || rep.Changed[0].BeforeLen != len([]rune(secret)) {
		t.Fatalf("the report row keeps the texts in memory: %+v", rep.Changed[0])
	}

	shown := &Report{RunID: uuid.New(), Apply: true, ShowText: true}
	shown.changed(entityEmailNote, "e1", "", secret, "<p>"+secret+"</p>")
	if md := shown.Markdown(); !strings.Contains(md, "777 123 456") || !strings.Contains(md, "Before:") {
		t.Fatalf("--show-text report lacks the texts:\n%s", md)
	}
}

func TestReportMarkdown_CursorAndPurge(t *testing.T) {
	next := uuid.New()
	if md := (&Report{RunID: uuid.New(), NextAfter: next}).Markdown(); !strings.Contains(md, "--after "+next.String()) {
		t.Fatalf("the cursor is not printed:\n%s", md)
	}
	md := (&Report{Apply: true, Purge: &purgeResult{Days: 30, Rows: 7}}).Markdown()
	if !strings.Contains(md, "older than 30 days deleted: 7") {
		t.Fatalf("purge report:\n%s", md)
	}
	if md := (&Report{Purge: &purgeResult{Days: 30, Rows: 7}}).Markdown(); !strings.Contains(md, "would be deleted: 7") {
		t.Fatalf("purge dry run:\n%s", md)
	}
}

// The report file is created with mode 0600, and an existing wider one is
// narrowed: with --show-text it holds the texts of events and notes.
func TestWriteReport_FileModeIs0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.md")
	rep := &Report{RunID: uuid.New()}
	if err := writeReport(nil, path, rep); err != nil {
		t.Fatal(err)
	}
	check := func(label string) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s: report mode is %v", label, info.Mode().Perm())
		}
	}
	check("new file")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeReport(nil, path, rep); err != nil {
		t.Fatal(err)
	}
	check("existing file")
	if body, err := os.ReadFile(path); err != nil || !strings.Contains(string(body), "Rich-text backfill") {
		t.Fatalf("report body: %q %v", body, err)
	}
	var sb strings.Builder
	if err := writeReport(&sb, "", rep); err != nil || !strings.Contains(sb.String(), "Rich-text backfill") {
		t.Fatalf("stdout report: %q %v", sb.String(), err)
	}
}

// --html-as-art on a WordPress text keeps its paragraphs (blank lines) and
// line breaks; a text that has <p> is read as it is.
func TestConvertText_HTMLAsARTKeepsWordPressParagraphs(t *testing.T) {
	wp := "Intro with <b>bold</b>.\n\nSecond,\nsame paragraph.\n\nThird."
	got, err := convertText(wp, richtext.ProfileDescription, true)
	want := "<p>Intro with <strong>bold</strong>.</p><p>Second,<br>same paragraph.</p><p>Third.</p>"
	if err != nil || got != want {
		t.Fatalf("got %q %v, want %q", got, err, want)
	}
	got, err = convertText("<p>a</p>\n\n<p>b</p>", richtext.ProfileDescription, true)
	if err != nil || got != "<p>a</p><p>b</p>" {
		t.Fatalf("with <p>: %q %v", got, err)
	}
}

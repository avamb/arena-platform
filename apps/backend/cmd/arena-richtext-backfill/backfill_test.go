package main

import (
	"strings"
	"testing"

	"github.com/google/uuid"
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
	rep := &Report{RunID: id, Apply: true, EventsScanned: 2, NotesScanned: 1}
	rep.Changed = append(rep.Changed, changedRow{Entity: entityDescriptionI18n, ID: "e1", Locale: "he", Before: "שלום‏ **x**", After: "<p>שלום‏ <strong>x</strong></p>"})
	rep.Skipped = append(rep.Skipped, skippedRow{Entity: entityDescription, ID: "e2", Reason: "too_long", Detail: "richtext: the visible text is too long"})
	rep.SingleMarkers = append(rep.SingleMarkers, noticeRow{Entity: entityDescription, ID: "e3", Detail: "- Hello!"})
	rep.LiteralTags = append(rep.LiteralTags, noticeRow{Entity: entityEmailNote, ID: "e4", Detail: "<b>"})
	md := rep.Markdown()
	for _, want := range []string{
		"APPLIED", id.String(), "locale=he", "Before:", "After:", "<p>שלום‏ <strong>x</strong></p>",
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

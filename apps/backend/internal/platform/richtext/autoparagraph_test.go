package richtext

import (
	"strings"
	"testing"
)

// A WordPress post_content sample: no <p>, blank lines between paragraphs,
// inline tags, a single newline inside a paragraph.
const wpSample = "Первый абзац с <strong>важным</strong> словом.\r\n\r\nВторой абзац,\r\nвторая строка со ссылкой <a href=\"https://example.com/a\">здесь</a>.\n\n\n\nТретий &amp; последний."

func TestAutoParagraph_WordPressSample(t *testing.T) {
	got := AutoParagraph(wpSample)
	want := "<p>Первый абзац с <strong>важным</strong> словом.</p><p>Второй абзац,<br>вторая строка со ссылкой <a href=\"https://example.com/a\">здесь</a>.</p><p>Третий &amp; последний.</p>"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestAutoParagraph_Unchanged(t *testing.T) {
	for _, in := range []string{
		"",
		"one line",
		"<p>a</p>\n\n<p>b</p>",
		"a<br>\n\nb",
		"<div>a</div>\n\nb",
		"<ul>\n<li>a</li>\n</ul>\n\nb",
		"<h2>T</h2>\n\nb",
		"<blockquote>q</blockquote>\n\nb",
		"<table><tr><td>a</td></tr></table>\n\nb",
		"<BR/>\n\nb",
	} {
		if got := AutoParagraph(in); got != in {
			t.Errorf("AutoParagraph(%q) = %q, want it unchanged", in, got)
		}
	}
}

func TestAutoParagraph_TagsAreNotBrokenByNewlines(t *testing.T) {
	in := "a\n\n<a\nhref=\"https://example.com\"\n>x</a>\nb"
	got := AutoParagraph(in)
	if !strings.Contains(got, "<a\nhref=\"https://example.com\"\n>") {
		t.Fatalf("a tag was changed: %s", got)
	}
	if !strings.Contains(got, "</a><br>b</p>") {
		t.Fatalf("got %s", got)
	}
}

func TestAutoParagraph_CommentsAndLessThan(t *testing.T) {
	// "<!-- <p> -->" is a comment and "a < b" is text: neither is a block tag.
	got := AutoParagraph("a < b\n\n<!-- <p> -->c")
	if got != "<p>a < b</p><p><!-- <p> -->c</p>" {
		t.Fatalf("got %s", got)
	}
}

func TestNormalizeART_WordPressParagraphs(t *testing.T) {
	got, err := Normalize(wpSample, FormatART, ProfileDescription)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(got, "<p>"); n != 3 {
		t.Fatalf("want 3 paragraphs, got %d: %s", n, got)
	}
	if !strings.Contains(got, "Второй абзац,<br>вторая строка") {
		t.Fatalf("the single newline must be a <br>: %s", got)
	}
	// idempotent
	again, err := Normalize(got, FormatART, ProfileDescription)
	if err != nil || again != got {
		t.Fatalf("not idempotent: %q %v", again, err)
	}
}

func TestNormalizeART_WithParagraphsIsUntouched(t *testing.T) {
	in := "<p>one\ntwo</p>\n\n<p>three</p>"
	got, err := Normalize(in, FormatART, ProfileDescription)
	if err != nil {
		t.Fatal(err)
	}
	if got != "<p>one two</p><p>three</p>" {
		t.Fatalf("got %s", got)
	}
}

func TestRenderART_WordPressParagraphs(t *testing.T) {
	got, err := Render("a\n\nb\nc", FormatART, ProfileDescription)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "<p>a</p>") || !strings.Contains(got, "b<br>c") {
		t.Fatalf("got %s", got)
	}
}

func TestAutoParagraph_ScriptStaysDropped(t *testing.T) {
	got, err := Normalize("a\n\n<script>alert(1)\n\nx</script>b\n\n<img src=x onerror=alert(1)>", FormatART, ProfileDescription)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "alert") || strings.Contains(got, "script") || strings.Contains(got, "onerror") {
		t.Fatalf("unsafe: %s", got)
	}
}

func TestLooksLikeHTML(t *testing.T) {
	yes := []string{"<p>a</p>", "x <b>y</b>", "a<br>b", "<div>a</div>", "<span>a</span>", "</strong>", "<A href=x>y</a>", "<h3>t</h3>", "<h2>t</h2>", "<br/>", "<i>x</i>"}
	no := []string{"", "plain", "a < b", "a < b and c > d", "1<2>3", "<3", "<!-- <p> -->", "<script>x</script>", "<foo>bar</foo>", "&lt;p&gt;", "x <img src=y>", "**bold** _it_"}
	for _, s := range yes {
		if !LooksLikeHTML(s) {
			t.Errorf("LooksLikeHTML(%q) = false", s)
		}
	}
	for _, s := range no {
		if LooksLikeHTML(s) {
			t.Errorf("LooksLikeHTML(%q) = true", s)
		}
	}
}

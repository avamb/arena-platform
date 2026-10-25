package eventnote

import (
	"strings"
	"testing"
)

func TestBuildMessage(t *testing.T) {
	t.Run("blank is the zero block", func(t *testing.T) {
		if b := BuildMessage("  \n ", "art"); !b.Empty() || b.Plain != "" {
			t.Fatalf("got %+v", b)
		}
	})
	t.Run("art keeps its markup, links get rel", func(t *testing.T) {
		b := BuildMessage(`<p>Hi <strong>all</strong> <a href="https://example.com/x">https://example.com/x</a></p>`, "art")
		html := string(b.HTML)
		for _, want := range []string{"<strong>all</strong>", `rel="noopener noreferrer"`, `target="_blank"`} {
			if !strings.Contains(html, want) {
				t.Errorf("html lacks %q: %s", want, html)
			}
		}
		if b.Plain != "Hi all https://example.com/x" {
			t.Errorf("plain = %q", b.Plain)
		}
	})
	t.Run("art tags the profile has no place for become text, never markup", func(t *testing.T) {
		b := BuildMessage(`<h3>Title</h3><blockquote>q</blockquote><img src="x"><script>x()</script>`, "art")
		html := string(b.HTML)
		for _, bad := range []string{"<h3", "<blockquote", "<img", "<script"} {
			if strings.Contains(html, bad) {
				t.Errorf("html holds %s: %s", bad, html)
			}
		}
		if !strings.Contains(b.Plain, "Title") {
			t.Errorf("plain = %q", b.Plain)
		}
	})
	t.Run("an old plain row (format text or empty) is read through FromLegacy", func(t *testing.T) {
		for _, format := range []string{"text", "", "weird"} {
			b := BuildMessage("Line one\nLine <two>\n\nNew **para**", format)
			html := string(b.HTML)
			if !strings.Contains(html, "<br>") || !strings.Contains(html, "&lt;two&gt;") || !strings.Contains(html, "<strong>para</strong>") {
				t.Errorf("format %q: html = %s", format, html)
			}
			// The text part drops the markers the HTML turned into bold.
			if !strings.HasPrefix(b.Plain, "Line one\nLine <two>") || !strings.HasSuffix(b.Plain, "New para") {
				t.Errorf("format %q: plain = %q", format, b.Plain)
			}
		}
	})
}

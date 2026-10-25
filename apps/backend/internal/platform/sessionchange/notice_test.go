package sessionchange

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeNotice(t *testing.T) {
	t.Run("a plain text (the default and what the bot sends) is stored exactly as before, format text", func(t *testing.T) {
		for _, format := range []string{"", "text"} {
			got, stored, err := NormalizeNotice("  Doors open at 19:30.\r\nPlease bring ID. \r\n\r\nSee you!  ", format)
			if err != nil {
				t.Fatalf("format %q: %v", format, err)
			}
			if want := "Doors open at 19:30.\nPlease bring ID. \n\nSee you!"; got != want || stored != "text" {
				t.Errorf("format %q: got %q (%s) want %q (text)", format, got, stored, want)
			}
		}
	})
	t.Run("a typed tag in a plain text stays what was typed, the letters show it as text", func(t *testing.T) {
		got, stored, err := NormalizeNotice("a <b> c", "text")
		if err != nil || got != "a <b> c" || stored != "text" {
			t.Fatalf("got %q (%s), %v", got, stored, err)
		}
	})
	t.Run("stray control characters of a plain text are dropped as before", func(t *testing.T) {
		got, _, err := NormalizeNotice("a\x00b\x07c\x7f", "text")
		if err != nil || got != "abc" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("a plain text is never refused for a character an older client sent, it is stripped like Render does", func(t *testing.T) {
		cases := []struct{ name, in, want string }{
			{"england flag", "Match \U0001F3F4\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F today", "Match \U0001F3F4 today"},
			{"rtl embedding", "\u202bshalom\u202c now", "shalom now"},
			{"bidi override", "a\u202eb", "ab"},
			{"c1 control", "a\u0085b\u009fc", "abc"},
			{"invisible operator", "a\u2062b", "ab"},
		}
		for _, c := range cases {
			for _, format := range []string{"", "text"} {
				got, stored, err := NormalizeNotice(c.in, format)
				if err != nil || got != c.want || stored != "text" {
					t.Errorf("%s (%q): got %q (%s), %v want %q", c.name, format, got, stored, err, c.want)
				}
			}
			if got, err := NormalizeMessage(c.in); err != nil || got != c.want {
				t.Errorf("%s: NormalizeMessage got %q, %v want %q", c.name, got, err, c.want)
			}
		}
	})
	t.Run("a plain text is bounded by its characters, not by the HTML Render makes of it", func(t *testing.T) {
		// 900 "&" are 4500 characters of HTML, over the 4000 of the profile.
		if got, _, err := NormalizeNotice(strings.Repeat("&", 900), "text"); err != nil || got != strings.Repeat("&", 900) {
			t.Errorf("900 ampersands: %v", err)
		}
		if _, _, err := NormalizeNotice(strings.Repeat("&", 1000), "text"); err != nil {
			t.Errorf("1000 ampersands: %v", err)
		}
	})
	t.Run("art is cut to the whitelist of the profile and stored as ART, format art", func(t *testing.T) {
		got, stored, err := NormalizeNotice(`<div><h3>T</h3><p onclick="x()">A <b>b</b> <i>i</i> <u>u</u></p><ul><li>x</li></ul><img src=x><script>y()</script></div>`, "art")
		if err != nil || stored != "art" {
			t.Fatalf("%v (%s)", err, stored)
		}
		for _, bad := range []string{"<h3", "<div", "onclick", "<img", "<script", "y()"} {
			if strings.Contains(got, bad) {
				t.Errorf("%q survived in %q", bad, got)
			}
		}
		for _, want := range []string{"<strong>b</strong>", "<em>i</em>", "<u>u</u>", "<ul><li>x</li></ul>"} {
			if !strings.Contains(got, want) {
				t.Errorf("%q missing in %q", want, got)
			}
		}
	})
	t.Run("an https link survives, a javascript or http one does not", func(t *testing.T) {
		got, _, err := NormalizeNotice(`<p><a href="https://example.com/a">ok</a> <a href="javascript:alert(1)">bad</a> <a href="http://example.com">plain</a></p>`, "art")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, `<a href="https://example.com/a">ok</a>`) || strings.Contains(got, "javascript") || strings.Contains(got, "http://") {
			t.Errorf("got %q", got)
		}
	})
	t.Run("the limit is 1000 VISIBLE characters of ART, and 1000 characters of a plain text as before", func(t *testing.T) {
		if _, _, err := NormalizeNotice(strings.Repeat("я", 1000), "text"); err != nil {
			t.Errorf("1000 characters refused: %v", err)
		}
		if _, _, err := NormalizeNotice(strings.Repeat("я", 1001), "text"); !errors.Is(err, ErrMessageTooLong) {
			t.Errorf("1001 characters: %v", err)
		}
		// 1000 visible characters in ART with markup: the tags do not count.
		art := "<p><strong>" + strings.Repeat("ы", 500) + "</strong><em>" + strings.Repeat("ы", 500) + "</em></p>"
		if got, _, err := NormalizeNotice(art, "art"); err != nil || len(got) < 1000 {
			t.Errorf("1000 visible characters of markup refused: %v", err)
		}
		if _, _, err := NormalizeNotice("<p>"+strings.Repeat("я", 1001)+"</p>", "art"); !errors.Is(err, ErrMessageTooLong) {
			t.Errorf("1001 visible characters of ART: %v", err)
		}
		// Far longer than any message can be: refused before parsing, same error.
		for _, format := range []string{"text", "art"} {
			if _, _, err := NormalizeNotice(strings.Repeat("я", 5000), format); !errors.Is(err, ErrMessageTooLong) {
				t.Errorf("5000 characters (%s): %v", format, err)
			}
		}
	})
	t.Run("an unknown format and a forbidden character are reasons, not 'too long'", func(t *testing.T) {
		_, _, err := NormalizeNotice("x", "markdown")
		if !errors.Is(err, ErrMessageInvalid) || MessageReason(err) != "invalid_format" || errors.Is(err, ErrMessageTooLong) {
			t.Errorf("unknown format: %v (%q)", err, MessageReason(err))
		}
		_, _, err = NormalizeNotice("<p>a\u202eb</p>", "art")
		if !errors.Is(err, ErrMessageInvalid) || MessageReason(err) != "control_characters" {
			t.Errorf("bidi override (art): %v (%q)", err, MessageReason(err))
		}
		_, _, err = NormalizeNotice(`<p><a href="https://a.example/1">1</a> <a href="https://a.example/2">2</a> <a href="https://a.example/3">3</a> <a href="https://a.example/4">4</a></p>`, "art")
		if !errors.Is(err, ErrMessageInvalid) || MessageReason(err) != "too_many_links" {
			t.Errorf("four links: %v (%q)", err, MessageReason(err))
		}
	})
	t.Run("an empty text stays empty", func(t *testing.T) {
		for _, format := range []string{"", "art"} {
			got, _, err := NormalizeNotice(" \n ", format)
			if err != nil || got != "" {
				t.Errorf("format %q: %q, %v", format, got, err)
			}
		}
	})
	t.Run("normalizing art twice changes nothing", func(t *testing.T) {
		once, _, _ := NormalizeNotice("<p>A <b>b</b></p><ul><li>x</li><li>y</li></ul>", "art")
		twice, _, err := NormalizeNotice(once, "art")
		if err != nil || once != twice {
			t.Errorf("%q vs %q (%v)", once, twice, err)
		}
	})
}

func TestNoticeForFormatKeepsTheFormat(t *testing.T) {
	if n := NoticeFor(t.Context(), "x"); n.MessageFormat != "text" || n.Message != "x" {
		t.Errorf("NoticeFor = %+v", n)
	}
	if n := NoticeForFormat(t.Context(), "<p>x</p>", "art"); n.MessageFormat != "art" {
		t.Errorf("NoticeForFormat = %+v", n)
	}
}

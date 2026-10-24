package textformat

import (
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

// noLink checks that Verify accepts the words of html but leaves no link.
func noLink(t *testing.T, kind Kind, input, html string) {
	t.Helper()
	got, reason := Verify(input, html, kind.Profile())
	if reason != "" {
		t.Fatalf("rejected with %q: %s", reason, html)
	}
	if strings.Contains(got, "<a") {
		t.Fatalf("a link survived: %s", got)
	}
}

// links checks that Verify accepts html and keeps exactly n links.
func links(t *testing.T, kind Kind, input, html string, n int) string {
	t.Helper()
	got, reason := Verify(input, html, kind.Profile())
	if reason != "" {
		t.Fatalf("rejected with %q: %s", reason, html)
	}
	if c := strings.Count(got, "<a "); c != n {
		t.Fatalf("%d links kept, want %d: %s", c, n, got)
	}
	return got
}

func TestVerify_LinkBoundaries(t *testing.T) {
	d := KindDescription
	t.Run("prefix: a link on the tail of a longer mailbox", func(t *testing.T) {
		noLink(t, d, "Write to sales-info@x.com today",
			`<p>Write to sales-<a href="mailto:info@x.com">info@x.com</a> today</p>`)
	})
	t.Run("the same address written alone elsewhere does not allow the piece", func(t *testing.T) {
		noLink(t, d, "Write to info@x.com or sales-info@x.com",
			`<p>Write to info@x.com or sales-<a href="mailto:info@x.com">info@x.com</a></p>`)
	})
	t.Run("the whole longer mailbox is a link", func(t *testing.T) {
		got := links(t, d, "Write to sales-info@x.com today",
			`<p>Write to <a href="mailto:sales-info@x.com">sales-info@x.com</a> today</p>`, 1)
		if !strings.Contains(got, `<a href="mailto:sales-info@x.com">sales-info@x.com</a>`) {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("suffix: example.co and an m", func(t *testing.T) {
		noLink(t, d, "Tickets: https://tickets.example.com",
			`<p>Tickets: <a href="https://tickets.example.co">https://tickets.example.co</a>m</p>`)
	})
	t.Run("suffix: a longer domain written elsewhere does not allow the piece", func(t *testing.T) {
		noLink(t, d, "See https://x.example.co or https://x.example.com",
			`<p>See https://x.example.co or <a href="https://x.example.co">https://x.example.co</a>m</p>`)
	})
	t.Run("prefix of a web address", func(t *testing.T) {
		noLink(t, d, "See https://x.example.com or xhttps://x.example.com",
			`<p>See https://x.example.com or x<a href="https://x.example.com">https://x.example.com</a></p>`)
	})
	t.Run("a dot and more letters continue the address", func(t *testing.T) {
		noLink(t, d, "Mail info@x.com or info@x.com.au",
			`<p>Mail info@x.com or <a href="mailto:info@x.com">info@x.com</a>.au</p>`)
	})
	t.Run("a path continues the address", func(t *testing.T) {
		noLink(t, d, "See https://x.example.com or https://x.example.com/tickets",
			`<p>See https://x.example.com or <a href="https://x.example.com">https://x.example.com</a>/tickets</p>`)
	})
	t.Run("a closing parenthesis is not a part of the address", func(t *testing.T) {
		input := "Tickets (https://x.example.com/a) and (info@x.com)."
		noLink(t, d, input,
			`<p>Tickets (<a href="https://x.example.com/a)">https://x.example.com/a)</a> and (info@x.com).</p>`)
		links(t, d, input,
			`<p>Tickets (<a href="https://x.example.com/a">https://x.example.com/a</a>) and (<a href="mailto:info@x.com">info@x.com</a>).</p>`, 2)
	})
	t.Run("a sentence sign after the address is not a continuation", func(t *testing.T) {
		links(t, d, "Go to https://x.example.com/a. Or mail info@x.com? Yes!",
			`<p>Go to <a href="https://x.example.com/a">https://x.example.com/a</a>. Or mail <a href="mailto:info@x.com">info@x.com</a>? Yes!</p>`, 2)
	})
	t.Run("a session message link may not be a piece either", func(t *testing.T) {
		noLink(t, KindSessionMessage, "Moved: see https://x.example.com or https://x.example.com.au",
			`<p>Moved: see https://x.example.com or <a href="https://x.example.com">https://x.example.com</a>.au</p>`)
	})
}

func TestVerify_LinkCaseIsFreeOnlyForSchemeAndHost(t *testing.T) {
	d := richtext.ProfileDescription
	t.Run("another case of the path is another address", func(t *testing.T) {
		input := "Join: https://bit.ly/AbC"
		noLink(t, KindDescription, input, `<p>Join: <a href="https://bit.ly/abc">https://bit.ly/AbC</a></p>`)
		// Changing the visible text too is a change of the words.
		if got, reason := Verify(input, `<p>Join: <a href="https://bit.ly/abc">https://bit.ly/abc</a></p>`, d); reason != ReasonTextChanged {
			t.Fatalf("got %q, %q", got, reason)
		}
	})
	t.Run("the query keeps its case too", func(t *testing.T) {
		noLink(t, KindDescription, "Join: https://t.me/+AbC?x=Y",
			`<p>Join: <a href="https://t.me/+abc?x=y">https://t.me/+AbC?x=Y</a></p>`)
	})
	t.Run("the mailbox name keeps its case", func(t *testing.T) {
		noLink(t, KindDescription, "Mail Info@x.com",
			`<p>Mail <a href="mailto:info@x.com">Info@x.com</a></p>`)
	})
	t.Run("scheme and host may be written in any case", func(t *testing.T) {
		links(t, KindDescription, "Join: HTTPS://Example.COM/AbC and Info@Example.COM",
			`<p>Join: <a href="https://example.com/AbC">HTTPS://Example.COM/AbC</a> and <a href="mailto:Info@example.com">Info@Example.COM</a></p>`, 2)
	})
	t.Run("the path of the same case is kept", func(t *testing.T) {
		got := links(t, KindDescription, "Join: https://bit.ly/AbC", `<p>Join: <a href="https://bit.ly/AbC">https://bit.ly/AbC</a></p>`, 1)
		if !strings.Contains(got, `<a href="https://bit.ly/AbC">https://bit.ly/AbC</a>`) {
			t.Fatalf("got %s", got)
		}
	})
}

func TestVerify_LinksInBoldAndInLists(t *testing.T) {
	// The scanner reads the text the way FromLegacy does: the ** markers are
	// not part of the address.
	links(t, KindEmailNote, "Tickets: **https://x.example.com/a** now",
		`<p>Tickets: <strong><a href="https://x.example.com/a">https://x.example.com/a</a></strong> now</p>`, 1)
	links(t, KindDescription, "Tickets\n- https://x.example.com/a\n- info@x.com",
		`<p>Tickets</p><ul><li><a href="https://x.example.com/a">https://x.example.com/a</a></li><li><a href="mailto:info@x.com">info@x.com</a></li></ul>`, 2)
}

func TestVerify_EmailNoteKeepsTheLinksFromLegacyDoes(t *testing.T) {
	input := "Tickets: https://x.example.com/a and https://y.example.com/b"
	p := richtext.ProfileEmailNote
	// The model forgot to wrap the addresses: FromLegacy would have linked
	// them, so the answer is not used.
	if got, reason := Verify(input, "<p>Tickets: https://x.example.com/a and https://y.example.com/b</p>", p); reason != ReasonLinksLost || got != "" {
		t.Fatalf("got %q, %q", got, reason)
	}
	// One of two wrapped: still lost.
	if _, reason := Verify(input, `<p>Tickets: <a href="https://x.example.com/a">https://x.example.com/a</a> and https://y.example.com/b</p>`, p); reason != ReasonLinksLost {
		t.Fatalf("reason %q", reason)
	}
	// An invented link is unwrapped, and then one is missing.
	if _, reason := Verify(input, `<p>Tickets: <a href="https://x.example.com/a">https://x.example.com/a</a> and <a href="https://z.example.net/">https://y.example.com/b</a></p>`, p); reason != ReasonLinksLost {
		t.Fatalf("reason %q", reason)
	}
	// All wrapped: accepted.
	links(t, KindEmailNote, input, `<p>Tickets: <a href="https://x.example.com/a">https://x.example.com/a</a> and <a href="https://y.example.com/b">https://y.example.com/b</a></p>`, 2)
	// A profile that does not auto-link does not need them.
	if _, reason := Verify(input, "<p>Tickets: https://x.example.com/a and https://y.example.com/b</p>", richtext.ProfileDescription); reason != "" {
		t.Fatalf("description: reason %q", reason)
	}
}

func TestVerify_InvisibleCharactersInTheAnswerChangeTheText(t *testing.T) {
	for name, ch := range map[string]string{
		"LRM": "\u200e", "RLM": "\u200f", "ZWJ": "\u200d", "ZWNJ": "\u200c",
		"soft hyphen": "\u00ad", "combining grapheme joiner": "\u034f",
	} {
		t.Run(name, func(t *testing.T) {
			got, reason := Verify("Hello world", "<p>Hel"+ch+"lo world</p>", richtext.ProfileDescription)
			if reason != ReasonTextChanged || got != "" {
				t.Fatalf("got %q, %q", got, reason)
			}
		})
	}
}

func TestVerify_ResultIsNFC(t *testing.T) {
	nfc := "caf\u00e9 \u00e9t\u00e9"
	nfd := "cafe\u0301 e\u0301te\u0301"
	got, reason := Verify(nfc, "<p><strong>"+nfd+"</strong></p>", richtext.ProfileDescription)
	if reason != "" {
		t.Fatalf("an answer in NFD for an input in NFC was refused: %q", reason)
	}
	if got != "<p><strong>"+nfc+"</strong></p>" {
		t.Fatalf("stored %q, want NFC", got)
	}
}

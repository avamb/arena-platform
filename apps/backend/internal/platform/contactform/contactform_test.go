package contactform

import (
	"strings"
	"testing"
)

func TestRenderEmail_EscapesTheBuyersWords(t *testing.T) {
	subject, htmlBody, textBody := RenderEmail("ru-RU", "Teatro <b>", "teatro", "Anna <script>", "anna@example.com", "Hi <img src=x onerror=alert(1)> & bye")
	if !strings.HasPrefix(subject, "Сообщение от покупателя: Anna <script>") {
		t.Errorf("subject = %q (subjects are plain text, not HTML)", subject)
	}
	for _, bad := range []string{"<script>", "<img", "Teatro <b>"} {
		if strings.Contains(htmlBody, bad) {
			t.Errorf("HTML body carries raw %q", bad)
		}
	}
	if !strings.Contains(htmlBody, "&lt;img src=x onerror=alert(1)&gt; &amp; bye") {
		t.Errorf("message not escaped: %s", htmlBody)
	}
	if !strings.Contains(textBody, "anna@example.com") || !strings.Contains(textBody, "Hi <img") {
		t.Errorf("text body keeps the words as typed: %s", textBody)
	}
}

func TestRenderEmail_FallsBackToEnglishAndRTLForHebrew(t *testing.T) {
	if s, _, _ := RenderEmail("xx", "Org", "org", "Ann", "a@b.example", "0123456789"); !strings.HasPrefix(s, "Message from a buyer") {
		t.Errorf("unknown locale subject = %q", s)
	}
	if _, h, _ := RenderEmail("he", "Org", "org", "Ann", "a@b.example", "0123456789"); !strings.Contains(h, `dir="rtl"`) {
		t.Error("Hebrew letter must be right-to-left")
	}
}

// Every language of the letter comes from the catalog: no text falls back to
// a bare message id.
func TestRenderEmail_EveryLanguageHasEveryText(t *testing.T) {
	for _, l := range []string{"en", "ru", "es", "cs", "he"} {
		s, h, txt := RenderEmail(l, "Org", "org", "Ann", "a@b.example", "0123456789")
		for _, part := range []string{s, h, txt} {
			if strings.Contains(part, "contact.email.") {
				t.Errorf("%s: a text is missing from the catalog: %s", l, part)
			}
		}
	}
}

func TestFormatTelegram_EscapesAndClipsWithinTheTelegramLimit(t *testing.T) {
	for _, body := range []string{
		strings.Repeat("<", 2000), // escaping makes it five times longer
		strings.Repeat("😀", 2000), // two UTF-16 units each
		strings.Repeat("&😀", 1000),
	} {
		msg := FormatTelegram("Org & Co", "org", "<Ann>", "a@b.example", body, EmailMailed)
		if strings.Contains(msg, "<Ann>") || strings.Contains(msg, "Org & Co") {
			t.Errorf("unescaped values in %q", msg[:120])
		}
		if n := utf16Len(msg); n > telegramMaxUTF16 {
			t.Errorf("a Telegram message must stay within %d UTF-16 units, got %d", telegramMaxUTF16, n)
		}
		if !strings.Contains(msg, "…") {
			t.Error("a clipped message ends with an ellipsis")
		}
		if !strings.HasSuffix(msg, "Reply to the buyer from there.") {
			t.Error("the closing line is never cut")
		}
	}
	short := FormatTelegram("Org", "org", "Ann", "a@b.example", "Hello there!", EmailMailed)
	if strings.Contains(short, "…") || !strings.Contains(short, "Hello there!") {
		t.Errorf("a short message is kept whole: %q", short)
	}
}

func TestFormatTelegram_SaysWhenTheEmailDidNotGoOut(t *testing.T) {
	msg := FormatTelegram("Org", "org", "Ann", "a@b.example", "Hello there!", EmailNoAddress)
	if strings.Contains(msg, "went to your e-mail") || !strings.Contains(msg, "was not e-mailed") {
		t.Errorf("the post must not claim an e-mail that was not sent: %q", msg)
	}
	msg = FormatTelegram("Org", "org", "Ann", "a@b.example", "Hello there!", EmailRetrying)
	if strings.Contains(msg, "went to your e-mail") || strings.Contains(msg, "no e-mail address") || !strings.Contains(msg, "keeps trying") {
		t.Errorf("a failing e-mail is said to be retried, not missing: %q", msg)
	}
}

func TestContactEmail(t *testing.T) {
	if got := contactEmail([]byte(`{"email":" t@x.example ","phone":"1"}`)); got != "t@x.example" {
		t.Errorf("got %q", got)
	}
	for _, raw := range []string{"", "null", `{"email":5}`, "[]"} {
		if got := contactEmail([]byte(raw)); got != "" {
			t.Errorf("%s -> %q", raw, got)
		}
	}
}

// A value written around the API (a direct SQL edit, an old row) is checked
// again before anything is mailed to it. Without a public e-mail the message
// goes to the organization's own address, which the page never shows.
func TestRecipient_RevalidatesTheAddress(t *testing.T) {
	if got := recipient([]byte(`{"email":" Tickets@Teatro.Example "}`), "owner@org.example"); got != "tickets@teatro.example" {
		t.Errorf("got %q", got)
	}
	for _, raw := range []string{`{"email":"not an address"}`, `{"email":"a@b"}`, `{"email":"x@y.example
Bcc: z@evil.example"}`} {
		if got := recipient([]byte(raw), ""); got != "" {
			t.Errorf("%s -> %q, want nothing to mail", raw, got)
		}
		if got := recipient([]byte(raw), " Owner@Org.Example "); got != "owner@org.example" {
			t.Errorf("%s with an organization address -> %q", raw, got)
		}
	}
	if got := recipient(nil, "not an address"); got != "" {
		t.Errorf("a broken organization address -> %q", got)
	}
	// Owner decision 2026-10-13: with no public e-mail and no organization
	// address the owner's account e-mail receives the message.
	if got := recipient(nil, "", " Boss@Org.Example "); got != "boss@org.example" {
		t.Errorf("owner's account e-mail -> %q", got)
	}
	if got := recipient(nil, "box@org.example", "boss@org.example"); got != "box@org.example" {
		t.Errorf("the organization's address comes before the owner's -> %q", got)
	}
	if got := recipient(nil, "not an address", "boss@org.example"); got != "boss@org.example" {
		t.Errorf("a broken organization address falls through to the owner -> %q", got)
	}
}

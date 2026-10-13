package contactform

import (
	"strings"
	"testing"
	"unicode/utf8"
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

func TestFormatTelegram_EscapesAndClips(t *testing.T) {
	msg := FormatTelegram("Org & Co", "org", "<Ann>", "a@b.example", strings.Repeat("<", 2000))
	if strings.Contains(msg, "<Ann>") || strings.Contains(msg, "Org & Co") {
		t.Errorf("unescaped values in %q", msg[:120])
	}
	if utf8.RuneCountInString(msg) > 4096 {
		t.Errorf("a Telegram message must stay under 4096 characters, got %d", utf8.RuneCountInString(msg))
	}
	if !strings.Contains(msg, "…") {
		t.Error("a clipped message ends with an ellipsis")
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

package emailstatus

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestParseBrevo_UserAgentOnlyForOpensAndClicks: Brevo's user_agent is kept
// for opened, proxy_open and click (under every spelling Normalize knows),
// and for no other event type (U-7).
func TestParseBrevo_UserAgentOnlyForOpensAndClicks(t *testing.T) {
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	const ua = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15"
	cases := []struct {
		event string
		want  string
	}{
		{"opened", ua}, {"unique_opened", ua}, {"first_opening", ua},
		{"proxy_open", ua}, {"unique_proxy_open", ua},
		{"click", ua},
		{"delivered", ""}, {"soft_bounce", ""}, {"deferred", ""}, {"request", ""},
	}
	for _, c := range cases {
		body := `{"event":"` + c.event + `","message-id":"<a@b>","ts_epoch":1760608800000,"user_agent":"` + ua + `","sending_ip":"203.0.113.9","link":"https://x.example/p"}`
		evs, err := ParseBrevo([]byte(body), now)
		if err != nil || len(evs) != 1 {
			t.Fatalf("%s: %v %v", c.event, evs, err)
		}
		if evs[0].UserAgent != c.want {
			t.Errorf("%s: UserAgent = %q, want %q", c.event, evs[0].UserAgent, c.want)
		}
		// The stored raw record keeps only the event name and the time.
		for k := range evs[0].Payload {
			if k != "event" && k != "ts_epoch" {
				t.Errorf("%s: payload carries %q", c.event, k)
			}
		}
	}
}

func TestParseBrevo_NoUserAgentIsEmpty(t *testing.T) {
	evs, err := ParseBrevo([]byte(`{"event":"opened","message-id":"<a@b>","ts_epoch":1760608800000}`), time.Now())
	if err != nil || evs[0].UserAgent != "" {
		t.Fatalf("%+v %v", evs, err)
	}
	// A non-string user_agent is ignored, never a failure.
	evs, err = ParseBrevo([]byte(`{"event":"opened","message-id":"<a@b>","user_agent":{"x":1}}`), time.Now())
	if err != nil || evs[0].UserAgent != "" {
		t.Fatalf("%+v %v", evs, err)
	}
}

func TestCleanUserAgent(t *testing.T) {
	if got := CleanUserAgent("  Mail/1.0 \t (X)\r\n  "); got != "Mail/1.0 (X)" {
		t.Errorf("whitespace: %q", got)
	}
	// NUL and other control characters would break the INSERT or the log.
	if got := CleanUserAgent("Mo\x00zi\x07lla"); got != "Mozilla" {
		t.Errorf("control: %q", got)
	}
	if got := CleanUserAgent("ok\xff\xfeagent"); got != "okagent" || !utf8.ValidString(got) {
		t.Errorf("invalid utf-8: %q", got)
	}
	long := strings.Repeat("я", 600)
	got := CleanUserAgent(long)
	if utf8.RuneCountInString(got) != MaxUserAgentRunes {
		t.Errorf("cap: %d runes, want %d", utf8.RuneCountInString(got), MaxUserAgentRunes)
	}
	if CleanUserAgent("\x00\x01 ") != "" {
		t.Errorf("only controls should be empty")
	}
}

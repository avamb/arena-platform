package pagestats

import (
	"strings"
	"testing"
	"time"
)

func TestParseKind(t *testing.T) {
	for _, k := range []string{"page_view", "video_play", "partner_click"} {
		if got, ok := ParseKind(k); !ok || got != k {
			t.Errorf("ParseKind(%q) = %q, %v", k, got, ok)
		}
	}
	for _, k := range []string{"", "PAGE_VIEW", "page_view ", "click", "page_view;drop", strings.Repeat("a", 5000)} {
		if _, ok := ParseKind(k); ok {
			t.Errorf("ParseKind(%q) accepted", k)
		}
	}
}

func TestCleanUTM(t *testing.T) {
	cases := []struct{ in, want string }{
		{"instagram", "instagram"},
		{"  Spring_Sale-2026.v2~x ", "Spring_Sale-2026.v2~x"},
		{"", ""},
		{"a b", ""},
		{"spring/sale", ""},
		{"привет", ""},
		{"x'; drop table", ""},
		{"<script>", ""},
		{strings.Repeat("a", 64), strings.Repeat("a", 64)},
		{strings.Repeat("a", 100), strings.Repeat("a", 64)},
		{strings.Repeat("a", 500), ""},
		{"a\x00b", ""},
	}
	for _, c := range cases {
		if got := CleanUTM(c.in); got != c.want {
			t.Errorf("CleanUTM(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsSuspectAgent(t *testing.T) {
	suspect := []string{
		"", "   ",
		"Googlebot/2.1 (+http://www.google.com/bot.html)",
		"Mozilla/5.0 (compatible; bingbot/2.0)",
		"curl/8.4.0", "Wget/1.21", "python-requests/2.31", "Go-http-client/2.0",
		"Mozilla/5.0 HeadlessChrome/120.0", "Mozilla/5.0 (X11) AppleWebKit Lighthouse",
		"Slackbot-LinkExpanding 1.0", "facebookexternalhit/1.1", "Java/17.0.2",
		"PostmanRuntime/7.36", "Mozilla/5.0 (compatible; UptimeRobot/2.0)",
	}
	for _, ua := range suspect {
		if !IsSuspectAgent(ua) {
			t.Errorf("IsSuspectAgent(%q) = false, want true", ua)
		}
	}
	people := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Mobile/15E148 Safari/604.1",
		"Mozilla/5.0 (Android 14; Mobile; rv:121.0) Gecko/121.0 Firefox/121.0",
	}
	for _, ua := range people {
		if IsSuspectAgent(ua) {
			t.Errorf("IsSuspectAgent(%q) = true, want false", ua)
		}
	}
}

func TestDay_FollowsTheVenueZone(t *testing.T) {
	// 23:30 on 5 May in UTC is already 01:30 on 6 May in Madrid (UTC+2).
	now := time.Date(2026, 5, 5, 23, 30, 0, 0, time.UTC)
	if got := Day(now, "Europe/Madrid"); !got.Equal(time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("Madrid day = %v", got)
	}
	if got := Day(now, ""); !got.Equal(time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("empty zone must fall back to UTC, got %v", got)
	}
	if got := Day(now, "Not/AZone"); !got.Equal(time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("unknown zone must fall back to UTC, got %v", got)
	}
	// America/Los_Angeles is behind UTC: the same instant is still 5 May there.
	late := time.Date(2026, 5, 6, 3, 0, 0, 0, time.UTC)
	if got := Day(late, "America/Los_Angeles"); !got.Equal(time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("LA day = %v", got)
	}
}

func TestPageKey_IsBoundedAndCaseInsensitive(t *testing.T) {
	if PageKey("Org", "Show") != PageKey("ORG", "show") {
		t.Error("PageKey must ignore case")
	}
	long := PageKey(strings.Repeat("a", 1000), strings.Repeat("b", 1000))
	if len(long) > 80+1+120 {
		t.Errorf("PageKey length %d is not bounded", len(long))
	}
}

// Package pagestats holds the pure rules of the hosted page's statistics
// beacon (HP-2a, POST /v1/public/pages/{slug}/{event_slug}/track): which
// kinds are counted, how the optional utm_source / utm_campaign are cleaned,
// which User-Agents are counted apart as suspect, and which calendar day a hit
// belongs to.
//
// The beacon is public and unauthenticated, so the rules are deliberately
// narrow. A row carries only the event, the day, the kind and the two cleaned
// utm values; the visitor's IP address, User-Agent, country and time of day
// are read to decide, never stored, never logged. The numbers are an
// organizer's guide, not proof for an ad account.
package pagestats

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventmaterials"
)

const (
	// MaxUTMLength bounds a stored utm value. The table CHECK allows the same
	// 64 characters.
	MaxUTMLength = 64
	// MaxUTMPairsPerDay is how many different (utm_source, utm_campaign) pairs
	// one event may keep per day. A hit that would add another pair is counted
	// with empty utm values instead, so a caller inventing campaigns cannot
	// grow the table.
	MaxUTMPairsPerDay = 200
	// MaxBodyBytes is the largest request body the route reads.
	MaxBodyBytes = 1024
	// RetentionMonths is how long a row is meant to be kept (see AGENTS.md:
	// no purge job yet).
	RetentionMonths = 24
)

// ParseKind returns kind when it is one of the counted kinds.
func ParseKind(kind string) (string, bool) {
	for _, k := range eventmaterials.StatKinds {
		if kind == k {
			return k, true
		}
	}
	return "", false
}

// CleanUTM trims a utm value, keeps it only when every character is in the
// table's allowlist [A-Za-z0-9._~-] and cuts it to MaxUTMLength. A value with
// any other character is dropped whole (empty), not repaired: a half-cleaned
// campaign name would count under a name nobody sent.
func CleanUTM(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > 4*MaxUTMLength {
		// A value several times over the limit is not a campaign name.
		return ""
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == '~', c == '-':
		default:
			return ""
		}
	}
	if len(v) > MaxUTMLength {
		v = v[:MaxUTMLength]
	}
	return v
}

// suspectAgentMarkers are lower-case fragments of the User-Agent of clients
// that are obviously not a person with a browser. The list is a convenience,
// not a defence: a bot may send any string. Its only job is to keep the
// obvious crawlers out of the main count.
var suspectAgentMarkers = []string{
	"bot", "crawler", "spider", "slurp", "headless", "phantomjs", "puppeteer",
	"playwright", "selenium", "lighthouse", "curl/", "wget", "libwww",
	"python-requests", "python-urllib", "aiohttp", "httpx", "go-http-client",
	"java/", "okhttp", "apache-httpclient", "node-fetch", "axios/", "postman",
	"scrapy", "facebookexternalhit", "preview", "monitor", "uptime", "pingdom",
}

// IsSuspectAgent reports whether a hit with this User-Agent is counted apart.
// An empty agent is suspect: every browser sends one.
func IsSuspectAgent(userAgent string) bool {
	ua := strings.ToLower(strings.TrimSpace(userAgent))
	if ua == "" {
		return true
	}
	for _, m := range suspectAgentMarkers {
		if strings.Contains(ua, m) {
			return true
		}
	}
	return false
}

// Day returns the calendar date of now in the zone named tz (an IANA name),
// as a UTC-midnight time.Time so it binds to a SQL date unchanged. An empty or
// unknown zone falls back to UTC.
//
// The day follows the event's venue, not UTC: an organizer reads "yesterday"
// by the clock of the city the show is in, and a hit at 23:30 in Madrid
// belongs to that Madrid day.
func Day(now time.Time, tz string) time.Time {
	loc := time.UTC
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// PageKey is the key of the per-page rate-limit bucket: the two slugs,
// lower-cased and cut, so a caller cannot grow the bucket map with long keys.
func PageKey(slug, eventSlug string) string {
	return clip(strings.ToLower(slug), 80) + "/" + clip(strings.ToLower(eventSlug), 120)
}

func clip(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}

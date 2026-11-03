package eventpage

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/html"
)

// startRe reads an ISO 8601 date-time as pages write it: a date, an
// optional wall-clock time (seconds and fractions ignored) and an optional
// offset ("Z", "+01:00", "+0100").
var startRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})(?:[T ](\d{2}):(\d{2})(?::\d{2}(?:[.,]\d+)?)?)?\s*(Z|[+-]\d{2}:?\d{2})?$`)

// clockRe reads a bare time of day ("18:30", "18:30:00").
var clockRe = regexp.MustCompile(`^(\d{2}):(\d{2})(?::\d{2})?`)

// parsedStart is a date as the page wrote it.
type parsedStart struct {
	date, clock, offset string
	instant             time.Time // zero without an offset
}

// parseStart reads a startDate. The wall clock is kept exactly as written:
// spec 59 §5, the page's time is the venue's time in practice.
func parseStart(s string) (parsedStart, bool) {
	m := startRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return parsedStart{}, false
	}
	y, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	d, _ := strconv.Atoi(m[3])
	day := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
	if day.Year() != y || int(day.Month()) != mo || day.Day() != d || y < 2000 || y > 2100 {
		return parsedStart{}, false
	}
	p := parsedStart{date: m[1] + "-" + m[2] + "-" + m[3]}
	hh, mm := 0, 0
	if m[4] != "" {
		hh, _ = strconv.Atoi(m[4])
		mm, _ = strconv.Atoi(m[5])
		if hh > 23 || mm > 59 {
			return parsedStart{}, false
		}
		p.clock = m[4] + ":" + m[5]
	}
	if m[6] != "" {
		off := m[6]
		secs := 0
		if off == "Z" {
			off = "+00:00"
		} else {
			if len(off) == 5 { // +0100
				off = off[:3] + ":" + off[3:]
			}
			oh, _ := strconv.Atoi(off[1:3])
			om, _ := strconv.Atoi(off[4:6])
			if oh > 14 || om > 59 {
				return parsedStart{}, false
			}
			secs = oh*3600 + om*60
			if off[0] == '-' {
				secs = -secs
			}
		}
		p.offset = off
		p.instant = time.Date(y, time.Month(mo), d, hh, mm, 0, 0, time.FixedZone("", secs))
	}
	return p, true
}

// past reports whether a date is over at now. With an offset the instant
// decides; without one the day does, with a day of grace, because the page
// does not say in which zone its day ends.
func (p parsedStart) past(now time.Time) bool {
	if !p.instant.IsZero() {
		return p.instant.Before(now)
	}
	return p.date < now.UTC().Add(-24*time.Hour).Format("2006-01-02") // allow:timeformat
}

// doorsClock reads doorTime: a full date-time of the same day, or a bare
// time. Anything else is no doors time.
func doorsClock(s, date string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if p, ok := parseStart(s); ok {
		if p.date == date {
			return p.clock
		}
		return ""
	}
	if m := clockRe.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		if h <= 23 && mi <= 59 {
			return m[1] + ":" + m[2]
		}
	}
	return ""
}

// maxPriceMinor is the largest price the wizards accept.
const maxPriceMinor = 99_999_999

// priceToMinor reads a price as pages write it ("25", "34.90", "34,90",
// "1 200,00", "1.200,50", "€ 25") into minor units with two decimals, the
// unit every wizard price uses. No float is involved. ok is false for
// anything that is not one non-negative amount.
func priceToMinor(s string) (int64, bool) {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= '0' && r <= '9', r == '.', r == ',':
			b.WriteRune(r)
		case unicode.IsSpace(r), r == '\'', r == ' ', r == ' ':
			// thousands separators
		case r == '-':
			return 0, false
		case unicode.IsLetter(r) || unicode.IsSymbol(r):
			// a currency symbol or code next to the amount
		default:
			return 0, false
		}
	}
	num := b.String()
	if num == "" {
		return 0, false
	}
	lastDot, lastComma := strings.LastIndexByte(num, '.'), strings.LastIndexByte(num, ',')
	intPart, frac := num, ""
	switch {
	case lastDot >= 0 && lastComma >= 0:
		// The later separator is the decimal one, the other groups thousands.
		dec := lastDot
		group := ","
		if lastComma > lastDot {
			dec, group = lastComma, "."
		}
		intPart, frac = strings.ReplaceAll(num[:dec], group, ""), num[dec+1:]
	case lastComma >= 0:
		if strings.Count(num, ",") == 1 && len(num)-lastComma-1 <= 2 {
			intPart, frac = num[:lastComma], num[lastComma+1:]
		} else {
			intPart = strings.ReplaceAll(num, ",", "")
		}
	case lastDot >= 0:
		if strings.Count(num, ".") == 1 {
			intPart, frac = num[:lastDot], num[lastDot+1:]
		} else {
			intPart = strings.ReplaceAll(num, ".", "")
		}
	}
	if strings.ContainsAny(intPart+frac, ".,") {
		return 0, false
	}
	frac = strings.TrimRight(frac, "0")
	if len(frac) > 2 {
		return 0, false
	}
	for len(frac) < 2 {
		frac += "0"
	}
	if intPart == "" {
		intPart = "0"
	}
	if len(intPart) > 8 {
		return 0, false
	}
	i, err1 := strconv.ParseInt(intPart, 10, 64)
	f, err2 := strconv.ParseInt(frac, 10, 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	minor := i*100 + f
	if minor < 0 || minor > maxPriceMinor {
		return 0, false
	}
	return minor, true
}

// currencyCode keeps an ISO 4217 code, "" for anything else.
func currencyCode(s string) string {
	c := strings.ToUpper(strings.TrimSpace(s))
	if len(c) != 3 {
		return ""
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return ""
		}
	}
	return c
}

// countryCode keeps an ISO 3166-1 alpha-2 code ("ES", "es"), "" for a
// country written by name: the wizard then asks.
func countryCode(s string) string {
	c := strings.ToUpper(strings.TrimSpace(s))
	if len(c) != 2 || c[0] < 'A' || c[0] > 'Z' || c[1] < 'A' || c[1] > 'Z' {
		return ""
	}
	return c
}

var ageNumRe = regexp.MustCompile(`\d{1,2}`)

// ageRating turns typicalAgeRange ("18-", "18+", "6-12") into one of the
// platform's ratings when its lower bound is exactly one of them.
func ageRating(s string) string {
	m := ageNumRe.FindString(s)
	switch m {
	case "0", "6", "12", "16", "18":
		return m + "+"
	}
	return ""
}

// plainText turns a description that may hold HTML into plain text with
// its paragraphs, cut to the wizards' limit.
func plainText(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.ContainsAny(s, "<&") {
		var b strings.Builder
		z := html.NewTokenizer(bytes.NewReader([]byte("<div>" + s + "</div>")))
	loop:
		for {
			switch z.Next() {
			case html.ErrorToken:
				break loop
			case html.TextToken:
				b.Write(z.Text())
			case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
				name, _ := z.TagName()
				if blockElements[string(name)] {
					b.WriteByte('\n')
				}
			}
		}
		s = b.String()
	}
	var paras []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		if l = strings.Join(strings.Fields(l), " "); l != "" {
			paras = append(paras, l)
		}
	}
	return clipRunes(strings.Join(paras, "\n"), max)
}

// oneLine cleans a short value (a name, a venue): no markup, one line.
func oneLine(s string, max int) string {
	return clipRunes(strings.Join(strings.Fields(strings.ReplaceAll(plainText(s, 10*max), "\n", " ")), " "), max)
}

// languageTag keeps the primary subtag of a language ("es-ES" -> "es").
func languageTag(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexAny(s, "-_"); i >= 0 {
		s = s[:i]
	}
	if len(s) < 2 || len(s) > 3 {
		return ""
	}
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	return s
}

package eventbot

import (
	"fmt"
	"strings"
	"time"
)

// FormatMoney renders a minor-unit amount as "1 234,50 EUR" (ru) or
// "1,234.50 EUR" (en). Integer arithmetic only: the amount is money.
func FormatMoney(minor int64, currency, locale string) string {
	neg := minor < 0
	if neg {
		minor = -minor
	}
	whole := minor / 100
	cents := minor % 100
	thousands, decimal := ",", "."
	if NormalizeLocale(locale) == "ru" {
		thousands, decimal = " ", ","
	}
	digits := fmt.Sprintf("%d", whole)
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteString(thousands)
		}
		b.WriteRune(r)
	}
	out := b.String()
	if cents != 0 {
		out += fmt.Sprintf("%s%02d", decimal, cents)
	}
	if neg {
		out = "-" + out
	}
	return out + " " + strings.ToUpper(strings.TrimSpace(currency))
}

// FormatWhen renders a session start in the venue's zone: "15.10.2026 20:00".
// An unknown or empty zone falls back to UTC and says so.
func FormatWhen(t time.Time, tz string) string {
	loc, err := time.LoadLocation(strings.TrimSpace(tz))
	if tz == "" || err != nil {
		// allow:timeformat: human-readable date for an organizer's chat message, not a wire timestamp
		return t.UTC().Format("02.01.2006 15:04") + " UTC"
	}
	// allow:timeformat: human-readable date for an organizer's chat message, not a wire timestamp
	return t.In(loc).Format("02.01.2006 15:04")
}

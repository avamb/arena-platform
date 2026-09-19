package opsalert

import (
	"fmt"
	"regexp"
)

// emailPattern matches a reasonably permissive email address shape. It is
// intentionally simple (no RFC 5322 edge cases) — this is a best-effort
// scrub for error strings that MIGHT embed a buyer's address (e.g. an SMTP
// bounce reason, a webhook validation error echoing the request), not a
// validator.
var emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// ScrubEmails replaces every email-shaped substring of s with "[redacted]".
// Alert messages must never contain a buyer's email, name, or phone number
// (only order numbers, amounts, counts, and ids) — this guards the one
// input that is genuinely free-form and provider-controlled: a job's
// last_error / failure reason string, which can legitimately embed the
// address it failed to deliver to.
func ScrubEmails(s string) string {
	return emailPattern.ReplaceAllString(s, "[redacted]")
}

// Truncate cuts s to at most n runes, appending an ellipsis marker when it
// had to cut. Used to bound last_error strings (which can be arbitrarily
// long) before they are embedded in a Telegram message.
func Truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(runes[:n-1]) + "…"
}

// FormatMinorUnits renders a minor-currency-unit integer amount (e.g. cents)
// as a human "major.minor CUR" string using pure integer arithmetic (no
// float rounding surprises). Negative amounts render with a leading "-".
func FormatMinorUnits(amountMinor int64, currency string) string {
	neg := amountMinor < 0
	if neg {
		amountMinor = -amountMinor
	}
	major := amountMinor / 100
	minor := amountMinor % 100
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%02d %s", sign, major, minor, currency)
}

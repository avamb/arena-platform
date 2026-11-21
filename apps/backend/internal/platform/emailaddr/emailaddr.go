// Package emailaddr checks the shape of an e-mail address a buyer typed,
// before arena writes anything for it or hands it to a payment provider.
//
// It is stricter than net/mail on the DOMAIN, because that is where typing
// slips land and where a provider refuses the address after arena has
// already taken a hold (finding F-BR-03, 2026-11-12: Stripe answered 400
// email_invalid to "x@abhteam.comRUN1109-FC FC-0" once the seats were held).
// No DNS lookup is made: a well-formed address at a domain nobody owns is
// still accepted.
package emailaddr

import (
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxLen is the longest address accepted (RFC 5321 path limit).
const MaxLen = 254

// maxLocalLen is the longest local part (RFC 5321).
const maxLocalLen = 64

// maxLabelLen is the longest domain label (RFC 1035).
const maxLabelLen = 63

// Valid reports whether s is one plain address, for example
// anna@example.com. The caller trims it first: surrounding spaces count as
// invalid. It refuses:
//   - spaces, control characters, a display name, quotes, brackets, a list;
//   - an empty local part or one longer than 64 bytes;
//   - a domain without a dot, with an empty label, a label longer than 63
//     bytes, a label that starts or ends with a hyphen, or a character other
//     than a letter, a digit or a hyphen;
//   - a top-level domain that is not letters only (at least two), except an
//     IDN one written as xn--...
func Valid(s string) bool {
	if s == "" || len(s) > MaxLen || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("<>()[]\\,;:\"", r) {
			return false
		}
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Name != "" || a.Address != s {
		return false
	}
	at := strings.LastIndex(s, "@")
	if at < 1 || at > maxLocalLen {
		return false
	}
	return validDomain(s[at+1:])
}

// validDomain checks the part after the "@".
func validDomain(d string) bool {
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if !validLabel(l) {
			return false
		}
	}
	return validTLD(labels[len(labels)-1])
}

// validLabel is one dot-separated part of the domain: letters (any script,
// for an IDN written in its own letters), digits and inner hyphens.
func validLabel(l string) bool {
	if l == "" || len(l) > maxLabelLen || strings.HasPrefix(l, "-") || strings.HasSuffix(l, "-") {
		return false
	}
	for _, r := range l {
		if r != '-' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// validTLD is the last label: letters only, two or more, or an IDN TLD in
// its ASCII form (xn--p1ai).
func validTLD(t string) bool {
	lower := strings.ToLower(t)
	if strings.HasPrefix(lower, "xn--") {
		rest := lower[len("xn--"):]
		if rest == "" {
			return false
		}
		for _, r := range rest {
			if r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
				return false
			}
		}
		return true
	}
	if utf8.RuneCountInString(t) < 2 {
		return false
	}
	for _, r := range t {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

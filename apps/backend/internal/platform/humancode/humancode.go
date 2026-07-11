// Package humancode generates and normalizes the human-readable ticket
// credential code introduced by SEAT-C4 (mobile-first ticket).
//
// A human code is the manual-entry fallback printed under the QR code on
// the PDF e-ticket and shown in the delivery email body: when the QR
// image cannot be scanned at the gate, staff type the code instead.
//
// # Format
//
// Eight characters drawn from the Crockford Base32 alphabet — the digits
// 0-9 and the uppercase letters A-Z minus the look-alikes I, L, O and U.
// The FIRST character is always a letter. That rule makes the code
// Excel-safe by construction: a string that starts with a letter can
// never be parsed as a number, neither in plain form (12345678) nor in
// scientific notation (1234E567), so agent/organizer spreadsheets cannot
// silently corrupt exported or imported code batches into 1.23E+12
// floats. The same corruption risk applies to external barcode imports —
// see the barcode-batch documentation.
//
// # Storage vs display form
//
// The CANONICAL form — what is stored in ticket_credentials.human_code
// and what every lookup compares against — is the bare 8-character
// string WITHOUT the hyphen (for example "M7KT2QV9"). The DISPLAY form
// inserts one hyphen after the fourth character ("M7KT-2QV9"); use
// Format to produce it. Normalize converts arbitrary user input
// (either form, any case, stray spaces, Crockford look-alike aliases)
// back to the canonical form for lookups.
package humancode

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// Alphabet is the Crockford Base32 alphabet: digits 0-9 plus uppercase
// letters excluding the look-alikes I, L, O and U. 32 symbols total.
const Alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Letters is the letters-only subset of Alphabet used for the first
// character of every generated code (22 symbols). Starting with a letter
// guarantees the code can never be mistaken for a number by CSV/Excel
// tooling.
const Letters = "ABCDEFGHJKMNPQRSTVWXYZ"

// Length is the canonical (hyphen-less) code length.
const Length = 8

// Generate returns a new human code in canonical form: 8 Crockford
// Base32 characters, first character always a letter, no hyphen.
// Randomness comes from crypto/rand; the letter pick uses unbiased
// rejection sampling so every alphabet symbol is equally likely.
func Generate() (string, error) {
	var out [Length]byte

	// First character: unbiased pick from the 22-letter subset.
	first, err := randomIndex(len(Letters))
	if err != nil {
		return "", fmt.Errorf("humancode: generate first char: %w", err)
	}
	out[0] = Letters[first]

	// Remaining characters: the full 32-symbol alphabet divides 256
	// evenly, so a simple masked byte is already unbiased.
	var buf [Length - 1]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("humancode: read random bytes: %w", err)
	}
	for i, b := range buf {
		out[i+1] = Alphabet[int(b)&31]
	}
	return string(out[:]), nil
}

// randomIndex returns an unbiased random integer in [0, n) using
// rejection sampling over single crypto/rand bytes. n must be in (0, 256].
func randomIndex(n int) (int, error) {
	// Largest multiple of n that fits in a byte range; bytes at or above
	// this bound are rejected to avoid modulo bias.
	bound := 256 - 256%n
	var b [1]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		if int(b[0]) < bound {
			return int(b[0]) % n, nil
		}
	}
}

// Format renders a canonical 8-character code in the grouped display
// form "XXXX-XXXX" used on PDFs and in email bodies. Input that is not
// exactly 8 characters is returned unchanged (defensive: never mangle a
// value we do not recognize).
func Format(canonical string) string {
	if len(canonical) != Length {
		return canonical
	}
	return canonical[:4] + "-" + canonical[4:]
}

// Normalize converts arbitrary user input to the canonical lookup form:
// uppercase, hyphens and whitespace stripped, and the Crockford
// look-alike aliases mapped (I→1, L→1, O→0). Returns ok=false when the
// result is not exactly 8 characters from the Crockford alphabet — the
// caller should then treat the input as "not a human code" (for example
// a raw QR token) rather than an invalid one.
//
// Note that Normalize deliberately does NOT require the first character
// to be a letter: generated codes always start with a letter, so a
// digit-leading candidate simply never matches a stored row.
func Normalize(input string) (string, bool) {
	var b strings.Builder
	b.Grow(len(input))
	for _, r := range input {
		switch {
		case r == '-' || r == ' ' || r == '\t':
			continue // grouping/typing noise
		case r >= 'a' && r <= 'z':
			r -= 'a' - 'A'
		}
		switch r {
		case 'I', 'L':
			r = '1'
		case 'O':
			r = '0'
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) != Length {
		return "", false
	}
	for i := 0; i < len(out); i++ {
		if !strings.ContainsRune(Alphabet, rune(out[i])) {
			return "", false
		}
	}
	return out, true
}

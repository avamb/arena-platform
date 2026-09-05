// Package ean13 implements the standard GS1 EAN-13 encoder and check-digit
// validator used to mint the platform's own barcode numbers (feature #502,
// W1-B6a; spec 08_architecture/18_bil24_compat_wave1_specification_ru.md
// §11).
//
// The platform prefixes every generated code with "21" — GS1 reserves the
// 20-29 prefix range for internal / in-store use — so platform-minted codes
// can never collide with a real Bil24 barcode (which the spec documents as
// starting with "24…"). The remaining 10 digits are the zero-padded
// tickets.system_ticket_id (migration 0088), followed by a single GS1
// check digit (weights 1/3, computed left to right over the first 12
// digits).
package ean13

import "strconv"

// Encode returns the 13-digit EAN-13 string for n under prefix: the prefix,
// followed by n zero-padded to fill the remaining digits up to position 12,
// followed by the GS1 check digit.
//
// The platform always calls Encode("21", ticket.SystemTicketID): a 2-digit
// prefix plus a 10-digit zero-padded ticket id makes exactly 12 digits, so
// the returned string is exactly 13 digits long. Encode does not itself
// enforce the "21" prefix or a specific digit width — it fills n into
// however many digits remain after prefix within a 12-digit body, so a
// system_ticket_id that has grown past 10 digits still produces a valid
// (if wider) EAN-13-shaped body; the ticket_credentials_ean13_shape CHECK
// (migration 0093) is the source of truth for the 13-digit invariant on
// the platform's actual code range.
func Encode(prefix string, n int64) string {
	pad := 12 - len(prefix)
	if pad < 0 {
		pad = 0
	}
	body := prefix + zeroPad(n, pad)
	return body + strconv.Itoa(checkDigit(body))
}

// zeroPad renders n as a decimal string left-padded with zeros to at least
// width digits (no truncation if n already has more digits than width).
func zeroPad(n int64, width int) string {
	s := strconv.FormatInt(n, 10)
	for len(s) < width {
		s = "0" + s
	}
	return s
}

// checkDigit computes the GS1 check digit for a 12-digit body: digits at
// 1-indexed odd positions carry weight 1, even positions carry weight 3;
// the check digit is the amount needed to round the weighted sum up to the
// next multiple of 10 (0 when the sum is already a multiple of 10).
func checkDigit(body string) int {
	sum := 0
	for i, c := range body {
		d := int(c - '0')
		if i%2 == 0 {
			sum += d
		} else {
			sum += d * 3
		}
	}
	return (10 - sum%10) % 10
}

// Valid reports whether s is a syntactically well-formed 13-digit EAN-13
// code with a correct GS1 check digit. It does not verify that s was
// actually minted by Encode (e.g. the "21" prefix), only that the checksum
// is internally consistent — the same contract as the DB-side
// ticket_credentials_ean13_shape CHECK plus a checksum verification that
// SQL cannot express.
func Valid(s string) bool {
	if len(s) != 13 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	want := checkDigit(s[:12])
	got := int(s[12] - '0')
	return want == got
}

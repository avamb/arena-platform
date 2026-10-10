package horders

import (
	"strconv"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

// EC-04 (spec 35 §5.5): ONE `q` parameter covers a barcode, an order number,
// an e-mail, a phone and a name. classifyQuery decides which of the SQL
// matchers of gen.SearchOrdersByOrg the value feeds, in this precedence:
//
//  1. exactly 13 digits (separators ignored)  -> barcode (EAN-13 of a ticket
//     of the order, any authority; a legacy deterministic PlatformCode is
//     decoded back to its system_ticket_id for a ticket with no stored
//     credential). The same digits are ALSO tried as a phone: a 13-digit
//     dial string with its 00 prefix is a phone, not a code, and an exact
//     match on the other matcher cannot produce a false hit.
//  2. a bare number of up to 12 digits, or `#`-prefixed -> orders.system_id
//     exact. A bare 7-12 digit number is ALSO tried as a phone (a national
//     number typed without its country code), with the same reasoning.
//  3. a value containing `@` -> lower-cased exact e-mail (buyer_email, or an
//     e-mail identity of the order's customer).
//  4. a value that normalizes to a phone (optional leading +, 7-15 digits
//     after dropping spaces, dots, dashes and parentheses) -> exact match on
//     the digits (leading 00/+ stripped on both sides), a 9+ digit suffix of
//     the buyer_phone digits, and the customer's phone identities.
//  5. anything else -> the pg_trgm similarity over buyer name/e-mail/phone.
//
// The matchers are OR-ed in SQL, so populating several is safe: an exact
// match on one of them is what the organizer asked for.

// searchTerms is the classified `q`; the zero value means "no search".
type searchTerms struct {
	Barcode        string
	LegacyTicketID *int64
	SystemID       *int64
	Email          string
	PhoneDigits    string
	Text           string
}

func (t searchTerms) empty() bool {
	return t.Barcode == "" && t.LegacyTicketID == nil && t.SystemID == nil &&
		t.Email == "" && t.PhoneDigits == "" && t.Text == ""
}

func (t searchTerms) apply(p *gen.OrderSearchParams) {
	p.Barcode = t.Barcode
	p.LegacyTicketID = t.LegacyTicketID
	p.SystemID = t.SystemID
	p.Email = t.Email
	p.PhoneDigits = t.PhoneDigits
	p.Text = t.Text
}

// maxSystemIDDigits bounds the "order number" reading of a bare number:
// orders.system_id draws from compatibility_system_id_seq (>= 1e9, ten
// digits today) and a 13-digit value is a barcode.
const maxSystemIDDigits = 12

// classifyQuery maps a trimmed `q` onto the search matchers. An empty q
// yields the zero searchTerms.
func classifyQuery(q string) searchTerms {
	q = strings.TrimSpace(q)
	if q == "" {
		return searchTerms{}
	}

	// `#1000000500` — an order number as the bot and the sites print it.
	if strings.HasPrefix(q, "#") {
		if id, ok := parseSystemID(strings.TrimSpace(q[1:])); ok {
			return searchTerms{SystemID: &id}
		}
		return searchTerms{Text: q}
	}

	if digits, ok := bareDigits(q); ok {
		if len(digits) == 13 {
			t := searchTerms{Barcode: digits, PhoneDigits: phoneDigits(digits)}
			if id, ok := legacyTicketSystemID(digits); ok {
				t.LegacyTicketID = &id
			}
			return t
		}
		if len(digits) <= maxSystemIDDigits {
			if id, ok := parseSystemID(digits); ok {
				t := searchTerms{SystemID: &id}
				if p := phoneDigits(digits); p != "" {
					t.PhoneDigits = p
				}
				return t
			}
		}
		// Longer than 13 digits: nothing exact fits, let similarity try.
		return searchTerms{Text: q}
	}

	if strings.Contains(q, "@") {
		return searchTerms{Email: strings.ToLower(q)}
	}

	if p := phoneDigits(q); p != "" {
		return searchTerms{PhoneDigits: p}
	}

	return searchTerms{Text: q}
}

// bareDigits reports q as a digit string when it is a number possibly
// broken up by spaces or dashes ("1000 000 500", "4600-0510-0000-1"), with
// no other characters. A leading + is NOT bare — that is a phone.
func bareDigits(q string) (string, bool) {
	var b strings.Builder
	for _, r := range q {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-':
		default:
			return "", false
		}
	}
	if b.Len() == 0 {
		return "", false
	}
	return b.String(), true
}

// parseSystemID parses an orders.system_id candidate: 1-12 digits, no
// sign, fits int64.
func parseSystemID(s string) (int64, bool) {
	if s == "" || len(s) > maxSystemIDDigits {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// phoneDigits normalizes a typed phone to its digits: spaces, dots, dashes
// and parentheses are dropped, one leading + or 00 (the international
// dialing prefix) is stripped, and the result must be 7-15 digits (E.164
// allows at most 15). Anything else answers "" — not a phone.
func phoneDigits(raw string) string {
	s := strings.TrimSpace(raw)
	plus := strings.HasPrefix(s, "+")
	if plus {
		s = s[1:]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '.' || r == '(' || r == ')':
		default:
			return ""
		}
	}
	d := b.String()
	if !plus {
		d = strings.TrimPrefix(d, "00")
	}
	if len(d) < 7 || len(d) > 15 {
		return ""
	}
	return d
}

// legacyTicketSystemID decodes a 13-digit code minted by the retired
// deterministic ean13.PlatformCode formula ("21" + zero-padded
// system_ticket_id + check digit) back to the ticket id, so a pre-#502
// ticket that has no stored credential — orderexport prints exactly this
// fallback for it — is still found by the code on its PDF.
func legacyTicketSystemID(code string) (int64, bool) {
	if len(code) != 13 || !strings.HasPrefix(code, ean13.PlatformPrefix) || !ean13.Valid(code) {
		return 0, false
	}
	id, err := strconv.ParseInt(code[len(ean13.PlatformPrefix):12], 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	if ean13.PlatformCode(id) != code {
		return 0, false
	}
	return id, true
}

// Order tabs (EC-04): every orders.status maps to exactly one group.
//
//	paid   = paid, partially_refunded, refunded (money was taken, whatever
//	         was returned later);
//	unpaid = pending_payment, expired, cancelled, abandoned, manual_review
//	         (no confirmed payment behind the order; manual_review is a
//	         payment that could not complete and waits for an operator).
//
// "recent" (the default) is every status, newest first.
const (
	tabRecent = "recent"
	tabPaid   = "paid"
	tabUnpaid = "unpaid"
)

// tabFilter maps the `tab` query parameter onto the SQL tab filter; ok is
// false for an unknown value.
func tabFilter(tab string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(tab)) {
	case "", tabRecent:
		return "", true
	case tabPaid:
		return tabPaid, true
	case tabUnpaid:
		return tabUnpaid, true
	}
	return "", false
}

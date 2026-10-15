package flitt

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Flitt's create-order request has ONE buyer field of its own, sender_email.
// The buyer's name and mobile phone are accepted only inside `reservation_data`
// (docs.flitt.com/api/reservation_data): a BASE64-encoded JSON object whose
// `customer_name` (AN 1024) and `phonemobile` (AN 16) pre-fill the payment
// page. The whole BASE64 string is one more request parameter, so it is signed
// exactly like sender_email (sorted by key, empty skipped).
//
// The page says every value there "must be alphanumeric and contain latin
// characters, digits and separator symbols". A value that does not fit is
// left out, never sent: a refused field would fail the whole checkout, and
// the buyer can type their own name on Flitt's page.
const (
	maxCustomerNameRunes = 1024
	maxPhoneMobileChars  = 16
	minPhoneDigits       = 7
)

// reservationData builds the `reservation_data` value for the buyer's name and
// phone, or "" when neither survives the checks below.
func reservationData(name, phone string) string {
	obj := map[string]string{}
	if n := latinName(name); n != "" {
		obj["customer_name"] = n
	}
	if p := phoneMobile(phone); p != "" {
		obj["phonemobile"] = p
	}
	if len(obj) == 0 {
		return ""
	}
	// json.Marshal sorts map keys, so the value (and its signature) is stable.
	b, err := json.Marshal(obj)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

// latinName folds Latin diacritics (García -> Garcia) and returns the name
// when what is left is plain Latin letters, digits and separators. A name in
// another script (Cyrillic, Hebrew) or with a letter that does not decompose
// (ø, ł, ß) returns "": Flitt documents Latin only.
func latinName(raw string) string {
	var b strings.Builder
	space := false
	for _, r := range norm.NFD.String(strings.TrimSpace(raw)) {
		switch {
		case unicode.Is(unicode.Mn, r):
			// a combining accent: dropped, the base letter stays
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			space = false
		case r == ' ' || r == '\t':
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = true
		case r == '-' || r == '.' || r == '\'' || r == ',' || r == '_':
			b.WriteRune(r)
			space = false
		default:
			return ""
		}
	}
	out := strings.TrimSpace(b.String())
	if rs := []rune(out); len(rs) > maxCustomerNameRunes {
		out = string(rs[:maxCustomerNameRunes])
	}
	return out
}

// phoneMobile reduces a typed phone to Flitt's `phonemobile` shape ("+12345678":
// an optional leading +, then digits, 16 characters at most). "00" becomes "+";
// spaces, dots, dashes and brackets are dropped; anything else, fewer than
// minPhoneDigits digits or a longer number returns "". A number without an
// international prefix is passed on as its digits: arena does not know the
// buyer's country here, and the field only pre-fills a form.
func phoneMobile(raw string) string {
	s := strings.TrimSpace(raw)
	plus := strings.HasPrefix(s, "+")
	if plus {
		s = s[1:]
	}
	var digits strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == ' ' || r == '.' || r == '-' || r == '(' || r == ')':
		default:
			return ""
		}
	}
	d := digits.String()
	if !plus && strings.HasPrefix(d, "00") {
		plus, d = true, d[2:]
	}
	if len(d) < minPhoneDigits {
		return ""
	}
	out := d
	if plus {
		out = "+" + d
	}
	if len(out) > maxPhoneMobileChars {
		return ""
	}
	return out
}

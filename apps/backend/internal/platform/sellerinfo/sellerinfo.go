// Package sellerinfo holds the rules for the seller's legal data and the
// public contact of a hosted-page channel (HP-4a): the tax-number formats,
// the shape of a public e-mail and phone, the "looks like a personal mailbox"
// hint, and the list of what must be filled before a channel may show the
// seller block on the sales page.
//
// ONE copy of each rule: PATCH /v1/organizations/{id} (hiam), organizer
// approval (provisioning.OrgTax), the seller-profile routes (hseller) and the
// Telegram bot's "Organization data" screens (eventbot) all call this
// package, so a format accepted on one surface is never refused on another.
package sellerinfo

import (
	"net/mail"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Tax-number schemes, the values of organizations.tax_id_scheme.
const (
	SchemeEUVAT = "eu_vat"
	SchemeGBVAT = "gb_vat"
	SchemeILVAT = "il_vat"
	SchemeUSEIN = "us_ein"
	SchemeOther = "other"
)

// Schemes lists every scheme in the order a chooser shows them.
var Schemes = []string{SchemeEUVAT, SchemeGBVAT, SchemeILVAT, SchemeUSEIN, SchemeOther}

var (
	euVAT = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{2,12}$`)
	gbVAT = regexp.MustCompile(`^GB(?:[0-9]{9}|[0-9]{12}|GD[0-9]{3}|HA[0-9]{3})$`)
	ilVAT = regexp.MustCompile(`^[0-9]{9}$`)
	usEIN = regexp.MustCompile(`^[0-9]{2}-?[0-9]{7}$`)
)

// KnownScheme reports whether s is one of Schemes.
func KnownScheme(s string) bool {
	for _, k := range Schemes {
		if k == s {
			return true
		}
	}
	return false
}

// ValidTaxID reports whether id fits scheme. It is the check of PATCH
// /v1/organizations/{id}: a VAT number is compared upper-cased, an Israeli
// number and an EIN as given, "other" is any 2-64 bytes. An unknown scheme is
// never valid.
func ValidTaxID(scheme, id string) bool {
	switch scheme {
	case SchemeEUVAT:
		return euVAT.MatchString(strings.ToUpper(id))
	case SchemeGBVAT:
		return gbVAT.MatchString(strings.ToUpper(id))
	case SchemeILVAT:
		return ilVAT.MatchString(id)
	case SchemeUSEIN:
		return usEIN.MatchString(id)
	case SchemeOther:
		return len(id) >= 2 && len(id) <= 64
	}
	return false
}

// NormalizeTaxID folds what a person typed into the form the scheme stores:
// a VAT number upper-cased without spaces, dots, dashes and slashes ("es b-123
// 456 78" -> "ESB12345678"), an Israeli number without spaces and dashes, an
// EIN and "other" only trimmed.
func NormalizeTaxID(scheme, raw string) string {
	s := strings.TrimSpace(raw)
	switch scheme {
	case SchemeEUVAT, SchemeGBVAT:
		return strings.ToUpper(strings.NewReplacer(" ", "", ".", "", "-", "", "/", "").Replace(s))
	case SchemeILVAT:
		return strings.NewReplacer(" ", "", "-", "").Replace(s)
	}
	return s
}

// MaxEmailRunes bounds a public e-mail (RFC 5321 path limit).
const MaxEmailRunes = 254

// NormalizeEmail trims and lower-cases a public e-mail and checks its shape:
// a bare address (no display name), one @, a dotted domain. The second
// result is false when the text is not such an address.
func NormalizeEmail(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" || utf8.RuneCountInString(s) > MaxEmailRunes || strings.ContainsAny(s, " \t\r\n") {
		return "", false
	}
	parsed, err := mail.ParseAddress(s)
	if err != nil || parsed.Address != s {
		return "", false
	}
	at := strings.LastIndex(s, "@")
	if at < 1 || !strings.Contains(s[at+1:], ".") || strings.HasSuffix(s, ".") {
		return "", false
	}
	return s, true
}

// MaxPhoneRunes bounds a public phone as typed.
const MaxPhoneRunes = 32

// NormalizePhone trims a public phone and checks its shape: digits with an
// optional leading "+", spaces, brackets, dashes and dots, 6 to 15 digits
// (E.164 allows 15). It keeps the person's own spacing.
func NormalizePhone(raw string) (string, bool) {
	s := strings.Join(strings.Fields(raw), " ")
	if s == "" || utf8.RuneCountInString(s) > MaxPhoneRunes {
		return "", false
	}
	digits := 0
	for i, r := range s {
		switch {
		case unicode.IsDigit(r) && r < 128:
			digits++
		case r == '+' && i == 0:
		case r == ' ' || r == '(' || r == ')' || r == '-' || r == '.':
		default:
			return "", false
		}
	}
	if digits < 6 || digits > 15 {
		return "", false
	}
	return s, true
}

// personalDomains are the free mailbox providers: an address there is most
// likely one person's own mailbox, not a role address of the organization.
var personalDomains = map[string]bool{
	"gmail.com": true, "googlemail.com": true,
	"inbox.ru": true, "mail.ru": true, "bk.ru": true, "list.ru": true, "internet.ru": true,
	"yandex.ru": true, "yandex.com": true, "yandex.by": true, "yandex.kz": true, "ya.ru": true,
	"outlook.com": true, "outlook.es": true, "hotmail.com": true, "hotmail.es": true, "live.com": true, "msn.com": true,
	"icloud.com": true, "me.com": true, "mac.com": true,
	"yahoo.com": true, "yahoo.es": true, "ymail.com": true,
	"seznam.cz": true, "email.cz": true, "post.cz": true, "centrum.cz": true,
	"gmx.com": true, "gmx.net": true, "gmx.de": true, "web.de": true,
	"proton.me": true, "protonmail.com": true, "aol.com": true, "walla.co.il": true,
}

// LooksPersonalEmail reports whether a (normalized) e-mail is at a free
// mailbox provider. It is a hint for the owner, never a refusal: an
// organization may really use such a mailbox for tickets.
func LooksPersonalEmail(email string) bool {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false
	}
	return personalDomains[strings.ToLower(strings.TrimSpace(email[at+1:]))]
}

// The data a channel needs before it may show the seller block, in the order
// the bot lists what is missing. The address country is not among them: the
// organization's own country (always set) stands in for it, exactly as the
// page prints it.
const (
	FieldLegalName  = "legal_name"
	FieldTaxID      = "tax_id"
	FieldAddress    = "address_line1"
	FieldCity       = "address_city"
	FieldPostalCode = "address_postal_code"
	FieldPublicMail = "public_email"
)

// Profile is the input of Missing: what is stored now.
type Profile struct {
	LegalName   string
	TaxID       string
	AddressLine string
	City        string
	PostalCode  string
	PublicEmail string
}

// Missing lists the required fields that are empty, in a stable order. An
// empty list means the channel may show the seller block. Required: the
// legal name, the tax number, the registered address (street, city, postal
// code) and a public e-mail of the channel (the address buyers' messages go
// to). The legal form, the registration number and the phone are optional.
func Missing(p Profile) []string {
	var out []string
	add := func(field, value string) {
		if strings.TrimSpace(value) == "" {
			out = append(out, field)
		}
	}
	add(FieldLegalName, p.LegalName)
	add(FieldTaxID, p.TaxID)
	add(FieldAddress, p.AddressLine)
	add(FieldCity, p.City)
	add(FieldPostalCode, p.PostalCode)
	add(FieldPublicMail, p.PublicEmail)
	return out
}

// MaxLegalFormRunes bounds organizations.legal_form (migration 0146 CHECK).
const MaxLegalFormRunes = 80

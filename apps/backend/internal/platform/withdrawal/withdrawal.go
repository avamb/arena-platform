// Package withdrawal decides whether the consumer right-of-withdrawal notice
// is shown for a seller. Tickets for an event on a specific date are exempt
// from the EU/EEA 14-day withdrawal right, and the buyer must be told so
// before paying and again in the confirmation. The rule is keyed on the
// SELLER's country only: the legal-address country of the organization, with
// the organization's plain country as the fallback. Any other country, and
// an unknown one, answers false (Israeli consumer law, for one, has its own
// cancellation regime and its own text).
package withdrawal

import "strings"

// euEEA is the set of ISO 3166-1 alpha-2 codes of the 27 EU member states
// plus Iceland, Liechtenstein and Norway.
var euEEA = map[string]struct{}{
	"AT": {}, "BE": {}, "BG": {}, "HR": {}, "CY": {}, "CZ": {}, "DK": {},
	"EE": {}, "FI": {}, "FR": {}, "DE": {}, "GR": {}, "HU": {}, "IE": {},
	"IT": {}, "LV": {}, "LT": {}, "LU": {}, "MT": {}, "NL": {}, "PL": {},
	"PT": {}, "RO": {}, "SK": {}, "SI": {}, "ES": {}, "SE": {},
	"IS": {}, "LI": {}, "NO": {},
}

// SellerCountry picks the country that governs the notice: the legal-address
// country when it is set, otherwise the organization's country.
func SellerCountry(legalAddressCountry *string, orgCountry string) string {
	if legalAddressCountry != nil {
		if c := strings.TrimSpace(*legalAddressCountry); c != "" {
			return strings.ToUpper(c)
		}
	}
	return strings.ToUpper(strings.TrimSpace(orgCountry))
}

// NoticeApplies reports whether the given ISO 3166-1 alpha-2 country is in
// the EU/EEA, i.e. whether the withdrawal notice must be shown.
func NoticeApplies(country string) bool {
	_, ok := euEEA[strings.ToUpper(strings.TrimSpace(country))]
	return ok
}

// ForSeller combines SellerCountry and NoticeApplies.
func ForSeller(legalAddressCountry *string, orgCountry string) bool {
	return NoticeApplies(SellerCountry(legalAddressCountry, orgCountry))
}

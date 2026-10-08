package provisioning

import "testing"

// A number that does not fit the scheme it was filed under is kept as free
// text under "other": the organization save endpoint rejects the pair with 400
// invalid_tax_id, and an owner could never edit the legal block afterwards.
func TestOrgTax(t *testing.T) {
	cases := []struct {
		name, scheme, country, id string
		wantScheme, wantNumber    string
	}{
		{"spanish VAT", "vat", "ES", "ESB1234567", "eu_vat", "ESB1234567"},
		{"VAT typed with noise", "vat", "ES", "es b-123 4567", "eu_vat", "ESB1234567"},
		{"codice fiscale filed as VAT", "vat", "IT", "RSSMRA80A01H501U", "other", "RSSMRA80A01H501U"},
		{"italian VAT", "vat", "IT", "IT12345678901", "eu_vat", "IT12345678901"},
		{"british VAT", "vat", "GB", "GB123456789", "gb_vat", "GB123456789"},
		{"israeli VAT", "vat", "IL", "514123456", "il_vat", "514123456"},
		{"israeli VAT of the wrong length", "vat", "IL", "5141234", "other", "5141234"},
		{"EIN", "ein", "US", "12-3456789", "us_ein", "12-3456789"},
		{"EIN without a dash", "ein", "US", "123456789", "us_ein", "123456789"},
		{"EIN that is not one", "ein", "US", "ABC", "other", "ABC"},
		{"company id", "ico", "CZ", "12345678", "other", "12345678"},
		{"other", "other", "IT", "free text 1", "other", "free text 1"},
		{"no scheme", "", "IT", "123", "", "123"},
		{"scheme but no number", "vat", "IT", "  ", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotScheme, gotNumber := OrgTax(c.scheme, c.country, c.id)
			if gotScheme != c.wantScheme || gotNumber != c.wantNumber {
				t.Fatalf("OrgTax(%q, %q, %q) = (%q, %q), want (%q, %q)",
					c.scheme, c.country, c.id, gotScheme, gotNumber, c.wantScheme, c.wantNumber)
			}
		})
	}
}

package sellerinfo

import "testing"

func TestIDsLine(t *testing.T) {
	cases := []struct {
		loc, reg, tax, scheme, want string
	}{
		{"en", "123", "CZ123", "eu_vat", "Registration no. 123 · VAT ID CZ123"},
		{"en", "", "12-3456789", "us_ein", "EIN 12-3456789"},
		{"en", " 7 ", "", "", "Registration no. 7"},
		{"es-ES", "", "X1", "other", "ID fiscal X1"},
		{"cs", "12345678", "CZ12345678", "eu_vat", "IČO 12345678 · DIČ CZ12345678"},
		{"cs", "", "X1", "other", "Daňové číslo X1"}, // never the VAT label twice
		{"xx", "1", "", "", "Registration no. 1"},
		{"ru", "", "", "eu_vat", ""},
		{"de", "", "DE1", "gb_vat", "USt-IdNr. DE1"},
	}
	for _, c := range cases {
		if got := IDsLine(c.loc, c.reg, c.tax, c.scheme); got != c.want {
			t.Errorf("IDsLine(%q,%q,%q,%q) = %q, want %q", c.loc, c.reg, c.tax, c.scheme, got, c.want)
		}
	}
}

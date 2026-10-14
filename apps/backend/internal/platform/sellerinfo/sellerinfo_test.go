package sellerinfo

import (
	"reflect"
	"testing"
)

func TestValidTaxID(t *testing.T) {
	cases := []struct {
		scheme, id string
		ok         bool
	}{
		{SchemeEUVAT, "ESB12345678", true},
		{SchemeEUVAT, "esb12345678", true}, // compared upper-cased, as PATCH /v1/organizations does
		{SchemeEUVAT, "B12345678", false},  // a bare NIF has no country code
		{SchemeGBVAT, "GB123456789", true},
		{SchemeGBVAT, "GB12345", false},
		{SchemeILVAT, "512345678", true},
		{SchemeILVAT, "51234567", false},
		{SchemeUSEIN, "12-3456789", true},
		{SchemeUSEIN, "123456789", true},
		{SchemeUSEIN, "1-23456789", false},
		{SchemeOther, "12345678Z", true},
		{SchemeOther, "1", false},
		{"vat", "ESB12345678", false}, // the form's word, not a stored scheme
	}
	for _, c := range cases {
		if got := ValidTaxID(c.scheme, c.id); got != c.ok {
			t.Errorf("ValidTaxID(%q, %q) = %v, want %v", c.scheme, c.id, got, c.ok)
		}
	}
}

func TestNormalizeTaxID(t *testing.T) {
	cases := map[[2]string]string{
		{SchemeEUVAT, " es b-123 456.78 "}: "ESB12345678",
		{SchemeGBVAT, "gb 123/456/789"}:    "GB123456789",
		{SchemeILVAT, "51-234 5678"}:       "512345678",
		{SchemeUSEIN, " 12-3456789 "}:      "12-3456789",
		{SchemeOther, " 12345678-Z "}:      "12345678-Z",
	}
	for in, want := range cases {
		if got := NormalizeTaxID(in[0], in[1]); got != want {
			t.Errorf("NormalizeTaxID(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
		if !ValidTaxID(in[0], NormalizeTaxID(in[0], in[1])) {
			t.Errorf("normalized %q is not valid for %s", in[1], in[0])
		}
	}
}

func TestNormalizeEmail(t *testing.T) {
	good := map[string]string{
		" Tickets@Teatro.Example ": "tickets@teatro.example",
		"info@sub.example.com":     "info@sub.example.com",
	}
	for in, want := range good {
		if got, ok := NormalizeEmail(in); !ok || got != want {
			t.Errorf("NormalizeEmail(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "tickets", "a@b", "Name <a@b.example>", "a b@c.example", "a@b.example.", "@example.com"} {
		if _, ok := NormalizeEmail(bad); ok {
			t.Errorf("NormalizeEmail(%q) accepted a malformed address", bad)
		}
	}
}

func TestNormalizePhone(t *testing.T) {
	good := map[string]string{
		" +34  600 111 222 ": "+34 600 111 222",
		"(420) 777-888.999":  "(420) 777-888.999",
	}
	for in, want := range good {
		if got, ok := NormalizePhone(in); !ok || got != want {
			t.Errorf("NormalizePhone(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "call me", "12345", "+1234567890123456", "600+111222", "١٢٣٤٥٦٧"} {
		if _, ok := NormalizePhone(bad); ok {
			t.Errorf("NormalizePhone(%q) accepted a malformed phone", bad)
		}
	}
}

func TestLooksPersonalEmail(t *testing.T) {
	for _, e := range []string{"someone@gmail.com", "x@inbox.ru", "x@mail.ru", "x@yandex.ru", "x@outlook.com", "x@icloud.com", "x@Seznam.cz"} {
		if !LooksPersonalEmail(e) {
			t.Errorf("%s should look personal", e)
		}
	}
	for _, e := range []string{"tickets@teatro.example", "info@gmail.com.example", "nobody"} {
		if LooksPersonalEmail(e) {
			t.Errorf("%s should not look personal", e)
		}
	}
}

func TestMissing(t *testing.T) {
	full := Profile{LegalName: "Teatro S.L.", TaxID: "ESB1", AddressLine: "Calle 1", City: "Madrid", PostalCode: "28001", PublicEmail: "t@x.example"}
	if got := Missing(full); len(got) != 0 {
		t.Errorf("a full profile misses %v", got)
	}
	got := Missing(Profile{LegalName: " ", City: "Madrid"})
	want := []string{FieldLegalName, FieldTaxID, FieldAddress, FieldPostalCode, FieldPublicMail}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Missing = %v, want %v", got, want)
	}
}

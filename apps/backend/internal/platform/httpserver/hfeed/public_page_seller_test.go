package hfeed

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func sp(s string) *string { return &s }

func fullSellerRow() gen.HostedPageSellerRow {
	return gen.HostedPageSellerRow{
		OrgName:            "Teatro Ejemplo",
		LegalName:          sp("Teatro Ejemplo S.L."),
		TaxID:              sp("ESB12345678"),
		TaxIDScheme:        sp("eu_vat"),
		RegistrationNumber: sp("B12345678"),
		AddressLine1:       sp("Calle Mayor 1"),
		AddressLine2:       sp("Planta 2"),
		AddressPostalCode:  sp("28013"),
		AddressCity:        sp("Madrid"),
		AddressCountry:     sp("es"),
		OrgCountry:         "DE",
		ShowSeller:         true,
		PublicContact:      []byte(`{"email":"tickets@example.com","phone":"+34 600 000 000"}`),
	}
}

func TestBuildSeller_FlagOffPublishesNothing(t *testing.T) {
	t.Parallel()
	row := fullSellerRow()
	row.ShowSeller = false
	if got := buildSeller(row); got != nil {
		t.Fatalf("buildSeller with show_seller off = %+v, want nil", got)
	}
}

func TestBuildSeller_FlagOnPublishesLegalIdentity(t *testing.T) {
	t.Parallel()
	got := buildSeller(fullSellerRow())
	if got == nil {
		t.Fatal("buildSeller returned nil with show_seller on")
	}
	if got.LegalName != "Teatro Ejemplo S.L." || got.TaxID != "ESB12345678" ||
		got.TaxScheme != "eu_vat" || got.RegistrationNumber != "B12345678" {
		t.Fatalf("identity wrong: %+v", got)
	}
	if got.Country != "ES" {
		t.Fatalf("country = %q, want the legal-address country ES, not the org country", got.Country)
	}
	if got.Address == nil || got.Address.Line1 != "Calle Mayor 1" || got.Address.City != "Madrid" ||
		got.Address.PostalCode != "28013" || got.Address.Country != "ES" {
		t.Fatalf("address wrong: %+v", got.Address)
	}
	if got.PublicContact == nil || got.PublicContact.Email != "tickets@example.com" || got.PublicContact.Phone != "+34 600 000 000" {
		t.Fatalf("contact wrong: %+v", got.PublicContact)
	}
}

func TestBuildSeller_MissingFieldsAreOmitted(t *testing.T) {
	t.Parallel()
	got := buildSeller(gen.HostedPageSellerRow{
		OrgName:       "Plain Org",
		LegalName:     sp("   "),
		TaxID:         sp(""),
		OrgCountry:    "cz",
		ShowSeller:    true,
		PublicContact: nil,
	})
	if got == nil {
		t.Fatal("nil seller")
	}
	if got.LegalName != "Plain Org" {
		t.Fatalf("legal_name = %q, want fallback to the organization name", got.LegalName)
	}
	if got.Country != "CZ" {
		t.Fatalf("country = %q, want fallback to org country CZ", got.Country)
	}
	if got.Address != nil || got.PublicContact != nil {
		t.Fatalf("address/contact must be omitted: %+v", got)
	}
	b, _ := json.Marshal(got)
	if s := string(b); strings.Contains(s, "tax_id") || strings.Contains(s, "registration_number") ||
		strings.Contains(s, "address") || strings.Contains(s, "public_contact") {
		t.Fatalf("empty values leaked into the JSON: %s", s)
	}
}

func TestParsePublicContact(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want *hostedPageSellerContact
	}{
		{"both", `{"email":" a@b.c ","phone":"123"}`, &hostedPageSellerContact{Email: "a@b.c", Phone: "123"}},
		{"email only", `{"email":"a@b.c"}`, &hostedPageSellerContact{Email: "a@b.c"}},
		{"phone only", `{"phone":"123","email":""}`, &hostedPageSellerContact{Phone: "123"}},
		{"empty object", `{}`, nil},
		{"blank values", `{"email":"  ","phone":""}`, nil},
		{"wrong types", `{"email":5,"phone":["x"]}`, nil},
		{"not an object", `"a@b.c"`, nil},
		{"null", `null`, nil},
		{"garbage", `{`, nil},
	}
	for _, tc := range cases {
		got := parsePublicContact([]byte(tc.raw))
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("%s: parsePublicContact(%s) = %+v, want %+v", tc.name, tc.raw, got, tc.want)
		}
	}
	if parsePublicContact(nil) != nil {
		t.Error("nil input must yield nil")
	}
}

// The seller row has no field the organization's own contact columns could
// travel in: a contact can only come from the channel's public_contact.
func TestSellerRowCarriesNoOrganizationContact(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal(gen.HostedPageSellerRow{})
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"Email", "Phone", "Contact"} {
		if strings.Contains(strings.ReplaceAll(string(b), "PublicContact", ""), banned) {
			t.Fatalf("HostedPageSellerRow exposes a %q field: %s", banned, b)
		}
	}
}

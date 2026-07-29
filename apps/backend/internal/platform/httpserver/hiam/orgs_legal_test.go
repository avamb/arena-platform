package hiam

import (
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func legalRequest(t *testing.T, body string) updateOrgRequest {
	t.Helper()
	var req updateOrgRequest
	if err := decodeUpdateOrgRequest([]byte(body), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return req
}

func TestLegalUpdateRetainsOmittedFieldsAndValidatesKYB(t *testing.T) {
	legalName := "Arena Test GmbH"
	current := gen.OrganizationRow{LegalName: &legalName, KybStatus: "unverified"}
	req := legalRequest(t, `{"kyb_status":"verified"}`)
	gotName, _, _, _, _, _, _, _, _, _, _, _, status, code := validateLegalUpdate(req, current)
	if code != "" || gotName == nil || *gotName != legalName || status != "verified" {
		t.Fatalf("valid KYB transition = name=%v status=%q code=%q", gotName, status, code)
	}

	empty := gen.OrganizationRow{KybStatus: "unverified"}
	_, _, _, _, _, _, _, _, _, _, _, _, _, code = validateLegalUpdate(req, empty)
	if code != "legal_name_required" {
		t.Fatalf("KYB without legal name code = %q, want legal_name_required", code)
	}
}

func TestLegalUpdateValidatesTaxAndCountry(t *testing.T) {
	valid := legalRequest(t, `{"tax_id":"DE123456789","tax_id_scheme":"eu_vat","legal_address_country":"de"}`)
	_, _, _, _, _, _, _, _, country, _, _, _, _, code := validateLegalUpdate(valid, gen.OrganizationRow{KybStatus: "unverified"})
	if code != "" || country == nil || *country != "DE" {
		t.Fatalf("valid legal fields = country=%v code=%q", country, code)
	}
	invalid := legalRequest(t, `{"tax_id":"D1","tax_id_scheme":"eu_vat"}`)
	_, _, _, _, _, _, _, _, _, _, _, _, _, code = validateLegalUpdate(invalid, gen.OrganizationRow{KybStatus: "unverified"})
	if code != "invalid_tax_id" {
		t.Fatalf("invalid tax code = %q", code)
	}
}

func TestDecodeUpdateOrganizationRejectsUnknownFields(t *testing.T) {
	var req updateOrgRequest
	err := decodeUpdateOrgRequest([]byte(`{"legal_name":"Arena","discarded_field":"no"}`), &req)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
}

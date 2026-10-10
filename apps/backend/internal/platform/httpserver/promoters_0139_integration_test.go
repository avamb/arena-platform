//go:build integration

package httpserver

// Migration 0139: a promoter's address and website (EC-14). Set at creation,
// changed with the same tri-state PATCH as the other contact fields, refused in
// plain words when malformed, and out of reach of another organization's key.
// Uses the fixture of promoters_integration_test.go.

import (
	"net/http"
	"strings"
	"testing"
)

func TestPromoters0139_AddressAndWebsite(t *testing.T) {
	f := newProm0113Fixture(t)
	orgA, orgB := f.org(t, "A"), f.org(t, "B")
	keyA := f.key(t, orgA, "promoter.read", "promoter.manage")
	keyReadOnly := f.key(t, orgA, "promoter.read")
	keyB := f.key(t, orgB, "promoter.read", "promoter.manage")
	base := "/v1/organizations/" + orgA.String()

	st, body := f.do(t, http.MethodPost, base+"/promoters", keyA,
		`{"name":"Addr Agency","address":"  Dlouha 12,\n110 00 Praha ","website":"partner.example"}`)
	if st != http.StatusCreated {
		t.Fatalf("create: status %d body %v", st, body)
	}
	id := prom0113Field(t, body, "promoter", "id").(string)
	if got := prom0113Field(t, body, "promoter", "address"); got != "Dlouha 12, 110 00 Praha" {
		t.Errorf("address = %v", got)
	}
	if got := prom0113Field(t, body, "promoter", "website"); got != "https://partner.example" {
		t.Errorf("website = %v, want https:// added", got)
	}

	// Malformed values are 400 with their own codes and change nothing.
	for _, c := range []struct{ body, code string }{
		{`{"website":"two words.example"}`, "promoter.invalid_website"},
		{`{"website":"ftp://partner.example"}`, "promoter.invalid_website"},
		{`{"website":"nodot"}`, "promoter.invalid_website"},
		{`{"address":"` + strings.Repeat("x", 301) + `"}`, "promoter.invalid_address"},
		{`{"website":"https://` + strings.Repeat("a", 300) + `.example"}`, "promoter.invalid_website"},
	} {
		if st, body := f.do(t, http.MethodPatch, base+"/promoters/"+id, keyA, c.body); st != http.StatusBadRequest || prom0113Code(body) != c.code {
			t.Errorf("PATCH %.60s: status %d code %q, want 400 %s", c.body, st, prom0113Code(body), c.code)
		}
	}

	// Tri-state: website cleared, address kept; then address replaced.
	st, body = f.do(t, http.MethodPatch, base+"/promoters/"+id, keyA, `{"website":null}`)
	if st != http.StatusOK || prom0113Field(t, body, "promoter", "website") != nil || prom0113Field(t, body, "promoter", "address") != "Dlouha 12, 110 00 Praha" {
		t.Fatalf("clear website: status %d body %v", st, body["promoter"])
	}
	st, body = f.do(t, http.MethodPatch, base+"/promoters/"+id, keyA, `{"address":"Nova 1"}`)
	if st != http.StatusOK || prom0113Field(t, body, "promoter", "address") != "Nova 1" {
		t.Fatalf("replace address: status %d body %v", st, body["promoter"])
	}
	// The list carries both fields.
	_, body = f.do(t, http.MethodGet, base+"/promoters", keyA, "")
	first := body["promoters"].([]any)[0].(map[string]any)
	if first["address"] != "Nova 1" {
		t.Errorf("list address = %v", first["address"])
	}
	if _, ok := first["website"]; !ok {
		t.Error("list items must carry the website key")
	}

	// Gates: a read-only key and another organization's key change nothing.
	if st, _ := f.do(t, http.MethodPatch, base+"/promoters/"+id, keyReadOnly, `{"address":"Hack"}`); st != http.StatusForbidden {
		t.Errorf("read-only key PATCH: status %d, want 403", st)
	}
	if st, _ := f.do(t, http.MethodPatch, base+"/promoters/"+id, keyB, `{"address":"Hack"}`); st != http.StatusForbidden {
		t.Errorf("foreign key PATCH: status %d, want 403", st)
	}
	if st, _ := f.do(t, http.MethodPatch, "/v1/organizations/"+orgB.String()+"/promoters/"+id, keyB, `{"address":"Hack"}`); st != http.StatusNotFound {
		t.Errorf("foreign promoter through own org path: status %d, want 404", st)
	}
	_, body = f.do(t, http.MethodGet, base+"/promoters", keyA, "")
	if got := body["promoters"].([]any)[0].(map[string]any)["address"]; got != "Nova 1" {
		t.Errorf("address after refused edits = %v", got)
	}
}

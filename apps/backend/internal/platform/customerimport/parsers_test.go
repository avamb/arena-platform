package customerimport

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"
)

// bil24FixturePath points at the real Bil24 orders export fixture shared
// with the compat test suite — using the same file here (rather than a
// hand-rolled sample) is what actually exercises the parser's date/name/
// per-ticket-aggregation assumptions against real pseudonymized data.
const bil24FixturePath = "../../../tests/compat/bil24/testdata/wp/bil24_orders_pseudonymized.json"

func TestParseBil24OrdersJSON_Fixture(t *testing.T) {
	data, err := os.ReadFile(bil24FixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	orgID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	mapping := Mapping{
		Frontends: map[string]uuid.UUID{
			"https://www.einatwinery.com/": orgID,
		},
	}

	rows, err := ParseBil24OrdersJSON(data, mapping)
	if err != nil {
		t.Fatalf("ParseBil24OrdersJSON: %v", err)
	}
	if len(rows) != 68 {
		t.Fatalf("got %d rows, want 68", len(rows))
	}

	var olgaCount int
	for _, r := range rows {
		if r.Name != "Olga Svoboda" {
			continue
		}
		olgaCount++
		if r.Phone != "+97258447708" {
			t.Errorf("Olga Svoboda row: phone = %q, want +97258447708", r.Phone)
		}
		if r.FirstOrderAt == nil || r.LastOrderAt == nil {
			t.Errorf("Olga Svoboda row: expected FirstOrderAt/LastOrderAt to be set (RFC3339 date should parse)")
		}
		if r.FirstOrderAt != nil && r.LastOrderAt != nil && !r.FirstOrderAt.Equal(*r.LastOrderAt) {
			t.Errorf("Olga Svoboda row: FirstOrderAt != LastOrderAt for a single order")
		}
	}
	if olgaCount < 10 {
		t.Errorf("got %d Olga Svoboda rows, want at least 10 (fixture regression?)", olgaCount)
	}

	// Every row must carry re-marshalled RawJSON bytes that round-trip.
	for i, r := range rows {
		if len(r.RawJSON) == 0 {
			t.Fatalf("row %d: RawJSON is empty", i)
		}
		var obj map[string]any
		if err := json.Unmarshal(r.RawJSON, &obj); err != nil {
			t.Fatalf("row %d: RawJSON does not parse as JSON: %v", i, err)
		}
	}
}

func TestParseBil24OrdersJSON_FrontendMapping(t *testing.T) {
	data, err := os.ReadFile(bil24FixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	orgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	mapping := Mapping{
		Frontends: map[string]uuid.UUID{
			"https://www.einatwinery.com/": orgID,
		},
	}
	rows, err := ParseBil24OrdersJSON(data, mapping)
	if err != nil {
		t.Fatalf("ParseBil24OrdersJSON: %v", err)
	}

	var sawMapped, sawUnmapped bool
	for _, r := range rows {
		switch r.OrgKey {
		case orgID.String():
			sawMapped = true
		case "":
			// no frontend.name at all — acceptable, not asserted either way
		default:
			sawUnmapped = true
		}
	}
	if !sawMapped {
		t.Errorf("expected at least one row mapped to org %s via frontends", orgID)
	}
	_ = sawUnmapped // informational only; the fixture may be single-frontend
}

func TestParseBil24OrdersJSON_InterestsAndPromoCodesDeduped(t *testing.T) {
	data, err := os.ReadFile(bil24FixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	rows, err := ParseBil24OrdersJSON(data, Mapping{})
	if err != nil {
		t.Fatalf("ParseBil24OrdersJSON: %v", err)
	}

	var sawPromo, sawInterest bool
	for _, r := range rows {
		seen := map[string]bool{}
		for _, p := range r.PromoCodesUsed {
			if p == "" {
				t.Errorf("PromoCodesUsed contains an empty string")
			}
			if seen[p] {
				t.Errorf("PromoCodesUsed contains duplicate %q", p)
			}
			seen[p] = true
			sawPromo = true
		}
		seenI := map[string]bool{}
		for _, in := range r.Interests {
			if in == "" {
				t.Errorf("Interests contains an empty string")
			}
			if seenI[in] {
				t.Errorf("Interests contains duplicate %q", in)
			}
			seenI[in] = true
			sawInterest = true
		}
	}
	if !sawPromo {
		t.Errorf("expected at least one row with a non-empty PromoCodesUsed (fixture regression?)")
	}
	if !sawInterest {
		t.Errorf("expected at least one row with a non-empty Interests (fixture regression?)")
	}
}

func TestParseCSVRows_Basic(t *testing.T) {
	csvData := []byte("Email,Phone,Full Name,Tickets,Org\n" +
		"alice@example.com,+972501234567,Alice Cohen,2,acme\n" +
		"bob@example.com,,Bob Levi,1,acme\n" +
		",,\"No Contact\",0,other\n")

	orgAcme := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	mapping := Mapping{
		Frontends: map[string]uuid.UUID{"acme": orgAcme},
		Columns: map[string]string{
			"email":         "Email",
			"phone":         "Phone",
			"name":          "Full Name",
			"tickets_count": "Tickets",
		},
		OrgRule: OrgRule{Column: "Org"},
	}

	rows, err := ParseCSVRows(csvData, mapping)
	if err != nil {
		t.Fatalf("ParseCSVRows: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}

	if rows[0].Email != "alice@example.com" || rows[0].Phone != "+972501234567" || rows[0].Name != "Alice Cohen" {
		t.Errorf("row 0 mismatch: %+v", rows[0])
	}
	if rows[0].TicketsCount != 2 {
		t.Errorf("row 0 TicketsCount = %d, want 2", rows[0].TicketsCount)
	}
	if rows[0].OrgKey != orgAcme.String() {
		t.Errorf("row 0 OrgKey = %q, want %s (resolved via frontends)", rows[0].OrgKey, orgAcme)
	}

	if rows[2].OrgKey != "other" {
		t.Errorf("row 2 OrgKey = %q, want \"other\" (unmapped, kept verbatim)", rows[2].OrgKey)
	}
	if rows[2].Email != "" || rows[2].Phone != "" {
		t.Errorf("row 2 expected empty email/phone, got %+v", rows[2])
	}
}

func TestParseCSVRows_OrgIDPinsEveryRow(t *testing.T) {
	csvData := []byte("Email\nalice@example.com\nbob@example.com\n")
	pinned := uuid.MustParse("44444444-4444-4444-4444-444444444444")
	mapping := Mapping{
		Columns: map[string]string{"email": "Email"},
		OrgRule: OrgRule{OrgID: pinned.String(), Column: "ignored-when-org_id-set"},
	}

	rows, err := ParseCSVRows(csvData, mapping)
	if err != nil {
		t.Fatalf("ParseCSVRows: %v", err)
	}
	for i, r := range rows {
		if r.OrgKey != pinned.String() {
			t.Errorf("row %d OrgKey = %q, want pinned org_id %s", i, r.OrgKey, pinned)
		}
	}
}

func TestParseCSVRows_NoHeaderRow(t *testing.T) {
	if _, err := ParseCSVRows([]byte(""), Mapping{}); err == nil {
		t.Fatalf("expected an error for an empty CSV file")
	}
}

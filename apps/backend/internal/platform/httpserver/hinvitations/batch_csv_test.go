package hinvitations

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestParseCSV_CommaSemicolonAndBOM(t *testing.T) {
	const comma = "guest,name,email,sector,row,seat\n1,Ana López,ana@x.es,Sala,3,12\n1,Ana López,,Sala,3,13\n"
	semi := strings.ReplaceAll(comma, ",", ";")
	for name, in := range map[string]string{
		"comma":         comma,
		"semicolon":     semi,
		"bom comma":     "\xEF\xBB\xBF" + comma,
		"bom semicolon": "\xEF\xBB\xBF" + semi,
		"crlf":          strings.ReplaceAll(comma, "\n", "\r\n"),
		"tab":           strings.ReplaceAll(comma, ",", "\t"),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ParseCSV([]byte(in))
			if err != nil {
				t.Fatalf("ParseCSV: %v", err)
			}
			if len(got.Rows) != 2 {
				t.Fatalf("rows = %d, want 2", len(got.Rows))
			}
			r := got.Rows[0]
			if r.Name != "Ana López" || r.Email != "ana@x.es" || r.Sector != "Sala" || r.Row != "3" || r.Seat != "12" || r.Guest != "1" {
				t.Fatalf("row 0 = %+v", r)
			}
			if r.Line != 2 || got.Rows[1].Line != 3 {
				t.Fatalf("lines = %d, %d, want 2, 3", r.Line, got.Rows[1].Line)
			}
		})
	}
}

func TestParseCSV_QuotedSeparatorAndColumnOrder(t *testing.T) {
	in := "seat_key;name;note\r\n\"sala|3|12\";\"Smith; John\";\"a, b\"\r\n"
	got, err := ParseCSV([]byte(in))
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}
	r := got.Rows[0]
	if r.SeatKey != "sala|3|12" || r.Name != "Smith; John" || r.Note != "a, b" {
		t.Fatalf("row = %+v", r)
	}
}

func TestParseCSV_HeaderAliasesAndBlankRows(t *testing.T) {
	in := "Full Name,E-mail,Category,Quantity,Unknown column\nPress ABC,press@abc.com,Standing,4,x\n,,,,\n"
	got, err := ParseCSV([]byte(in))
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}
	if len(got.Rows) != 1 || got.Rows[0].Name != "Press ABC" || got.Rows[0].Qty != "4" || got.Rows[0].Category != "Standing" {
		t.Fatalf("rows = %+v", got.Rows)
	}
}

func TestParseCSV_FileLevelErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		in   string
		code string
	}{
		"empty":         {"", "invitation_batch.empty_file"},
		"only bom":      {"\xEF\xBB\xBF", "invitation_batch.empty_file"},
		"header only":   {"name,seat_key\n", "invitation_batch.empty_file"},
		"not utf8":      {"name,seat_key\n\xff\xfe,x\n", "invitation_batch.invalid_encoding"},
		"no name":       {"email,seat_key\na@b.c,x\n", "invitation_batch.invalid_header"},
		"no places":     {"name,email\nAna,a@b.c\n", "invitation_batch.invalid_header"},
		"duplicate col": {"name,name,seat_key\nA,B,x\n", "invitation_batch.invalid_header"},
		"half a place":  {"name,row\nAna,3\n", "invitation_batch.invalid_header"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseCSV([]byte(tc.in))
			if err == nil || err.Code != tc.code {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
		})
	}
}

func TestParseCSV_TooManyRows(t *testing.T) {
	var b strings.Builder
	b.WriteString("name,seat_key\n")
	for i := 0; i <= maxBatchRows; i++ {
		b.WriteString("Ana,k\n")
	}
	if _, err := ParseCSV([]byte(b.String())); err == nil || err.Code != "invitation_batch.too_many_rows" {
		t.Fatalf("err = %v", err)
	}
}

func testPlan() (Plan, uuid.UUID, uuid.UUID) {
	gaTier, seatTier := uuid.New(), uuid.New()
	plan := Plan{
		Seats: []PlanSeat{
			{ID: uuid.New(), Key: "Sala|3|12", Sector: "Sala", Row: "3", Seat: "12", Status: "unavailable"},
			{ID: uuid.New(), Key: "Sala|3|13", Sector: "Sala", Row: "3", Seat: "13", Status: "available"},
			{ID: uuid.New(), Key: "Sala|3|14", Sector: "Sala", Row: "3", Seat: "14", Status: "sold"},
			{ID: uuid.New(), Key: "Balcon|3|12", Sector: "Balcón", Row: "3", Seat: "12", Status: "available"},
			{ID: uuid.New(), Key: "Balcon|9|1", Sector: "Balcón", Row: "9", Seat: "1", Status: "available"},
		},
		Tiers: []PlanTier{
			{ID: seatTier, Name: "Parterre"},
			{ID: gaTier, Name: "Invitado", GA: true, Available: 3, Reserve: 2},
		},
	}
	for i := range plan.Seats {
		plan.Seats[i].TierID = &seatTier
	}
	return plan, seatTier, gaTier
}

func resolve(t *testing.T, plan Plan, csvText, delivery string) Resolution {
	t.Helper()
	p, err := ParseCSV([]byte(csvText))
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}
	return ResolveBatch(p.Rows, plan, delivery)
}

func issueCodes(r Resolution) []string {
	var out []string
	for _, i := range r.Issues {
		out = append(out, i.Code)
	}
	return out
}

func TestResolveBatch_SpecExample(t *testing.T) {
	plan, _, gaTier := testPlan()
	// Sector names resolved case-insensitively and accent-exact; guest 1 owns
	// two rows (one order), guest 2 is a GA row.
	res := resolve(t, plan, `guest,name,email,phone,locale,sector,row,seat,category,qty,note
1,Ana López,ana@x.es,,es-ES,sala,3,12,,,
1,Ana López,,,,Sala,3,13,,,
2,Press ABC,press@abc.com,,en,,,,Invitado,4,prensa
`, DeliveryPDFOnly)
	if len(res.Issues) != 0 {
		t.Fatalf("issues = %+v", res.Issues)
	}
	if len(res.Guests) != 2 || res.Rows != 3 {
		t.Fatalf("guests = %d rows = %d", len(res.Guests), res.Rows)
	}
	g1, g2 := res.Guests[0], res.Guests[1]
	if g1.Name != "Ana López" || g1.Email != "ana@x.es" || g1.Locale != "es" || g1.Delivery != DeliveryGuestEmail {
		t.Fatalf("guest 1 = %+v", g1)
	}
	if len(g1.SeatKeys) != 2 || g1.SeatKeys[0] != "Sala|3|12" || g1.SeatKeys[1] != "Sala|3|13" {
		t.Fatalf("guest 1 seats = %v", g1.SeatKeys)
	}
	if len(g2.GA) != 1 || g2.GA[0].TierID != gaTier || g2.GA[0].Quantity != 4 || g2.Note != "prensa" {
		t.Fatalf("guest 2 = %+v", g2)
	}
	// 1 reserve seat + 4 GA places: 3 free, 1 from the reserve.
	if res.Seats != 2 || res.GAPlaces != 4 || res.Places() != 6 || res.FromReserve != 2 {
		t.Fatalf("seats=%d ga=%d places=%d reserve=%d", res.Seats, res.GAPlaces, res.Places(), res.FromReserve)
	}
}

func TestResolveBatch_SeatKeyAndSectorlessLookup(t *testing.T) {
	plan, _, _ := testPlan()
	res := resolve(t, plan, "name;seat_key;row;seat\nAna;Sala|3|13;;\nBea;;9;1\n", DeliveryMyEmail)
	if len(res.Issues) != 0 {
		t.Fatalf("issues = %+v", res.Issues)
	}
	if res.Guests[1].SeatKeys[0] != "Balcon|9|1" || res.Guests[0].Delivery != DeliveryMyEmail {
		t.Fatalf("guests = %+v", res.Guests)
	}
}

func TestResolveBatch_RowErrors(t *testing.T) {
	plan, seatTier, _ := testPlan()
	res := resolve(t, plan, `guest,name,email,sector,row,seat,category,qty
1,Ana,not-an-email,Sala,3,12,,
2,,,Sala,3,13,,
3,Cy,,Sala,3,14,,
4,Di,,Sala,3,12,,
5,Ed,,,3,12,,
6,Fy,,Sala,3,99,,
7,Gus,,Sala,,,Invitado,2
8,Hal,,,,,,
9,Ivy,,,,,Invitado,9
10,Jo,,,,,Invitado,x
11,Kim,,,,,Nope,1
12,Lee,,,,,Parterre,1
`, DeliveryPDFOnly)
	_ = seatTier
	want := []string{
		"invalid_email",     // line 2
		"name_required",     // line 3
		"seat_taken",        // line 4: sold
		"duplicate_seat",    // line 5
		"ambiguous_seat",    // line 6: row 3 seat 12 exists in two sectors
		"unknown_seat",      // line 7
		"place_ambiguous",   // line 8
		"place_missing",     // line 9
		"not_enough_places", // line 10
		"invalid_qty",       // line 11
		"unknown_category",  // line 12
		"category_not_ga",   // line 13
	}
	got := issueCodes(res)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("codes =\n%v\nwant\n%v\n%+v", got, want, res.Issues)
	}
	for i, issue := range res.Issues {
		if issue.Row != i+2 {
			t.Fatalf("issue %d is on row %d, want %d (%+v)", i, issue.Row, i+2, issue)
		}
	}
}

func TestResolveBatch_GuestEmailMode(t *testing.T) {
	plan, _, _ := testPlan()
	res := resolve(t, plan, "name,email,seat_key\nAna,,Sala|3|13\nBea,bea@x.es,Balcon|9|1\n", DeliveryGuestEmail)
	if got := issueCodes(res); len(got) != 1 || got[0] != "email_required" || res.Issues[0].Row != 2 {
		t.Fatalf("issues = %+v", res.Issues)
	}
}

func TestResolveBatch_GAAcrossRowsDrawsDownTheCategory(t *testing.T) {
	plan, _, _ := testPlan()
	// 5 places in the category (3 free + 2 reserve): 3 + 2 fit, one more does not.
	res := resolve(t, plan, "name,category,qty\nAna,Invitado,3\nBea,Invitado,2\nCy,Invitado,1\n", DeliveryPDFOnly)
	if got := issueCodes(res); len(got) != 1 || got[0] != "not_enough_places" || res.Issues[0].Row != 4 {
		t.Fatalf("issues = %+v", res.Issues)
	}
	if res.GAPlaces != 5 || res.FromReserve != 2 {
		t.Fatalf("ga = %d reserve = %d", res.GAPlaces, res.FromReserve)
	}
}

func TestResolveBatch_SameGuestSameCategoryMerges(t *testing.T) {
	plan, _, gaTier := testPlan()
	res := resolve(t, plan, "guest,name,category,qty\n1,Ana,Invitado,1\n1,Ana,Invitado,2\n", DeliveryPDFOnly)
	if len(res.Issues) != 0 || len(res.Guests) != 1 || len(res.Guests[0].GA) != 1 ||
		res.Guests[0].GA[0].TierID != gaTier || res.Guests[0].GA[0].Quantity != 3 {
		t.Fatalf("res = %+v", res)
	}
}

func TestResolveBatch_FileLimits(t *testing.T) {
	plan, _, _ := testPlan()
	plan.Tiers[1].Available = 5000
	res := resolve(t, plan, "name,category,qty\nAna,Invitado,200\nBea,Invitado,51\n", DeliveryPDFOnly)
	if got := issueCodes(res); len(got) != 1 || got[0] != "too_many_places" || res.Issues[0].Row != 0 {
		t.Fatalf("issues = %+v", res.Issues)
	}
	var b strings.Builder
	b.WriteString("name,category,qty\n")
	for i := 0; i <= MaxBatchGuests; i++ {
		b.WriteString("Ana,Invitado,1\n")
	}
	res = resolve(t, plan, b.String(), DeliveryPDFOnly)
	if got := issueCodes(res); len(got) != 1 || got[0] != "too_many_guests" {
		t.Fatalf("issues = %+v", res.Issues)
	}
}

func TestResolveBatch_SeatWithoutCategoryAndCapacity(t *testing.T) {
	plan, _, _ := testPlan()
	plan.Seats[1].TierID = nil // Sala|3|13
	res := resolve(t, plan, "name,seat_key\nAna,Sala|3|13\n", DeliveryPDFOnly)
	if got := issueCodes(res); len(got) != 1 || got[0] != "seat_without_category" {
		t.Fatalf("issues = %+v", res.Issues)
	}
	two := 2
	plan.CapacityLeft = &two
	res = resolve(t, plan, "name,seat_key\nAna,Balcon|9|1\nBea,Balcon|3|12\nCy,Sala|3|12\n", DeliveryPDFOnly)
	if got := issueCodes(res); len(got) != 1 || got[0] != "capacity_exceeded" || res.Issues[0].Row != 0 {
		t.Fatalf("issues = %+v", res.Issues)
	}
}

func TestResolveBatch_GuestNumbersAreComparedAsNumbers(t *testing.T) {
	plan, _, _ := testPlan()
	res := resolve(t, plan, "guest,name,seat_key\n1,Ana,Sala|3|13\n01,Ana,Balcon|9|1\n 1 ,Ana,Balcon|3|12\nA,Bea,Sala|3|12\na,Bea,Sala|3|14\n", DeliveryPDFOnly)
	if len(res.Guests) != 2 || len(res.Guests[0].SeatKeys) != 3 {
		t.Fatalf("guests = %+v issues = %+v", res.Guests, res.Issues)
	}
}

func TestParseCSV_ExcelSepLineAndLineNumbers(t *testing.T) {
	in := "sep=;\r\nname;seat_key\r\nAna;Sala|3|13\r\n"
	got, err := ParseCSV([]byte(in))
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}
	// The sep= line is line 1, the header line 2, the first row line 3.
	if len(got.Rows) != 1 || got.Rows[0].Name != "Ana" || got.Rows[0].Line != 3 {
		t.Fatalf("rows = %+v", got.Rows)
	}
}

func TestParseCSV_UnclosedQuoteIsAnError(t *testing.T) {
	in := "name,seat_key\nAna,Sala|3|13\n\"Bea,Sala|3|12\nCy,Sala|3|14\n"
	_, err := ParseCSV([]byte(in))
	if err == nil || err.Code != "invitation_batch.invalid_csv" {
		t.Fatalf("err = %v, want invalid_csv", err)
	}
	// A bare quote inside an unquoted field is an error too, with its line.
	_, err = ParseCSV([]byte("name,seat_key\nAna 5\" tall,Sala|3|13\n"))
	if err == nil || err.Code != "invitation_batch.invalid_csv" || err.Details["line"] != 2 {
		t.Fatalf("err = %v", err)
	}
}

package guestlist

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/csvexport"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/xlsxexport"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/xlsxexport/xlsxtest"
)

func TestCells_ColumnsAndHeaders(t *testing.T) {
	sector, row, seat := "A", "3", "12"
	r := gen.GuestListRow{SystemTicketID: 7, GuestName: "Anna Ivanova", SeatSector: &sector, SeatRow: &row, SeatNumber: &seat, VIP: true, TicketStatus: "active"}
	c := Cells(r, "en", time.UTC)
	if len(c) != len(Columns) {
		t.Fatalf("cells = %d, columns = %d", len(c), len(Columns))
	}
	want := "Last name;First name;Phone;E-mail;Category;Row;Seat;Ticket status;Status reason;Barcode;Order number"
	if got := strings.Join(csvexport.Header("en", Columns[:11]...), ";"); got != want {
		t.Errorf("en header = %q", got)
	}
	if h := csvexport.Header("ru", Columns...); h[0] != "Фамилия" || h[5] != "Ряд" || h[6] != "Место" || h[10] != "Номер заказа" {
		t.Errorf("ru header = %v", h)
	}
	if h := csvexport.Header("es", Columns...); h[0] != "Apellido" || h[7] != "Estado de la entrada" {
		t.Errorf("es header = %v", h)
	}
	if c[0].Value() != "Ivanova" || c[1].Value() != "Anna" || c[5].Value() != "3" || c[6].Value() != "12" || c[11].Value() != "A" {
		t.Errorf("name/row/seat cells = %q %q %q %q %q", c[0].Value(), c[1].Value(), c[5].Value(), c[6].Value(), c[11].Value())
	}
}

func TestSplitName(t *testing.T) {
	for in, want := range map[string][2]string{
		"Anna Ivanova":               {"Ivanova", "Anna"},
		"  Maria del  Carmen Lopez ": {"Lopez", "Maria del Carmen"},
		"Madonna":                    {"Madonna", ""},
		"":                           {"", ""},
		"Иван Петров":                {"Петров", "Иван"},
	} {
		if l, f := SplitName(in); l != want[0] || f != want[1] {
			t.Errorf("SplitName(%q) = %q, %q; want %q, %q", in, l, f, want[0], want[1])
		}
	}
}

func TestStatusOf_FourMACSStatesAndReasons(t *testing.T) {
	at := time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)
	gone := "Refunded / cancelled (3)"
	replaced := "Replaced: sent to a wrong e-mail address"
	for _, tc := range []struct {
		name     string
		row      gen.GuestListRow
		code     int
		reason   string
		labelEN  string
		reasonEN string
	}{
		{"not entered", gen.GuestListRow{TicketStatus: "active", SessionStatus: "scheduled"}, StatusValid, "", "Valid (0)", ""},
		{"entered by used_at", gen.GuestListRow{TicketStatus: "active", SessionStatus: "scheduled", EnteredAt: &at}, StatusCheckedIn, "", "Checked in (1)", ""},
		{"one MACS scan", gen.GuestListRow{TicketStatus: "active", SessionStatus: "scheduled", MacsAdmittedScans: 1}, StatusCheckedIn, "", "Checked in (1)", ""},
		{"three MACS scans", gen.GuestListRow{TicketStatus: "active", SessionStatus: "scheduled", EnteredAt: &at, MacsAdmittedScans: 3}, StatusCheckedIn, "", "Checked in (1)", ""},
		{"two MACS scans stay checked in", gen.GuestListRow{TicketStatus: "active", SessionStatus: "scheduled", EnteredAt: &at, MacsAdmittedScans: 2}, StatusCheckedIn, "", "Checked in (1)", ""},
		{"refunded", gen.GuestListRow{TicketStatus: "cancelled", Refunded: true}, StatusRefunded, ReasonRefunded, gone, "Refunded"},
		{"cancelled", gen.GuestListRow{TicketStatus: "cancelled"}, StatusRefunded, ReasonCancelled, gone, "Cancelled"},
		{"revoked invitation", gen.GuestListRow{TicketStatus: "revoked"}, StatusRefunded, ReasonCancelled, gone, "Cancelled"},
		{"session cancelled", gen.GuestListRow{TicketStatus: "active", SessionStatus: "cancelled"}, StatusRefunded, ReasonSessionCancelled, gone, "Session cancelled"},
		{"reissued", gen.GuestListRow{TicketStatus: "transferred", Reissued: true}, StatusRefunded, ReasonReissued, gone, replaced},
		// A replaced ticket that had been scanned is still status 3.
		{"reissued after entry", gen.GuestListRow{TicketStatus: "transferred", Reissued: true, EnteredAt: &at, MacsAdmittedScans: 1}, StatusRefunded, ReasonReissued, gone, replaced},
	} {
		code, reason := StatusOf(tc.row)
		if code != tc.code || reason != tc.reason {
			t.Errorf("%s: StatusOf = %d, %q; want %d, %q", tc.name, code, reason, tc.code, tc.reason)
		}
		if got := StatusLabel("en", code); got != tc.labelEN {
			t.Errorf("%s: label = %q, want %q", tc.name, got, tc.labelEN)
		}
		if got := ReasonLabel("en", reason); got != tc.reasonEN {
			t.Errorf("%s: reason label = %q, want %q", tc.name, got, tc.reasonEN)
		}
	}
	if StatusLabel("ru", StatusCheckedOut) != "Вышел (2)" || ReasonLabel("es", ReasonRefunded) != "Devuelta" || ReasonLabel("xx", ReasonCancelled) != "Cancelled" {
		t.Error("localized labels")
	}
}

func TestBuild_XLSXBarcodeIsATextCellAndRowsAreMapped(t *testing.T) {
	madrid, _ := time.LoadLocation("Europe/Madrid")
	entered := time.Date(2026, 11, 20, 18, 31, 0, 0, time.UTC)
	order, barcode, phone, email, tier := int64(1000000500), "4600051000001", "+34600111222", "anna@example.com", "VIP"
	row, seat := "3", "12"
	start := time.Date(2026, 11, 20, 19, 0, 0, 0, time.UTC)
	q := fakeQuerier{rows: []gen.GuestListRow{
		{SystemTicketID: 349, GuestName: "Anna Ivanova", TierName: &tier, Barcode: &barcode, EnteredAt: &entered,
			OrderNumber: &order, Email: &email, Phone: &phone, VIP: true, SeatRow: &row, SeatNumber: &seat,
			TicketStatus: "active", SessionStatus: "scheduled", SessionStartAt: start},
		{SystemTicketID: 7, GuestName: "Bob Adams", TicketStatus: "transferred", Reissued: true, SessionStartAt: start},
	}}
	file, err := Build(context.Background(), q, uuid.New(), uuid.New(), "en", madrid, 0, XLSX)
	if err != nil {
		t.Fatal(err)
	}
	if file.Rows != 2 || file.ContentType != xlsxexport.ContentType || file.Counts != [4]int{0, 1, 0, 1} {
		t.Fatalf("file = %d rows, %q, counts %v", file.Rows, file.ContentType, file.Counts)
	}
	p := xlsxtest.Open(t, file.Body)
	// Sorted by last name: Adams (row 2), Ivanova (row 3). Columns: A last,
	// B first, C phone, D e-mail, E category, F row, G seat, H status,
	// I reason, J barcode, K order, M ticket.
	for ref, want := range map[string]string{
		"A2": "Adams", "B2": "Bob", "J2": ean13.PlatformCode(7), "H2": "Refunded / cancelled (3)", "I2": "Replaced: sent to a wrong e-mail address",
		"A3": "Ivanova", "B3": "Anna", "C3": phone, "D3": email, "E3": tier, "F3": "3", "G3": "12",
		"H3": "Checked in (1)", "I3": "", "J3": barcode, "K3": "1000000500", "M3": "349",
	} {
		c, _ := p.CellAt(ref)
		if c.Value != want {
			t.Errorf("%s = %q, want %q", ref, c.Value, want)
		}
	}
	// The barcode is a TEXT cell: an inline string with the "@" format, never
	// a number, and so is its whole column (a value typed later stays text).
	for _, ref := range []string{"J2", "J3"} {
		c, ok := p.CellAt(ref)
		if !ok || (c.Type != "inlineStr" && c.Type != "s") {
			t.Errorf("%s type = %q, want a string cell", ref, c.Type)
		}
		if !p.TextFormat(c.Style) {
			t.Errorf("%s is not in the text format", ref)
		}
	}
	for _, col := range []int{3, 10, 11, 13} { // phone, barcode, order number, ticket
		if !p.TextFormat(p.ColumnStyle(col)) {
			t.Errorf("column %d is not a text column", col)
		}
	}
	if c, _ := p.CellAt("O3"); c.Value != "2026-11-20 19:31" || c.Type != "inlineStr" {
		t.Errorf("entry time = %+v", c)
	}
	if p.SheetName != "Guests" {
		t.Errorf("sheet = %q", p.SheetName)
	}
	if v := p.Values(); v[0][0] != "Last name" || v[0][10] != "Order number" {
		t.Errorf("header = %v", v[0])
	}

	csv, err := Build(context.Background(), q, uuid.New(), uuid.New(), "en", madrid, 0, CSV)
	if err != nil {
		t.Fatal(err)
	}
	if csv.ContentType != "text/csv; charset=utf-8" || !strings.Contains(string(csv.Body), "4600051000001") {
		t.Errorf("csv = %q %s", csv.ContentType, csv.Body)
	}
}

type fakeEventQuerier struct{ rows []gen.GuestListRow }

func (f fakeEventQuerier) CountEventGuestListRows(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
	return int64(len(f.rows)), nil
}

func (f fakeEventQuerier) ListEventGuestListRows(context.Context, uuid.UUID, uuid.UUID, int32) ([]gen.GuestListRow, error) {
	return f.rows, nil
}

func TestBuildEvent_SessionColumnOnTheVenueClockAndSessionOrder(t *testing.T) {
	tokyo, riga := "Asia/Tokyo", "Europe/Riga"
	late := time.Date(2026, 12, 2, 18, 0, 0, 0, time.UTC)
	early := time.Date(2026, 12, 1, 18, 0, 0, 0, time.UTC)
	s1, s2 := uuid.New(), uuid.New()
	q := fakeEventQuerier{rows: []gen.GuestListRow{
		{SystemTicketID: 1, GuestName: "Zed Zorn", TicketStatus: "active", SessionID: s1, SessionStartAt: late, VenueTimezone: &tokyo},
		{SystemTicketID: 2, GuestName: "Amy Adler", TicketStatus: "active", SessionID: s2, SessionStartAt: early, VenueTimezone: &riga},
	}}
	file, err := BuildEvent(context.Background(), q, uuid.New(), uuid.New(), "es", time.UTC, 0, XLSX)
	if err != nil {
		t.Fatal(err)
	}
	v := xlsxtest.Open(t, file.Body).Values()
	if v[0][0] != "Sesión" || v[0][1] != "Apellido" {
		t.Errorf("header = %v", v[0])
	}
	if v[1][0] != "01.12.2026 20:00" || v[1][1] != "Adler" || v[2][0] != "03.12.2026 03:00" || v[2][1] != "Zorn" {
		t.Errorf("rows = %v", v[1:])
	}
}

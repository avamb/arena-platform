package guestlist

import (
	"context"
	"errors"
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

func TestCells_OneCellPerColumn(t *testing.T) {
	sector, row, seat := "A", "3", "12"
	r := gen.GuestListRow{SystemTicketID: 7, GuestName: "Anna", SeatSector: &sector, SeatRow: &row, SeatNumber: &seat, VIP: true}
	if got := len(Cells(r, time.UTC)); got != len(Columns) {
		t.Fatalf("cells = %d, columns = %d", got, len(Columns))
	}
	if h := csvexport.Header("ru", Columns...); h[0] != "Гость" || h[2] != "Место" || h[11] != "Пригласительный" {
		t.Errorf("ru header = %v", h)
	}
}

func TestSeatLabel(t *testing.T) {
	a, empty, n := "A", " ", "12"
	if got := SeatLabel(&a, &empty, &n); got != "A / 12" {
		t.Errorf("SeatLabel = %q", got)
	}
	if got := SeatLabel(nil, nil, nil); got != "" {
		t.Errorf("GA SeatLabel = %q", got)
	}
}

func TestFileName_EventNameVenueDayAndFormat(t *testing.T) {
	prague, _ := time.LoadLocation("Europe/Prague")
	// 23:30 UTC on the 24th is already the 25th in Prague.
	start := time.Date(2026, 12, 24, 23, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, slug string
		format     Format
		want       string
	}{
		{"Лебединое озеро", "swan", XLSX, "guests_lebedinoe-ozero_2026-12-25.xlsx"},
		{"Café Noir / Live!", "", CSV, "guests_cafe-noir-live_2026-12-25.csv"},
		// A name nothing survives of (Hebrew) falls back to the page slug,
		// then to "event"; never to an id.
		{"מופע", "show-2026", XLSX, "guests_show-2026_2026-12-25.xlsx"},
		{"מופע", "", XLSX, "guests_event_2026-12-25.xlsx"},
		{strings.Repeat("very long words ", 10), "", XLSX, "guests_very-long-words-very-long-words-very-long-words-very-long_2026-12-25.xlsx"},
	} {
		if got := FileName(tc.name, tc.slug, start, prague, tc.format); got != tc.want {
			t.Errorf("FileName(%q, %q) = %q, want %q", tc.name, tc.slug, got, tc.want)
		}
	}
	if got := FileName("X", "", start, nil, XLSX); got != "guests_x_2026-12-24.xlsx" {
		t.Errorf("nil zone FileName = %q", got)
	}
}

type fakeQuerier struct{ rows []gen.GuestListRow }

func (f fakeQuerier) CountGuestListRows(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
	return int64(len(f.rows)), nil
}

func (f fakeQuerier) ListGuestListRows(context.Context, uuid.UUID, uuid.UUID, int32) ([]gen.GuestListRow, error) {
	return f.rows, nil
}

func TestBuild_XLSXKeepsIdentifiersAsText(t *testing.T) {
	madrid, _ := time.LoadLocation("Europe/Madrid")
	entered := time.Date(2026, 11, 20, 18, 31, 0, 0, time.UTC)
	order, barcode, phone, email, tier := int64(1000000500), "4600051000001", "+34600111222", "anna@example.com", "VIP"
	q := fakeQuerier{rows: []gen.GuestListRow{
		{SystemTicketID: 349, GuestName: "Анна", TierName: &tier, Barcode: &barcode, EnteredAt: &entered,
			OrderNumber: &order, Email: &email, Phone: &phone, VIP: true},
		{SystemTicketID: 7, GuestName: "Bob"},
	}}
	file, err := Build(context.Background(), q, uuid.New(), uuid.New(), "ru", madrid, 0, XLSX)
	if err != nil {
		t.Fatal(err)
	}
	if file.Rows != 2 || file.ContentType != xlsxexport.ContentType {
		t.Fatalf("file = %d rows, %q", file.Rows, file.ContentType)
	}
	p := xlsxtest.Open(t, file.Body)
	if p.SheetName != "Гости" {
		t.Errorf("sheet = %q", p.SheetName)
	}
	// Columns: D ticket, E barcode, I order, K phone.
	for ref, want := range map[string]string{"D2": "349", "E2": barcode, "I2": "1000000500", "K2": phone, "E3": ean13.PlatformCode(7)} {
		c, ok := p.CellAt(ref)
		if !ok || c.Value != want {
			t.Errorf("%s = %+v, want %q", ref, c, want)
			continue
		}
		if c.Type != "inlineStr" && c.Type != "s" {
			t.Errorf("%s type = %q, want a string", ref, c.Type)
		}
		if !p.TextFormat(c.Style) {
			t.Errorf("%s is not in the text format", ref)
		}
	}
	for _, col := range []int{4, 5, 9, 11} {
		if !p.TextFormat(p.ColumnStyle(col)) {
			t.Errorf("column %d is not a text column", col)
		}
	}
	// Entry time is readable text on the venue's clock; flags are yes/no.
	if c, _ := p.CellAt("H2"); c.Value != "2026-11-20 19:31" || c.Type != "inlineStr" {
		t.Errorf("entry time = %+v", c)
	}
	if v := p.Values(); v[0][0] != "Гость" || v[1][5] != "yes" || v[1][6] != "yes" || v[2][6] != "no" {
		t.Errorf("rows = %v", v)
	}

	csv, err := Build(context.Background(), q, uuid.New(), uuid.New(), "en", madrid, 0, CSV)
	if err != nil {
		t.Fatal(err)
	}
	if csv.ContentType != "text/csv; charset=utf-8" || !strings.Contains(string(csv.Body), `="4600051000001"`) {
		t.Errorf("csv = %q %s", csv.ContentType, csv.Body)
	}
}

func TestBuild_TooManyRows(t *testing.T) {
	q := fakeQuerier{rows: make([]gen.GuestListRow, 3)}
	if _, err := Build(context.Background(), q, uuid.New(), uuid.New(), "en", time.UTC, 2, XLSX); !errors.Is(err, ErrTooManyRows) {
		t.Errorf("err = %v", err)
	}
}

func TestNormalizeLocale(t *testing.T) {
	for in, want := range map[string]string{"ru-RU": "ru", "ES": "es", "cs": "en", "": "en"} {
		if got := NormalizeLocale(in); got != want {
			t.Errorf("NormalizeLocale(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderEmail_EscapesTheEventNameAndNamesTheMoment(t *testing.T) {
	start := time.Date(2026, 12, 24, 17, 30, 0, 0, time.UTC)
	l := RenderEmail("ru", SlotH3, "<b>Шоу</b>", start, time.UTC, 42)
	if !strings.Contains(l.Subject, "<b>Шоу</b>") || !strings.Contains(l.Subject, "24.12.2026 17:30") {
		t.Errorf("subject = %q", l.Subject)
	}
	if strings.Contains(l.HTML, "<b>Шоу</b>") || !strings.Contains(l.HTML, "&lt;b&gt;Шоу&lt;/b&gt;") {
		t.Errorf("the event name is not escaped in the HTML body: %s", l.HTML)
	}
	if !strings.Contains(l.Text, "До начала сеанса 3 часа.") || !strings.Contains(l.Text, "Билетов в списке: 42.") {
		t.Errorf("text body = %q", l.Text)
	}
	for _, loc := range Locales {
		for _, slot := range []string{SlotH24, SlotH3, SlotDoors, "other"} {
			l := RenderEmail(loc, slot, "X", start, time.UTC, 1)
			if strings.Contains(l.Text, "guestlist.email.") || strings.Contains(l.Subject, "guestlist.email.") {
				t.Errorf("%s/%s: a key is missing from the catalog: %q", loc, slot, l.Text)
			}
		}
	}
}

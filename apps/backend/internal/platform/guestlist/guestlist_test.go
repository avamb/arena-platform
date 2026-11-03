package guestlist

import (
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/csvexport"
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

func TestFileName_VenueDayAndSafeChars(t *testing.T) {
	prague, _ := time.LoadLocation("Europe/Prague")
	// 23:30 UTC on the 24th is already the 25th in Prague.
	start := time.Date(2026, 12, 24, 23, 30, 0, 0, time.UTC)
	if got := FileName("Swan Lake/Лебединое", start, prague); got != "guests_swan-lake_2026-12-25.csv" {
		t.Errorf("FileName = %q", got)
	}
	if got := FileName("", start, nil); got != "guests_session_2026-12-24.csv" {
		t.Errorf("empty slug FileName = %q", got)
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

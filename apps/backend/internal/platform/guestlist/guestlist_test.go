package guestlist

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

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

package guestlistteam

import (
	"strings"
	"testing"
	"time"
)

func TestDue_WindowsAroundTheStart(t *testing.T) {
	start := time.Date(2026, 12, 24, 18, 0, 0, 0, time.UTC)
	at := func(d time.Duration) time.Time { return start.Add(d) }
	for _, tc := range []struct {
		name string
		now  time.Time
		want string
	}{
		{"25 hours before: too early", at(-25 * time.Hour), ""},
		{"1 ns before the 24 h moment", at(-24*time.Hour - 1), ""},
		{"exactly 24 h before", at(-24 * time.Hour), "24h"},
		{"12 hours before", at(-12 * time.Hour), "24h"},
		{"3 h 1 s before: still the 24 h list", at(-3*time.Hour - time.Second), "24h"},
		{"exactly 3 h before: only the 3 h list", at(-3 * time.Hour), "3h"},
		{"1 hour before", at(-time.Hour), "3h"},
		{"1 s before the start", at(-time.Second), "3h"},
		{"exactly at the start: nothing", at(0), ""},
		{"after the start: nothing", at(time.Hour), ""},
	} {
		got := strings.Join(Due(tc.now, start), ",")
		if got != tc.want {
			t.Errorf("%s: Due = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRender_LocalizedAndEscaped(t *testing.T) {
	riga, _ := time.LoadLocation("Europe/Riga")
	start := time.Date(2026, 12, 24, 17, 30, 0, 0, time.UTC) // 19:30 in Riga
	counts := [4]int{10, 5, 1, 2}
	for _, tc := range []struct {
		locale, kind string
		subject      string
		bodyHas      []string
	}{
		{"en", Kind24h, "Guest list: <b>Show</b>, 24.12.2026 19:30", []string{"24 hours before the start", "Tickets: 18.", "Valid: 10.", "Refunded / cancelled: 2."}},
		{"ru", Kind3h, "Список гостей: <b>Show</b>, 24.12.2026 19:30", []string{"за 3 часа до начала", "Билетов: 18.", "Вошёл: 5."}},
		{"es", Kind24h, "Lista de invitados: <b>Show</b>, 24.12.2026 19:30", []string{"24 horas antes del inicio", "Entradas: 18."}},
		{"xx", Kind3h, "Guest list: <b>Show</b>, 24.12.2026 19:30", []string{"3 hours before the start"}},
	} {
		l := Render(tc.locale, tc.kind, "<b>Show</b>", start, riga, counts)
		if l.Subject != tc.subject {
			t.Errorf("%s: subject = %q", tc.locale, l.Subject)
		}
		for _, w := range tc.bodyHas {
			if !strings.Contains(l.Text, w) {
				t.Errorf("%s: text lacks %q: %s", tc.locale, w, l.Text)
			}
		}
		if strings.Contains(l.HTML, "<b>Show</b>") || !strings.Contains(l.HTML, "&lt;b&gt;Show&lt;/b&gt;") {
			t.Errorf("%s: event name is not escaped in the HTML: %s", tc.locale, l.HTML)
		}
		if l.Caption == "" || len([]rune(l.Caption)) > 1024 {
			t.Errorf("%s: caption length %d", tc.locale, len([]rune(l.Caption)))
		}
	}
}

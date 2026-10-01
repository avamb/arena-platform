package eventbot

import (
	"context"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// A date added to an event that already has dates offers the time of the
// date before it, not a fixed 20:00 (an organizer with a 18:30 session was
// offered "keep 20:00" for the second date on 2026-10-01).
func TestDefaultTime_FollowsThePreviousDate(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.d.Sessions = []DraftSession{{Date: "2026-10-28", Time: "18:30"}}
	r.d.Cur = 1
	r.d.Step = stSTime

	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if got := screen.Buttons[0][0].Label; !strings.Contains(got, "18:30") || strings.Contains(got, "20:00") {
		t.Errorf("keep button = %q, want the previous date's 18:30", got)
	}
	r.d.Sessions = append(r.d.Sessions, DraftSession{Date: "2026-11-04"})
	r.press("default", stSSame)
	if got := r.d.Sessions[1].Time; got != "18:30" {
		t.Errorf("time = %q, want 18:30", got)
	}

	// The very first date has nothing to copy.
	first := newWizardRun(t, newFakeRefs())
	first.d.Step = stSTime
	if got := first.d.defaultTime(); got != "20:00" {
		t.Errorf("first date default = %q", got)
	}
}

// "Sold 2" says nothing about a ticket that came back: once one has been
// cancelled the card also says how many were issued, returned and valid.
func TestSessionLine_TicketsBreakdown(t *testing.T) {
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	texts := NewTexts(bundle)
	line := texts.T("ru", "bot.session_tickets_line", map[string]any{"Issued": 2, "Cancelled": 1, "Active": 1})
	for _, want := range []string{"выдано 2", "возвращено или отменено 1", "действует 1"} {
		if !strings.Contains(line, want) {
			t.Errorf("breakdown lacks %q: %s", want, line)
		}
	}
	base := map[string]any{"When": "w", "Venue": "v", "Sold": 1, "Total": 15, "Available": 14, "Held": 0, "Money": "m"}
	without := texts.T("ru", "bot.session_line", base)
	if strings.Contains(without, "выдано") {
		t.Errorf("no breakdown expected when nothing was cancelled: %s", without)
	}
	base["Tickets"] = line
	with := texts.T("ru", "bot.session_line", base)
	if !strings.Contains(with, "выдано 2") || !strings.Contains(with, "Продано 1 из 15") {
		t.Errorf("card = %s", with)
	}
}

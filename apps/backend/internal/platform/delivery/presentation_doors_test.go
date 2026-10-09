package delivery

import (
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// The doors-open time is resolved from the session like the start, and a
// hint the enqueuer set wins.
func TestApplyPresentation_DoorsOpen(t *testing.T) {
	doors := time.Date(2026, 11, 4, 18, 30, 0, 0, time.UTC)
	var p Payload
	applyPresentation(&p, gen.TicketPresentationRow{DoorsOpenAt: &doors})
	if p.DoorsOpenAt == nil || !p.DoorsOpenAt.Equal(doors) {
		t.Fatalf("doors = %v, want %v", p.DoorsOpenAt, doors)
	}

	hint := doors.Add(-time.Hour)
	q := Payload{DoorsOpenAt: &hint}
	applyPresentation(&q, gen.TicketPresentationRow{DoorsOpenAt: &doors})
	if !q.DoorsOpenAt.Equal(hint) {
		t.Fatalf("a set hint must win, got %v", q.DoorsOpenAt)
	}

	if got := formatClockForEmail(&doors, "Europe/Madrid"); got != "19:30" {
		t.Errorf("formatClockForEmail = %q, want 19:30 (Madrid)", got)
	}
	if got := formatClockForEmail(nil, "Europe/Madrid"); got != "" {
		t.Errorf("no doors time must format as empty, got %q", got)
	}
}

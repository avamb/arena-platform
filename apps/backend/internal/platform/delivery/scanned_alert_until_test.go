package delivery

import (
	"testing"
	"time"
)

// The exception's end is shown in the venue's zone, Madrid when the venue
// has none, never in UTC.
func TestExceptionUntil_UsesTheVenueZone(t *testing.T) {
	at := time.Date(2026, 10, 14, 18, 0, 0, 0, time.UTC)
	if got := exceptionUntil(at, "Europe/Prague"); got != "2026-10-14 20:00 (Europe/Prague)" {
		t.Errorf("Prague: %q", got)
	}
	if got := exceptionUntil(at, ""); got != "2026-10-14 20:00 (Europe/Madrid)" {
		t.Errorf("no zone: %q", got)
	}
	if got := exceptionUntil(at, "Not/AZone"); got != "2026-10-14 20:00 (Europe/Madrid)" {
		t.Errorf("unknown zone: %q", got)
	}
}

package hbilling

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCursor_RoundTripAndRejectsGarbage(t *testing.T) {
	at := time.Date(2026, 10, 10, 12, 0, 0, 123456000, time.UTC)
	id := uuid.New()
	gotAt, gotID, ok := decodeCursor(encodeCursor(at, id))
	if !ok || !gotAt.Equal(at) || gotID != id {
		t.Fatalf("round trip = %v %v %v", gotAt, gotID, ok)
	}
	for _, bad := range []string{"", "zzz", "bm9wZQ", "MTIzfG5vdC1hLXV1aWQ"} {
		if _, _, ok := decodeCursor(bad); ok {
			t.Errorf("cursor %q accepted", bad)
		}
	}
}

func TestParseBoundary(t *testing.T) {
	if got, ok := parseBoundary("2026-10-01"); !ok || !got.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("date = %v %v", got, ok)
	}
	if got, ok := parseBoundary("2026-10-01T10:00:00+02:00"); !ok || !got.Equal(time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("rfc3339 = %v %v", got, ok)
	}
	if _, ok := parseBoundary("yesterday"); ok {
		t.Error("garbage accepted")
	}
}

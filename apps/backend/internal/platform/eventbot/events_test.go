package eventbot

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func ts(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func TestSortEvents(t *testing.T) {
	t.Parallel()
	now := *ts("2026-10-01T12:00:00Z")
	events := []openapi.EventItem{
		{Name: "past-old", FirstSessionAt: ts("2026-01-01T20:00:00Z"), LastSessionAt: ts("2026-01-01T22:00:00Z")},
		{Name: "far", FirstSessionAt: ts("2026-12-01T20:00:00Z"), LastSessionAt: ts("2026-12-01T22:00:00Z")},
		{Name: "no-dates"},
		{Name: "soon", FirstSessionAt: ts("2026-10-05T20:00:00Z"), LastSessionAt: ts("2026-10-05T22:00:00Z")},
		{Name: "past-recent", FirstSessionAt: ts("2026-09-20T20:00:00Z"), LastSessionAt: ts("2026-09-20T22:00:00Z")},
		{Name: "running", FirstSessionAt: ts("2026-09-30T20:00:00Z"), LastSessionAt: ts("2026-10-02T22:00:00Z")},
	}
	upcoming, past := SortEvents(events, now)
	wantUp := []string{"running", "soon", "far", "no-dates"}
	if len(upcoming) != len(wantUp) {
		t.Fatalf("upcoming = %d, want %d", len(upcoming), len(wantUp))
	}
	for i, w := range wantUp {
		if upcoming[i].Name != w {
			t.Errorf("upcoming[%d] = %q, want %q", i, upcoming[i].Name, w)
		}
	}
	wantPast := []string{"past-recent", "past-old"}
	if len(past) != len(wantPast) {
		t.Fatalf("past = %d, want %d", len(past), len(wantPast))
	}
	for i, w := range wantPast {
		if past[i].Name != w {
			t.Errorf("past[%d] = %q, want %q", i, past[i].Name, w)
		}
	}
}

func TestPageOf(t *testing.T) {
	t.Parallel()
	items := []int{1, 2, 3, 4, 5, 6, 7}
	if got, page, pages := PageOf(items, 1, 5); len(got) != 5 || page != 1 || pages != 2 || got[0] != 1 {
		t.Errorf("page 1: %v %d %d", got, page, pages)
	}
	if got, page, pages := PageOf(items, 2, 5); len(got) != 2 || page != 2 || pages != 2 || got[0] != 6 {
		t.Errorf("page 2: %v %d %d", got, page, pages)
	}
	if got, page, _ := PageOf(items, 9, 5); len(got) != 2 || page != 2 {
		t.Errorf("clamped high: %v %d", got, page)
	}
	if got, page, _ := PageOf(items, 0, 5); len(got) != 5 || page != 1 {
		t.Errorf("clamped low: %v %d", got, page)
	}
	if got, page, pages := PageOf([]int{}, 1, 5); len(got) != 0 || page != 1 || pages != 1 {
		t.Errorf("empty: %v %d %d", got, page, pages)
	}
}

func TestSummaryPlaces(t *testing.T) {
	t.Parallel()
	var s openapi.SessionSummary
	s.Places.Ga = openapi.SessionPlaceCounts{Sold: 10, SoldUpstream: 2, Total: 50, Available: 30, Held: 8}
	s.Places.Seats = openapi.SessionPlaceCounts{Sold: 5, Total: 20, Available: 15}
	got := SummaryPlaces(s)
	want := PlaceTotals{Sold: 17, Total: 70, Available: 45, Held: 8}
	if got != want {
		t.Errorf("SummaryPlaces = %+v, want %+v", got, want)
	}
	_ = uuid.Nil
}

package monthcharge_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/monthcharge"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 7, 30, 0, 0, time.UTC) }

func TestMonths_CompletedMonthsFromTheDateX(t *testing.T) {
	got := monthcharge.Months(day(2026, time.October, 17), day(2026, time.December, 1))
	if len(got) != 2 || !got[0].Equal(time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)) ||
		!got[1].Equal(time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("months %v", got)
	}
	if got := monthcharge.Months(day(2026, time.December, 1), day(2026, time.December, 20)); len(got) != 0 {
		t.Errorf("the current month is not over: %v", got)
	}
	if got := monthcharge.Months(time.Time{}, day(2026, time.December, 20)); got != nil {
		t.Errorf("no date X: %v", got)
	}
	if got := monthcharge.Months(day(2015, time.January, 1), day(2026, time.December, 20)); len(got) != monthcharge.MaxMonthsBack {
		t.Errorf("a far date X reads %d months, want %d", len(got), monthcharge.MaxMonthsBack)
	}
}

func TestWalk_AFailingMonthDoesNotStopTheNext(t *testing.T) {
	boom := errors.New("boom")
	var seen []string
	n, err := monthcharge.Walk(context.Background(), "test month", day(2026, time.October, 1), day(2027, time.January, 5),
		func(_ context.Context, m time.Time) (int, error) {
			seen = append(seen, m.Format(monthcharge.MonthLayout))
			if m.Month() == time.November {
				return 0, boom
			}
			return 2, nil
		})
	if n != 4 || !errors.Is(err, boom) || !strings.Contains(err.Error(), "test month 2026-11") {
		t.Errorf("n=%d err=%v", n, err)
	}
	if strings.Join(seen, ",") != "2026-10,2026-11,2026-12" {
		t.Errorf("walked %v", seen)
	}
}

func TestDayOf_IsTheUTCDay(t *testing.T) {
	zone := time.FixedZone("x", 5*3600)
	if got := monthcharge.DayOf(time.Date(2026, time.November, 2, 1, 0, 0, 0, zone)); !got.Equal(time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("day %s", got)
	}
}

package clock_test

import (
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/clock"
)

// ---------------------------------------------------------------------------
// realClock tests
// ---------------------------------------------------------------------------

func TestRealClock_NowIsClose(t *testing.T) {
	c := clock.New()
	before := time.Now()
	got := c.Now()
	after := time.Now()

	if got.Before(before) || got.After(after) {
		t.Errorf("realClock.Now() = %v, want in [%v, %v]", got, before, after)
	}
}

func TestRealClock_SinceIsNonNegative(t *testing.T) {
	c := clock.New()
	past := time.Now().Add(-time.Second)
	if c.Since(past) < 0 {
		t.Error("realClock.Since(past) should be non-negative")
	}
}

func TestRealClock_ImplementsInterface(_ *testing.T) {
	var _ = clock.New()
}

// ---------------------------------------------------------------------------
// FakeClock tests
// ---------------------------------------------------------------------------

func TestFakeClock_NowReturnsSetTime(t *testing.T) {
	start := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)

	if got := fc.Now(); !got.Equal(start) {
		t.Errorf("FakeClock.Now() = %v, want %v", got, start)
	}
}

func TestFakeClock_AdvanceMovesTimeForward(t *testing.T) {
	start := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)

	fc.Advance(5 * time.Minute)

	want := start.Add(5 * time.Minute)
	if got := fc.Now(); !got.Equal(want) {
		t.Errorf("after Advance(5m): FakeClock.Now() = %v, want %v", got, want)
	}
}

func TestFakeClock_AdvanceAccumulates(t *testing.T) {
	start := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)

	fc.Advance(1 * time.Hour)
	fc.Advance(30 * time.Minute)

	want := start.Add(90 * time.Minute)
	if got := fc.Now(); !got.Equal(want) {
		t.Errorf("after two Advances: FakeClock.Now() = %v, want %v", got, want)
	}
}

func TestFakeClock_AdvanceNegativeMoveBackward(t *testing.T) {
	start := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)

	fc.Advance(-10 * time.Second)

	want := start.Add(-10 * time.Second)
	if got := fc.Now(); !got.Equal(want) {
		t.Errorf("after Advance(-10s): FakeClock.Now() = %v, want %v", got, want)
	}
}

func TestFakeClock_SetTimeOverridesTime(t *testing.T) {
	fc := clock.NewFake(time.Now())
	newTime := time.Date(2030, 12, 31, 23, 59, 59, 0, time.UTC)
	fc.SetTime(newTime)

	if got := fc.Now(); !got.Equal(newTime) {
		t.Errorf("after SetTime: FakeClock.Now() = %v, want %v", got, newTime)
	}
}

func TestFakeClock_SinceReturnsDuration(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)

	past := start.Add(-5 * time.Second)
	if got := fc.Since(past); got != 5*time.Second {
		t.Errorf("FakeClock.Since(5s ago) = %v, want %v", got, 5*time.Second)
	}
}

func TestFakeClock_SinceAfterAdvance(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)

	mark := fc.Now()
	fc.Advance(2 * time.Hour)

	if got := fc.Since(mark); got != 2*time.Hour {
		t.Errorf("FakeClock.Since(mark) after Advance(2h) = %v, want %v", got, 2*time.Hour)
	}
}

func TestFakeClock_SinceZeroWhenSameTime(t *testing.T) {
	start := time.Date(2024, 3, 15, 8, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)

	if got := fc.Since(start); got != 0 {
		t.Errorf("FakeClock.Since(now) = %v, want 0", got)
	}
}

func TestFakeClock_ImplementsInterface(_ *testing.T) {
	var _ clock.Clock = clock.NewFake(time.Now())
}

func TestFakeClock_ZeroValueUsable(t *testing.T) {
	var fc clock.FakeClock

	// Zero time is valid; Advance should work from it.
	fc.Advance(time.Minute)

	want := time.Time{}.Add(time.Minute)
	if got := fc.Now(); !got.Equal(want) {
		t.Errorf("zero FakeClock after Advance(1m) = %v, want %v", got, want)
	}
}

package shop

import (
	"context"
	"errors"
	"testing"
	"time"
)

type countingScheduler struct{ calls int }

func (c *countingScheduler) ScheduleNext(context.Context, time.Time) error {
	c.calls++
	return nil
}

// SA-28: the next run is queued before the pass works, so a pass that fails
// (the worker retries it three times) never ends the self-scheduling chain.
func TestSweepHandler_SchedulesTheNextRunBeforeWorking(t *testing.T) {
	sched := &countingScheduler{}
	// A service without a database fails its pass at once.
	h := NewSweepHandler(SweepOptions{Service: New(nil, nil), Scheduler: sched})
	err := h(context.Background(), nil)
	if err == nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("pass error = %v; want the unavailable error", err)
	}
	if sched.calls != 1 {
		t.Fatalf("ScheduleNext calls after a failed pass = %d; want 1", sched.calls)
	}
}

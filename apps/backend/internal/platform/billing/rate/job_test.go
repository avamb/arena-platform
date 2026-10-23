package rate_test

import (
	"context"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/rate"
)

type countingScheduler struct{ calls int }

func (s *countingScheduler) ScheduleNext(context.Context, time.Time) error {
	s.calls++
	return nil
}

// Without a Rater (no date X) the handler does nothing and does not schedule
// itself again.
func TestHandler_WithoutARaterDoesNothing(t *testing.T) {
	sched := &countingScheduler{}
	h := rate.NewHandler(rate.HandlerOptions{Scheduler: sched})
	if err := h(context.Background(), nil); err != nil || sched.calls != 0 {
		t.Fatalf("err=%v scheduled=%d, want nothing", err, sched.calls)
	}
}

package collect_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/collect"
)

type fakeScheduler struct {
	calls int
	err   error
}

func (s *fakeScheduler) ScheduleNext(context.Context, time.Time) error {
	s.calls++
	return s.err
}

// Without a date X the handler does nothing and does not schedule itself.
func TestHandler_DisabledDoesNothing(t *testing.T) {
	sched := &fakeScheduler{}
	h := collect.NewHandler(collect.HandlerOptions{Collector: collect.New(nil, collect.Options{}), Scheduler: sched})
	if err := h(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if sched.calls != 0 {
		t.Fatalf("scheduled %d times with collection off", sched.calls)
	}
	var nilCollector = collect.NewHandler(collect.HandlerOptions{Scheduler: sched})
	if err := nilCollector(context.Background(), nil); err != nil || sched.calls != 0 {
		t.Fatalf("nil collector: err=%v calls=%d", err, sched.calls)
	}
}

// A scheduling failure fails the run before any work is done.
func TestHandler_SchedulingFailureIsReturned(t *testing.T) {
	boom := errors.New("queue down")
	h := collect.NewHandler(collect.HandlerOptions{
		Collector: collect.New(nil, collect.Options{Since: time.Now()}),
		Scheduler: &fakeScheduler{err: boom},
	})
	if err := h(context.Background(), nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the scheduler's", err)
	}
}

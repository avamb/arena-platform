package paymentrecover

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type fakeProcessor struct {
	err   error
	limit int32
}

func (f *fakeProcessor) RecoverStalledPayments(_ context.Context, limit int32) (Stats, error) {
	f.limit = limit
	return Stats{Examined: 1, Relinked: 1}, f.err
}

type recordingScheduler struct{ at []time.Time }

func (s *recordingScheduler) ScheduleNext(_ context.Context, at time.Time) error {
	s.at = append(s.at, at)
	return nil
}

func TestHandler_RunsAndSchedulesTheNextRun(t *testing.T) {
	proc := &fakeProcessor{}
	sched := &recordingScheduler{}
	h := NewHandler(Options{Processor: proc, Scheduler: sched, Interval: time.Minute,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	before := time.Now()
	if err := h(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if proc.limit != DefaultBatchSize {
		t.Fatalf("limit %d, want the default batch", proc.limit)
	}
	if len(sched.at) != 1 || sched.at[0].Sub(before) < time.Minute {
		t.Fatalf("next run %v, want one run an interval away", sched.at)
	}
}

func TestHandler_AFailedRunStillSchedulesTheNext(t *testing.T) {
	sched := &recordingScheduler{}
	h := NewHandler(Options{Processor: &fakeProcessor{err: errors.New("db down")}, Scheduler: sched,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := h(context.Background(), nil); err != nil {
		t.Fatalf("a failed run must not be retried beside the scheduled one: %v", err)
	}
	if len(sched.at) != 1 {
		t.Fatalf("scheduled %d runs, want 1", len(sched.at))
	}
}

func TestJobType(t *testing.T) {
	if JobType != "checkout.payment_recover" {
		t.Fatalf("JobType = %q", JobType)
	}
}

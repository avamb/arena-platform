package reservationexpiry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeProcessor drives a scripted sequence of ProcessExpiredReservations
// results so the handler's batch-looping and scheduling behaviour can be
// tested without a database.
type fakeProcessor struct {
	// results is consumed in order; once exhausted, 0 is returned.
	results []int
	err     error
	errOn   int // 1-indexed call number that returns err, 0 = never
	calls   []int32
}

func (f *fakeProcessor) ProcessExpiredReservations(_ context.Context, limit int32) (int, error) {
	f.calls = append(f.calls, limit)
	call := len(f.calls)
	if f.errOn != 0 && call == f.errOn {
		return 0, f.err
	}
	if call-1 < len(f.results) {
		return f.results[call-1], nil
	}
	return 0, nil
}

type recordingScheduler struct {
	at  []time.Time
	err error
}

func (s *recordingScheduler) ScheduleNext(_ context.Context, at time.Time) error {
	if s.err != nil {
		return s.err
	}
	s.at = append(s.at, at)
	return nil
}

func TestHandler_SelfSchedulesTheNextRun(t *testing.T) {
	proc := &fakeProcessor{results: []int{3}}
	sched := &recordingScheduler{}
	h := NewHandler(Options{
		Processor: proc,
		Logger:    quietLogger(),
		Interval:  time.Minute,
		Scheduler: sched,
	})

	before := time.Now()
	if err := h(context.Background(), nil); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(sched.at) != 1 {
		t.Fatalf("scheduled %d follow-ups, want 1", len(sched.at))
	}
	if gap := sched.at[0].Sub(before); gap < time.Minute {
		t.Fatalf("next run in %v, want at least the interval away", gap)
	}
	if len(proc.calls) != 1 {
		t.Fatalf("processor called %d times, want 1 (batch was short)", len(proc.calls))
	}
}

// If the follow-up cannot be enqueued the handler must fail, so the worker
// retries and the cadence is not lost.
func TestHandler_FailsWhenReschedulingFails(t *testing.T) {
	proc := &fakeProcessor{results: []int{1}}
	boom := errors.New("worker_jobs unavailable")
	h := NewHandler(Options{
		Processor: proc,
		Logger:    quietLogger(),
		Scheduler: &recordingScheduler{err: boom},
	})

	if err := h(context.Background(), nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the scheduling failure", err)
	}
}

func TestHandler_RunsWithoutAScheduler(t *testing.T) {
	proc := &fakeProcessor{results: []int{1}}
	h := NewHandler(Options{Processor: proc, Logger: quietLogger()})

	if err := h(context.Background(), nil); err != nil {
		t.Fatalf("handler: %v", err)
	}
}

// A run that fills the batch every time loops immediately to catch up on a
// backlog instead of waiting a full Interval between each batch.
func TestHandler_LoopsWhileBatchesAreFull(t *testing.T) {
	proc := &fakeProcessor{
		results: []int{5, 5, 5, 2}, // three full batches of 5, then a short one
	}
	h := NewHandler(Options{
		Processor: proc,
		Logger:    quietLogger(),
		BatchSize: 5,
	})

	if err := h(context.Background(), nil); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(proc.calls) != 4 {
		t.Fatalf("processor called %d times, want 4 (3 full + 1 short)", len(proc.calls))
	}
	for _, l := range proc.calls {
		if l != 5 {
			t.Fatalf("batch size passed = %d, want 5", l)
		}
	}
}

// The loop must not run unbounded even if every single batch comes back
// full — maxBatchesPerRun caps one invocation so the rest of the backlog
// waits for the next scheduled run.
func TestHandler_CapsBatchesPerRun(t *testing.T) {
	full := make([]int, maxBatchesPerRun+5)
	for i := range full {
		full[i] = 5
	}
	proc := &fakeProcessor{results: full}
	sched := &recordingScheduler{}
	h := NewHandler(Options{
		Processor: proc,
		Logger:    quietLogger(),
		BatchSize: 5,
		Scheduler: sched,
	})

	if err := h(context.Background(), nil); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(proc.calls) != maxBatchesPerRun {
		t.Fatalf("processor called %d times, want the cap of %d", len(proc.calls), maxBatchesPerRun)
	}
	// Even though the backlog was not drained, the run must still
	// self-schedule the next tick — otherwise the reaper stalls forever.
	if len(sched.at) != 1 {
		t.Fatalf("scheduled %d follow-ups, want 1", len(sched.at))
	}
}

// A genuine processing failure must fail the job so the worker retries it.
func TestHandler_PropagatesProcessorFailures(t *testing.T) {
	boom := errors.New("deadlock detected")
	proc := &fakeProcessor{err: boom, errOn: 1}
	h := NewHandler(Options{Processor: proc, Logger: quietLogger()})

	if err := h(context.Background(), nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the underlying failure", err)
	}
}

func TestHandler_DefaultsAreApplied(t *testing.T) {
	proc := &fakeProcessor{results: []int{0}}
	h := NewHandler(Options{Processor: proc})
	if err := h(context.Background(), nil); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(proc.calls) != 1 || proc.calls[0] != DefaultBatchSize {
		t.Fatalf("calls = %+v, want one call with DefaultBatchSize=%d", proc.calls, DefaultBatchSize)
	}
}

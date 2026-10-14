package jobretention

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type call struct {
	status string
	cutoff time.Time
	limit  int
}

type fakeStore struct {
	results map[string][]int64 // per status, consumed in order, then 0
	err     error
	calls   []call
}

func (f *fakeStore) DeleteBatch(_ context.Context, status string, cutoff time.Time, limit int) (int64, error) {
	f.calls = append(f.calls, call{status, cutoff, limit})
	if f.err != nil {
		return 0, f.err
	}
	r := f.results[status]
	if len(r) == 0 {
		return 0, nil
	}
	f.results[status] = r[1:]
	return r[0], nil
}

type fakeScheduler struct{ at []time.Time }

func (s *fakeScheduler) ScheduleNext(_ context.Context, at time.Time) error {
	s.at = append(s.at, at)
	return nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestHandler_CutoffsPerStatusAndSchedulesNext(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{results: map[string][]int64{}}
	sched := &fakeScheduler{}
	h := NewHandler(Options{Store: store, Scheduler: sched, Logger: quiet(), Now: func() time.Time { return now }})
	if err := h(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(store.calls) != 2 {
		t.Fatalf("calls = %+v, want one per status", store.calls)
	}
	if c := store.calls[0]; c.status != StatusDone || !c.cutoff.Equal(now.Add(-30*24*time.Hour)) || c.limit != DefaultBatchSize {
		t.Fatalf("done call = %+v", c)
	}
	if c := store.calls[1]; c.status != StatusFailed || !c.cutoff.Equal(now.Add(-90*24*time.Hour)) {
		t.Fatalf("failed call = %+v", c)
	}
	if len(sched.at) != 1 {
		t.Fatalf("scheduled %d next runs, want 1", len(sched.at))
	}
}

func TestHandler_LoopsWhileBatchesAreFullAndStopsAtTheCap(t *testing.T) {
	full := make([]int64, 30)
	for i := range full {
		full[i] = 10
	}
	store := &fakeStore{results: map[string][]int64{StatusDone: {10, 10, 3}, StatusFailed: full}}
	h := NewHandler(Options{Store: store, Logger: quiet(), BatchSize: 10})
	if err := h(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	var done, failed int
	for _, c := range store.calls {
		switch c.status {
		case StatusDone:
			done++
		case StatusFailed:
			failed++
		}
	}
	if done != 3 {
		t.Fatalf("done batches = %d, want 3 (stop on the short one)", done)
	}
	if failed != maxBatchesPerRun {
		t.Fatalf("failed batches = %d, want the cap %d", failed, maxBatchesPerRun)
	}
}

func TestHandler_StoreErrorFailsTheRunWithoutScheduling(t *testing.T) {
	store := &fakeStore{results: map[string][]int64{}, err: errors.New("boom")}
	sched := &fakeScheduler{}
	h := NewHandler(Options{Store: store, Scheduler: sched, Logger: quiet()})
	if err := h(context.Background(), nil); err == nil {
		t.Fatal("want an error")
	}
	if len(sched.at) != 0 {
		t.Fatal("a failed run must not schedule the next one; the worker retries it")
	}
}

func TestPGStore_RefusesNonTerminalStatus(t *testing.T) {
	s := NewPGStore(nil)
	for _, st := range []string{"pending", "claimed", ""} {
		if _, err := s.DeleteBatch(context.Background(), st, time.Now(), 10); err == nil {
			t.Fatalf("status %q accepted", st)
		}
	}
}

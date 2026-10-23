package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeScheduler struct {
	calls int
	err   error
	at    time.Time
}

func (s *fakeScheduler) ScheduleNext(_ context.Context, at time.Time) error {
	s.calls++
	s.at = at
	return s.err
}

// Without a Reconciler (the date X is not set) the handler does nothing and
// does not start a chain.
func TestHandler_InertWithoutReconciler(t *testing.T) {
	s := &fakeScheduler{}
	h := NewHandler(HandlerOptions{Scheduler: s})
	if err := h(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if s.calls != 0 {
		t.Errorf("an inert handler scheduled %d times", s.calls)
	}
}

// The next run is scheduled before the work, a day ahead, so a run that fails
// does not end the daily chain; a scheduling failure fails the handler.
func TestHandler_SchedulesADayAheadBeforeTheWork(t *testing.T) {
	s := &fakeScheduler{}
	// A Reconciler with no date X fails its run with ErrNoSince - after the
	// next run has been scheduled.
	h := NewHandler(HandlerOptions{Reconciler: New(nil, Options{}), Scheduler: s})
	before := time.Now()
	if err := h(context.Background(), nil); !errors.Is(err, ErrNoSince) {
		t.Fatalf("err = %v, want ErrNoSince", err)
	}
	if s.calls != 1 {
		t.Fatalf("scheduled %d times, want 1", s.calls)
	}
	if d := s.at.Sub(before); d < 23*time.Hour || d > 25*time.Hour {
		t.Errorf("next run in %s, want about a day", d)
	}
	s2 := &fakeScheduler{err: errors.New("db down")}
	h = NewHandler(HandlerOptions{Reconciler: New(nil, Options{}), Scheduler: s2})
	if err := h(context.Background(), nil); err == nil || errors.Is(err, ErrNoSince) {
		t.Errorf("a scheduling failure must fail the handler before the work, got %v", err)
	}
}

// A finding becomes an alert input without personal data and with a stable
// fingerprint; lags are warnings, gaps are high.
func TestAlertInput_Shape(t *testing.T) {
	in := alertInput(Finding{Check: CheckOrderItemsWithoutEvent, Count: 2, Note: "n"}, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if in.Fingerprint != "billing_reconcile:order_items_without_event" || in.Severity != "high" {
		t.Errorf("gap alert = %+v", in)
	}
	lag := alertInput(Finding{Check: CheckCollectLag, Count: 1, LagSeconds: 7200}, time.Now())
	if lag.Severity != "warn" || lag.Details["lag_minutes"] != int64(120) {
		t.Errorf("lag alert = %+v", lag)
	}
}

package accel_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/accel"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

func ev(t string) outbox.Event { return outbox.Event{EventType: t, AggregateID: "no-such-order"} }

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The leg always answers nil: for the events it wants, the ones it ignores, an
// unknown order and a nil leg.
func TestDispatch_AlwaysNil(t *testing.T) {
	a := accel.New(func(context.Context) error { return errors.New("boom") }, time.Millisecond, nil)
	for _, e := range []string{accel.EventOrderPaid, accel.EventTicketRefunded, accel.EventTicketCancelled,
		accel.EventTicketRevoked, accel.EventSessionCancel, accel.EventEventPublished, "v1.something.else", ""} {
		if err := a.Dispatch(context.Background(), ev(e)); err != nil {
			t.Errorf("Dispatch(%q) = %v, want nil", e, err)
		}
	}
	var nilLeg *accel.Accelerator
	if err := nilLeg.Dispatch(context.Background(), ev(accel.EventOrderPaid)); err != nil {
		t.Errorf("nil leg = %v", err)
	}
}

// A relevant event triggers a pass; an irrelevant one does not; a burst is
// coalesced; a failing or panicking pass changes nothing for the caller and
// the leg keeps working.
func TestRun_KickedByRelevantEventsOnly(t *testing.T) {
	var passes atomic.Int32
	failNext := atomic.Bool{}
	panicNext := atomic.Bool{}
	a := accel.New(func(context.Context) error {
		passes.Add(1)
		if panicNext.Swap(false) {
			panic("bad pass")
		}
		if failNext.Swap(false) {
			return errors.New("database down")
		}
		return nil
	}, time.Millisecond, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)

	_ = a.Dispatch(ctx, ev("v1.ticket.scanned"))
	time.Sleep(50 * time.Millisecond)
	if passes.Load() != 0 {
		t.Fatalf("an unrelated event started %d passes", passes.Load())
	}
	_ = a.Dispatch(ctx, ev(accel.EventOrderPaid))
	waitFor(t, func() bool { return passes.Load() == 1 })

	failNext.Store(true)
	if err := a.Dispatch(ctx, ev(accel.EventSessionCancel)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return passes.Load() == 2 })
	panicNext.Store(true)
	_ = a.Dispatch(ctx, ev(accel.EventEventPublished))
	waitFor(t, func() bool { return passes.Load() == 3 })
	_ = a.Dispatch(ctx, ev(accel.EventOrderPaid))
	waitFor(t, func() bool { return passes.Load() == 4 })
}

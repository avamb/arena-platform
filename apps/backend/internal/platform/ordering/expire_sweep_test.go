package ordering

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// sweepStoreWith seeds n expirable candidates, all pending_payment.
func sweepStoreWith(n int) (*fakeStore, []uuid.UUID) {
	f := newFakeStore()
	ids := make([]uuid.UUID, 0, n)
	expiresAt := time.Date(2026, 9, 5, 9, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		row := gen.OrderRow{ID: uuid.New(), Status: StatusPendingPayment, ExpiresAt: &expiresAt}
		f.orders[row.ID] = row
		f.expirable = append(f.expirable, row)
		ids = append(ids, row.ID)
	}
	return f, ids
}

func TestRunExpireSweep_ExpiresEveryCandidateAndAudits(t *testing.T) {
	f, ids := sweepStoreWith(3)

	n, err := RunExpireSweep(context.Background(), f, time.Now(), 100, quietLogger())
	if err != nil {
		t.Fatalf("RunExpireSweep: %v", err)
	}
	if n != 3 {
		t.Fatalf("expired = %d, want 3", n)
	}
	for _, id := range ids {
		if f.orders[id].Status != StatusExpired {
			t.Fatalf("order %s = %s, want expired", id, f.orders[id].Status)
		}
	}
	if len(f.events) != 3 {
		t.Fatalf("got %d events, want one per expired order", len(f.events))
	}
	if f.events[0].Type != EventHoldExpired || f.events[0].Actor != ActorSystem {
		t.Fatalf("event = %+v, want a system hold_expired", f.events[0])
	}
	// The payload names the job so an operator reading order_events can tell a
	// swept order from one an admin expired by hand.
	if !strings.Contains(string(f.events[0].Payload), ExpireSweepJobType) {
		t.Fatalf("payload = %s, want the job name", f.events[0].Payload)
	}
}

// A webhook that lands between the candidate query and the guarded UPDATE wins:
// the guard matches zero rows and the sweep skips that order instead of
// expiring an order the customer just paid for.
func TestRunExpireSweep_SkipsOrdersThatWonTheRace(t *testing.T) {
	f, ids := sweepStoreWith(3)
	f.expireErrOn[ids[1]] = pgx.ErrNoRows

	n, err := RunExpireSweep(context.Background(), f, time.Now(), 100, quietLogger())
	if err != nil {
		t.Fatalf("RunExpireSweep: %v", err)
	}
	if n != 2 {
		t.Fatalf("expired = %d, want 2 (one lost the race)", n)
	}
	if f.orders[ids[1]].Status != StatusPendingPayment {
		t.Fatalf("order %s = %s, want left alone", ids[1], f.orders[ids[1]].Status)
	}
	if len(f.events) != 2 {
		t.Fatalf("got %d events, want no audit row for the skipped order", len(f.events))
	}
}

// A genuine database failure must fail the job so the worker retries it,
// rather than being silently swallowed like the lost-race case.
func TestRunExpireSweep_PropagatesRealFailures(t *testing.T) {
	f, ids := sweepStoreWith(2)
	boom := errors.New("deadlock detected")
	f.expireErrOn[ids[0]] = boom

	_, err := RunExpireSweep(context.Background(), f, time.Now(), 100, quietLogger())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the underlying failure", err)
	}
}

func TestRunExpireSweep_HonoursBatchSize(t *testing.T) {
	f, _ := sweepStoreWith(5)

	n, err := RunExpireSweep(context.Background(), f, time.Now(), 2, quietLogger())
	if err != nil {
		t.Fatalf("RunExpireSweep: %v", err)
	}
	if n != 2 {
		t.Fatalf("expired = %d, want the batch cap of 2", n)
	}
}

func TestRunExpireSweep_NothingToDoIsNotAnError(t *testing.T) {
	f := newFakeStore()
	n, err := RunExpireSweep(context.Background(), f, time.Now(), 100, quietLogger())
	if err != nil || n != 0 {
		t.Fatalf("RunExpireSweep on an empty queue = (%d, %v), want (0, nil)", n, err)
	}
}

// recordingScheduler captures the self-scheduling of the next tick.
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

// Each run enqueues the next one — that is the whole cadence mechanism, so a
// run that expires orders but forgets to reschedule silently stops the reaper.
func TestExpireSweepHandler_SelfSchedulesTheNextRun(t *testing.T) {
	f, _ := sweepStoreWith(1)
	sched := &recordingScheduler{}
	h := NewExpireSweepHandler(ExpireSweepOptions{
		Store:     f,
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
}

// If the follow-up cannot be enqueued the handler must fail, so the worker
// retries and the cadence is not lost.
func TestExpireSweepHandler_FailsWhenReschedulingFails(t *testing.T) {
	f, _ := sweepStoreWith(1)
	boom := errors.New("worker_jobs unavailable")
	h := NewExpireSweepHandler(ExpireSweepOptions{
		Store:     f,
		Logger:    quietLogger(),
		Scheduler: &recordingScheduler{err: boom},
	})

	if err := h(context.Background(), nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the scheduling failure", err)
	}
}

func TestExpireSweepHandler_RunsWithoutAScheduler(t *testing.T) {
	f, ids := sweepStoreWith(1)
	h := NewExpireSweepHandler(ExpireSweepOptions{Store: f, Logger: quietLogger()})

	if err := h(context.Background(), nil); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if f.orders[ids[0]].Status != StatusExpired {
		t.Fatalf("order = %s, want expired", f.orders[ids[0]].Status)
	}
}

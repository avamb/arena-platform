package holdclose

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// ── fakes ───────────────────────────────────────────────────────────────────

type fakeStore struct {
	mu        sync.Mutex
	cands     []gen.HoldCloseCandidate
	intents   map[uuid.UUID][]gen.OpenPageIntent
	claimOK   bool
	claimErr  error
	listErr   error
	marks     map[uuid.UUID]time.Time
	cleared   map[uuid.UUID]int
	released  map[uuid.UUID][]uuid.UUID
	claims    int
	releaseOK bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		intents: map[uuid.UUID][]gen.OpenPageIntent{}, claimOK: true, releaseOK: true,
		marks: map[uuid.UUID]time.Time{}, cleared: map[uuid.UUID]int{}, released: map[uuid.UUID][]uuid.UUID{},
	}
}

func (s *fakeStore) add(id uuid.UUID, expiresAt time.Time, sessionIDs ...string) {
	s.cands = append(s.cands, gen.HoldCloseCandidate{ReservationID: id, ExpiresAt: expiresAt})
	for _, sid := range sessionIDs {
		s.intents[id] = append(s.intents[id], gen.OpenPageIntent{
			PaymentIntentID: uuid.New(), Provider: "stripe", ProviderSessionID: sid,
			CheckoutSessionID: uuid.New(), OrgID: uuid.New(), ChannelID: uuid.New(),
		})
	}
}

func (s *fakeStore) ListCandidates(_ context.Context, _ []string, _ time.Time, _ int32) ([]gen.HoldCloseCandidate, error) {
	return s.cands, s.listErr
}

func (s *fakeStore) Claim(_ context.Context, id uuid.UUID, _ time.Time) (time.Time, time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims++
	if s.claimErr != nil {
		return time.Time{}, time.Time{}, false, s.claimErr
	}
	if !s.claimOK {
		return time.Time{}, time.Time{}, false, nil
	}
	m := time.Now()
	s.marks[id] = m
	var exp time.Time
	for _, c := range s.cands {
		if c.ReservationID == id {
			exp = c.ExpiresAt
		}
	}
	return m, exp, true, nil
}

func (s *fakeStore) ClearMark(_ context.Context, id uuid.UUID, mark time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.marks[id].Equal(mark) {
		delete(s.marks, id)
		s.cleared[id]++
	}
	return nil
}

func (s *fakeStore) OpenIntents(_ context.Context, id uuid.UUID, _ []string) ([]gen.OpenPageIntent, error) {
	return s.intents[id], nil
}

func (s *fakeStore) Release(_ context.Context, id uuid.UUID, _ time.Time, ids []uuid.UUID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.releaseOK {
		return false, nil
	}
	s.released[id] = ids
	return true, nil
}

// fakeStripe answers per session id with a script of reads and one expire.
type fakeStripe struct {
	mu        sync.Mutex
	reads     map[string][]readAnswer
	expire    map[string]error
	expired   map[string]int
	readCalls map[string]int
	inFlight  int32
	maxFlight int32
	delay     time.Duration
}

type readAnswer struct {
	sess payments.CheckoutSessionPayment
	err  error
}

func newFakeStripe() *fakeStripe {
	return &fakeStripe{
		reads: map[string][]readAnswer{}, expire: map[string]error{},
		expired: map[string]int{}, readCalls: map[string]int{},
	}
}

func (f *fakeStripe) script(sid string, answers ...readAnswer) { f.reads[sid] = answers }

func (f *fakeStripe) enter() func() {
	n := atomic.AddInt32(&f.inFlight, 1)
	for {
		m := atomic.LoadInt32(&f.maxFlight)
		if n <= m || atomic.CompareAndSwapInt32(&f.maxFlight, m, n) {
			break
		}
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return func() { atomic.AddInt32(&f.inFlight, -1) }
}

func (f *fakeStripe) ReadCheckoutSessionPayment(_ context.Context, _, _ uuid.UUID, _, sid string) (payments.CheckoutSessionPayment, error) {
	defer f.enter()()
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.readCalls[sid]
	f.readCalls[sid]++
	answers := f.reads[sid]
	if len(answers) == 0 {
		return payments.CheckoutSessionPayment{ID: sid, Status: payments.EmbeddedSessionOpen}, nil
	}
	if i >= len(answers) {
		i = len(answers) - 1
	}
	return answers[i].sess, answers[i].err
}

func (f *fakeStripe) ExpireEmbeddedCheckout(_ context.Context, _, _ uuid.UUID, _, sid string) error {
	defer f.enter()()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expired[sid]++
	return f.expire[sid]
}

func open(sid string) readAnswer {
	return readAnswer{sess: payments.CheckoutSessionPayment{ID: sid, Status: payments.EmbeddedSessionOpen}}
}

func withStatus(sid, status, paymentStatus string) readAnswer {
	return readAnswer{sess: payments.CheckoutSessionPayment{ID: sid, Status: status, PaymentStatus: paymentStatus}}
}

var errNetwork = errors.New("dial tcp: connection reset")

func notExpirable() error {
	return fmt.Errorf("stripe: ExpireEmbeddedCheckoutSession: %w: refused", payments.ErrCheckoutSessionNotExpirable)
}

func newTestProcessor(st Store, cl PageCloser, m *Metrics) *Processor {
	return NewProcessor(ProcessorOptions{
		Store: st, Closer: cl, Providers: []string{"stripe"}, Metrics: m,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func outcome(m *Metrics, o string) float64 { return testutil.ToFloat64(m.Expire.WithLabelValues(o)) }

// ── §5.2 branches ───────────────────────────────────────────────────────────

// Steps 2, 3, 5: an open page with no payment is closed and the hold released.
func TestRun_OpenPage_ClosedAndReleased(t *testing.T) {
	st, sp, m := newFakeStore(), newFakeStripe(), NewMetrics()
	id := uuid.New()
	st.add(id, time.Now().Add(-30*time.Second), "cs_open")
	got, err := newTestProcessor(st, sp, m).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Released != 1 || len(st.released[id]) != 1 {
		t.Fatalf("released = %+v / %v, want the hold released with its one attempt", got, st.released)
	}
	if sp.expired["cs_open"] != 1 {
		t.Fatalf("expire calls = %d, want 1", sp.expired["cs_open"])
	}
	if outcome(m, OutcomeClosed) != 1 {
		t.Fatal("closed outcome not counted")
	}
	var lag dto.Metric
	if err := m.Lag.Write(&lag); err != nil || lag.GetHistogram().GetSampleCount() != 1 {
		t.Fatal("lag not observed")
	}
}

// Step 2: a payment being confirmed (requires_action, processing) is not
// closed; the mark is cleared and the hold waits a minute.
func TestRun_PaymentInProgress_NotClosed(t *testing.T) {
	for _, ps := range []string{payments.SessionPaymentRequiresAction, payments.SessionPaymentProcessing} {
		t.Run(ps, func(t *testing.T) {
			st, sp, m := newFakeStore(), newFakeStripe(), NewMetrics()
			id := uuid.New()
			st.add(id, time.Now().Add(-time.Minute), "cs_3ds")
			sp.script("cs_3ds", withStatus("cs_3ds", payments.EmbeddedSessionOpen, ps))
			p := newTestProcessor(st, sp, m)
			got, _ := p.Run(context.Background())
			if got.InProgress != 1 || got.Released != 0 || sp.expired["cs_3ds"] != 0 {
				t.Fatalf("stats %+v, expire calls %d: want in progress, no expire", got, sp.expired["cs_3ds"])
			}
			if st.cleared[id] != 1 {
				t.Fatal("the closing mark was not cleared")
			}
			// The next pass (15 s later) leaves it alone: it waits a minute.
			st.claims = 0
			got, _ = p.Run(context.Background())
			if got.Skipped != 1 || st.claims != 0 {
				t.Fatalf("second pass %+v claims %d: want skipped without a claim", got, st.claims)
			}
			// After the minute it is tried again.
			p.opts.Now = func() time.Time { return time.Now().Add(InProgressRetry + time.Second) }
			if _, err := p.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if st.claims != 1 {
				t.Fatalf("claims after the wait = %d, want 1", st.claims)
			}
		})
	}
}

// Step 2: a session already expired is released without an expire call.
func TestRun_AlreadyExpired_Released(t *testing.T) {
	st, sp, m := newFakeStore(), newFakeStripe(), NewMetrics()
	id := uuid.New()
	st.add(id, time.Now().Add(-time.Minute), "cs_gone")
	sp.script("cs_gone", withStatus("cs_gone", payments.EmbeddedSessionExpired, ""))
	got, _ := newTestProcessor(st, sp, m).Run(context.Background())
	if got.Released != 1 || sp.expired["cs_gone"] != 0 {
		t.Fatalf("stats %+v expire calls %d", got, sp.expired["cs_gone"])
	}
	if outcome(m, OutcomeAlreadyExpired) != 1 {
		t.Fatal("already_expired not counted")
	}
}

// Step 2: a complete session is left to the webhook, seats kept.
func TestRun_Complete_SeatsKept(t *testing.T) {
	st, sp, m := newFakeStore(), newFakeStripe(), NewMetrics()
	id := uuid.New()
	st.add(id, time.Now().Add(-time.Minute), "cs_paid")
	sp.script("cs_paid", withStatus("cs_paid", payments.EmbeddedSessionComplete, payments.SessionPaymentSucceeded))
	got, _ := newTestProcessor(st, sp, m).Run(context.Background())
	if got.Complete != 1 || got.Released != 0 || len(st.released) != 0 || sp.expired["cs_paid"] != 0 {
		t.Fatalf("stats %+v released %v", got, st.released)
	}
	if st.cleared[id] != 1 {
		t.Fatal("mark not cleared")
	}
}

// Step 4: the refusal is decided by a second read — expired releases,
// complete keeps the seats, open retries next pass.
func TestRun_Refused_DecidedByRead(t *testing.T) {
	cases := []struct {
		name         string
		second       readAnswer
		wantReleased int
		wantComplete int
		wantRetry    int
		wantOutcome  string
	}{
		{"expired", withStatus("cs_r", payments.EmbeddedSessionExpired, ""), 1, 0, 0, OutcomeAlreadyExpired},
		{"complete", withStatus("cs_r", payments.EmbeddedSessionComplete, payments.SessionPaymentSucceeded), 0, 1, 0, OutcomeComplete},
		{"open", open("cs_r"), 0, 0, 1, OutcomeOpen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, sp, m := newFakeStore(), newFakeStripe(), NewMetrics()
			id := uuid.New()
			st.add(id, time.Now().Add(-time.Minute), "cs_r")
			sp.script("cs_r", open("cs_r"), tc.second)
			sp.expire["cs_r"] = notExpirable()
			got, _ := newTestProcessor(st, sp, m).Run(context.Background())
			if got.Released != tc.wantReleased || got.Complete != tc.wantComplete || got.Retry != tc.wantRetry {
				t.Fatalf("stats %+v", got)
			}
			if sp.readCalls["cs_r"] != 2 {
				t.Fatalf("reads = %d, want 2", sp.readCalls["cs_r"])
			}
			if outcome(m, tc.wantOutcome) != 1 {
				t.Fatalf("outcome %s not counted", tc.wantOutcome)
			}
			if tc.wantReleased == 0 && st.cleared[id] != 1 {
				t.Fatal("mark not cleared")
			}
		})
	}
}

// Step 6: the provider does not answer (read or expire): mark cleared, no
// release, retried on the next pass.
func TestRun_NetworkError_Retried(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		st, sp, m := newFakeStore(), newFakeStripe(), NewMetrics()
		id := uuid.New()
		st.add(id, time.Now().Add(-time.Minute), "cs_n")
		sp.script("cs_n", readAnswer{err: errNetwork})
		p := newTestProcessor(st, sp, m)
		got, _ := p.Run(context.Background())
		if got.Retry != 1 || len(st.released) != 0 || st.cleared[id] != 1 || sp.expired["cs_n"] != 0 {
			t.Fatalf("stats %+v cleared %d", got, st.cleared[id])
		}
		// Next pass: no backoff, the provider answers, the hold is released.
		sp.script("cs_n", open("cs_n"))
		sp.readCalls["cs_n"] = 0
		got, _ = p.Run(context.Background())
		if got.Released != 1 {
			t.Fatalf("second pass %+v, want released", got)
		}
	})
	t.Run("expire", func(t *testing.T) {
		st, sp, m := newFakeStore(), newFakeStripe(), NewMetrics()
		id := uuid.New()
		st.add(id, time.Now().Add(-time.Minute), "cs_n")
		sp.expire["cs_n"] = errNetwork
		got, _ := newTestProcessor(st, sp, m).Run(context.Background())
		if got.Retry != 1 || len(st.released) != 0 || st.cleared[id] != 1 {
			t.Fatalf("stats %+v", got)
		}
		if outcome(m, OutcomeError) != 1 {
			t.Fatal("error not counted")
		}
	})
}

// Step 1: a hold that cannot be claimed (extended, paying, locked) is skipped
// without any provider call.
func TestRun_ClaimRefused_Skipped(t *testing.T) {
	st, sp, m := newFakeStore(), newFakeStripe(), NewMetrics()
	st.claimOK = false
	st.add(uuid.New(), time.Now().Add(-time.Minute), "cs_x")
	got, _ := newTestProcessor(st, sp, m).Run(context.Background())
	if got.Skipped != 1 || sp.readCalls["cs_x"] != 0 {
		t.Fatalf("stats %+v reads %d", got, sp.readCalls["cs_x"])
	}
}

// Two pages: the hold is released only when both are closed.
func TestRun_TwoPages_OneInProgress_NotReleased(t *testing.T) {
	st, sp, m := newFakeStore(), newFakeStripe(), NewMetrics()
	id := uuid.New()
	st.add(id, time.Now().Add(-time.Minute), "cs_a", "cs_b")
	sp.script("cs_b", withStatus("cs_b", payments.EmbeddedSessionOpen, payments.SessionPaymentRequiresAction))
	got, _ := newTestProcessor(st, sp, m).Run(context.Background())
	if got.Released != 0 || got.InProgress != 1 || len(st.released) != 0 {
		t.Fatalf("stats %+v", got)
	}
}

// At most MaxConcurrentCalls provider calls at once.
func TestRun_ConcurrencyBounded(t *testing.T) {
	st, sp, m := newFakeStore(), newFakeStripe(), NewMetrics()
	sp.delay = 20 * time.Millisecond
	for i := 0; i < 40; i++ {
		st.add(uuid.New(), time.Now().Add(-time.Minute), fmt.Sprintf("cs_%d", i))
	}
	got, err := newTestProcessor(st, sp, m).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Released != 40 {
		t.Fatalf("released %d, want 40", got.Released)
	}
	if mx := atomic.LoadInt32(&sp.maxFlight); mx > MaxConcurrentCalls || mx < 2 {
		t.Fatalf("max concurrent provider calls = %d, want 2..%d", mx, MaxConcurrentCalls)
	}
}

// The handler schedules its next run even after a failed pass.
func TestHandler_SchedulesNext(t *testing.T) {
	st := newFakeStore()
	st.listErr = errors.New("db down")
	sched := &fakeScheduler{}
	h := NewHandler(HandlerOptions{
		Processor: newTestProcessor(st, newFakeStripe(), nil),
		Scheduler: sched,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := h(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(sched.at) != 1 || time.Until(sched.at[0]) > DefaultInterval+time.Second {
		t.Fatalf("scheduled %v, want one run about %s later", sched.at, DefaultInterval)
	}
}

type fakeScheduler struct{ at []time.Time }

func (s *fakeScheduler) ScheduleNext(_ context.Context, at time.Time) error {
	s.at = append(s.at, at)
	return nil
}

// ── purge ───────────────────────────────────────────────────────────────────

type fakePurger struct {
	left   int64
	before []time.Time
}

func (p *fakePurger) PurgeVisitorHashes(_ context.Context, before time.Time, limit int32) (int64, error) {
	p.before = append(p.before, before)
	n := min(p.left, int64(limit))
	p.left -= n
	return n, nil
}

func TestPurge_24HoursAndBatches(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	p := &fakePurger{left: 2500}
	sched := &fakeScheduler{}
	h := NewPurgeHandler(PurgeOptions{Purger: p, Scheduler: sched, Now: func() time.Time { return now },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := h(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(p.before) != 3 || !p.before[0].Equal(now.Add(-24*time.Hour)) || p.left != 0 {
		t.Fatalf("calls %v left %d", p.before, p.left)
	}
	if len(sched.at) != 1 || time.Until(sched.at[0]) < 59*time.Minute {
		t.Fatalf("next purge %v, want an hour later", sched.at)
	}
}

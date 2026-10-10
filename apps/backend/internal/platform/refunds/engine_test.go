// engine_test.go — pure unit tests of the refund engine's decisions. The
// database-backed behaviour (locks, claims, outcomes, the sweep) is in
// engine_integration_test.go.
package refunds

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

type stubModules map[string]payments.Descriptor

func (s stubModules) Descriptor(p string) (payments.Descriptor, bool) {
	d, ok := s[p]
	if !ok {
		return payments.Descriptor{}, false
	}
	if d.Name == "declared" {
		return d, false
	}
	return d, true
}

func (stubModules) Build(context.Context, uuid.UUID, string) (payments.Module, error) {
	return nil, errors.New("not used")
}

func TestRouteFor(t *testing.T) {
	m := stubModules{
		"refundable": {Name: "refundable", Capabilities: payments.Capabilities{Refund: true}},
		"norefund":   {Name: "norefund", Capabilities: payments.Capabilities{HostedCheckout: true}},
		"declared":   {Name: "declared"},
	}
	cases := []struct {
		provider, source string
		want             Route
	}{
		{"refundable", "public_feed", RouteArenaProvider},
		{" Refundable ", "", RouteArenaProvider},
		{"refundable", "bil24_gateway", RouteSellerSite},
		{"manual", "", RouteSellerSite},
		{"norefund", "public_feed", RouteUnsupported},
		{"declared", "", RouteUnsupported},
		{"mock", "", RouteUnknownProvider},
	}
	for _, c := range cases {
		if got := RouteFor(m, c.provider, c.source); got != c.want {
			t.Errorf("RouteFor(%q, %q) = %s; want %s", c.provider, c.source, got, c.want)
		}
	}
}

func TestRouteRefusal_CodesAndStatuses(t *testing.T) {
	if e := RouteRefusal(RouteSellerSite, "manual"); e == nil || e.Status != http.StatusConflict || e.Code != CodeSellerSiteOrder {
		t.Errorf("seller site: %+v", e)
	}
	if e := RouteRefusal(RouteUnsupported, "x"); e == nil || e.Status != http.StatusUnprocessableEntity || e.Code != CodeProviderNotSupported {
		t.Errorf("unsupported: %+v", e)
	}
	if e := RouteRefusal(RouteArenaProvider, "x"); e != nil {
		t.Errorf("arena provider refused: %+v", e)
	}
	// PAY-03 review: a provider the registry does not know is refused like
	// an unsupported one — no route pretends to refund it.
	if e := RouteRefusal(RouteUnknownProvider, "x"); e == nil || e.Status != http.StatusUnprocessableEntity || e.Code != CodeProviderNotSupported {
		t.Errorf("unknown provider: %+v", e)
	}
}

// TestReplayMatches_RefusesADifferentRequest: an Idempotency-Key replay
// answers the first batch only when the request is the same one.
func TestReplayMatches_RefusesADifferentRequest(t *testing.T) {
	order, t1, t2 := uuid.New(), uuid.New(), uuid.New()
	amt := func(v int64) *int64 { return &v }
	stored := storedBatch{
		BatchResult:   BatchResult{OrderID: order, Refunds: []Refund{{TicketID: &t1, Amount: 500}, {TicketID: &t2, Amount: 700}}},
		Reason:        "sick",
		CancelTickets: true,
	}
	base := BatchInput{OrderID: order, Reason: "sick", CancelTickets: true,
		Items: []Item{{TicketID: t1}, {TicketID: t2, Amount: amt(700)}}}
	if err := replayMatches(stored, base); err != nil {
		t.Fatalf("same request refused: %v", err)
	}
	cases := map[string]func(in *BatchInput){
		"order":   func(in *BatchInput) { in.OrderID = uuid.New() },
		"reason":  func(in *BatchInput) { in.Reason = "other" },
		"flags":   func(in *BatchInput) { in.CancelTickets = false },
		"notify":  func(in *BatchInput) { in.NotifyBuyer = true },
		"tickets": func(in *BatchInput) { in.Items = in.Items[:1] },
		"swap":    func(in *BatchInput) { in.Items = []Item{{TicketID: t1}, {TicketID: uuid.New()}} },
		"amounts": func(in *BatchInput) { in.Items = []Item{{TicketID: t1}, {TicketID: t2, Amount: amt(600)}} },
	}
	for name, mutate := range cases {
		in := base
		in.Items = append([]Item(nil), base.Items...)
		mutate(&in)
		if err := replayMatches(stored, in); err == nil || err.Code != CodeIdempotencyKeyReused || err.Status != http.StatusConflict {
			t.Errorf("%s: got %+v, want 409 %s", name, err, CodeIdempotencyKeyReused)
		}
	}
}

func TestValidateBatch(t *testing.T) {
	tk := uuid.New()
	neg := int64(-1)
	ok := BatchInput{IdempotencyKey: "k", Reason: "r", Items: []Item{{TicketID: tk}}}
	if err := validateBatch(ok); err != nil {
		t.Fatalf("valid batch refused: %v", err)
	}
	bad := []BatchInput{
		{Reason: "r", Items: ok.Items},
		{IdempotencyKey: "k", Reason: "  ", Items: ok.Items},
		{IdempotencyKey: "k", Reason: "r"},
		{IdempotencyKey: "k", Reason: "r", Items: []Item{{TicketID: tk}, {TicketID: tk}}},
		{IdempotencyKey: "k", Reason: "r", Items: []Item{{TicketID: uuid.Nil}}},
		{IdempotencyKey: "k", Reason: "r", Items: []Item{{TicketID: tk, Amount: &neg}}},
	}
	for i, b := range bad {
		if err := validateBatch(b); err == nil || err.Code != CodeInvalidRequest || err.Status != http.StatusBadRequest {
			t.Errorf("case %d: got %v; want 400 %s", i, err, CodeInvalidRequest)
		}
	}
}

func TestRefundAccepted(t *testing.T) {
	id := "re_1"
	cases := []struct {
		r    Refund
		want bool
	}{
		{Refund{State: StateSucceeded}, true},
		{Refund{State: StateProviderPending, ProviderRefundID: &id}, true},
		{Refund{State: StateProviderPending}, false},
		{Refund{State: StateFailed, ProviderRefundID: &id}, false},
		{Refund{State: StateManualReview, ProviderRefundID: &id}, false},
	}
	for i, c := range cases {
		if got := c.r.Accepted(); got != c.want {
			t.Errorf("case %d: Accepted = %v; want %v", i, got, c.want)
		}
	}
}

type recordingScheduler struct{ at []time.Time }

func (s *recordingScheduler) ScheduleNext(_ context.Context, at time.Time) error {
	s.at = append(s.at, at)
	return nil
}

// A sweep pass that fails must still schedule the next one: one bad row
// may never stop the sweep for good.
func TestSweepHandler_AlwaysSchedulesTheNextRun(t *testing.T) {
	sched := &recordingScheduler{}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	h := NewSweepHandler(SweepOptions{Scheduler: sched, Now: func() time.Time { return now }})
	if err := h(context.Background(), nil); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(sched.at) != 1 || !sched.at[0].Equal(now.Add(DefaultSweepInterval)) {
		t.Fatalf("scheduled %v; want one run at now+%s", sched.at, DefaultSweepInterval)
	}
}

func TestTruncateAndInterval(t *testing.T) {
	if got := truncate("абвгд", 3); got != "абв" {
		t.Errorf("truncate = %q", got)
	}
	if got := intervalText(90 * time.Second); got != "90 seconds" {
		t.Errorf("intervalText = %q", got)
	}
}

// TestOptionsValidate_FailsFastOnUnsafeTimings (third review, L2): a call
// plus the recording of its answer must fit inside CallStaleAfter, and a
// sweep pass inside the worker's stale-claim timeout; New refuses anything
// else at construction.
func TestOptionsValidate_FailsFastOnUnsafeTimings(t *testing.T) {
	if err := (Options{}).Validate(); err != nil {
		t.Fatalf("the defaults must validate: %v", err)
	}
	if err := (Options{CallTimeout: 30 * time.Second}).Validate(); err == nil {
		t.Error("2 x 30s CallTimeout reaches CallStaleAfter and must be refused")
	}
	if err := (Options{SweepPassTimeout: 4 * time.Minute}).Validate(); err == nil {
		t.Error("a 4-minute pass plus the alert budget and a call overruns the worker's stale claim")
	}
	// Fifth review, LOW a: every section may overrun its deadline by its
	// last item — a drive's call and recording plus one ticket cancellation
	// and the settle writes (retry), a lookup plus the same tail, a repair's
	// tail — and the alert section by one bookkeeping write. With a 3-minute
	// pass that is ~350 s, past the worker's 300 s stale-claim timeout.
	if err := (Options{SweepPassTimeout: 3 * time.Minute}).Validate(); err == nil {
		t.Error("a 3-minute pass plus every section's settle overrun reaches the worker's stale claim")
	}
	if worst := SweepWorstCase(DefaultCallTimeout, DefaultSweepPassTimeout); worst >= WorkerStaleClaimTimeout {
		t.Errorf("the default pass may take %s, not below %s", worst, WorkerStaleClaimTimeout)
	}
	defer func() {
		if recover() == nil {
			t.Error("New must panic on invalid timings")
		}
	}()
	New(Options{CallTimeout: time.Minute})
}

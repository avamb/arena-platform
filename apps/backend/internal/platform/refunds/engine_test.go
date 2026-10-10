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
	if e := RouteRefusal(RouteUnknownProvider, "x"); e != nil {
		t.Errorf("unknown provider refused here (the flat routes keep the legacy path): %+v", e)
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

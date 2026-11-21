package main

import (
	"context"
	"errors"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// recordingLeg counts the events it receives and answers err.
type recordingLeg struct {
	got int
	err error
}

func (r *recordingLeg) Dispatch(context.Context, outbox.Event) error {
	r.got++
	return r.err
}

// TestOutboxLegs_DisabledGenericLegDoesNotBlockTheRest guards production
// mode: OUTBOX_MODE=disabled must switch off only the generic webhook. With
// the DisabledDispatcher left in the fan-out every event stopped on
// ErrDispatchDisabled and MACS and the selling sites received nothing.
func TestOutboxLegs_DisabledGenericLegDoesNotBlockTheRest(t *testing.T) {
	cfg := &config.Config{OutboxMode: config.OutboxModeDisabled}
	notifier, macs, sites := &recordingLeg{}, &recordingLeg{}, &recordingLeg{}

	legs := outboxLegs(
		outbox.Leg{Name: legSalesNotify, Dispatcher: notifier},
		outbox.Leg{Name: legWebhook, Dispatcher: buildOutboxDispatcher(cfg, testLogger())},
		outbox.Leg{Name: legMACS, Dispatcher: macs},
		outbox.Leg{Name: legBil24WP, Dispatcher: sites},
	)
	if len(legs) != 3 {
		t.Fatalf("legs = %d, want 3 (the disabled generic leg dropped)", len(legs))
	}
	for _, l := range legs {
		if l.Name == legWebhook {
			t.Fatalf("the disabled %q leg stayed in the fan-out", legWebhook)
		}
	}
	d, err := outbox.NewLegFanOut(nil, testLogger(), legs...)
	if err != nil {
		t.Fatalf("NewLegFanOut: %v", err)
	}
	if err := d.Dispatch(context.Background(), outbox.Event{}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if notifier.got != 1 || macs.got != 1 || sites.got != 1 {
		t.Fatalf("deliveries notifier=%d macs=%d sites=%d, want 1 each", notifier.got, macs.got, sites.got)
	}
}

// TestOutboxLegs_NoopGenericLegStays keeps today's staging behaviour: the
// noop leg is harmless and stays in place.
func TestOutboxLegs_NoopGenericLegStays(t *testing.T) {
	cfg := &config.Config{OutboxMode: config.OutboxModeNoop}
	legs := outboxLegs(
		outbox.Leg{Name: legSalesNotify, Dispatcher: &recordingLeg{}},
		outbox.Leg{Name: legWebhook, Dispatcher: buildOutboxDispatcher(cfg, testLogger())},
		outbox.Leg{Name: legMACS, Dispatcher: &recordingLeg{}},
	)
	if len(legs) != 3 {
		t.Fatalf("legs = %d, want 3", len(legs))
	}
}

// TestOutboxLegs_FailingMACSDoesNotBlockTheSites guards F-FC-01 at the worker
// wiring: MACS answering errors must not keep the WordPress sites and billing
// from receiving the event.
func TestOutboxLegs_FailingMACSDoesNotBlockTheSites(t *testing.T) {
	macsErr := errors.New("macs returned 500")
	notifier, conv, macs, sites, billing := &recordingLeg{}, &recordingLeg{}, &recordingLeg{err: macsErr}, &recordingLeg{}, &recordingLeg{}
	d, err := outbox.NewLegFanOut(nil, testLogger(), outboxLegs(
		outbox.Leg{Name: legSalesNotify, Dispatcher: notifier},
		outbox.Leg{Name: legConversions, Dispatcher: conv},
		outbox.Leg{Name: legMACS, Dispatcher: macs},
		outbox.Leg{Name: legBil24WP, Dispatcher: sites},
		outbox.Leg{Name: legBillingAccel, Dispatcher: billing},
	)...)
	if err != nil {
		t.Fatalf("NewLegFanOut: %v", err)
	}
	if err := d.Dispatch(context.Background(), outbox.Event{}); !errors.Is(err, macsErr) {
		t.Fatalf("Dispatch error = %v, want the MACS error", err)
	}
	for name, l := range map[string]*recordingLeg{"sales_notify": notifier, "conversions": conv, "macs": macs, "bil24_wp": sites, "billing_accel": billing} {
		if l.got != 1 {
			t.Errorf("%s got %d event(s), want 1", name, l.got)
		}
	}
}

// TestOutboxLegs_NamesAreStable pins the stored leg names: renaming one makes
// every pending event call that leg again.
func TestOutboxLegs_NamesAreStable(t *testing.T) {
	want := []string{"sales_notify", "conversions", "webhook", "macs", "bil24_wp", "billing_accel"}
	got := []string{legSalesNotify, legConversions, legWebhook, legMACS, legBil24WP, legBillingAccel}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("leg %d = %q, want %q", i, got[i], want[i])
		}
	}
}

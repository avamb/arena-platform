package main

import (
	"context"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// recordingLeg counts the events it receives.
type recordingLeg struct{ got int }

func (r *recordingLeg) Dispatch(context.Context, outbox.Event) error {
	r.got++
	return nil
}

// TestOutboxLegs_DisabledGenericLegDoesNotBlockTheRest guards production
// mode: OUTBOX_MODE=disabled must switch off only the generic webhook. With
// the DisabledDispatcher left in the fan-out every event stopped on
// ErrDispatchDisabled and MACS and the selling sites received nothing.
func TestOutboxLegs_DisabledGenericLegDoesNotBlockTheRest(t *testing.T) {
	cfg := &config.Config{OutboxMode: config.OutboxModeDisabled}
	notifier, macs, sites := &recordingLeg{}, &recordingLeg{}, &recordingLeg{}

	legs := outboxLegs(notifier, buildOutboxDispatcher(cfg, testLogger()), macs, sites)
	if len(legs) != 3 {
		t.Fatalf("legs = %d, want 3 (the disabled generic leg dropped)", len(legs))
	}
	d := &multiDispatcher{dispatchers: legs}
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
	legs := outboxLegs(&recordingLeg{}, buildOutboxDispatcher(cfg, testLogger()), &recordingLeg{})
	if len(legs) != 3 {
		t.Fatalf("legs = %d, want 3", len(legs))
	}
}

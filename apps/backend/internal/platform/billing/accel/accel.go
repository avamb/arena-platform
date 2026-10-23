// Package accel is the outbox leg that speeds billing up (billing wave B-A,
// task B2-4, spec 41 section 6.2). It is only an accelerator: the sales tables
// stay the source of truth and the billing.collect / billing.rate jobs find
// every fact on their own schedule. When an order is paid, a ticket is
// refunded, cancelled or revoked, a session is cancelled or an event is
// published, the leg asks for an extra collect-and-rate pass NOW instead of
// waiting up to a minute.
//
// The leg touches no database and no network from Dispatch: it sets a flag
// and returns nil, always. A failure of the extra pass is logged and changes
// nothing for the other legs of the fan-out (the WordPress and MACS webhooks,
// the Telegram notification) or for the outbox row.
package accel

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// The outbox events that mean "billing has something new to read".
const (
	EventOrderPaid       = "v1.order.paid"
	EventTicketRefunded  = "v1.ticket.refunded"
	EventTicketCancelled = "v1.ticket.cancelled"
	EventTicketRevoked   = "v1.ticket.revoked"
	EventSessionCancel   = "v1.session.cancelled"
	EventEventPublished  = "v1.event.published"
)

// DefaultMinGap is the shortest time between two extra passes, so a burst of
// events costs one pass, not one per event.
const DefaultMinGap = 2 * time.Second

// Pass is one collect-and-rate pass.
type Pass func(ctx context.Context) error

// Accelerator is the outbox leg. Build it with New, add it to the fan-out as
// an outbox.Dispatcher and run Run in a goroutine.
type Accelerator struct {
	pass   Pass
	kick   chan struct{}
	minGap time.Duration
	logger *slog.Logger
}

// New builds an Accelerator that runs pass when kicked. minGap <= 0 uses
// DefaultMinGap; a nil logger uses slog.Default().
func New(pass Pass, minGap time.Duration, logger *slog.Logger) *Accelerator {
	if minGap <= 0 {
		minGap = DefaultMinGap
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Accelerator{pass: pass, kick: make(chan struct{}, 1), minGap: minGap, logger: logger}
}

// Dispatch implements outbox.Dispatcher. It never fails and never blocks.
func (a *Accelerator) Dispatch(_ context.Context, ev outbox.Event) error {
	if a == nil {
		return nil
	}
	switch ev.EventType {
	case EventOrderPaid, EventTicketRefunded, EventTicketCancelled, EventTicketRevoked,
		EventSessionCancel, EventEventPublished:
	default:
		return nil
	}
	select {
	case a.kick <- struct{}{}:
	default: // a pass is already requested: the burst shares it
	}
	return nil
}

// Run runs a pass whenever kicked, until ctx ends.
func (a *Accelerator) Run(ctx context.Context) {
	if a == nil || a.pass == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.kick:
			a.runOnce(ctx)
			select {
			case <-ctx.Done():
				return
			case <-time.After(a.minGap):
			}
		}
	}
}

func (a *Accelerator) runOnce(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			a.logger.Error("billing accelerator: panic in an extra pass", "panic", fmt.Sprint(r))
		}
	}()
	if err := a.pass(ctx); err != nil {
		a.logger.Warn("billing accelerator: extra pass failed, the scheduled jobs will catch up", "error", err.Error())
	}
}

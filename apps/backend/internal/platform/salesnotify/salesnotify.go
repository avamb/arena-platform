// Package salesnotify sends every sale and every refund to the Telegram chats
// that subscribed to them — the organizer's own group and the operator's.
//
// It is the arena counterpart of Bil24's per-organization "Notifications"
// (chat id + triggers "Order paid" / "Ticket refunded" + "Allowed"), stored
// in sales_notification_subscriptions (migration 0111). A subscription with
// no org_id is an operator one and receives every organization's events.
//
// It is event-driven: Dispatcher is a leg of arena-worker's outbox fan-out,
// next to the WordPress and MACS webhooks, so a message follows
// v1.order.paid / v1.ticket.refunded / v1.ticket.cancelled within seconds.
// The leg never fails the fan-out — it queues the event and returns nil, so
// a slow or broken Telegram can neither delay nor re-trigger the webhooks a
// site or the scanner depend on. Because the fan-out still retries an event
// whenever ANOTHER leg fails, every announcement is claimed once in
// sales_notification_deliveries ('paid:<order>', 'refund:<ticket>').
//
// It posts through its own bot (SALES_TELEGRAM_BOT_TOKEN), so organizers
// never see the operator alerts the ops watchdog's bot sends.
package salesnotify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// Outbox event types this leg announces.
const (
	EventOrderPaid       = "v1.order.paid"
	EventTicketRefunded  = "v1.ticket.refunded"
	EventTicketCancelled = "v1.ticket.cancelled"
)

// queueSize bounds the events waiting for Telegram. A burst beyond it is
// dropped with a log line rather than blocking the outbox.
const queueSize = 1024

// Trigger names an event a subscription can opt into.
type Trigger int

const (
	TriggerOrderPaid Trigger = iota
	TriggerTicketRefunded
)

// Subscription is one allowed row of sales_notification_subscriptions.
type Subscription struct {
	ID               string
	OrgID            string // "" = operator: every organization
	Name             string
	ChatID           string
	OnOrderPaid      bool
	OnTicketRefunded bool
}

// Route returns the subscriptions that must receive trigger for orgID.
func Route(subs []Subscription, orgID string, trigger Trigger) []Subscription {
	var out []Subscription
	for _, s := range subs {
		if s.OrgID != "" && s.OrgID != orgID {
			continue
		}
		if (trigger == TriggerOrderPaid && s.OnOrderPaid) ||
			(trigger == TriggerTicketRefunded && s.OnTicketRefunded) {
			out = append(out, s)
		}
	}
	return out
}

// ErrNotFound: the order or ticket an event names does not exist (any more).
var ErrNotFound = errors.New("salesnotify: not found")

// Store is what the notifier reads and writes.
type Store interface {
	Subscriptions(ctx context.Context) ([]Subscription, error)
	SaleByOrder(ctx context.Context, orderID string) (Sale, error)
	RefundByTicket(ctx context.Context, ticketID string) (Refund, error)
	// Claim records key and reports whether this call was the first.
	Claim(ctx context.Context, key string) (bool, error)
	// UpdateChatID follows a group upgraded to a supergroup.
	UpdateChatID(ctx context.Context, subscriptionID, chatID string) error
	// RecordDelivery stores the last delivery error ("" clears it).
	RecordDelivery(ctx context.Context, subscriptionID, lastError string) error
}

// Sender delivers one message to one chat.
type Sender interface {
	Send(ctx context.Context, chatID, text string) error
}

// Dispatcher is the outbox leg. Construct with NewDispatcher and start Run.
type Dispatcher struct {
	store  Store
	sender Sender
	logger *slog.Logger
	queue  chan outbox.Event
}

// NewDispatcher returns the leg; a nil sender (no bot token configured)
// makes Dispatch a no-op.
func NewDispatcher(store Store, sender Sender, logger *slog.Logger) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{store: store, sender: sender, logger: logger, queue: make(chan outbox.Event, queueSize)}
}

// Dispatch implements outbox.Dispatcher. It only queues the event and never
// returns an error — see the package doc.
func (d *Dispatcher) Dispatch(_ context.Context, ev outbox.Event) error {
	if d == nil || d.sender == nil {
		return nil
	}
	switch ev.EventType {
	case EventOrderPaid, EventTicketRefunded, EventTicketCancelled:
	default:
		return nil
	}
	select {
	case d.queue <- ev:
	default:
		d.logger.Warn("sales.notify: queue full, notification dropped",
			"event_type", ev.EventType, "aggregate_id", ev.AggregateID)
	}
	return nil
}

// Run delivers queued events until ctx ends.
func (d *Dispatcher) Run(ctx context.Context) {
	if d == nil || d.sender == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-d.queue:
			d.Handle(ctx, ev)
		}
	}
}

// Handle announces one event synchronously. Failures are logged, never
// returned: a notification is best effort.
func (d *Dispatcher) Handle(ctx context.Context, ev outbox.Event) {
	defer func() {
		if r := recover(); r != nil {
			d.logger.Error("sales.notify: panic while handling event", "event_type", ev.EventType, "panic", fmt.Sprint(r))
		}
	}()
	key, trigger, text, orgID, err := d.compose(ctx, ev)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			d.logger.Warn("sales.notify: could not build notification",
				"event_type", ev.EventType, "aggregate_id", ev.AggregateID, "error", err.Error())
		}
		return
	}
	subs, err := d.store.Subscriptions(ctx)
	if err != nil {
		d.logger.Warn("sales.notify: load subscriptions", "error", err.Error())
		return
	}
	targets := Route(subs, orgID, trigger)
	if len(targets) == 0 {
		return
	}
	first, err := d.store.Claim(ctx, key)
	if err != nil {
		d.logger.Warn("sales.notify: claim", "key", key, "error", err.Error())
		return
	}
	if !first {
		return // an outbox retry, or the sibling event of the same refund
	}
	for _, sub := range targets {
		d.deliver(ctx, sub, text)
	}
}

// compose loads what the event is about and renders the message.
func (d *Dispatcher) compose(ctx context.Context, ev outbox.Event) (key string, trigger Trigger, text, orgID string, err error) {
	switch ev.EventType {
	case EventOrderPaid:
		id := payloadString(ev.Payload, "order_id", ev.AggregateID)
		sale, err := d.store.SaleByOrder(ctx, id)
		if err != nil {
			return "", 0, "", "", err
		}
		return "paid:" + id, TriggerOrderPaid, FormatSale(sale), sale.OrgID, nil
	default: // v1.ticket.refunded / v1.ticket.cancelled
		id := payloadString(ev.Payload, "ticket_id", ev.AggregateID)
		r, err := d.store.RefundByTicket(ctx, id)
		if err != nil {
			return "", 0, "", "", err
		}
		// A provider refund names its amount on the event before the ticket
		// row carries it.
		if r.Amount == 0 {
			if amt, ok := payloadInt(ev.Payload, "amount"); ok {
				r.Amount = amt
			}
		}
		return "refund:" + id, TriggerTicketRefunded, FormatRefund(r), r.OrgID, nil
	}
}

// deliver sends text to one chat, following a supergroup migration once and
// recording the outcome on the subscription.
func (d *Dispatcher) deliver(ctx context.Context, sub Subscription, text string) {
	err := d.sender.Send(ctx, sub.ChatID, text)
	var mig *MigratedError
	if errors.As(err, &mig) {
		d.logger.Info("sales.notify: chat upgraded to a supergroup",
			"subscription", sub.Name, "from", sub.ChatID, "to", mig.NewChatID)
		if uerr := d.store.UpdateChatID(ctx, sub.ID, mig.NewChatID); uerr != nil {
			d.logger.Warn("sales.notify: could not store the new chat id", "subscription", sub.Name, "error", uerr.Error())
		}
		err = d.sender.Send(ctx, mig.NewChatID, text)
	}
	lastErr := ""
	if err != nil {
		lastErr = err.Error()
		d.logger.Warn("sales.notify: delivery failed", "subscription", sub.Name, "error", lastErr)
	}
	if rerr := d.store.RecordDelivery(ctx, sub.ID, lastErr); rerr != nil {
		d.logger.Warn("sales.notify: could not record delivery", "subscription", sub.Name, "error", rerr.Error())
	}
}

func payloadString(p map[string]any, key, fallback string) string {
	if v, ok := p[key].(string); ok && v != "" {
		return v
	}
	return fallback
}

func payloadInt(p map[string]any, key string) (int64, bool) {
	switch v := p[key].(type) {
	case float64:
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	}
	return 0, false
}

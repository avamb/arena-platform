// Package salesnotify sends every sale and every refund to the Telegram chats
// that subscribed to them — the organizer's own group and the operator's.
//
// It is the arena counterpart of Bil24's per-organization "Notifications"
// (chat id + triggers "Order paid" / "Ticket refunded" + "Allowed"), stored
// in sales_notification_subscriptions (migration 0111). A subscription with
// no org_id is an operator one and receives every organization's events.
//
// Like the ops watchdog it is a self-scheduling worker job that follows
// cursors in ops_watchdog_state (keys salesnotify.*) and never replays
// history: the first run starts from "now". It uses its own bot
// (SALES_TELEGRAM_BOT_TOKEN), so organizers never see the operator alerts
// the watchdog's bot sends. Delivery is best effort: a chat the bot was
// removed from is recorded in last_error and never stops the cursor.
package salesnotify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opswatchdog"
)

// JobType is the worker_jobs.job_type of the self-scheduling notifier.
const JobType = "sales.notify"

// DefaultInterval is the gap between runs.
const DefaultInterval = 30 * time.Second

const (
	cursorSales   = "salesnotify.sales"
	cursorRefunds = "salesnotify.refunds"

	// rowLimit bounds one run's query; the rest follows on the next run.
	rowLimit = 200
	// digestThreshold: more messages than this for one chat in one run are
	// sent as a single summary instead.
	digestThreshold = 10
)

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

// Operator reports whether the subscription receives every organization.
func (s Subscription) Operator() bool { return s.OrgID == "" }

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

// Store is what the job reads and writes.
type Store interface {
	Subscriptions(ctx context.Context) ([]Subscription, error)
	SalesSince(ctx context.Context, ts time.Time, id string, limit int) ([]Sale, error)
	RefundsSince(ctx context.Context, ts time.Time, id string, limit int) ([]Refund, error)
	// UpdateChatID follows a group upgraded to a supergroup.
	UpdateChatID(ctx context.Context, subscriptionID, chatID string) error
	// RecordDelivery stores the last delivery error ("" clears it).
	RecordDelivery(ctx context.Context, subscriptionID, lastError string) error
}

// Sender delivers one message to one chat.
type Sender interface {
	Send(ctx context.Context, chatID, text string) error
}

// Scheduler enqueues the next run.
type Scheduler interface {
	ScheduleNext(ctx context.Context, at time.Time) error
}

// Options configures NewHandler.
type Options struct {
	Store     Store
	Cursors   opswatchdog.CursorStore
	Sender    Sender // nil: the job only advances its cursors
	Scheduler Scheduler
	Interval  time.Duration
	Now       func() time.Time
	Logger    *slog.Logger
}

func (o Options) withDefaults() Options {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// NewHandler returns the worker handler for JobType. It always succeeds:
// a failed run is logged and retried on the next tick.
func NewHandler(opts Options) func(ctx context.Context, payload []byte) error {
	opts = opts.withDefaults()
	return func(ctx context.Context, _ []byte) error {
		if err := RunOnce(ctx, opts); err != nil {
			opts.Logger.Warn("sales.notify: run failed", "error", err.Error())
		}
		if opts.Scheduler != nil {
			if err := opts.Scheduler.ScheduleNext(ctx, opts.Now().Add(opts.Interval)); err != nil {
				return fmt.Errorf("salesnotify: schedule next run: %w", err)
			}
		}
		return nil
	}
}

// RunOnce processes every sale and refund past the cursors.
func RunOnce(ctx context.Context, opts Options) error {
	opts = opts.withDefaults()
	subs, err := opts.Store.Subscriptions(ctx)
	if err != nil {
		return fmt.Errorf("load subscriptions: %w", err)
	}
	d := &dispatcher{opts: opts, outbox: map[string]*chatBatch{}}
	errSales := d.collectSales(ctx, subs)
	errRefunds := d.collectRefunds(ctx, subs)
	d.flush(ctx)
	// Cursors move only after delivery was attempted, so a crash in the
	// middle repeats a message rather than losing one.
	if err := d.saveCursors(ctx); err != nil {
		return err
	}
	return errors.Join(errSales, errRefunds)
}

type chatBatch struct {
	sub      Subscription
	messages []string
	sales    []Sale
	refunds  []Refund
}

type dispatcher struct {
	opts    Options
	outbox  map[string]*chatBatch
	order   []string
	cursors []cursorUpdate
}

type cursorUpdate struct {
	key string
	ts  time.Time
	id  string
}

func (d *dispatcher) batch(s Subscription) *chatBatch {
	b, ok := d.outbox[s.ID]
	if !ok {
		b = &chatBatch{sub: s}
		d.outbox[s.ID] = b
		d.order = append(d.order, s.ID)
	}
	return b
}

func (d *dispatcher) loadCursor(ctx context.Context, key string) (time.Time, string, error) {
	c, err := d.opts.Cursors.Get(ctx, key)
	if err != nil {
		return time.Time{}, "", err
	}
	if c == nil || c.TS == nil {
		now := d.opts.Now().UTC()
		if err := d.opts.Cursors.Set(ctx, key, opswatchdog.Cursor{TS: &now}); err != nil {
			return time.Time{}, "", err
		}
		return now, "", nil
	}
	return *c.TS, c.ID, nil
}

func (d *dispatcher) collectSales(ctx context.Context, subs []Subscription) error {
	ts, id, err := d.loadCursor(ctx, cursorSales)
	if err != nil {
		return fmt.Errorf("sales cursor: %w", err)
	}
	sales, err := d.opts.Store.SalesSince(ctx, ts, id, rowLimit)
	if err != nil {
		return fmt.Errorf("sales query: %w", err)
	}
	for _, s := range sales {
		for _, sub := range Route(subs, s.OrgID, TriggerOrderPaid) {
			b := d.batch(sub)
			b.sales = append(b.sales, s)
			b.messages = append(b.messages, FormatSale(s))
		}
	}
	if n := len(sales); n > 0 {
		d.cursors = append(d.cursors, cursorUpdate{cursorSales, sales[n-1].At, sales[n-1].ID})
	}
	return nil
}

func (d *dispatcher) collectRefunds(ctx context.Context, subs []Subscription) error {
	ts, id, err := d.loadCursor(ctx, cursorRefunds)
	if err != nil {
		return fmt.Errorf("refunds cursor: %w", err)
	}
	refunds, err := d.opts.Store.RefundsSince(ctx, ts, id, rowLimit)
	if err != nil {
		return fmt.Errorf("refunds query: %w", err)
	}
	for _, r := range refunds {
		for _, sub := range Route(subs, r.OrgID, TriggerTicketRefunded) {
			b := d.batch(sub)
			b.refunds = append(b.refunds, r)
			b.messages = append(b.messages, FormatRefund(r))
		}
	}
	if n := len(refunds); n > 0 {
		d.cursors = append(d.cursors, cursorUpdate{cursorRefunds, refunds[n-1].At, refunds[n-1].ID})
	}
	return nil
}

func (d *dispatcher) flush(ctx context.Context) {
	if d.opts.Sender == nil {
		return
	}
	for _, id := range d.order {
		b := d.outbox[id]
		msgs := b.messages
		if len(msgs) > digestThreshold {
			msgs = []string{FormatDigest(b.sales, b.refunds)}
		}
		d.deliver(ctx, b.sub, msgs)
	}
}

// deliver sends msgs to one chat, following a supergroup migration once and
// recording the outcome on the subscription.
func (d *dispatcher) deliver(ctx context.Context, sub Subscription, msgs []string) {
	chat := sub.ChatID
	lastErr := ""
	for _, m := range msgs {
		err := d.opts.Sender.Send(ctx, chat, m)
		var mig *MigratedError
		if errors.As(err, &mig) {
			d.opts.Logger.Info("sales.notify: chat upgraded to a supergroup",
				"subscription", sub.Name, "from", chat, "to", mig.NewChatID)
			if uerr := d.opts.Store.UpdateChatID(ctx, sub.ID, mig.NewChatID); uerr != nil {
				d.opts.Logger.Warn("sales.notify: could not store the new chat id", "subscription", sub.Name, "error", uerr.Error())
			}
			chat = mig.NewChatID
			err = d.opts.Sender.Send(ctx, chat, m)
		}
		if err != nil {
			lastErr = err.Error()
			d.opts.Logger.Warn("sales.notify: delivery failed", "subscription", sub.Name, "error", lastErr)
			var perm *PermanentError
			if errors.As(err, &perm) {
				break // bot removed / chat gone: the rest would fail the same way
			}
		}
	}
	if rerr := d.opts.Store.RecordDelivery(ctx, sub.ID, lastErr); rerr != nil {
		d.opts.Logger.Warn("sales.notify: could not record delivery", "subscription", sub.Name, "error", rerr.Error())
	}
}

func (d *dispatcher) saveCursors(ctx context.Context) error {
	for _, c := range d.cursors {
		ts := c.ts
		if err := d.opts.Cursors.Set(ctx, c.key, opswatchdog.Cursor{TS: &ts, ID: c.id}); err != nil {
			return fmt.Errorf("save cursor %s: %w", c.key, err)
		}
	}
	return nil
}

// EnsureInitialCursors seeds both cursors at now, so enabling the job never
// replays past sales.
func EnsureInitialCursors(ctx context.Context, pool *pgxpool.Pool, now time.Time) error {
	return opswatchdog.EnsureInitialCursors(ctx, pool, []string{cursorSales, cursorRefunds}, now)
}

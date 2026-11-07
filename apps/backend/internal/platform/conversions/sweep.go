package conversions

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat/money"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// EventOrderPaid is the outbox event the leg reacts to.
const EventOrderPaid = "v1.order.paid"

// JobType is the worker_jobs.job_type of the self-scheduling sender.
const JobType = "conversions.sweep"

const (
	// DefaultInterval is the gap between sweeps.
	DefaultInterval = 30 * time.Second
	// MaxAttempts is how many sends a row gets before it is marked failed.
	MaxAttempts = 8
	batchSize   = 50
	maxErrLen   = 200
)

// Dispatcher is the outbox leg. It only queues, and always returns nil.
type Dispatcher struct {
	store  Store
	logger *slog.Logger
}

// NewDispatcher builds the leg.
func NewDispatcher(store Store, logger *slog.Logger) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{store: store, logger: logger}
}

// Dispatch implements outbox.Dispatcher.
func (d *Dispatcher) Dispatch(ctx context.Context, ev outbox.Event) error {
	if d == nil || d.store == nil || ev.EventType != EventOrderPaid {
		return nil
	}
	raw := ev.AggregateID
	if s, ok := ev.Payload["order_id"].(string); ok && s != "" {
		raw = s
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil
	}
	if err := d.store.Enqueue(ctx, id); err != nil {
		d.logger.Warn("conversions: could not queue conversions", "order_id", id.String(), "error", scrub(err.Error()))
	}
	return nil
}

// Senders bundles the two ad-platform clients so a test can aim them at stubs.
type Senders struct {
	Meta MetaSender
	GA4  GA4Sender
}

// Backoff is the pause before attempt number attempts+1: 30 s doubling, capped
// at 6 hours.
func Backoff(attempts int) time.Duration {
	d := 30 * time.Second
	for i := 0; i < attempts && d < 6*time.Hour; i++ {
		d *= 2
	}
	if d > 6*time.Hour {
		d = 6 * time.Hour
	}
	return d
}

// ContextPurger is the optional part of a Store that empties old buyer
// contexts (PGStore.PurgeContexts).
type ContextPurger interface {
	PurgeContexts(ctx context.Context, olderThan time.Time) (int64, error)
}

// RunOnce sends one batch of due conversions and returns how many it handled.
// It also empties the buyer contexts older than ContextRetention when the
// store can: a context is never kept indefinitely, consent or not.
func RunOnce(ctx context.Context, store Store, senders Senders, logger *slog.Logger, now time.Time) (int, error) {
	due, err := store.ClaimDue(ctx, batchSize)
	if err != nil {
		return 0, fmt.Errorf("conversions: claim: %w", err)
	}
	for _, d := range due {
		handle(ctx, store, senders, logger, d, now)
	}
	if p, ok := store.(ContextPurger); ok {
		if _, err := p.PurgeContexts(ctx, now.Add(-ContextRetention)); err != nil {
			logger.Warn("conversions: could not purge old buyer contexts", "error", scrub(err.Error()))
		}
	}
	return len(due), nil
}

func handle(ctx context.Context, store Store, senders Senders, logger *slog.Logger, d Delivery, now time.Time) {
	skip := func(reason string) {
		if err := store.MarkSkipped(ctx, d.ID, reason); err != nil {
			logger.Warn("conversions: could not mark skipped", "delivery_id", d.ID.String(), "error", scrub(err.Error()))
		}
	}
	retry := func(msg string, permanent bool) {
		msg = scrub(msg)
		var err error
		if permanent || d.Attempts+1 >= MaxAttempts {
			err = store.MarkFailed(ctx, d.ID, msg)
		} else {
			err = store.MarkRetry(ctx, d.ID, now.Add(Backoff(d.Attempts)), msg)
		}
		if err != nil {
			logger.Warn("conversions: could not record failure", "delivery_id", d.ID.String(), "error", scrub(err.Error()))
		}
	}

	st, ok, err := store.Settings(ctx, d.OrgID)
	if err != nil {
		retry("settings: "+err.Error(), false)
		return
	}
	if !ok {
		skip("not_configured")
		return
	}
	granted, err := store.ConsentForOrder(ctx, d.OrderID)
	if err != nil {
		retry("consent: "+err.Error(), false)
		return
	}
	if !granted {
		skip("no_consent") // spec 62 section 8.1: no consent, no server conversion
		return
	}
	od, err := store.Order(ctx, d.OrderID)
	if err != nil {
		retry("order: "+err.Error(), false)
		return
	}
	p := Purchase{
		OrderID:     d.OrderID.String(),
		OrderNumber: od.Number,
		Value:       money.MajorScale(od.Total, money.ScaleFor(od.Currency)),
		Currency:    od.Currency,
		EventTime:   d.CreatedAt,
		IsTest:      od.IsTest,
		Context:     od.Context,
		Email:       od.Email, // consent is true here
		Phone:       od.Phone,
	}
	var sendErr error
	switch d.Provider {
	case "meta":
		if !st.MetaReady() {
			skip("not_configured")
			return
		}
		if od.IsTest && st.MetaTestEventCode == "" {
			skip("test_order_without_test_event_code") // never pollute the live pixel
			return
		}
		code := ""
		if od.IsTest {
			code = st.MetaTestEventCode
		}
		sendErr = senders.Meta.Send(ctx, st.MetaPixelID, st.MetaCAPIToken, code, p)
	case "ga4":
		if !st.GA4Ready() {
			skip("not_configured")
			return
		}
		sendErr = senders.GA4.Send(ctx, st.GA4MeasurementID, st.GA4APISecret, p)
	default:
		skip("unknown_provider")
		return
	}
	if sendErr != nil {
		retry(sendErr.Error(), IsPermanent(sendErr))
		return
	}
	if err := store.MarkSent(ctx, d.ID); err != nil {
		logger.Warn("conversions: sent but could not record it", "delivery_id", d.ID.String(), "error", scrub(err.Error()))
	}
}

// scrub bounds an error text. Sender errors carry no secret by construction;
// this is the last line of defence for a database error.
func scrub(s string) string {
	if len(s) > maxErrLen {
		s = s[:maxErrLen]
	}
	return s
}

// Scheduler enqueues the next sweep.
type Scheduler interface {
	ScheduleNext(ctx context.Context, at time.Time) error
}

// PGScheduler is the worker_jobs-backed Scheduler.
type PGScheduler struct{ pool *pgxpool.Pool }

// NewPGScheduler wraps a pool.
func NewPGScheduler(pool *pgxpool.Pool) *PGScheduler { return &PGScheduler{pool: pool} }

// ScheduleNext inserts a pending job due at at.
func (s *PGScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', $2)`, JobType, at)
	if err != nil {
		return fmt.Errorf("conversions: schedule next sweep: %w", err)
	}
	return nil
}

// HandlerOptions configures NewHandler.
type HandlerOptions struct {
	Store     Store
	Senders   Senders
	Scheduler Scheduler
	Logger    *slog.Logger
	Interval  time.Duration
}

// NewHandler returns the handler for JobType (worker.HandlerFunc shape).
func NewHandler(o HandlerOptions) func(ctx context.Context, payload []byte) error {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	return func(ctx context.Context, _ []byte) error {
		n, err := RunOnce(ctx, o.Store, o.Senders, o.Logger, time.Now())
		if err != nil {
			return err
		}
		if o.Scheduler != nil {
			if err := o.Scheduler.ScheduleNext(ctx, time.Now().Add(o.Interval)); err != nil {
				return err
			}
		}
		if n > 0 {
			o.Logger.Info("conversions sweep complete", "handled", n)
		}
		return nil
	}
}

// ScheduleInitialJob seeds the first sweep unless one is already queued.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool) error {
	var cnt int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND status IN ('pending', 'claimed')`, JobType).Scan(&cnt); err != nil {
		return fmt.Errorf("conversions: check initial sweep job: %w", err)
	}
	if cnt > 0 {
		return nil
	}
	return NewPGScheduler(pool).ScheduleNext(ctx, time.Now())
}

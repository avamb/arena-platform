// Package monthcharge holds what the monthly-service charge jobs share: white
// label (billing.white_label, spec 08_architecture/53) and the paid shop
// module (billing.org_modules, spec 61). Each job writes one billable event
// per period and completed UTC month, prorated by days; the rate job
// (billing.rate) turns it into a charge.
//
// The package owns the parts that must behave the same for every monthly
// service: the walk over completed months, the days a period covers, the
// idempotent event insert with the currency check, and the self-scheduling
// worker job. A service supplies its period rows, its event kind, its subject
// namespace and its attributes.
package monthcharge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

// DefaultInterval is the gap between passes: a month closes once a day is
// late enough to be seen.
const DefaultInterval = 6 * time.Hour

// MaxMonthsBack bounds one pass, so a date X far in the past cannot make a
// pass read years of months.
const MaxMonthsBack = 36

// MonthLayout is the YYYY-MM month key of a charged month.
const MonthLayout = "2006-01"

// DayOf is the UTC calendar day of t at 00:00.
func DayOf(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// SubjectID is the idempotency key of the event of one period and month
// (uuid v5 in the service's namespace).
func SubjectID(namespace, periodID uuid.UUID, month time.Time) uuid.UUID {
	return uuid.NewSHA1(namespace, []byte(periodID.String()+":"+billing.MonthStart(month).Format(MonthLayout)))
}

// Months lists the first days of the completed months a pass covers: from the
// month of since (at most MaxMonthsBack months back) up to, not including,
// the month of now. Nothing when since is zero.
func Months(since, now time.Time) []time.Time {
	if since.IsZero() {
		return nil
	}
	current := billing.MonthStart(now)
	first := billing.MonthStart(since)
	if earliest := current.AddDate(0, -MaxMonthsBack, 0); first.Before(earliest) {
		first = earliest
	}
	var out []time.Time
	for m := first; m.Before(current); m = m.AddDate(0, 1, 0) {
		out = append(out, m)
	}
	return out
}

// Walk runs fn for every completed month of the pass and adds up how many
// events were new. A failing month does not stop the next ones; the errors
// are joined, each naming the month with label ("white label month 2026-10").
func Walk(ctx context.Context, label string, since, now time.Time,
	fn func(ctx context.Context, month time.Time) (int, error)) (int, error) {
	inserted := 0
	var errs []error
	for _, m := range Months(since, now) {
		n, err := fn(ctx, m)
		inserted += n
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", label, m.Format(MonthLayout), err))
		}
		if ctx.Err() != nil {
			break
		}
	}
	return inserted, errors.Join(errs...)
}

// Store is the query set ChargeMonth needs. *gen.Queries satisfies it.
type Store interface {
	billing.AccountQueries
	GetOrgBillingCountry(ctx context.Context, orgID uuid.UUID) (string, error)
	InsertBillableEvent(ctx context.Context, p gen.InsertBillableEventParams) (uuid.UUID, bool, error)
}

// Period is the part of a period row every monthly service shares.
type Period struct {
	ID       uuid.UUID
	OrgID    uuid.UUID
	StartsOn time.Time
	// EndsOn is the last day, inclusive; nil has no end.
	EndsOn *time.Time
	// MonthlyPriceMinor is the individual price (nil = the price list, 0 =
	// free) in Currency.
	MonthlyPriceMinor *int64
	Currency          *string
}

// Spec says what a service writes.
type Spec struct {
	Kind      billing.EventKind
	Namespace uuid.UUID
	// ErrCurrency is wrapped into the error of a month skipped because the
	// account currency is not the currency of a non-zero individual price.
	ErrCurrency error
	// Attributes builds the event attributes of one month.
	Attributes func(p Period, account gen.BillingAccountRow, month time.Time, daysActive int64) ([]byte, error)
}

// ChargeMonth writes the event of one period and month and reports whether it
// was new. sinceDay (the date X) clips the start of the period; a month
// without active days writes nothing. The account is the organization's
// billing account for its billing country. A non-zero individual price is
// charged only in the currency it was typed in: for another account currency
// the month is not written and an error wrapping spec.ErrCurrency says why.
func ChargeMonth(ctx context.Context, store Store, spec Spec, p Period, month, sinceDay time.Time) (bool, error) {
	start := p.StartsOn
	if start.Before(sinceDay) {
		start = sinceDay
	}
	days := billing.PeriodActiveDays(start, p.EndsOn, month)
	if days <= 0 {
		return false, nil
	}
	country, err := store.GetOrgBillingCountry(ctx, p.OrgID)
	if err != nil {
		return false, fmt.Errorf("billing country of org %s: %w", p.OrgID, err)
	}
	acc, err := billing.EnsureAccount(ctx, store, p.OrgID, country)
	if err != nil {
		return false, err
	}
	if p.MonthlyPriceMinor != nil && *p.MonthlyPriceMinor > 0 &&
		(p.Currency == nil || *p.Currency != acc.Currency) {
		typed := ""
		if p.Currency != nil {
			typed = *p.Currency
		}
		return false, fmt.Errorf("%w: price in %q, account %s in %s (end the period and open a new one)",
			spec.ErrCurrency, typed, acc.ID, acc.Currency)
	}
	attrs, err := spec.Attributes(p, acc, month, days)
	if err != nil {
		return false, err
	}
	_, isNew, err := store.InsertBillableEvent(ctx, gen.InsertBillableEventParams{
		Kind:       string(spec.Kind),
		SubjectID:  SubjectID(spec.Namespace, p.ID, month),
		OccurredAt: billing.MonthOccurredAt(month),
		OrgID:      p.OrgID,
		AccountID:  acc.ID,
		Attributes: attrs,
	})
	if err != nil {
		return false, fmt.Errorf("insert %s: %w", spec.Kind, err)
	}
	return isNew, nil
}

// ── worker job ──────────────────────────────────────────────────────────

// Scheduler enqueues the next pass.
type Scheduler interface {
	ScheduleNext(ctx context.Context, at time.Time) error
}

// PGScheduler is the production Scheduler backed by worker_jobs.
type PGScheduler struct {
	pool    *pgxpool.Pool
	jobType string
	label   string
}

// NewPGScheduler wraps a pool for one job type; label names the service in
// errors ("billing white label").
func NewPGScheduler(pool *pgxpool.Pool, jobType, label string) *PGScheduler {
	return &PGScheduler{pool: pool, jobType: jobType, label: label}
}

// ScheduleNext inserts a pending job due at `at` unless one is already
// pending, so a retry never forks a second chain.
func (s *PGScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	const q = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1, '{}', 3, 'pending', $2
		WHERE  NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status = 'pending')`
	if _, err := s.pool.Exec(ctx, q, s.jobType, at); err != nil {
		return fmt.Errorf("%s: schedule next pass: %w", s.label, err)
	}
	return nil
}

// ScheduleInitialJob seeds the first pass at worker start unless a job is
// already pending or running.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool, jobType, label string) error {
	const q = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1, '{}', 3, 'pending', now() + interval '2 minutes'
		WHERE  NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status IN ('pending', 'claimed'))`
	if _, err := pool.Exec(ctx, q, jobType); err != nil {
		return fmt.Errorf("%s: enqueue initial job: %w", label, err)
	}
	return nil
}

// Runner is a charger a handler drives.
type Runner interface {
	Enabled() bool
	Run(ctx context.Context) (int, error)
}

// HandlerOptions configures NewHandler.
type HandlerOptions struct {
	Runner    Runner
	Scheduler Scheduler
	Interval  time.Duration
	Logger    *slog.Logger
	// Label names the service in log lines ("billing white label").
	Label string
}

// NewHandler returns a worker job handler. Without the date X it does nothing
// and does not schedule itself again. The next pass is scheduled BEFORE the
// work, so a failed pass does not end the chain.
func NewHandler(opts HandlerOptions) func(ctx context.Context, payload []byte) error {
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return func(ctx context.Context, _ []byte) error {
		if opts.Runner == nil || !opts.Runner.Enabled() {
			return nil
		}
		if opts.Scheduler != nil {
			if err := opts.Scheduler.ScheduleNext(ctx, time.Now().Add(opts.Interval)); err != nil {
				return err
			}
		}
		n, err := opts.Runner.Run(ctx)
		if err != nil {
			opts.Logger.Error(opts.Label+" pass failed", "error", err.Error(), "inserted", n)
			return err
		}
		if n > 0 {
			opts.Logger.Info(opts.Label+" pass", "inserted", n)
		}
		return nil
	}
}

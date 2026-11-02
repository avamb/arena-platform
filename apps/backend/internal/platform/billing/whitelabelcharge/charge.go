// Package whitelabelcharge writes the billing facts of white label (spec
// 08_architecture/52): one billable event white_label_month per white label
// period and completed calendar month (UTC), prorated by days. The rate job
// (billing.rate) turns it into a charge with the subscription rule of the
// price list, or the period's individual monthly price.
//
// Rules:
//   - only completed months are charged, in arrears; the month runs in UTC
//     and the event is dated noon UTC on its last day;
//   - days before the date X (BILLING_COLLECT_SINCE) are never charged, like
//     sales before it;
//   - the event is idempotent by (white_label_month, uuid of period+month),
//     so a pass may run any number of times;
//   - the account is the organization's billing account for its billing
//     country (legal address, else country), as for a sale without a venue.
package whitelabelcharge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

// JobType is the worker_jobs.job_type of the charge job.
const JobType = "billing.white_label"

// DefaultInterval is the gap between passes: a month closes once a day is
// late enough to be seen.
const DefaultInterval = 6 * time.Hour

// maxMonthsBack bounds one pass, so a date X far in the past cannot make a
// pass read years of months.
const maxMonthsBack = 36

// monthLayout is the YYYY-MM month key of a brand-month.
const monthLayout = "2006-01"

// subjectNamespace makes the subject id of a brand-month from its period and
// month (uuid v5), the idempotency key of the event.
var subjectNamespace = uuid.MustParse("6b2d3c1e-5a0f-4f7e-9c3a-2f8e1b7d4a90")

// SubjectID is the subject id of the white_label_month event of one period
// and month.
func SubjectID(periodID uuid.UUID, month time.Time) uuid.UUID {
	return uuid.NewSHA1(subjectNamespace, []byte(periodID.String()+":"+billing.MonthStart(month).Format(monthLayout)))
}

// Store is the query set a Charger needs. *gen.Queries satisfies it.
type Store interface {
	billing.AccountQueries
	ListWhiteLabelPeriodsForMonth(ctx context.Context, firstDay, lastDay time.Time, orgID *uuid.UUID) ([]gen.WhiteLabelPeriodRow, error)
	GetOrgBillingCountry(ctx context.Context, orgID uuid.UUID) (string, error)
	InsertBillableEvent(ctx context.Context, p gen.InsertBillableEventParams) (uuid.UUID, bool, error)
}

// Options configures a Charger.
type Options struct {
	// Since is the date X; zero means billing collection is off and Run does
	// nothing.
	Since time.Time
	// Now is the clock (tests); defaults to time.Now.
	Now func() time.Time
	// OrgID narrows a pass to one organization (integration tests in the
	// shared database); nil means every organization.
	OrgID *uuid.UUID
}

// Charger writes white_label_month events.
type Charger struct {
	store Store
	opts  Options
}

// New returns a Charger.
func New(store Store, opts Options) *Charger {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Charger{store: store, opts: opts}
}

// Enabled reports whether the date X is set.
func (c *Charger) Enabled() bool { return c != nil && !c.opts.Since.IsZero() }

// Run writes the events of every completed month from the month of the date
// X (at most maxMonthsBack months back) and returns how many were new.
func (c *Charger) Run(ctx context.Context) (int, error) {
	if !c.Enabled() {
		return 0, nil
	}
	current := billing.MonthStart(c.opts.Now())
	first := billing.MonthStart(c.opts.Since)
	if earliest := current.AddDate(0, -maxMonthsBack, 0); first.Before(earliest) {
		first = earliest
	}
	sinceDay := dayOf(c.opts.Since)
	inserted := 0
	for m := first; m.Before(current); m = m.AddDate(0, 1, 0) {
		n, err := c.month(ctx, m, sinceDay)
		inserted += n
		if err != nil {
			return inserted, fmt.Errorf("white label month %s: %w", m.Format(monthLayout), err)
		}
	}
	return inserted, nil
}

func (c *Charger) month(ctx context.Context, month, sinceDay time.Time) (int, error) {
	last := month.AddDate(0, 1, -1)
	periods, err := c.store.ListWhiteLabelPeriodsForMonth(ctx, month, last, c.opts.OrgID)
	if err != nil {
		return 0, err
	}
	inserted := 0
	for _, p := range periods {
		start := p.StartsOn
		if start.Before(sinceDay) {
			start = sinceDay
		}
		days := billing.WhiteLabelActiveDays(start, p.EndsOn, month)
		if days <= 0 {
			continue
		}
		country, err := c.store.GetOrgBillingCountry(ctx, p.OrgID)
		if err != nil {
			return inserted, fmt.Errorf("billing country of org %s: %w", p.OrgID, err)
		}
		acc, err := billing.EnsureAccount(ctx, c.store, p.OrgID, country)
		if err != nil {
			return inserted, err
		}
		attrs, err := json.Marshal(billing.WhiteLabelMonth{
			PeriodID: p.ID, PromoterID: p.PromoterID, Month: month.Format(monthLayout),
			DaysActive: days, DaysInMonth: billing.DaysInMonth(month),
			MonthlyPriceMinor: p.MonthlyPriceMinor, Currency: acc.Currency,
		})
		if err != nil {
			return inserted, err
		}
		_, isNew, err := c.store.InsertBillableEvent(ctx, gen.InsertBillableEventParams{
			Kind:       string(billing.KindWhiteLabelMonth),
			SubjectID:  SubjectID(p.ID, month),
			OccurredAt: billing.WhiteLabelMonthOccurredAt(month),
			OrgID:      p.OrgID,
			AccountID:  acc.ID,
			Attributes: attrs,
		})
		if err != nil {
			return inserted, fmt.Errorf("insert white label month of period %s: %w", p.ID, err)
		}
		if isNew {
			inserted++
		}
	}
	return inserted, nil
}

func dayOf(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// ── worker job ──────────────────────────────────────────────────────────

// Scheduler enqueues the next pass.
type Scheduler interface {
	ScheduleNext(ctx context.Context, at time.Time) error
}

// PGScheduler is the production Scheduler backed by worker_jobs.
type PGScheduler struct{ pool *pgxpool.Pool }

// NewPGScheduler wraps a pool.
func NewPGScheduler(pool *pgxpool.Pool) *PGScheduler { return &PGScheduler{pool: pool} }

// ScheduleNext inserts a pending job due at `at` unless one is already
// pending, so a retry never forks a second chain.
func (s *PGScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	const q = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1, '{}', 3, 'pending', $2
		WHERE  NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status = 'pending')`
	if _, err := s.pool.Exec(ctx, q, JobType, at); err != nil {
		return fmt.Errorf("billing white label: schedule next pass: %w", err)
	}
	return nil
}

// ScheduleInitialJob seeds the first pass at worker start unless a job is
// already pending or running.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool) error {
	const q = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1, '{}', 3, 'pending', now() + interval '2 minutes'
		WHERE  NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status IN ('pending', 'claimed'))`
	if _, err := pool.Exec(ctx, q, JobType); err != nil {
		return fmt.Errorf("billing white label: enqueue initial job: %w", err)
	}
	return nil
}

// HandlerOptions configures NewHandler.
type HandlerOptions struct {
	Charger   *Charger
	Scheduler Scheduler
	Interval  time.Duration
	Logger    *slog.Logger
}

// NewHandler returns the handler of job_type JobType. Without the date X it
// does nothing and does not schedule itself again. The next pass is
// scheduled BEFORE the work, so a failed pass does not end the chain.
func NewHandler(opts HandlerOptions) func(ctx context.Context, payload []byte) error {
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return func(ctx context.Context, _ []byte) error {
		if !opts.Charger.Enabled() {
			return nil
		}
		if opts.Scheduler != nil {
			if err := opts.Scheduler.ScheduleNext(ctx, time.Now().Add(opts.Interval)); err != nil {
				return err
			}
		}
		n, err := opts.Charger.Run(ctx)
		if err != nil {
			opts.Logger.Error("billing white label pass failed", "error", err.Error(), "inserted", n)
			return err
		}
		if n > 0 {
			opts.Logger.Info("billing white label pass", "inserted", n)
		}
		return nil
	}
}

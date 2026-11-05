// Package whitelabelcharge writes the billing facts of white label (spec
// 08_architecture/53): one billable event white_label_month per white label
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
//     country (legal address, else country), as for a sale without a venue;
//   - a non-zero individual price is charged only in the currency it was
//     typed in: when the account's currency is another one now, the month is
//     not written and the pass reports it;
//   - one broken period never holds back the others: its error is reported
//     and the pass goes on (the next pass tries it again).
//
// The walk over months, the day count, the event insert and the worker job
// are shared with the shop module charge (package monthcharge).
package whitelabelcharge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/monthcharge"
)

// JobType is the worker_jobs.job_type of the charge job.
const JobType = "billing.white_label"

// DefaultInterval is the gap between passes: a month closes once a day is
// late enough to be seen.
const DefaultInterval = monthcharge.DefaultInterval

// logLabel names the job in log lines and errors.
const logLabel = "billing white label"

// subjectNamespace makes the subject id of a brand-month from its period and
// month (uuid v5), the idempotency key of the event.
var subjectNamespace = uuid.MustParse("6b2d3c1e-5a0f-4f7e-9c3a-2f8e1b7d4a90")

// SubjectID is the subject id of the white_label_month event of one period
// and month.
func SubjectID(periodID uuid.UUID, month time.Time) uuid.UUID {
	return monthcharge.SubjectID(subjectNamespace, periodID, month)
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
// X (at most monthcharge.MaxMonthsBack months back) and returns how many were
// new.
func (c *Charger) Run(ctx context.Context) (int, error) {
	if !c.Enabled() {
		return 0, nil
	}
	sinceDay := monthcharge.DayOf(c.opts.Since)
	return monthcharge.Walk(ctx, "white label month", c.opts.Since, c.opts.Now(),
		func(ctx context.Context, month time.Time) (int, error) { return c.month(ctx, month, sinceDay) })
}

// ErrCurrencyChanged is reported for a period whose non-zero individual price
// is in another currency than the organization's billing account now.
var ErrCurrencyChanged = errors.New("individual white label price is in another currency than the billing account")

func (c *Charger) month(ctx context.Context, month, sinceDay time.Time) (int, error) {
	last := month.AddDate(0, 1, -1)
	periods, err := c.store.ListWhiteLabelPeriodsForMonth(ctx, month, last, c.opts.OrgID)
	if err != nil {
		return 0, err
	}
	inserted := 0
	var errs []error
	for _, p := range periods {
		if ctx.Err() != nil {
			return inserted, errors.Join(append(errs, ctx.Err())...)
		}
		isNew, err := c.charge(ctx, p, month, sinceDay)
		if err != nil {
			errs = append(errs, fmt.Errorf("period %s: %w", p.ID, err))
			continue
		}
		if isNew {
			inserted++
		}
	}
	return inserted, errors.Join(errs...)
}

// charge writes the event of one period and month and reports whether it was
// new; a month without active days writes nothing.
func (c *Charger) charge(ctx context.Context, p gen.WhiteLabelPeriodRow, month, sinceDay time.Time) (bool, error) {
	return monthcharge.ChargeMonth(ctx, c.store, monthcharge.Spec{
		Kind:        billing.KindWhiteLabelMonth,
		Namespace:   subjectNamespace,
		ErrCurrency: ErrCurrencyChanged,
		Attributes: func(_ monthcharge.Period, acc gen.BillingAccountRow, month time.Time, days int64) ([]byte, error) {
			return json.Marshal(billing.WhiteLabelMonth{
				PeriodID: p.ID, PromoterID: p.PromoterID, Month: month.Format(monthcharge.MonthLayout),
				DaysActive: days, DaysInMonth: billing.DaysInMonth(month),
				MonthlyPriceMinor: p.MonthlyPriceMinor, Currency: acc.Currency,
			})
		},
	}, monthcharge.Period{
		ID: p.ID, OrgID: p.OrgID, StartsOn: p.StartsOn, EndsOn: p.EndsOn,
		MonthlyPriceMinor: p.MonthlyPriceMinor, Currency: p.Currency,
	}, month, sinceDay)
}

// ── worker job ──────────────────────────────────────────────────────────

// Scheduler enqueues the next pass.
type Scheduler = monthcharge.Scheduler

// PGScheduler is the production Scheduler backed by worker_jobs.
type PGScheduler = monthcharge.PGScheduler

// NewPGScheduler wraps a pool.
func NewPGScheduler(pool *pgxpool.Pool) *PGScheduler {
	return monthcharge.NewPGScheduler(pool, JobType, logLabel)
}

// ScheduleInitialJob seeds the first pass at worker start unless a job is
// already pending or running.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool) error {
	return monthcharge.ScheduleInitialJob(ctx, pool, JobType, logLabel)
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
	return monthcharge.NewHandler(monthcharge.HandlerOptions{
		Runner: opts.Charger, Scheduler: opts.Scheduler, Interval: opts.Interval, Logger: opts.Logger,
		Label: logLabel,
	})
}

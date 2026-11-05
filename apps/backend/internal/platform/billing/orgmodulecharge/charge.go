// Package orgmodulecharge writes the billing facts of the paid organization
// modules (spec 08_architecture/61 section 3.2, today only the shop): one
// billable event shop_module_month per module period and completed calendar
// month (UTC), prorated by days. The rate job (billing.rate) turns it into a
// charge with the subscription rule of the price list for shop_module_month,
// or the period's individual monthly price. It is the white label charge
// (package whitelabelcharge) over org_module_periods; the shared parts live in
// package monthcharge.
//
// Rules:
//   - only completed months are charged, in arrears; the month runs in UTC
//     and the event is dated noon UTC on its last day;
//   - days before the date X (BILLING_COLLECT_SINCE) are never charged, like
//     sales before it; without the date X the job does nothing;
//   - the event is idempotent by (shop_module_month, uuid of period+month),
//     so a pass may run any number of times;
//   - the account is the organization's billing account for its billing
//     country (legal address, else country);
//   - a non-zero individual price is charged only in the currency it was
//     typed in: when the account's currency is another one now, the month is
//     not written and the pass reports it;
//   - a period of an organization deleted before the month starts pays
//     nothing for it; the month of the deletion is charged for the period's
//     days, like white label;
//   - one broken period never holds back the others: its error is reported
//     and the pass goes on (the next pass tries it again).
package orgmodulecharge

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
const JobType = "billing.org_modules"

// DefaultInterval is the gap between passes.
const DefaultInterval = monthcharge.DefaultInterval

// logLabel names the job in log lines and errors.
const logLabel = "billing org modules"

// subjectNamespace makes the subject id of a module month from its period and
// month (uuid v5), the idempotency key of the event.
var subjectNamespace = uuid.MustParse("a3c1f0d2-7b84-4e65-b19a-5d2e9c4f7a18")

// SubjectID is the subject id of the shop_module_month event of one period
// and month.
func SubjectID(periodID uuid.UUID, month time.Time) uuid.UUID {
	return monthcharge.SubjectID(subjectNamespace, periodID, month)
}

// Store is the query set a Charger needs. *gen.Queries satisfies it.
type Store interface {
	billing.AccountQueries
	ListOrgModulePeriodsForMonth(ctx context.Context, module string, firstDay, lastDay time.Time, orgID *uuid.UUID) ([]gen.OrgModulePeriodRow, error)
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

// Charger writes shop_module_month events.
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

// ErrCurrencyChanged is reported for a period whose non-zero individual price
// is in another currency than the organization's billing account now.
var ErrCurrencyChanged = errors.New("individual shop module price is in another currency than the billing account")

// Run writes the events of every completed month from the month of the date
// X (at most monthcharge.MaxMonthsBack months back) and returns how many were
// new.
func (c *Charger) Run(ctx context.Context) (int, error) {
	if !c.Enabled() {
		return 0, nil
	}
	sinceDay := monthcharge.DayOf(c.opts.Since)
	return monthcharge.Walk(ctx, "shop module month", c.opts.Since, c.opts.Now(),
		func(ctx context.Context, month time.Time) (int, error) { return c.month(ctx, month, sinceDay) })
}

func (c *Charger) month(ctx context.Context, month, sinceDay time.Time) (int, error) {
	last := month.AddDate(0, 1, -1)
	periods, err := c.store.ListOrgModulePeriodsForMonth(ctx, billing.ModuleShop, month, last, c.opts.OrgID)
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
func (c *Charger) charge(ctx context.Context, p gen.OrgModulePeriodRow, month, sinceDay time.Time) (bool, error) {
	return monthcharge.ChargeMonth(ctx, c.store, monthcharge.Spec{
		Kind:        billing.KindShopModuleMonth,
		Namespace:   subjectNamespace,
		ErrCurrency: ErrCurrencyChanged,
		Attributes: func(_ monthcharge.Period, acc gen.BillingAccountRow, month time.Time, days int64) ([]byte, error) {
			return json.Marshal(billing.ShopModuleMonth{
				PeriodID: p.ID, Module: p.Module, Month: month.Format(monthcharge.MonthLayout),
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

// NewPGScheduler wraps a pool.
func NewPGScheduler(pool *pgxpool.Pool) *monthcharge.PGScheduler {
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
	Scheduler monthcharge.Scheduler
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

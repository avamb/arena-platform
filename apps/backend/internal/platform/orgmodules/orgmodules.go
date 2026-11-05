// Package orgmodules answers one question for every surface of the product:
// does this organization have this paid module today? (spec
// 08_architecture/61, owner decisions 2026-11-05.) The first module is the
// shop.
//
// A module is connected for a PERIOD (org_module_periods, migration 0198):
// from starts_on to ends_on, both inclusive UTC calendar days, ends_on NULL =
// no end. Only a platform superadmin writes periods (Connect, End, Reprice);
// a period is closed by ends_on and never deleted; the periods of one
// (organization, module) never share a day.
//
// The shop has a second state after its module is switched off: "orders
// only" (ShopMode). While the organization still has an OPEN shop order
// (awaiting payment, in manual review, or paid and not shipped - the single
// definition is gen.openShopOrderPredicate) the order routes stay open so the
// goods can be shipped or refunded; catalog, settings and pages are gone.
//
// Reads take a Querier (a *gen.Queries over a pool or a transaction); writes
// take the caller's pgx.Tx and never commit.
package orgmodules

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Modules. A new module is one more value here AND in the CHECK
// org_module_periods_module_check.
const ModuleShop = "shop"

// Mode is how much of the shop an organization has.
type Mode string

const (
	// ModeFull: the module is connected today.
	ModeFull Mode = "full"
	// ModeOrdersOnly: the module is off, but open shop orders remain: only the
	// order routes work.
	ModeOrdersOnly Mode = "orders_only"
	// ModeNone: no shop section at all.
	ModeNone Mode = ""
)

// Errors of the write functions.
var (
	// ErrOverlap: the new period shares a day with another period of the same
	// (organization, module).
	ErrOverlap = errors.New("orgmodules: period overlaps another period of this module")
	// ErrInvalid: the input is not acceptable (the error text says which).
	ErrInvalid = errors.New("orgmodules: invalid input")
	// ErrNotFound: no such period.
	ErrNotFound = errors.New("orgmodules: period not found")
	// ErrAlreadyEnded: the period already ends on or before the requested day;
	// ending never extends a period.
	ErrAlreadyEnded = errors.New("orgmodules: period already ends on or before that day")
)

// maxMonthlyPriceMinor is the largest individual monthly price accepted, in
// minor units (the cap white label uses).
const maxMonthlyPriceMinor = 1_000_000_000

var currencyRE = regexp.MustCompile(`^[A-Z]{3}$`)

// Querier is the query set the package reads and writes with. *gen.Queries
// satisfies it.
type Querier interface {
	OrgModuleActive(ctx context.Context, orgID uuid.UUID, module string, day time.Time) (bool, error)
	ActiveOrgModules(ctx context.Context, orgID uuid.UUID, day time.Time) ([]string, error)
	OrgModuleStates(ctx context.Context, orgIDs []uuid.UUID, day time.Time) ([]gen.OrgModuleStateRow, error)
	ListOrgModulePeriods(ctx context.Context, orgID uuid.UUID, module *string) ([]gen.OrgModulePeriodRow, error)
}

// Period is one org_module_periods row. StartsOn and EndsOn are UTC calendar
// days at midnight; a nil EndsOn has no end. MonthlyPriceMinor is the
// individual monthly price (nil = the price list, 0 = free) in Currency.
type Period struct {
	ID                uuid.UUID
	OrgID             uuid.UUID
	Module            string
	StartsOn          time.Time
	EndsOn            *time.Time
	MonthlyPriceMinor *int64
	Currency          *string
	Reason            string
	CreatedBy         *uuid.UUID
	CreatedAt         time.Time
	EndedBy           *uuid.UUID
	EndedAt           *time.Time
}

// Covers reports whether the period includes the UTC calendar day of t.
func (p Period) Covers(t time.Time) bool {
	day := Day(t)
	if p.StartsOn.After(day) {
		return false
	}
	return p.EndsOn == nil || !p.EndsOn.Before(day)
}

func periodFromRow(r gen.OrgModulePeriodRow) Period {
	p := Period{
		ID: r.ID, OrgID: r.OrgID, Module: r.Module,
		StartsOn:          Day(r.StartsOn),
		MonthlyPriceMinor: r.MonthlyPriceMinor, Currency: r.Currency, Reason: r.Reason,
		CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, EndedBy: r.EndedBy, EndedAt: r.EndedAt,
	}
	if r.EndsOn != nil {
		e := Day(*r.EndsOn)
		p.EndsOn = &e
	}
	return p
}

// Day is the UTC calendar day of t, at midnight: the day periods are
// compared with.
func Day(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// KnownModule reports whether module is a module of the platform.
func KnownModule(module string) bool { return module == ModuleShop }

// State is what an organization has on one day.
type State struct {
	// Modules are the modules connected on the day, sorted.
	Modules []string
	// ShopMode is full, orders_only or none.
	ShopMode Mode
	// OpenShopOrders: the organization has an open shop order.
	OpenShopOrders bool
}

// Has reports whether module is connected in this state.
func (s State) Has(module string) bool {
	for _, m := range s.Modules {
		if m == module {
			return true
		}
	}
	return false
}

// decideShopMode is the whole rule of the shop's mode.
func decideShopMode(moduleActive, openOrders bool) Mode {
	switch {
	case moduleActive:
		return ModeFull
	case openOrders:
		return ModeOrdersOnly
	}
	return ModeNone
}

// Active reports whether a period of (org, module) covers the UTC day of day.
func Active(ctx context.Context, q Querier, orgID uuid.UUID, module string, day time.Time) (bool, error) {
	return q.OrgModuleActive(ctx, orgID, module, Day(day))
}

// ActiveModules lists, sorted, the modules the organization has on the UTC
// day of now.
func ActiveModules(ctx context.Context, q Querier, orgID uuid.UUID, now time.Time) ([]string, error) {
	return q.ActiveOrgModules(ctx, orgID, Day(now))
}

// ShopMode is full when the shop module is connected on the UTC day of now,
// else orders_only when the organization has at least one OPEN shop order,
// else none. "Open" is defined once, in gen.openShopOrderPredicate.
func ShopMode(ctx context.Context, q Querier, orgID uuid.UUID, now time.Time) (Mode, error) {
	states, err := States(ctx, q, []uuid.UUID{orgID}, now)
	if err != nil {
		return ModeNone, err
	}
	return states[orgID].ShopMode, nil
}

// States answers for many organizations in ONE query (the /v1/me handler lists
// every membership; a superadmin lists every organization). Every id in
// orgIDs has an entry in the result.
func States(ctx context.Context, q Querier, orgIDs []uuid.UUID, now time.Time) (map[uuid.UUID]State, error) {
	out := make(map[uuid.UUID]State, len(orgIDs))
	if len(orgIDs) == 0 {
		return out, nil
	}
	rows, err := q.OrgModuleStates(ctx, orgIDs, Day(now))
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		s := State{Modules: r.Modules, OpenShopOrders: r.OpenShopOrders}
		s.ShopMode = decideShopMode(s.Has(ModuleShop), r.OpenShopOrders)
		out[r.OrgID] = s
	}
	for _, id := range orgIDs {
		if _, ok := out[id]; !ok {
			out[id] = State{}
		}
	}
	return out, nil
}

// List returns every period of the organization (all modules), oldest first
// per module.
func List(ctx context.Context, q Querier, orgID uuid.UUID) ([]Period, error) {
	rows, err := q.ListOrgModulePeriods(ctx, orgID, nil)
	if err != nil {
		return nil, err
	}
	out := make([]Period, 0, len(rows))
	for _, r := range rows {
		out = append(out, periodFromRow(r))
	}
	return out, nil
}

// ConnectInput describes a new period. MonthlyPriceMinor and Currency are both
// nil (the price list) or both set (an individual price, 0 = free). EndsOn is
// optional (nil = no end).
type ConnectInput struct {
	OrgID             uuid.UUID
	Module            string
	StartsOn          time.Time
	EndsOn            *time.Time
	MonthlyPriceMinor *int64
	Currency          *string
	Reason            string
	ActorID           *uuid.UUID
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// validatePrice checks the price/currency pair and returns the normalized
// currency.
func validatePrice(price *int64, currency *string) (*string, error) {
	if (price == nil) != (currency == nil) {
		return nil, invalid("monthly price and currency are given together or not at all")
	}
	if price == nil {
		return nil, nil
	}
	if *price < 0 || *price > maxMonthlyPriceMinor {
		return nil, invalid("monthly price must be between 0 and %d minor units", maxMonthlyPriceMinor)
	}
	cur := strings.ToUpper(strings.TrimSpace(*currency))
	if !currencyRE.MatchString(cur) {
		return nil, invalid("currency must be a 3-letter code")
	}
	return &cur, nil
}

// validate checks a ConnectInput and returns its normalized form (days cut to
// UTC midnight, trimmed reason, upper-case currency).
func (in ConnectInput) validate() (ConnectInput, error) {
	if in.OrgID == uuid.Nil {
		return in, invalid("org_id is required")
	}
	if !KnownModule(in.Module) {
		return in, invalid("unknown module %q", in.Module)
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" {
		return in, invalid("a reason is required")
	}
	if in.StartsOn.IsZero() {
		return in, invalid("starts_on is required")
	}
	in.StartsOn = Day(in.StartsOn)
	if in.EndsOn != nil {
		e := Day(*in.EndsOn)
		if e.Before(in.StartsOn.AddDate(0, 0, -1)) {
			return in, invalid("ends_on cannot be earlier than the day before starts_on")
		}
		in.EndsOn = &e
	}
	cur, err := validatePrice(in.MonthlyPriceMinor, in.Currency)
	if err != nil {
		return in, err
	}
	in.Currency = cur
	return in, nil
}

// Connect opens a period. The caller supplies the transaction and commits.
// ErrOverlap when another period of the same (organization, module) shares a
// day with it, ErrInvalid for bad input.
func Connect(ctx context.Context, tx pgx.Tx, in ConnectInput) (Period, error) {
	in, err := in.validate()
	if err != nil {
		return Period{}, err
	}
	q := gen.New(tx)
	if err := q.LockOrgModule(ctx, in.OrgID, in.Module); err != nil {
		return Period{}, fmt.Errorf("orgmodules: lock: %w", err)
	}
	clash, err := q.ListOrgModulePeriodsOverlappingForUpdate(ctx, in.OrgID, in.Module, in.StartsOn, in.EndsOn, nil)
	if err != nil {
		return Period{}, fmt.Errorf("orgmodules: overlap: %w", err)
	}
	if len(clash) > 0 {
		return Period{}, ErrOverlap
	}
	row, err := q.InsertOrgModulePeriod(ctx, gen.InsertOrgModulePeriodParams{
		OrgID: in.OrgID, Module: in.Module, StartsOn: in.StartsOn, EndsOn: in.EndsOn,
		MonthlyPriceMinor: in.MonthlyPriceMinor, Currency: in.Currency, Reason: in.Reason, CreatedBy: in.ActorID,
	})
	if err != nil {
		return Period{}, fmt.Errorf("orgmodules: insert: %w", err)
	}
	return periodFromRow(row), nil
}

// lockPeriod reads the period, takes the (organization, module) lock and
// re-reads the row locked. The advisory lock is taken before the row lock in
// every write, as Connect does, so two writers cannot deadlock.
func lockPeriod(ctx context.Context, q *gen.Queries, periodID uuid.UUID) (Period, error) {
	first, err := q.GetOrgModulePeriod(ctx, periodID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Period{}, ErrNotFound
	}
	if err != nil {
		return Period{}, fmt.Errorf("orgmodules: get: %w", err)
	}
	if err := q.LockOrgModule(ctx, first.OrgID, first.Module); err != nil {
		return Period{}, fmt.Errorf("orgmodules: lock: %w", err)
	}
	row, err := q.GetOrgModulePeriodForUpdate(ctx, periodID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Period{}, ErrNotFound
	}
	if err != nil {
		return Period{}, fmt.Errorf("orgmodules: get for update: %w", err)
	}
	return periodFromRow(row), nil
}

// checkEnd decides whether cur may be ended on endsOn.
func checkEnd(cur Period, endsOn time.Time) error {
	if endsOn.Before(cur.StartsOn.AddDate(0, 0, -1)) {
		return invalid("ends_on cannot be earlier than the day before the period starts")
	}
	if cur.EndsOn != nil && !endsOn.Before(*cur.EndsOn) {
		return ErrAlreadyEnded
	}
	return nil
}

// End sets the last day of a period, never later than its current end. The
// reason belongs to the caller's audit row (the table keeps only the reason a
// period was created with) and must not be empty. ErrNotFound, ErrAlreadyEnded
// when the period already ends on or before endsOn, ErrInvalid when endsOn is
// earlier than the day before the period starts.
func End(ctx context.Context, tx pgx.Tx, periodID uuid.UUID, endsOn time.Time, reason string, actor *uuid.UUID) (Period, error) {
	if strings.TrimSpace(reason) == "" {
		return Period{}, invalid("a reason is required")
	}
	if endsOn.IsZero() {
		return Period{}, invalid("ends_on is required")
	}
	endsOn = Day(endsOn)
	q := gen.New(tx)
	cur, err := lockPeriod(ctx, q, periodID)
	if err != nil {
		return Period{}, err
	}
	if err := checkEnd(cur, endsOn); err != nil {
		return Period{}, err
	}
	row, err := q.EndOrgModulePeriod(ctx, periodID, endsOn, actor)
	if errors.Is(err, pgx.ErrNoRows) {
		return Period{}, ErrAlreadyEnded
	}
	if err != nil {
		return Period{}, fmt.Errorf("orgmodules: end: %w", err)
	}
	return periodFromRow(row), nil
}

// Reprice changes the price from the day `from` on: it ends the period on
// from-1 and opens a new one from `from` with the new price and the same
// ends_on as the old one (open-ended if it was), atomically in the caller's
// transaction. It returns the NEW period. `from` must not be before the
// period's start; a period that ends before `from` cannot be repriced
// (ErrAlreadyEnded). The reason is stored on the new period.
func Reprice(ctx context.Context, tx pgx.Tx, periodID uuid.UUID, from time.Time, price *int64, currency *string, reason string, actor *uuid.UUID) (Period, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Period{}, invalid("a reason is required")
	}
	if from.IsZero() {
		return Period{}, invalid("from is required")
	}
	from = Day(from)
	cur, err := validatePrice(price, currency)
	if err != nil {
		return Period{}, err
	}
	q := gen.New(tx)
	old, err := lockPeriod(ctx, q, periodID)
	if err != nil {
		return Period{}, err
	}
	if from.Before(old.StartsOn) {
		return Period{}, invalid("from cannot be before the period starts (%s)", old.StartsOn.Format("2006-01-02"))
	}
	if old.EndsOn != nil && old.EndsOn.Before(from) {
		return Period{}, ErrAlreadyEnded
	}
	dayBefore := from.AddDate(0, 0, -1)
	if _, err := q.EndOrgModulePeriod(ctx, periodID, dayBefore, actor); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Period{}, ErrAlreadyEnded
		}
		return Period{}, fmt.Errorf("orgmodules: end old: %w", err)
	}
	clash, err := q.ListOrgModulePeriodsOverlappingForUpdate(ctx, old.OrgID, old.Module, from, old.EndsOn, nil)
	if err != nil {
		return Period{}, fmt.Errorf("orgmodules: overlap: %w", err)
	}
	if len(clash) > 0 {
		return Period{}, ErrOverlap
	}
	row, err := q.InsertOrgModulePeriod(ctx, gen.InsertOrgModulePeriodParams{
		OrgID: old.OrgID, Module: old.Module, StartsOn: from, EndsOn: old.EndsOn,
		MonthlyPriceMinor: price, Currency: cur, Reason: reason, CreatedBy: actor,
	})
	if err != nil {
		return Period{}, fmt.Errorf("orgmodules: insert: %w", err)
	}
	return periodFromRow(row), nil
}

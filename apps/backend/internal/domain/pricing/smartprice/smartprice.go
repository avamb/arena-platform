// Package smartprice expands a "smart price" preset (spec
// 08_architecture/51_4_2_smart_price_ru.md) into the price mechanics arena
// already has: a chain of categories with sell limits (migration 0114) for a
// rise by tickets sold, and price windows (priceSchedule) for a rise by days.
// It is a pure function of the preset and the category: nothing here reads
// or writes the database, and nothing here decides a price at sale time
// (that stays the job of the chain sweep and priceresolve).
//
// Owner decisions 2026-11-02: every step adds the SAME percentage of the
// starting price (20 -> 22 -> 24, not compound), a step price is rounded UP to
// a whole unit of the currency, and the cap is mandatory.
package smartprice

import (
	"errors"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating/layoutgen"
)

// Preset kinds.
const (
	// KindBySales raises the price every N tickets (a chain of categories).
	KindBySales = "by_sales"
	// KindByDays raises the price every N days from a start day (price windows).
	KindByDays = "by_days"
	// KindLastDays raises the price once, N days before the session (one window).
	KindLastDays = "last_days"
)

// Limits of the parameters. MaxSteps bounds a chain; MaxWindows is the
// price-schedule limit of the API (hcatalog maxPriceWindows).
const (
	MaxStepPercent = 100
	MaxCapPercent  = 500
	MaxEveryTicket = 100_000
	MaxEveryDays   = 365
	MaxSteps       = 20
	MaxWindows     = 50
)

// Error is a refusal with a stable code for the API.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func fail(code, msg string) error { return &Error{Code: code, Message: msg} }

// Errors the expansion answers with.
var (
	ErrNotEnoughPlaces = fail("smart_price.not_enough_places", "the category has too few free places for a second step")
	ErrNoRoom          = fail("smart_price.no_room", "no price change fits before the end of sales")
	ErrTooLate         = fail("smart_price.too_late", "the price change would start in the past")
	ErrFreeCategory    = fail("smart_price.free_category", "a free category has no price to raise")
)

// Params is a preset as the organizer set it.
type Params struct {
	Kind string `json:"kind"`
	// StepPercent is the rise of each step, a percentage of the starting price.
	StepPercent int `json:"step_percent"`
	// Every is the step length: tickets for by_sales, days for by_days and,
	// for last_days, how many days before the session the price rises.
	Every int `json:"every"`
	// CapPercent is the highest rise over the starting price (by_sales,
	// by_days); last_days rises exactly StepPercent.
	CapPercent int `json:"cap_percent,omitempty"`
	// StartDate (YYYY-MM-DD, venue calendar) is the day by_days counts from;
	// "" counts from today.
	StartDate string `json:"start_date,omitempty"`
}

// Validate checks the parameters alone.
func (p Params) Validate() error {
	switch p.Kind {
	case KindBySales, KindByDays, KindLastDays:
	default:
		return fail("smart_price.invalid_kind", "kind must be by_sales, by_days or last_days")
	}
	if p.StepPercent < 1 || p.StepPercent > MaxStepPercent {
		return fail("smart_price.invalid_step", "step_percent must be between 1 and 100")
	}
	maxEvery := MaxEveryDays
	if p.Kind == KindBySales {
		maxEvery = MaxEveryTicket
	}
	if p.Every < 1 || p.Every > maxEvery {
		return fail("smart_price.invalid_every", "every is out of range")
	}
	if p.Kind != KindLastDays && (p.CapPercent < p.StepPercent || p.CapPercent > MaxCapPercent) {
		return fail("smart_price.invalid_cap", "cap_percent must be at least step_percent and at most 500")
	}
	if p.StartDate != "" {
		if p.Kind != KindByDays {
			return fail("smart_price.invalid_start", "start_date belongs to by_days only")
		}
		if _, err := time.Parse("2006-01-02", p.StartDate); err != nil { // allow:timeformat: calendar day
			return fail("smart_price.invalid_start", "start_date must be YYYY-MM-DD")
		}
	}
	return nil
}

// unit is one whole unit of the currency in minor units.
func unit(currency string) int64 {
	u := int64(1)
	for i := 0; i < layoutgen.MinorExponent(strings.ToUpper(currency)); i++ {
		u *= 10
	}
	return u
}

// roundUp rounds base * (100+pct) / 100 up to a whole unit of the currency.
func roundUp(base int64, pct int, currency string) int64 {
	num := base * int64(100+pct)
	minor := (num + 99) / 100
	u := unit(currency)
	return (minor + u - 1) / u * u
}

// StepPrice is the price of step k (0 = the starting price itself, kept as
// typed), capped at CapPrice.
func StepPrice(base int64, k, stepPct, capPct int, currency string) int64 {
	if k == 0 {
		return base
	}
	return min(roundUp(base, k*stepPct, currency), CapPrice(base, capPct, currency))
}

// CapPrice is the highest price the preset reaches.
func CapPrice(base int64, capPct int, currency string) int64 {
	return roundUp(base, capPct, currency)
}

// SalesInput is the category a by_sales preset is applied to.
type SalesInput struct {
	Base     int64
	Currency string
	// Owned is the number of places the category owns (sold, held and free).
	Owned int32
	// Used is how many of them are sold or held: the first step sells Every
	// MORE tickets from now.
	Used int32
}

// SalesStep is one category of the chain. Limit is the chain link's
// sell_limit (sold + held of THIS category that hands its places on); 0 for
// the last step, which sells the rest.
type SalesStep struct {
	Limit int32 `json:"limit"`
	// Places is how many places the step sells (for the preview table).
	Places int32 `json:"places"`
	Price  int64 `json:"price"`
}

// ExpandBySales lays a by_sales preset out as chain steps. The first step is
// the category itself; the last one takes every place left.
func ExpandBySales(p Params, in SalesInput) ([]SalesStep, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if p.Kind != KindBySales {
		return nil, fail("smart_price.invalid_kind", "not a by_sales preset")
	}
	if in.Base <= 0 {
		return nil, ErrFreeCategory
	}
	every := int32(p.Every)
	rest := in.Owned - in.Used
	if rest <= every {
		return nil, ErrNotEnoughPlaces
	}
	capPrice := CapPrice(in.Base, p.CapPercent, in.Currency)
	var steps []SalesStep
	for k := 0; ; k++ {
		price := StepPrice(in.Base, k, p.StepPercent, p.CapPercent, in.Currency)
		next := StepPrice(in.Base, k+1, p.StepPercent, p.CapPercent, in.Currency)
		last := price >= capPrice || rest <= every || k+1 >= MaxSteps || next <= price
		if last {
			steps = append(steps, SalesStep{Places: rest, Price: price})
			return steps, nil
		}
		limit := every
		if k == 0 {
			limit += in.Used
		}
		steps = append(steps, SalesStep{Limit: limit, Places: every, Price: price})
		rest -= every
	}
}

// DaysInput is the session a by_days or last_days preset is applied to.
type DaysInput struct {
	Base     int64
	Currency string
	Loc      *time.Location
	// SessionStart and SalesEnd bound the windows.
	SessionStart time.Time
	SalesEnd     time.Time
	Now          time.Time
}

// Window is one price window. To is nil for the last one (until sales end).
type Window struct {
	From  time.Time  `json:"from"`
	To    *time.Time `json:"to,omitempty"`
	Price int64      `json:"price"`
}

// midnight is the start of day t in loc.
func midnight(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// ExpandByDays lays a by_days or last_days preset out as price windows. The
// starting price needs no window: before the first window the category's own
// price applies.
func ExpandByDays(p Params, in DaysInput) ([]Window, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if in.Base <= 0 {
		return nil, ErrFreeCategory
	}
	loc := in.Loc
	if loc == nil {
		loc = time.UTC
	}
	switch p.Kind {
	case KindLastDays:
		from := midnight(in.SessionStart, loc).AddDate(0, 0, -p.Every)
		if !from.After(in.Now) {
			return nil, ErrTooLate
		}
		if !from.Before(in.SalesEnd) {
			return nil, ErrNoRoom
		}
		return []Window{{From: from, Price: roundUp(in.Base, p.StepPercent, in.Currency)}}, nil
	case KindByDays:
	default:
		return nil, fail("smart_price.invalid_kind", "not a by_days preset")
	}
	start := midnight(in.Now, loc)
	if p.StartDate != "" {
		d, _ := time.ParseInLocation("2006-01-02", p.StartDate, loc) // allow:timeformat: calendar day
		if d.Before(start) {
			return nil, ErrTooLate
		}
		start = d
	}
	capPrice := CapPrice(in.Base, p.CapPercent, in.Currency)
	var out []Window
	for k := 1; ; k++ {
		from := start.AddDate(0, 0, k*p.Every)
		if !from.Before(in.SalesEnd) {
			break
		}
		price := StepPrice(in.Base, k, p.StepPercent, p.CapPercent, in.Currency)
		if n := len(out); n > 0 {
			to := from
			out[n-1].To = &to
		}
		out = append(out, Window{From: from, Price: price})
		if price >= capPrice || len(out) >= MaxWindows {
			break
		}
	}
	if len(out) == 0 {
		return nil, ErrNoRoom
	}
	return out, nil
}

// AsError unwraps a smartprice refusal.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

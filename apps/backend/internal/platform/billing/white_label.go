package billing

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// WhiteLabelMonth is what a white_label_month billable event carries in its
// attributes (spec 08_architecture/53): which period and month it charges,
// how many days of the month the period covered, and the period's individual
// monthly price (nil = the price list; in Currency, the account currency).
type WhiteLabelMonth struct {
	PeriodID          uuid.UUID  `json:"white_label_period_id"`
	PromoterID        *uuid.UUID `json:"promoter_id,omitempty"`
	Month             string     `json:"month"`
	DaysActive        int64      `json:"days_active"`
	DaysInMonth       int64      `json:"days_in_month"`
	MonthlyPriceMinor *int64     `json:"monthly_price_minor,omitempty"`
	Currency          string     `json:"currency,omitempty"`
}

// MonthStart is the first instant (UTC) of the calendar month of t.
func MonthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// DaysInMonth is the number of days of the calendar month starting at month.
func DaysInMonth(month time.Time) int64 {
	return int64(MonthStart(month).AddDate(0, 1, -1).Day())
}

// WhiteLabelActiveDays counts the days of the calendar month starting at
// month that the period [startsOn, endsOn] covers, both ends inclusive; a nil
// endsOn means the period has no end. Dates are calendar days in UTC.
func WhiteLabelActiveDays(startsOn time.Time, endsOn *time.Time, month time.Time) int64 {
	first := MonthStart(month)
	last := first.AddDate(0, 1, -1)
	from := dayOf(startsOn)
	if from.Before(first) {
		from = first
	}
	to := last
	if endsOn != nil {
		if e := dayOf(*endsOn); e.Before(to) {
			to = e
		}
	}
	if to.Before(from) {
		return 0
	}
	return int64(to.Sub(from).Hours()/24) + 1
}

// WhiteLabelMonthOccurredAt is the instant a brand-month is billed at: noon
// UTC on the last day of the month, so the month is the same in every zone an
// invoice is cut in.
func WhiteLabelMonthOccurredAt(month time.Time) time.Time {
	return MonthStart(month).AddDate(0, 1, -1).Add(12 * time.Hour)
}

// WhiteLabelFromAttributes copies the proration of a white_label_month event
// into ev. Unreadable attributes leave ev as it is; Rate then refuses the
// event (days_in_month 0), so a broken row is never charged at zero.
func WhiteLabelFromAttributes(raw []byte, ev *Event) {
	if len(raw) == 0 {
		return
	}
	var m WhiteLabelMonth
	if err := json.Unmarshal(raw, &m); err != nil {
		return
	}
	ev.DaysActive = m.DaysActive
	ev.DaysInMonth = m.DaysInMonth
	ev.MonthlyPriceMinor = m.MonthlyPriceMinor
}

func dayOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// WhiteLabelMonthlyMinor is the monthly white label price of the price list
// the terms name, in the account currency (Terms.Currency); false when the
// price list has no such rule.
func (t Terms) WhiteLabelMonthlyMinor() (int64, bool) {
	r, ok := t.rule(lineStandard, string(KindWhiteLabelMonth), RuleSubscription, "")
	if !ok || r.Subscription == nil {
		return 0, false
	}
	return r.Subscription.MonthlyMinor, true
}

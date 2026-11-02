package billing_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestWhiteLabelActiveDays(t *testing.T) {
	nov := day(2026, time.November, 1)
	feb := day(2028, time.February, 1) // a leap year
	end := func(t time.Time) *time.Time { return &t }
	for _, tc := range []struct {
		name   string
		starts time.Time
		ends   *time.Time
		month  time.Time
		want   int64
	}{
		{"whole month, open end", day(2026, time.October, 3), nil, nov, 30},
		{"starts mid month", day(2026, time.November, 16), nil, nov, 15},
		{"ends mid month", day(2026, time.January, 1), end(day(2026, time.November, 10)), nov, 10},
		{"one day", day(2026, time.November, 5), end(day(2026, time.November, 5)), nov, 1},
		{"before the month", day(2026, time.January, 1), end(day(2026, time.October, 31)), nov, 0},
		{"after the month", day(2026, time.December, 1), nil, nov, 0},
		{"ended before it started", day(2026, time.November, 5), end(day(2026, time.November, 4)), nov, 0},
		{"leap February", day(2028, time.January, 1), nil, feb, 29},
	} {
		if got := billing.WhiteLabelActiveDays(tc.starts, tc.ends, tc.month); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
	if got := billing.DaysInMonth(feb); got != 29 {
		t.Errorf("DaysInMonth(Feb 2028) = %d", got)
	}
	if got := billing.WhiteLabelMonthOccurredAt(nov); !got.Equal(time.Date(2026, time.November, 30, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("occurred at %s", got)
	}
}

func whiteLabelTerms(t *testing.T, cur string) billing.Terms {
	t.Helper()
	at := billing.WhiteLabelMonthOccurredAt(day(2026, time.November, 1))
	return buildTerms(t, termsSpec{currency: cur, at: at, extra: []gen.PriceListRuleRow{
		ruleRow(uuid.Nil, "subscription", "standard", "white_label_month", nil, `{"monthly_minor":4500}`, 40),
	}})
}

func wlEvent(active, dim int64, individual *int64) billing.Event {
	return billing.Event{
		Kind: billing.KindWhiteLabelMonth, OccurredAt: billing.WhiteLabelMonthOccurredAt(day(2026, time.November, 1)),
		DaysActive: active, DaysInMonth: dim, MonthlyPriceMinor: individual,
	}
}

func TestRate_WhiteLabelMonth_ProratedByDays(t *testing.T) {
	for _, cur := range []string{"EUR", "USD"} {
		terms := whiteLabelTerms(t, cur)
		if m, ok := terms.WhiteLabelMonthlyMinor(); !ok || m != 4500 {
			t.Fatalf("%s monthly: %d %v", cur, m, ok)
		}
		for _, tc := range []struct{ active, want int64 }{{30, 4500}, {15, 2250}, {1, 150}, {7, 1050}, {0, 0}} {
			c := mustRate(t, terms, wlEvent(tc.active, 30, nil))
			if c.AmountMinor != tc.want || c.RuleType != billing.RuleSubscription || c.RuleID == nil {
				t.Errorf("%s %d days: %d %s, want %d subscription", cur, tc.active, c.AmountMinor, c.RuleType, tc.want)
			}
		}
	}
	// Floor, never round up: 4500 x 10 / 31 = 1451.6
	if c := mustRate(t, whiteLabelTerms(t, "EUR"), wlEvent(10, 31, nil)); c.AmountMinor != 1451 {
		t.Errorf("floor: %d", c.AmountMinor)
	}
}

func TestRate_WhiteLabelMonth_IndividualPrice(t *testing.T) {
	terms := whiteLabelTerms(t, "EUR")
	zero, cheap := int64(0), int64(3000)
	c := mustRate(t, terms, wlEvent(30, 30, &zero))
	if c.AmountMinor != 0 || c.Reason != billing.ReasonOverride || c.RuleType != billing.RuleSubscription {
		t.Errorf("free brand: %d %q %s", c.AmountMinor, c.Reason, c.RuleType)
	}
	if c := mustRate(t, terms, wlEvent(15, 30, &cheap)); c.AmountMinor != 1500 {
		t.Errorf("individual half month: %d", c.AmountMinor)
	}
}

func TestRate_WhiteLabelMonth_RefusesBrokenEventsAndMissingRule(t *testing.T) {
	terms := whiteLabelTerms(t, "EUR")
	for _, ev := range []billing.Event{wlEvent(31, 30, nil), wlEvent(-1, 30, nil), wlEvent(5, 0, nil)} {
		if _, err := billing.Rate(terms, ev); !errors.Is(err, billing.ErrInconsistentEvent) {
			t.Errorf("%+v: %v, want ErrInconsistentEvent", ev, err)
		}
	}
	bare := buildTerms(t, termsSpec{at: billing.WhiteLabelMonthOccurredAt(day(2026, time.November, 1))})
	if _, err := billing.Rate(bare, wlEvent(30, 30, nil)); !errors.Is(err, billing.ErrNoRule) {
		t.Errorf("no rule: %v, want ErrNoRule", err)
	}
}

func TestWhiteLabelFromAttributes(t *testing.T) {
	var ev billing.Event
	billing.WhiteLabelFromAttributes([]byte(`{"days_active":12,"days_in_month":30,"monthly_price_minor":0}`), &ev)
	if ev.DaysActive != 12 || ev.DaysInMonth != 30 || ev.MonthlyPriceMinor == nil || *ev.MonthlyPriceMinor != 0 {
		t.Errorf("parsed %+v", ev)
	}
	var broken billing.Event
	billing.WhiteLabelFromAttributes([]byte(`{nope`), &broken)
	if broken.DaysInMonth != 0 {
		t.Errorf("broken attributes filled %+v", broken)
	}
}

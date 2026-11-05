package billing_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

func shopTerms(t *testing.T, cur string, monthly int64) billing.Terms {
	t.Helper()
	at := billing.MonthOccurredAt(day(2026, time.November, 1))
	params, _ := json.Marshal(map[string]int64{"monthly_minor": monthly})
	return buildTerms(t, termsSpec{currency: cur, at: at, extra: []gen.PriceListRuleRow{
		ruleRow(uuid.Nil, "subscription", "standard", "shop_module_month", nil, string(params), 41),
	}})
}

func shopEvent(active, dim int64, individual *int64) billing.Event {
	return billing.Event{
		Kind: billing.KindShopModuleMonth, OccurredAt: billing.MonthOccurredAt(day(2026, time.November, 1)),
		DaysActive: active, DaysInMonth: dim, MonthlyPriceMinor: individual,
	}
}

func TestPeriodActiveDays_SharedByWhiteLabelAndModules(t *testing.T) {
	nov := day(2026, time.November, 1)
	end := day(2026, time.November, 10)
	if got := billing.PeriodActiveDays(day(2026, time.October, 1), &end, nov); got != 10 {
		t.Errorf("period ending on the 10th: %d days, want 10", got)
	}
	if got, want := billing.PeriodActiveDays(day(2026, time.November, 16), nil, nov),
		billing.WhiteLabelActiveDays(day(2026, time.November, 16), nil, nov); got != want || got != 15 {
		t.Errorf("open period from the 16th: %d, white label %d, want 15", got, want)
	}
	if got := billing.MonthOccurredAt(nov); !got.Equal(billing.WhiteLabelMonthOccurredAt(nov)) ||
		!got.Equal(time.Date(2026, time.November, 30, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("occurred at %s", got)
	}
}

func TestRate_ShopModuleMonth_ProratedByDaysFloor(t *testing.T) {
	for _, cur := range []string{"EUR", "USD"} {
		terms := shopTerms(t, cur, 2900)
		if m, ok := terms.ShopModuleMonthlyMinor(); !ok || m != 2900 {
			t.Fatalf("%s monthly: %d %v", cur, m, ok)
		}
		if _, ok := terms.WhiteLabelMonthlyMinor(); ok {
			t.Errorf("%s: the white label rule must not be read for a shop month", cur)
		}
		for _, tc := range []struct{ active, want int64 }{{30, 2900}, {15, 1450}, {1, 96}, {0, 0}} {
			c := mustRate(t, terms, shopEvent(tc.active, 30, nil))
			if c.AmountMinor != tc.want || c.RuleType != billing.RuleSubscription || c.RuleID == nil {
				t.Errorf("%s %d days: %d %s, want %d subscription", cur, tc.active, c.AmountMinor, c.RuleType, tc.want)
			}
		}
	}
	// Floor, never round up: 2900 x 10 / 31 = 935.48
	if c := mustRate(t, shopTerms(t, "EUR", 2900), shopEvent(10, 31, nil)); c.AmountMinor != 935 {
		t.Errorf("floor: %d", c.AmountMinor)
	}
}

// The price list of 0198 says 0: a zero charge that is written, reason free,
// with the rule that produced it.
func TestRate_ShopModuleMonth_PriceListZeroIsAZeroCharge(t *testing.T) {
	c := mustRate(t, shopTerms(t, "EUR", 0), shopEvent(30, 30, nil))
	if c.AmountMinor != 0 || c.Reason != billing.ReasonFree || c.RuleType != billing.RuleSubscription || c.RuleID == nil {
		t.Errorf("free by price list: %d %q %s", c.AmountMinor, c.Reason, c.RuleType)
	}
}

func TestRate_ShopModuleMonth_IndividualPrice(t *testing.T) {
	terms := shopTerms(t, "EUR", 2900)
	zero, own := int64(0), int64(1000)
	c := mustRate(t, terms, shopEvent(30, 30, &zero))
	if c.AmountMinor != 0 || c.Reason != billing.ReasonIndividualPrice || c.RuleType != billing.RuleSubscription {
		t.Errorf("free by period: %d %q %s", c.AmountMinor, c.Reason, c.RuleType)
	}
	c = mustRate(t, terms, shopEvent(15, 30, &own))
	if c.AmountMinor != 500 || c.Reason != billing.ReasonIndividualPrice {
		t.Errorf("individual half month: %d %q", c.AmountMinor, c.Reason)
	}
	if c.Inputs["individual_monthly_minor"] != 1000 || c.Inputs["days_active"] != 15 {
		t.Errorf("inputs %v", c.Inputs)
	}
}

func TestRate_ShopModuleMonth_RefusesBrokenEventsAndMissingRule(t *testing.T) {
	terms := shopTerms(t, "EUR", 2900)
	neg := int64(-5)
	for _, ev := range []billing.Event{shopEvent(31, 30, nil), shopEvent(-1, 30, nil), shopEvent(5, 0, nil), shopEvent(5, 30, &neg)} {
		if _, err := billing.Rate(terms, ev); !errors.Is(err, billing.ErrInconsistentEvent) {
			t.Errorf("%+v: %v, want ErrInconsistentEvent", ev, err)
		}
	}
	// A price list with only the white label rule has no shop price: never a free month.
	if _, err := billing.Rate(whiteLabelTermsAt(t, billing.MonthOccurredAt(day(2026, time.November, 1))), shopEvent(30, 30, nil)); !errors.Is(err, billing.ErrNoRule) {
		t.Errorf("white label rule only: %v, want ErrNoRule", err)
	}
	bare := buildTerms(t, termsSpec{at: billing.MonthOccurredAt(day(2026, time.November, 1))})
	if _, err := billing.Rate(bare, shopEvent(30, 30, nil)); !errors.Is(err, billing.ErrNoRule) {
		t.Errorf("no rule: %v, want ErrNoRule", err)
	}
}

func whiteLabelTermsAt(t *testing.T, at time.Time) billing.Terms {
	t.Helper()
	return buildTerms(t, termsSpec{currency: "EUR", at: at, extra: []gen.PriceListRuleRow{
		ruleRow(uuid.Nil, "subscription", "standard", "white_label_month", nil, `{"monthly_minor":4500}`, 40),
	}})
}

// A white label month never reads the shop rule either.
func TestRate_WhiteLabelMonth_IgnoresTheShopRule(t *testing.T) {
	at := billing.WhiteLabelMonthOccurredAt(day(2026, time.November, 1))
	terms := buildTerms(t, termsSpec{currency: "EUR", at: at, extra: []gen.PriceListRuleRow{
		ruleRow(uuid.Nil, "subscription", "standard", "shop_module_month", nil, `{"monthly_minor":2900}`, 41),
	}})
	if _, err := billing.Rate(terms, wlEvent(30, 30, nil)); !errors.Is(err, billing.ErrNoRule) {
		t.Errorf("shop rule only: %v, want ErrNoRule", err)
	}
}

func TestParseRule_SubscriptionForShopModuleMonth(t *testing.T) {
	plID := uuid.New()
	r, err := billing.ParseRule(ruleRow(plID, "subscription", "standard", "shop_module_month", nil, `{"monthly_minor":0}`, 41), "EUR")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Implemented() || r.Subscription == nil || r.Subscription.MonthlyMinor != 0 || r.Subscription.Currency != "EUR" {
		t.Errorf("parsed %+v", r)
	}
	for name, params := range map[string]string{
		"negative":  `{"monthly_minor":-1}`,
		"missing":   `{}`,
		"unknown":   `{"monthly_minor":1,"extra":2}`,
		"not a num": `{"monthly_minor":"1"}`,
	} {
		if _, err := billing.ParseRule(ruleRow(plID, "subscription", "standard", "shop_module_month", nil, params, 41), "EUR"); !errors.Is(err, billing.ErrBadRule) {
			t.Errorf("%s: %v, want ErrBadRule", name, err)
		}
	}
	// Any other kind stays "recognised, not computed".
	other, err := billing.ParseRule(ruleRow(plID, "subscription", "standard", "ticket_sold", nil, `{"any":1}`, 41), "EUR")
	if err != nil || other.Implemented() {
		t.Errorf("ticket_sold subscription: err %v implemented %v", err, other.Implemented())
	}
	if !billing.IsSubscriptionKind("shop_module_month") || !billing.IsSubscriptionKind("white_label_month") || billing.IsSubscriptionKind("ticket_sold") {
		t.Error("IsSubscriptionKind")
	}
}

func TestShopModuleFromAttributes(t *testing.T) {
	var ev billing.Event
	billing.ShopModuleFromAttributes([]byte(`{"days_active":12,"days_in_month":30,"monthly_price_minor":0}`), &ev)
	if ev.DaysActive != 12 || ev.DaysInMonth != 30 || ev.MonthlyPriceMinor == nil || *ev.MonthlyPriceMinor != 0 {
		t.Errorf("parsed %+v", ev)
	}
	var priceList billing.Event
	billing.ShopModuleFromAttributes([]byte(`{"days_active":30,"days_in_month":30}`), &priceList)
	if priceList.MonthlyPriceMinor != nil {
		t.Errorf("no individual price must stay nil: %+v", priceList)
	}
	var broken billing.Event
	billing.ShopModuleFromAttributes([]byte(`{nope`), &broken)
	if broken.DaysInMonth != 0 {
		t.Errorf("broken attributes filled %+v", broken)
	}
	// What the charge job writes is what the rating reads.
	price := int64(700)
	raw, err := json.Marshal(billing.ShopModuleMonth{PeriodID: uuid.New(), Module: billing.ModuleShop, Month: "2026-11",
		DaysActive: 9, DaysInMonth: 30, MonthlyPriceMinor: &price, Currency: "EUR"})
	if err != nil {
		t.Fatal(err)
	}
	var round billing.Event
	billing.ShopModuleFromAttributes(raw, &round)
	if round.DaysActive != 9 || round.DaysInMonth != 30 || round.MonthlyPriceMinor == nil || *round.MonthlyPriceMinor != 700 {
		t.Errorf("round trip %+v", round)
	}
}

package billing_test

import (
	"errors"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

// Boundaries of the low-price formula (policy section 5): the cap 0.35 is
// reached exactly at 8.75, the minimum 0.10 exactly at 2.50, and every
// percentage is rounded DOWN.
func TestRate_LowPriceBoundariesAndRounding(t *testing.T) {
	terms := buildTerms(t, termsSpec{})
	for _, tc := range []struct {
		unit, want int64
	}{
		{874, 34}, // 34.96 -> 34
		{875, 35}, // exactly the cap
		{876, 35}, // 35.04 -> 35, capped anyway
		{249, 10}, // 9.96 -> 9, raised to the minimum
		{250, 10}, // exactly the minimum
		{251, 10}, // 10.04 -> 10
		{333, 13}, // 13.32 -> 13, floor not nearest
		{337, 13}, // 13.48 -> 13
	} {
		c := mustRate(t, terms, sold(t, terms, "EUR", tc.unit, 0, false))
		if c.AmountMinor != tc.want || c.RuleType != billing.RuleLowPriceFormula {
			t.Errorf("unit=%d: %d %s, want %d low_price_formula", tc.unit, c.AmountMinor, c.RuleType, tc.want)
		}
		if seated := mustRate(t, terms, sold(t, terms, "EUR", tc.unit, 0, true)); seated.AmountMinor != tc.want+20 {
			t.Errorf("unit=%d seated: %d, want %d", tc.unit, seated.AmountMinor, tc.want+20)
		}
	}
}

func TestRate_VenueRoundsDown(t *testing.T) {
	venue := buildTerms(t, termsSpec{plan: billing.PlanVenue})
	for _, tc := range []struct {
		unit   int64
		seated bool
		want   int64
	}{
		{2575, false, 51}, // 51.5 -> 51
		{2599, true, 51},  // 51.98 -> 51
		{1000, true, 20},  // exactly 10.00 with a seat: no low-price on a venue plan
		{1000, false, 20},
		{15000, false, 300}, // exactly the cap
		{15049, false, 300},
		{14999, false, 299}, // 299.98 -> 299
	} {
		c := mustRate(t, venue, sold(t, venue, "EUR", tc.unit, 0, tc.seated))
		if c.AmountMinor != tc.want {
			t.Errorf("venue unit=%d seated=%v: %d, want %d", tc.unit, tc.seated, c.AmountMinor, tc.want)
		}
	}
}

// strict_below=false makes the threshold itself low-price.
func TestClassifyLowPrice_NotStrictBelow(t *testing.T) {
	terms := buildTerms(t, termsSpec{lowPriceParams: `{"threshold_minor":1000,"strict_below":false,"pct_bp":400,"min_minor":10,"max_minor":35,"seated_surcharge_minor":20}`})
	for _, tc := range []struct {
		unit  int64
		class string
	}{
		{999, billing.PricingClassLowPrice},
		{1000, billing.PricingClassLowPrice},
		{1001, billing.PricingClassStandard},
	} {
		class, resolved := billing.ClassifyLowPrice(terms, "EUR", tc.unit)
		if class != tc.class || !resolved {
			t.Errorf("not strict, unit=%d: %s resolved=%v, want %s", tc.unit, class, resolved, tc.class)
		}
	}
	// 10.00 now pays the formula: 4 % = 0.40, capped at 0.35.
	if c := mustRate(t, terms, sold(t, terms, "EUR", 1000, 0, false)); c.AmountMinor != 35 {
		t.Errorf("not strict, 10.00: %d, want 35", c.AmountMinor)
	}
	strict := buildTerms(t, termsSpec{})
	if class, _ := billing.ClassifyLowPrice(strict, "EUR", 1000); class != billing.PricingClassStandard {
		t.Errorf("strict, 10.00: %s, want standard", class)
	}
}

// A zero price is classified low-price, and still rated free.
func TestClassifyLowPrice_ZeroPrice(t *testing.T) {
	terms := buildTerms(t, termsSpec{})
	class, resolved := billing.ClassifyLowPrice(terms, "EUR", 0)
	if class != billing.PricingClassLowPrice || !resolved {
		t.Errorf("zero price: %s resolved=%v", class, resolved)
	}
	c := mustRate(t, terms, sold(t, terms, "EUR", 0, 0, true))
	if c.AmountMinor != 0 || c.Reason != billing.ReasonFree || c.RuleID != nil {
		t.Errorf("zero price rated: %+v, want free", c)
	}
	if class, resolved := billing.ClassifyLowPrice(terms, "", 500); class != billing.PricingClassStandard || resolved {
		t.Errorf("no sale currency: %s resolved=%v, want standard unresolved", class, resolved)
	}
}

func TestRate_LowPriceOverflowIsRefused(t *testing.T) {
	terms := buildTerms(t, termsSpec{})
	ev := sold(t, terms, "EUR", 500, 0, false)
	ev.UnitPriceMinor = 1 << 62
	ev.PricingClass = billing.PricingClassLowPrice
	if _, err := billing.Rate(terms, ev); !errors.Is(err, billing.ErrInconsistentEvent) {
		t.Errorf("low-price overflow: %v, want ErrInconsistentEvent", err)
	}
}

func TestRate_OverrideEdgeCases(t *testing.T) {
	// An individual rate in dollars on a dollar account.
	usd := buildTerms(t, termsSpec{currency: "USD", override: lampyrisOverride()})
	for _, seated := range []bool{false, true} {
		want := int64(50)
		if seated {
			want = 100
		}
		c := mustRate(t, usd, sold(t, usd, "USD", 500, 0, seated))
		if c.AmountMinor != want || c.Currency != "USD" || c.Reason != billing.ReasonOverride {
			t.Errorf("USD override seated=%v: %+v, want %d USD", seated, c, want)
		}
	}
	// A promo code that takes the whole price: free, the individual rate is
	// not charged.
	eur := buildTerms(t, termsSpec{override: lampyrisOverride()})
	c := mustRate(t, eur, sold(t, eur, "EUR", 2500, 2500, true))
	if c.AmountMinor != 0 || c.Reason != billing.ReasonFree || c.OverrideID != nil {
		t.Errorf("override with a fully discounted ticket: %+v, want free", c)
	}
}

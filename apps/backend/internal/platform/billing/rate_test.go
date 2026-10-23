package billing_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

// sold is a ticket_sold event at saleAt, classified the way collection
// (task B2-2) will classify it: ClassifyLowPrice on the price BEFORE the
// promo code.
func sold(t *testing.T, terms billing.Terms, currency string, unit, discount int64, seated bool) billing.Event {
	t.Helper()
	class, _ := billing.ClassifyLowPrice(terms, currency, unit)
	return billing.Event{
		Kind: billing.KindTicketSold, OccurredAt: saleAt, Seated: seated, Currency: currency,
		UnitPriceMinor: unit, DiscountMinor: discount, PricingClass: class,
	}
}

func mustRate(t *testing.T, terms billing.Terms, ev billing.Event) billing.Charge {
	t.Helper()
	c, err := billing.Rate(terms, ev)
	if err != nil {
		t.Fatalf("Rate(%+v): %v", ev, err)
	}
	if c.Currency != terms.Currency {
		t.Fatalf("charge currency %s, account %s", c.Currency, terms.Currency)
	}
	if c.PriceListID != terms.PriceList.ID || c.PriceListVersion != terms.PriceList.Version {
		t.Fatalf("charge names price list %s v%d, terms %s v%d", c.PriceListID, c.PriceListVersion,
			terms.PriceList.ID, terms.PriceList.Version)
	}
	return c
}

// Policy section 2: pay as you go, 0.59 unseated, 1.09 seated.
func TestRate_Policy2_FixedPerTicket(t *testing.T) {
	for _, cur := range []string{"EUR", "USD"} { // policy section 9: dollars carry the same numbers
		terms := buildTerms(t, termsSpec{currency: cur})
		for _, tc := range []struct {
			unit   int64
			seated bool
			want   int64
		}{
			{1500, false, 59}, {1500, true, 109}, {15000, false, 59}, {15000, true, 109},
			{1000, false, 59}, {1000, true, 109}, // 10.00 is NOT below 10.00 (policy 12.6)
		} {
			c := mustRate(t, terms, sold(t, terms, cur, tc.unit, 0, tc.seated))
			if c.AmountMinor != tc.want || c.RuleType != billing.RuleFixedPerTicket || c.Reason != "" || c.RuleID == nil {
				t.Errorf("%s unit=%d seated=%v: got %d %s %q, want %d fixed_per_ticket", cur, tc.unit, tc.seated,
					c.AmountMinor, c.RuleType, c.Reason, tc.want)
			}
		}
	}
}

// Policy section 3: every seeded pack, unseated and seated (+0.35).
func TestPackTicketRate_Policy3(t *testing.T) {
	terms := buildTerms(t, termsSpec{})
	want := []struct{ unseated, seated int64 }{
		{55, 90}, {52, 87}, {49, 84}, {46, 81}, {43, 78}, {40, 75}, {38, 73},
	}
	if len(terms.Packs) != len(want) {
		t.Fatalf("packs: %d, want %d", len(terms.Packs), len(want))
	}
	for i, p := range terms.Packs {
		if got := billing.PackTicketRate(p, false); got != want[i].unseated {
			t.Errorf("tier %d unseated: %d, want %d", p.Tier, got, want[i].unseated)
		}
		if got := billing.PackTicketRate(p, true); got != want[i].seated {
			t.Errorf("tier %d seated: %d, want %d", p.Tier, got, want[i].seated)
		}
	}
}

// Policy section 5, the pay-as-you-go column: 4 %, min 0.10, max 0.35,
// seated +0.20; 10.00 already pays the general rate.
func TestRate_Policy5_LowPrice(t *testing.T) {
	terms := buildTerms(t, termsSpec{})
	for _, tc := range []struct {
		unit             int64
		unseated, seated int64
		class            string
	}{
		{200, 10, 30, billing.PricingClassLowPrice},
		{300, 12, 32, billing.PricingClassLowPrice},
		{500, 20, 40, billing.PricingClassLowPrice},
		{700, 28, 48, billing.PricingClassLowPrice},
		{900, 35, 55, billing.PricingClassLowPrice},
		{999, 35, 55, billing.PricingClassLowPrice},
		{1000, 59, 109, billing.PricingClassStandard},
	} {
		class, resolved := billing.ClassifyLowPrice(terms, "EUR", tc.unit)
		if class != tc.class || !resolved {
			t.Errorf("unit=%d: class %s resolved=%v, want %s resolved", tc.unit, class, resolved, tc.class)
		}
		if c := mustRate(t, terms, sold(t, terms, "EUR", tc.unit, 0, false)); c.AmountMinor != tc.unseated {
			t.Errorf("unit=%d unseated: %d, want %d", tc.unit, c.AmountMinor, tc.unseated)
		}
		if c := mustRate(t, terms, sold(t, terms, "EUR", tc.unit, 0, true)); c.AmountMinor != tc.seated {
			t.Errorf("unit=%d seated: %d, want %d", tc.unit, c.AmountMinor, tc.seated)
		}
	}
	// Policy example: a disco, 400 tickets at 5.00 -> 80.00.
	var total int64
	for range 400 {
		total += mustRate(t, terms, sold(t, terms, "EUR", 500, 0, false)).AmountMinor
	}
	if total != 8000 {
		t.Errorf("400 × 5.00: %d, want 8000", total)
	}
}

// Policy section 6: venues pay 2 % of the price after the promo code, at
// most 3.00 a ticket, seated or not.
func TestRate_Policy6_Venue(t *testing.T) {
	terms := buildTerms(t, termsSpec{plan: billing.PlanVenue})
	for _, tc := range []struct {
		unit, discount, want int64
	}{
		{2500, 0, 50},
		{20000, 0, 300},
		{15000, 0, 300},
		{35000, 0, 300},
		{2500, 500, 40}, // the share is taken from unit price − discount
		{800, 0, 16},    // a cheap venue ticket is not low-price on a venue plan
	} {
		for _, seated := range []bool{false, true} {
			c := mustRate(t, terms, sold(t, terms, "EUR", tc.unit, tc.discount, seated))
			if c.AmountMinor != tc.want || c.RuleType != billing.RulePercentOfPriceWithCap {
				t.Errorf("unit=%d discount=%d seated=%v: %d %s, want %d", tc.unit, tc.discount, seated,
					c.AmountMinor, c.RuleType, tc.want)
			}
		}
	}
}

// An individual rate replaces the price list everywhere (owner decision
// B-2, run decision R-B6): the fixed rate, the low-price formula and the
// venue percentage.
func TestRate_OverrideReplacesThePriceListEverywhere(t *testing.T) {
	for _, plan := range []string{billing.PlanStandard, billing.PlanVenue} {
		ov := lampyrisOverride()
		terms := buildTerms(t, termsSpec{plan: plan, override: ov})
		for _, tc := range []struct {
			unit   int64
			seated bool
			want   int64
		}{
			{2500, false, 50}, {2500, true, 100},
			{500, false, 50}, {500, true, 100}, // not 20 / 40 of the low-price formula
			{20000, false, 50}, // not the 3.00 venue cap
		} {
			c := mustRate(t, terms, sold(t, terms, "EUR", tc.unit, 0, tc.seated))
			if c.AmountMinor != tc.want || c.Reason != billing.ReasonOverride ||
				c.RuleType != billing.RuleOrgRateOverride || c.OverrideID == nil || *c.OverrideID != ov.ID || c.RuleID != nil {
				t.Errorf("%s unit=%d seated=%v: %+v, want %d by override", plan, tc.unit, tc.seated, c, tc.want)
			}
		}
	}
}

// The low-price class is decided on the price BEFORE the promo code; the
// low-price and venue percentages are taken AFTER it.
func TestRate_LowPriceClassUsesPriceBeforePromo(t *testing.T) {
	terms := buildTerms(t, termsSpec{})
	// 12.00 with a 5.00 promo code stays standard: 0.59.
	if c := mustRate(t, terms, sold(t, terms, "EUR", 1200, 500, false)); c.AmountMinor != 59 || c.RuleType != billing.RuleFixedPerTicket {
		t.Errorf("12.00 - 5.00 promo: %d %s, want 59 fixed_per_ticket", c.AmountMinor, c.RuleType)
	}
	// 5.00 with a 1.00 promo code is low-price on 4.00: 0.16.
	if c := mustRate(t, terms, sold(t, terms, "EUR", 500, 100, false)); c.AmountMinor != 16 || c.RuleType != billing.RuleLowPriceFormula {
		t.Errorf("5.00 - 1.00 promo: %d %s, want 16 low_price_formula", c.AmountMinor, c.RuleType)
	}
}

func TestRate_FreeAndJournalOnly(t *testing.T) {
	terms := buildTerms(t, termsSpec{})
	at := saleAt
	cases := []struct {
		name   string
		ev     billing.Event
		reason string
		amount int64
	}{
		{"fully discounted", billing.Event{Kind: billing.KindTicketSold, OccurredAt: at, Currency: "EUR", UnitPriceMinor: 1000, DiscountMinor: 1000}, billing.ReasonFree, 0},
		{"zero price", billing.Event{Kind: billing.KindTicketSold, OccurredAt: at, Currency: "EUR", Seated: true}, billing.ReasonFree, 0},
		{"discount above price", billing.Event{Kind: billing.KindTicketSold, OccurredAt: at, Currency: "EUR", UnitPriceMinor: 500, DiscountMinor: 900}, billing.ReasonFree, 0},
		{"complimentary", billing.Event{Kind: billing.KindTicketComplimentary, OccurredAt: at, Currency: "EUR", Seated: true}, billing.ReasonFree, 0},
		{"refunded", billing.Event{Kind: billing.KindTicketRefunded, OccurredAt: at, Currency: "EUR", UnitPriceMinor: 2500}, billing.ReasonJournalOnly, 0},
		{"cancelled", billing.Event{Kind: billing.KindTicketCancelled, OccurredAt: at, Currency: "EUR", UnitPriceMinor: 2500}, billing.ReasonJournalOnly, 0},
		{"session cancelled", billing.Event{Kind: billing.KindSessionCancelled, OccurredAt: at, Currency: "EUR"}, billing.ReasonJournalOnly, 0},
		{"event published, no rule", billing.Event{Kind: billing.KindEventPublished, OccurredAt: at, Currency: "EUR"}, billing.ReasonNoRule, 0},
		{"complimentary over the free limit", billing.Event{Kind: billing.KindTicketComplimentary, OccurredAt: at, Currency: "EUR", Seated: true, FreeTicketsOverLimit: true}, "", 59},
	}
	for _, tc := range cases {
		c := mustRate(t, terms, tc.ev)
		if c.Reason != tc.reason || c.AmountMinor != tc.amount {
			t.Errorf("%s: %d %q, want %d %q", tc.name, c.AmountMinor, c.Reason, tc.amount, tc.reason)
		}
	}
	// Over the free limit with an individual rate: the unseated individual rate.
	ovTerms := buildTerms(t, termsSpec{override: lampyrisOverride()})
	ev := billing.Event{Kind: billing.KindTicketComplimentary, OccurredAt: at, Currency: "EUR", Seated: true, FreeTicketsOverLimit: true}
	if c := mustRate(t, ovTerms, ev); c.AmountMinor != 50 {
		t.Errorf("complimentary over limit with individual rate: %d, want 50", c.AmountMinor)
	}
	// A venue plan has no flat rate named for it: refused, not guessed.
	venue := buildTerms(t, termsSpec{plan: billing.PlanVenue})
	if _, err := billing.Rate(venue, ev); !errors.Is(err, billing.ErrNoRule) {
		t.Errorf("complimentary over limit on a venue plan: %v, want ErrNoRule", err)
	}
}

func TestRate_FixedPerEventWhenTheListHasOne(t *testing.T) {
	extra := gen.PriceListRuleRow{ID: uuid.New(), RuleType: "fixed_per_event", Line: "standard",
		EventKind: "event_published", Params: []byte(`{"amount_minor":250}`)}
	terms := buildTerms(t, termsSpec{extra: []gen.PriceListRuleRow{extra}})
	c := mustRate(t, terms, billing.Event{Kind: billing.KindEventPublished, OccurredAt: saleAt, Currency: "EUR"})
	if c.AmountMinor != 250 || c.RuleType != billing.RuleFixedPerEvent || c.RuleID == nil || *c.RuleID != extra.ID {
		t.Errorf("event_published: %+v, want 250 fixed_per_event", c)
	}
}

// A sale in another currency than the account's (CZK on an EUR account).
func TestRate_ForeignSaleCurrency(t *testing.T) {
	terms := buildTerms(t, termsSpec{})
	class, resolved := billing.ClassifyLowPrice(terms, "CZK", 500)
	if class != billing.PricingClassStandard || resolved {
		t.Errorf("CZK 5.00: %s resolved=%v, want standard unresolved", class, resolved)
	}
	if c := mustRate(t, terms, sold(t, terms, "CZK", 500, 0, false)); c.AmountMinor != 59 || c.Deferred != "" {
		t.Errorf("CZK on EUR fixed: %d %q, want 59", c.AmountMinor, c.Deferred)
	}
	venue := buildTerms(t, termsSpec{plan: billing.PlanVenue})
	c := mustRate(t, venue, sold(t, venue, "CZK", 50000, 0, false))
	if c.Deferred != billing.DeferredFXRateMissing || c.AmountMinor != 0 || c.RuleType != billing.RulePercentOfPriceWithCap {
		t.Errorf("CZK on EUR venue: %+v, want deferred fx_rate_missing", c)
	}
	// A low_price class on a CZK sale without a CZK threshold could not have
	// been snapshotted.
	ev := billing.Event{Kind: billing.KindTicketSold, OccurredAt: saleAt, Currency: "CZK", UnitPriceMinor: 500,
		PricingClass: billing.PricingClassLowPrice}
	if _, err := billing.Rate(terms, ev); !errors.Is(err, billing.ErrInconsistentEvent) {
		t.Errorf("low_price on CZK without a CZK rule: %v, want ErrInconsistentEvent", err)
	}
	// With a CZK threshold the class is real, but the charge needs the
	// exchange rate of the sale day: deferred like the venue percentage.
	// Fixture numbers, not a policy rate.
	czkRule := ruleRow(uuid.Nil, "low_price_formula", "standard", "ticket_sold", strp("CZK"),
		`{"threshold_minor":25000,"strict_below":true,"pct_bp":400,"min_minor":10,"max_minor":35,"seated_surcharge_minor":20}`, 11)
	withCZK := buildTerms(t, termsSpec{extra: []gen.PriceListRuleRow{czkRule}})
	czkSale := sold(t, withCZK, "CZK", 12000, 0, false)
	if czkSale.PricingClass != billing.PricingClassLowPrice {
		t.Fatalf("CZK 120.00 class %s, want low_price", czkSale.PricingClass)
	}
	c = mustRate(t, withCZK, czkSale)
	if c.Deferred != billing.DeferredFXRateMissing || c.AmountMinor != 0 || c.RuleType != billing.RuleLowPriceFormula ||
		c.RuleID == nil || *c.RuleID != czkRule.ID {
		t.Errorf("CZK low_price on EUR: %+v, want deferred fx_rate_missing by the CZK rule", c)
	}
}

func TestRate_Refusals(t *testing.T) {
	terms := buildTerms(t, termsSpec{})
	ok := billing.Event{Kind: billing.KindTicketSold, OccurredAt: saleAt, Currency: "EUR", UnitPriceMinor: 2500}

	other := ok
	other.OccurredAt = saleAt.Add(1)
	if _, err := billing.Rate(terms, other); !errors.Is(err, billing.ErrInconsistentEvent) {
		t.Errorf("other OccurredAt: %v", err)
	}
	neg := ok
	neg.DiscountMinor = -1
	if _, err := billing.Rate(terms, neg); !errors.Is(err, billing.ErrInconsistentEvent) {
		t.Errorf("negative discount: %v", err)
	}
	unknown := ok
	unknown.Kind = "ticket_resold"
	if _, err := billing.Rate(terms, unknown); !errors.Is(err, billing.ErrUnknownEventKind) {
		t.Errorf("unknown kind: %v", err)
	}
	badClass := ok
	badClass.PricingClass = "cheap"
	if _, err := billing.Rate(terms, badClass); !errors.Is(err, billing.ErrInconsistentEvent) {
		t.Errorf("unknown class: %v", err)
	}
	huge := ok
	huge.UnitPriceMinor = 1 << 62
	venue := buildTerms(t, termsSpec{plan: billing.PlanVenue})
	if _, err := billing.Rate(venue, huge); !errors.Is(err, billing.ErrInconsistentEvent) {
		t.Errorf("overflow: %v", err)
	}
}

// A line carrying a rule Rate cannot compute yet is refused, never charged
// by another rule of that line.
func TestRate_NotImplementedRuleStopsTheLine(t *testing.T) {
	for _, typ := range []string{"volume_above_baseline", "subscription", "generator_run"} {
		extra := gen.PriceListRuleRow{ID: uuid.New(), RuleType: typ, Line: "standard", EventKind: "ticket_sold",
			Params: []byte(`{}`)}
		terms := buildTerms(t, termsSpec{extra: []gen.PriceListRuleRow{extra}})
		if _, err := billing.Rate(terms, sold(t, terms, "EUR", 2500, 0, false)); !errors.Is(err, billing.ErrRuleNotImplemented) {
			t.Errorf("%s: %v, want ErrRuleNotImplemented", typ, err)
		}
		// The venue line is untouched by a standard-line rule.
		venue := buildTerms(t, termsSpec{plan: billing.PlanVenue, extra: []gen.PriceListRuleRow{extra}})
		if c := mustRate(t, venue, sold(t, venue, "EUR", 2500, 0, false)); c.AmountMinor != 50 {
			t.Errorf("%s on the standard line, venue plan: %d, want 50", typ, c.AmountMinor)
		}
	}
}

// A charge records what it read, so it can be reproduced.
func TestRate_InputsExplainTheCharge(t *testing.T) {
	terms := buildTerms(t, termsSpec{})
	ev := sold(t, terms, "EUR", 900, 0, true)
	ev.ServiceChargeMinor = 45
	c := mustRate(t, terms, ev)
	want := map[string]int64{
		"unit_price_minor": 900, "discount_minor": 0, "service_charge_minor": 45, "seated": 1,
		"net_price_minor": 900, "threshold_minor": 1000, "pct_bp": 400, "min_minor": 10, "max_minor": 35,
		"seated_surcharge_minor": 20, "share_minor": 36,
	}
	for k, v := range want {
		if c.Inputs[k] != v {
			t.Errorf("input %s = %d, want %d", k, c.Inputs[k], v)
		}
	}
	if c.AmountMinor != 55 {
		t.Errorf("amount %d, want 55", c.AmountMinor)
	}
}

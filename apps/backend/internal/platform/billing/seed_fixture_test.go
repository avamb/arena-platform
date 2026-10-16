package billing_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

// The rule and pack rows below repeat the seed of migration 0157
// (standard-2026 version 1) byte for byte, so the unit tables check the
// numbers the database will really carry. The integration test reads the
// real rows and must agree.
const (
	seedLowPriceParams = `{"threshold_minor":1000,"strict_below":true,"pct_bp":400,"min_minor":10,"max_minor":35,"seated_surcharge_minor":20}`
	seedFixedParams    = `{"unseated_minor":59,"seated_minor":109}`
	seedPackParams     = `{"seated_surcharge_minor":35}`
	seedVenueParams    = `{"pct_bp":200,"cap_minor":300}`
)

var (
	seedDate = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	saleAt   = time.Date(2026, time.October, 20, 18, 30, 0, 0, time.UTC)
)

func strp(s string) *string { return &s }

func ruleRow(plID uuid.UUID, typ, line, kind string, saleCurrency *string, params string, sort int32) gen.PriceListRuleRow {
	return gen.PriceListRuleRow{
		ID: uuid.New(), PriceListID: plID, RuleType: typ, Line: line, EventKind: kind,
		SaleCurrency: saleCurrency, Params: []byte(params), SortOrder: sort, CreatedAt: seedDate,
	}
}

// seedRules returns the four rules migration 0157 seeds for one currency.
func seedRules(plID uuid.UUID, currency string) []gen.PriceListRuleRow {
	return []gen.PriceListRuleRow{
		ruleRow(plID, "low_price_formula", "standard", "ticket_sold", strp(currency), seedLowPriceParams, 10),
		ruleRow(plID, "fixed_per_ticket", "standard", "ticket_sold", nil, seedFixedParams, 20),
		ruleRow(plID, "credit_pack", "standard", "ticket_sold", nil, seedPackParams, 30),
		ruleRow(plID, "percent_of_price_with_cap", "venue", "ticket_sold", nil, seedVenueParams, 10),
	}
}

// seedPacks returns the seven packs of policy section 3 as migration 0157
// seeds them.
func seedPacks(plID uuid.UUID, currency string) []gen.PriceListPackRow {
	faces := []struct{ face, rate int64 }{
		{1650, 55}, {5200, 52}, {14700, 49}, {23000, 46}, {43000, 43}, {100000, 40}, {190000, 38},
	}
	out := make([]gen.PriceListPackRow, 0, len(faces))
	for i, f := range faces {
		out = append(out, gen.PriceListPackRow{
			ID: uuid.New(), PriceListID: plID, Currency: currency, Tier: int16(i + 1), FaceMinor: f.face,
			UnseatedRateMinor: f.rate, SeatedSurchargeMinor: 35, Active: true, SortOrder: int32(i + 1),
		})
	}
	return out
}

type termsSpec struct {
	currency string
	plan     string
	// lowPriceParams, when set, replaces the params of the seeded
	// low_price_formula rule.
	lowPriceParams string
	override       *gen.OrgRateOverrideRow
	extra          []gen.PriceListRuleRow
	at             time.Time
}

// buildTerms assembles Terms on the standard-2026 seed for one account.
func buildTerms(t *testing.T, s termsSpec) billing.Terms {
	t.Helper()
	if s.currency == "" {
		s.currency = "EUR"
	}
	if s.plan == "" {
		s.plan = billing.PlanStandard
	}
	if s.at.IsZero() {
		s.at = saleAt
	}
	acc := gen.BillingAccountRow{ID: uuid.New(), OrgID: uuid.New(), Currency: s.currency, AccountKind: "organizer"}
	pl := gen.PriceListRow{ID: uuid.New(), Code: "standard-2026", Version: 1, Currency: s.currency, EffectiveFrom: seedDate}
	terms := gen.BillingAccountTermsRow{ID: uuid.New(), AccountID: acc.ID, EffectiveFrom: seedDate,
		PriceListCode: "standard-2026", PlanKind: s.plan, BillingCycle: "monthly"}
	rules := append(seedRules(pl.ID, s.currency), s.extra...)
	if s.lowPriceParams != "" {
		rules[0].Params = []byte(s.lowPriceParams)
	}
	for i := range s.extra {
		rules[len(rules)-len(s.extra)+i].PriceListID = pl.ID
	}
	if s.override != nil {
		s.override.OrgID = acc.OrgID
		if s.override.Currency == "" {
			s.override.Currency = s.currency
		}
	}
	out, err := billing.NewTerms(s.at, acc, terms, pl, rules, seedPacks(pl.ID, s.currency), s.override)
	if err != nil {
		t.Fatalf("NewTerms: %v", err)
	}
	return out
}

func lampyrisOverride() *gen.OrgRateOverrideRow {
	return &gen.OrgRateOverrideRow{ID: uuid.New(), UnseatedMinor: 50, SeatedMinor: 100, EffectiveFrom: seedDate}
}

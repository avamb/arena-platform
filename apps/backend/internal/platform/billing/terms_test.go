package billing_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

var _ billing.TermsQueries = (*gen.Queries)(nil)

// termsFake answers TermsQueries from in-memory rows with the same "in force
// at" rules as the SQL of billing_foundation.sql.
type termsFake struct {
	account   gen.BillingAccountRow
	terms     []gen.BillingAccountTermsRow
	lists     []gen.PriceListRow
	rules     map[uuid.UUID][]gen.PriceListRuleRow
	packs     map[uuid.UUID][]gen.PriceListPackRow
	overrides []gen.OrgRateOverrideRow
	failRules error
}

func (f *termsFake) GetBillingAccountByID(_ context.Context, id uuid.UUID) (gen.BillingAccountRow, error) {
	if id != f.account.ID {
		return gen.BillingAccountRow{}, pgx.ErrNoRows
	}
	return f.account, nil
}

func (f *termsFake) GetBillingAccountTermsAt(_ context.Context, accountID uuid.UUID, at time.Time) (gen.BillingAccountTermsRow, error) {
	var best *gen.BillingAccountTermsRow
	for i, t := range f.terms {
		if t.AccountID == accountID && !t.EffectiveFrom.After(at) && (best == nil || t.EffectiveFrom.After(best.EffectiveFrom)) {
			best = &f.terms[i]
		}
	}
	if best == nil {
		return gen.BillingAccountTermsRow{}, pgx.ErrNoRows
	}
	return *best, nil
}

func (f *termsFake) GetPriceListVersionAt(_ context.Context, code, currency string, at time.Time) (gen.PriceListRow, error) {
	cands := []gen.PriceListRow{}
	for _, pl := range f.lists {
		if pl.Code == code && pl.Currency == currency && !pl.EffectiveFrom.After(at) && pl.RevokedAt == nil {
			cands = append(cands, pl)
		}
	}
	if len(cands) == 0 {
		return gen.PriceListRow{}, pgx.ErrNoRows
	}
	sort.Slice(cands, func(i, j int) bool {
		if !cands[i].EffectiveFrom.Equal(cands[j].EffectiveFrom) {
			return cands[i].EffectiveFrom.After(cands[j].EffectiveFrom)
		}
		return cands[i].Version > cands[j].Version
	})
	return cands[0], nil
}

func (f *termsFake) ListPriceListRules(_ context.Context, id uuid.UUID) ([]gen.PriceListRuleRow, error) {
	if f.failRules != nil {
		return nil, f.failRules
	}
	return f.rules[id], nil
}

func (f *termsFake) ListPriceListPacks(_ context.Context, id uuid.UUID) ([]gen.PriceListPackRow, error) {
	return f.packs[id], nil
}

func (f *termsFake) GetOrgRateOverrideAt(_ context.Context, orgID uuid.UUID, currency string, at time.Time) (gen.OrgRateOverrideRow, error) {
	var best *gen.OrgRateOverrideRow
	for i, o := range f.overrides {
		if o.OrgID != orgID || o.Currency != currency || o.EffectiveFrom.After(at) ||
			(o.EffectiveTo != nil && !o.EffectiveTo.After(at)) {
			continue
		}
		if best == nil || o.EffectiveFrom.After(best.EffectiveFrom) {
			best = &f.overrides[i]
		}
	}
	if best == nil {
		return gen.OrgRateOverrideRow{}, pgx.ErrNoRows
	}
	return *best, nil
}

// addList adds a version of standard-2026 in EUR whose fixed rate is
// unseated/seated; the other rules repeat the seed.
func (f *termsFake) addList(version int32, from time.Time, unseated, seated string) gen.PriceListRow {
	pl := gen.PriceListRow{ID: uuid.New(), Code: "standard-2026", Version: version, Currency: "EUR", EffectiveFrom: from}
	f.lists = append(f.lists, pl)
	rules := seedRules(pl.ID, "EUR")
	rules[1].Params = []byte(`{"unseated_minor":` + unseated + `,"seated_minor":` + seated + `}`)
	f.rules[pl.ID] = rules
	f.packs[pl.ID] = seedPacks(pl.ID, "EUR")
	return pl
}

func newTermsFake() *termsFake {
	acc := gen.BillingAccountRow{ID: uuid.New(), OrgID: uuid.New(), Currency: "EUR", AccountKind: "organizer"}
	f := &termsFake{
		account: acc,
		terms: []gen.BillingAccountTermsRow{{ID: uuid.New(), AccountID: acc.ID, EffectiveFrom: seedDate,
			PriceListCode: "standard-2026", PlanKind: "standard", BillingCycle: "monthly"}},
		rules: map[uuid.UUID][]gen.PriceListRuleRow{},
		packs: map[uuid.UUID][]gen.PriceListPackRow{},
	}
	return f
}

func soldAt(at time.Time, unit int64) billing.Event {
	return billing.Event{Kind: billing.KindTicketSold, OccurredAt: at, Currency: "EUR", UnitPriceMinor: unit}
}

// A new price-list version in the middle of the month: a sale before its
// date is rated by the old version, a sale after it by the new one, whatever
// the clock says when rating runs (the fake has no clock at all).
func TestTermsAt_VersionByOccurredAt(t *testing.T) {
	f := newTermsFake()
	v1 := f.addList(1, seedDate, "59", "109")
	// Fixture numbers for a hypothetical v2, not a policy rate.
	v2 := f.addList(2, time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC), "61", "111")
	ctx := context.Background()

	for _, tc := range []struct {
		at      time.Time
		list    gen.PriceListRow
		want    int64
		version int32
	}{
		{time.Date(2026, time.October, 14, 23, 59, 59, 0, time.UTC), v1, 59, 1},
		{time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC), v2, 61, 2},
		{time.Date(2026, time.October, 16, 12, 0, 0, 0, time.UTC), v2, 61, 2},
		{time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC), v1, 59, 1},
	} {
		terms, err := billing.TermsAt(ctx, f, f.account.ID, tc.at)
		if err != nil {
			t.Fatalf("TermsAt %s: %v", tc.at, err)
		}
		if terms.PriceList.ID != tc.list.ID {
			t.Errorf("at %s: version %d, want %d", tc.at, terms.PriceList.Version, tc.version)
		}
		c, err := billing.Rate(terms, soldAt(tc.at, 2500))
		if err != nil {
			t.Fatal(err)
		}
		if c.AmountMinor != tc.want || c.PriceListVersion != tc.version {
			t.Errorf("at %s: %d from v%d, want %d from v%d", tc.at, c.AmountMinor, c.PriceListVersion, tc.want, tc.version)
		}
	}

	// A revoked version is never used: the sale falls back to v1.
	now := time.Now()
	for i := range f.lists {
		if f.lists[i].ID == v2.ID {
			f.lists[i].RevokedAt = &now
		}
	}
	terms, err := billing.TermsAt(ctx, f, f.account.ID, time.Date(2026, time.October, 16, 0, 0, 0, 0, time.UTC))
	if err != nil || terms.PriceList.ID != v1.ID {
		t.Errorf("revoked v2: version %d err %v, want v1", terms.PriceList.Version, err)
	}
}

// Terms rows and individual rates are also taken at occurred_at.
func TestTermsAt_TermsAndOverrideByOccurredAt(t *testing.T) {
	f := newTermsFake()
	f.addList(1, seedDate, "59", "109")
	switchAt := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)
	f.terms = append(f.terms, gen.BillingAccountTermsRow{ID: uuid.New(), AccountID: f.account.ID,
		EffectiveFrom: switchAt, PriceListCode: "standard-2026", PlanKind: "venue", BillingCycle: "monthly"})
	ovFrom := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	ovTo := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	ov := gen.OrgRateOverrideRow{ID: uuid.New(), OrgID: f.account.OrgID, Currency: "EUR",
		UnseatedMinor: 50, SeatedMinor: 100, EffectiveFrom: ovFrom, EffectiveTo: &ovTo}
	f.overrides = append(f.overrides, ov)
	ctx := context.Background()

	for _, tc := range []struct {
		at   time.Time
		want int64
		plan string
		ov   bool
	}{
		{time.Date(2026, time.May, 31, 0, 0, 0, 0, time.UTC), 59, "standard", false},
		{time.Date(2026, time.June, 2, 0, 0, 0, 0, time.UTC), 50, "venue", false}, // 2 % of 25.00
		{time.Date(2026, time.August, 15, 0, 0, 0, 0, time.UTC), 50, "venue", true},
		{time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC), 50, "venue", false}, // effective_to is exclusive
	} {
		terms, err := billing.TermsAt(ctx, f, f.account.ID, tc.at)
		if err != nil {
			t.Fatal(err)
		}
		if terms.PlanKind() != tc.plan || (terms.Override != nil) != tc.ov {
			t.Errorf("at %s: plan %s override %v, want %s %v", tc.at, terms.PlanKind(), terms.Override != nil, tc.plan, tc.ov)
		}
		c, err := billing.Rate(terms, soldAt(tc.at, 2500))
		if err != nil {
			t.Fatal(err)
		}
		if c.AmountMinor != tc.want || (c.OverrideID != nil) != tc.ov {
			t.Errorf("at %s: %+v, want %d", tc.at, c, tc.want)
		}
	}
}

func TestTermsAt_Errors(t *testing.T) {
	ctx := context.Background()
	f := newTermsFake()
	f.addList(1, seedDate, "59", "109")

	if _, err := billing.TermsAt(ctx, f, uuid.New(), saleAt); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("unknown account: %v", err)
	}
	if _, err := billing.TermsAt(ctx, f, f.account.ID, seedDate.Add(-time.Second)); !errors.Is(err, billing.ErrNoTerms) {
		t.Errorf("before the first terms: %v", err)
	}
	f.terms[0].PriceListCode = "missing"
	if _, err := billing.TermsAt(ctx, f, f.account.ID, saleAt); !errors.Is(err, billing.ErrNoPriceList) {
		t.Errorf("unknown price list: %v", err)
	}
	f.terms[0].PriceListCode = "standard-2026"
	boom := errors.New("db down")
	f.failRules = boom
	if _, err := billing.TermsAt(ctx, f, f.account.ID, saleAt); !errors.Is(err, boom) {
		t.Errorf("rules read failure: %v", err)
	}
}

func TestParseRule_RefusesBrokenRows(t *testing.T) {
	plID := uuid.New()
	eur := strp("EUR")
	for _, tc := range []struct {
		name string
		row  gen.PriceListRuleRow
	}{
		{"unknown type", ruleRow(plID, "per_seat_magic", "standard", "ticket_sold", nil, `{}`, 0)},
		{"unknown line", ruleRow(plID, "fixed_per_ticket", "vip", "ticket_sold", nil, seedFixedParams, 0)},
		{"no event kind", ruleRow(plID, "fixed_per_ticket", "standard", "", nil, seedFixedParams, 0)},
		{"missing key", ruleRow(plID, "fixed_per_ticket", "standard", "ticket_sold", nil, `{"unseated_minor":59}`, 0)},
		{"unknown key", ruleRow(plID, "fixed_per_ticket", "standard", "ticket_sold", nil, `{"unseated_minor":59,"seated_minor":109,"vip_minor":1}`, 0)},
		{"fraction", ruleRow(plID, "fixed_per_ticket", "standard", "ticket_sold", nil, `{"unseated_minor":0.59,"seated_minor":109}`, 0)},
		{"string number", ruleRow(plID, "fixed_per_ticket", "standard", "ticket_sold", nil, `{"unseated_minor":"59","seated_minor":109}`, 0)},
		{"negative", ruleRow(plID, "fixed_per_ticket", "standard", "ticket_sold", nil, `{"unseated_minor":-1,"seated_minor":109}`, 0)},
		{"not an object", ruleRow(plID, "fixed_per_ticket", "standard", "ticket_sold", nil, `[59,109]`, 0)},
		{"trailing data", ruleRow(plID, "fixed_per_ticket", "standard", "ticket_sold", nil, seedFixedParams+`{}`, 0)},
		{"pct above 100 %", ruleRow(plID, "percent_of_price_with_cap", "venue", "ticket_sold", nil, `{"pct_bp":10001,"cap_minor":300}`, 0)},
		{"min above max", ruleRow(plID, "low_price_formula", "standard", "ticket_sold", eur, `{"threshold_minor":1000,"strict_below":true,"pct_bp":400,"min_minor":40,"max_minor":35,"seated_surcharge_minor":20}`, 0)},
		{"low price without strict_below", ruleRow(plID, "low_price_formula", "standard", "ticket_sold", eur, `{"threshold_minor":1000,"pct_bp":400,"min_minor":10,"max_minor":35,"seated_surcharge_minor":20}`, 0)},
		{"low price without sale currency", ruleRow(plID, "low_price_formula", "standard", "ticket_sold", nil, seedLowPriceParams, 0)},
		{"pack without surcharge", ruleRow(plID, "credit_pack", "standard", "ticket_sold", nil, `{}`, 0)},
		{"per event without amount", ruleRow(plID, "fixed_per_event", "standard", "event_published", nil, `{}`, 0)},
		{"max above the sanity bound", ruleRow(plID, "low_price_formula", "standard", "ticket_sold", eur, `{"threshold_minor":1000,"strict_below":true,"pct_bp":400,"min_minor":10,"max_minor":1000000001,"seated_surcharge_minor":20}`, 0)},
		{"surcharge above the sanity bound", ruleRow(plID, "low_price_formula", "standard", "ticket_sold", eur, `{"threshold_minor":1000,"strict_below":true,"pct_bp":400,"min_minor":10,"max_minor":35,"seated_surcharge_minor":9223372036854775807}`, 0)},
		{"fixed above the sanity bound", ruleRow(plID, "fixed_per_ticket", "standard", "ticket_sold", nil, `{"unseated_minor":59,"seated_minor":1000000001}`, 0)},
		{"not implemented, not an object", ruleRow(plID, "subscription", "standard", "ticket_sold", nil, `null`, 0)},
	} {
		if _, err := billing.ParseRule(tc.row, "EUR"); !errors.Is(err, billing.ErrBadRule) {
			t.Errorf("%s: %v, want ErrBadRule", tc.name, err)
		}
	}
}

func TestParseRule_SeedRowsParse(t *testing.T) {
	plID := uuid.New()
	for _, row := range seedRules(plID, "EUR") {
		r, err := billing.ParseRule(row, "EUR")
		if err != nil {
			t.Fatalf("%s: %v", row.RuleType, err)
		}
		if !r.Implemented() {
			t.Errorf("%s reported as not implemented", row.RuleType)
		}
	}
	for _, typ := range []string{"volume_above_baseline", "subscription", "generator_run"} {
		r, err := billing.ParseRule(ruleRow(plID, typ, "standard", "ticket_sold", nil, `{"any":1}`, 0), "EUR")
		if err != nil || r.Implemented() {
			t.Errorf("%s: err %v implemented %v, want recognized and not implemented", typ, err, r.Implemented())
		}
	}
}

func TestNewTerms_RefusesInconsistentRows(t *testing.T) {
	acc := gen.BillingAccountRow{ID: uuid.New(), OrgID: uuid.New(), Currency: "EUR"}
	pl := gen.PriceListRow{ID: uuid.New(), Version: 1, Currency: "EUR"}
	terms := gen.BillingAccountTermsRow{ID: uuid.New(), AccountID: acc.ID, PlanKind: "standard"}
	rules := seedRules(pl.ID, "EUR")

	usd := pl
	usd.Currency = "USD"
	if _, err := billing.NewTerms(saleAt, acc, terms, usd, rules, nil, nil); err == nil {
		t.Error("price list in another currency accepted")
	}
	otherCode := terms
	otherCode.PriceListCode = "venue-2026"
	if _, err := billing.NewTerms(saleAt, acc, otherCode, pl, rules, nil, nil); err == nil {
		t.Error("price list of another code than the terms name accepted")
	}
	odd := terms
	odd.PlanKind = "festival"
	if _, err := billing.NewTerms(saleAt, acc, odd, pl, rules, nil, nil); err == nil {
		t.Error("unknown plan kind accepted")
	}
	foreignOv := &gen.OrgRateOverrideRow{ID: uuid.New(), OrgID: acc.OrgID, Currency: "USD"}
	if _, err := billing.NewTerms(saleAt, acc, terms, pl, rules, nil, foreignOv); err == nil {
		t.Error("individual rate in another currency accepted")
	}
	dup := append(append([]gen.PriceListRuleRow{}, rules...),
		ruleRow(pl.ID, "fixed_per_ticket", "standard", "ticket_sold", nil, `{"unseated_minor":1,"seated_minor":2}`, 99))
	if _, err := billing.NewTerms(saleAt, acc, terms, pl, dup, nil, nil); !errors.Is(err, billing.ErrBadRule) ||
		!strings.Contains(err.Error(), "both fixed_per_ticket") {
		t.Errorf("two fixed_per_ticket rules: %v", err)
	}
	dupLow := append(append([]gen.PriceListRuleRow{}, rules...),
		ruleRow(pl.ID, "low_price_formula", "standard", "ticket_sold", strp("EUR"), seedLowPriceParams, 99))
	if _, err := billing.NewTerms(saleAt, acc, terms, pl, dupLow, nil, nil); !errors.Is(err, billing.ErrBadRule) {
		t.Errorf("two EUR low_price rules: %v", err)
	}
	otherCur := append(append([]gen.PriceListRuleRow{}, rules...),
		ruleRow(pl.ID, "low_price_formula", "standard", "ticket_sold", strp("CZK"), seedLowPriceParams, 99))
	if _, err := billing.NewTerms(saleAt, acc, terms, pl, otherCur, nil, nil); err != nil {
		t.Errorf("EUR and CZK low_price rules side by side: %v", err)
	}
	broken := append(append([]gen.PriceListRuleRow{}, rules...),
		ruleRow(pl.ID, "fixed_per_ticket", "venue", "ticket_sold", nil, `{"unseated_minor":"x"}`, 99))
	if _, err := billing.NewTerms(saleAt, acc, terms, pl, broken, nil, nil); !errors.Is(err, billing.ErrBadRule) {
		t.Errorf("broken params: %v", err)
	}
}

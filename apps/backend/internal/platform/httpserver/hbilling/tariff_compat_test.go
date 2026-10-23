package hbilling

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

var _ tariffCompatQueries = (*gen.Queries)(nil)

var (
	compatSeedDate = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	compatNow      = time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)
)

// compatFake is an in-memory tariffCompatQueries holding one organization.
type compatFake struct {
	accounts  []gen.BillingAccountRow
	terms     []gen.BillingAccountTermsRow
	list      gen.PriceListRow
	rules     []gen.PriceListRuleRow
	overrides []gen.OrgRateOverrideRow
	legacy    *gen.TariffRow
}

func (f *compatFake) GetBillingAccountByID(_ context.Context, id uuid.UUID) (gen.BillingAccountRow, error) {
	for _, a := range f.accounts {
		if a.ID == id {
			return a, nil
		}
	}
	return gen.BillingAccountRow{}, pgx.ErrNoRows
}

func (f *compatFake) GetBillingAccountTermsAt(_ context.Context, accountID uuid.UUID, at time.Time) (gen.BillingAccountTermsRow, error) {
	for _, t := range f.terms {
		if t.AccountID == accountID && !t.EffectiveFrom.After(at) {
			return t, nil
		}
	}
	return gen.BillingAccountTermsRow{}, pgx.ErrNoRows
}

func (f *compatFake) GetPriceListVersionAt(_ context.Context, code, currency string, at time.Time) (gen.PriceListRow, error) {
	if f.list.ID == uuid.Nil || f.list.Code != code || f.list.Currency != currency || f.list.EffectiveFrom.After(at) {
		return gen.PriceListRow{}, pgx.ErrNoRows
	}
	return f.list, nil
}

func (f *compatFake) ListPriceListRules(context.Context, uuid.UUID) ([]gen.PriceListRuleRow, error) {
	return f.rules, nil
}

func (f *compatFake) ListPriceListPacks(context.Context, uuid.UUID) ([]gen.PriceListPackRow, error) {
	return nil, nil
}

func (f *compatFake) GetOrgRateOverrideAt(_ context.Context, orgID uuid.UUID, currency string, at time.Time) (gen.OrgRateOverrideRow, error) {
	for _, o := range f.overrides {
		if o.OrgID == orgID && o.Currency == currency && !o.EffectiveFrom.After(at) {
			return o, nil
		}
	}
	return gen.OrgRateOverrideRow{}, pgx.ErrNoRows
}

func (f *compatFake) ListBillingAccountsByOrg(_ context.Context, orgID uuid.UUID) ([]gen.BillingAccountRow, error) {
	var out []gen.BillingAccountRow
	for _, a := range f.accounts {
		if a.OrgID == orgID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *compatFake) GetActiveTariff(_ context.Context, _ uuid.UUID, _ time.Time) (gen.TariffRow, error) {
	if f.legacy == nil {
		return gen.TariffRow{}, pgx.ErrNoRows
	}
	return *f.legacy, nil
}

// newCompatFake builds an organization with a standard EUR account on the
// seeded standard-2026 version 1 numbers.
func newCompatFake(plan string) (*compatFake, uuid.UUID) {
	orgID := uuid.New()
	acc := gen.BillingAccountRow{ID: uuid.New(), OrgID: orgID, Currency: "EUR", AccountKind: billing.AccountKindOrganizer}
	pl := gen.PriceListRow{
		ID: uuid.New(), Code: billing.StandardPriceListCode, Version: 1, Currency: "EUR",
		EffectiveFrom: compatSeedDate, CreatedAt: compatSeedDate,
	}
	rule := func(typ, line, kind, params string) gen.PriceListRuleRow {
		return gen.PriceListRuleRow{ID: uuid.New(), PriceListID: pl.ID, RuleType: typ, Line: line,
			EventKind: kind, Params: []byte(params), CreatedAt: compatSeedDate}
	}
	return &compatFake{
		accounts: []gen.BillingAccountRow{acc},
		terms: []gen.BillingAccountTermsRow{{ID: uuid.New(), AccountID: acc.ID, EffectiveFrom: compatSeedDate,
			PriceListCode: pl.Code, PlanKind: plan, BillingCycle: "monthly"}},
		list: pl,
		rules: []gen.PriceListRuleRow{
			rule("fixed_per_ticket", "standard", "ticket_sold", `{"unseated_minor":59,"seated_minor":109}`),
			rule("percent_of_price_with_cap", "venue", "ticket_sold", `{"pct_bp":200,"cap_minor":300}`),
		},
	}, orgID
}

func TestEffectiveTariff_StandardAccountAnswersThePriceList(t *testing.T) {
	f, orgID := newCompatFake(billing.PlanStandard)
	resp, found, err := effectiveTariff(context.Background(), f, orgID, compatNow)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if resp["id"] != f.list.ID.String() || resp["plan_name"] != "standard-2026" ||
		resp["per_ticket_fee_minor"] != int64(59) || resp["per_event_fee_minor"] != int64(0) ||
		resp["monthly_fee_minor"] != int64(0) || resp["currency"] != "EUR" ||
		resp["org_id"] != orgID.String() || resp["effective_from"] != "2026-01-01" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestEffectiveTariff_IndividualRateWinsOverThePriceList(t *testing.T) {
	f, orgID := newCompatFake(billing.PlanStandard)
	by := "migration"
	f.overrides = []gen.OrgRateOverrideRow{{ID: uuid.New(), OrgID: orgID, Currency: "EUR",
		UnseatedMinor: 50, SeatedMinor: 100, EffectiveFrom: compatSeedDate, CreatedBy: &by, CreatedAt: compatSeedDate}}
	resp, found, err := effectiveTariff(context.Background(), f, orgID, compatNow)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if resp["per_ticket_fee_minor"] != int64(50) || resp["plan_name"] != individualPlanName ||
		resp["id"] != f.overrides[0].ID.String() || resp["created_by"] != "migration" {
		t.Fatalf("resp = %+v", resp)
	}
	// The internal note of the override is never shown.
	if n, _ := resp["notes"].(string); strings.Contains(n, "политика") {
		t.Fatalf("notes leak: %q", n)
	}
}

func TestEffectiveTariff_OverrideNotYetInForceIsIgnored(t *testing.T) {
	f, orgID := newCompatFake(billing.PlanStandard)
	f.overrides = []gen.OrgRateOverrideRow{{ID: uuid.New(), OrgID: orgID, Currency: "EUR",
		UnseatedMinor: 50, SeatedMinor: 100, EffectiveFrom: compatNow.AddDate(0, 1, 0)}}
	resp, _, err := effectiveTariff(context.Background(), f, orgID, compatNow)
	if err != nil || resp["per_ticket_fee_minor"] != int64(59) {
		t.Fatalf("resp = %+v err=%v", resp, err)
	}
}

func TestEffectiveTariff_VenuePlanHasNoFixedRateAndDescribesTheShare(t *testing.T) {
	f, orgID := newCompatFake(billing.PlanVenue)
	resp, found, err := effectiveTariff(context.Background(), f, orgID, compatNow)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if resp["per_ticket_fee_minor"] != int64(0) {
		t.Fatalf("venue plan per ticket = %v", resp["per_ticket_fee_minor"])
	}
	if n, _ := resp["notes"].(string); !strings.Contains(n, "2 %") || !strings.Contains(n, "3.00") {
		t.Fatalf("notes = %v", resp["notes"])
	}
}

func TestEffectiveTariff_NoAccountFallsBackToTheLegacyRow(t *testing.T) {
	f, orgID := newCompatFake(billing.PlanStandard)
	f.accounts = nil
	if _, found, err := effectiveTariff(context.Background(), f, orgID, compatNow); err != nil || found {
		t.Fatalf("no account and no legacy row: found=%v err=%v", found, err)
	}
	f.legacy = &gen.TariffRow{ID: uuid.New(), PlanName: "legacy-plan", EffectiveFrom: compatSeedDate,
		PerTicketFeeMinor: 77, Currency: "EUR", CreatedAt: compatSeedDate}
	resp, found, err := effectiveTariff(context.Background(), f, orgID, compatNow)
	if err != nil || !found || resp["plan_name"] != "legacy-plan" || resp["per_ticket_fee_minor"] != int64(77) {
		t.Fatalf("resp = %+v found=%v err=%v", resp, found, err)
	}
}

func TestEffectiveTariff_NoOrgAnswersTheDefaultPriceList(t *testing.T) {
	f, _ := newCompatFake(billing.PlanStandard)
	resp, found, err := effectiveTariff(context.Background(), f, uuid.Nil, compatNow)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if resp["plan_name"] != "standard-2026" || resp["per_ticket_fee_minor"] != int64(59) || resp["org_id"] != nil {
		t.Fatalf("resp = %+v", resp)
	}
}

// The answer keeps the exact key set of the legacy object.
func TestEffectiveTariff_KeepsTheLegacyKeys(t *testing.T) {
	f, orgID := newCompatFake(billing.PlanStandard)
	resp, _, _ := effectiveTariff(context.Background(), f, orgID, compatNow)
	legacy := TariffToResponse(gen.TariffRow{CreatedAt: compatNow, EffectiveFrom: compatNow})
	keys := func(m map[string]any) string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return strings.Join(ks, ",")
	}
	if keys(resp) != keys(legacy) {
		t.Fatalf("keys differ:\n new    %s\n legacy %s", keys(resp), keys(legacy))
	}
}

func TestFormatHelpers(t *testing.T) {
	for in, want := range map[int64]string{200: "2", 250: "2.5", 205: "2.05", 0: "0"} {
		if got := formatBasisPoints(in); got != want {
			t.Errorf("formatBasisPoints(%d) = %q, want %q", in, got, want)
		}
	}
	if got := formatMinor(305); got != "3.05" {
		t.Errorf("formatMinor = %q", got)
	}
}

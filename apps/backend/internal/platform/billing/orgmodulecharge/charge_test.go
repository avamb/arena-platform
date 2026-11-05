package orgmodulecharge_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/orgmodulecharge"
)

// Compile-time proof that the real queries satisfy the store.
var _ orgmodulecharge.Store = (*gen.Queries)(nil)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
func ptr[T any](v T) *T                        { return &v }

// fakeStore is the Store over memory: periods, a billing currency per
// organization, and billable events deduplicated by (kind, subject).
type fakeStore struct {
	issuer    uuid.UUID
	periods   []gen.OrgModulePeriodRow
	country   map[uuid.UUID]string // billing country per organization
	countryEr map[uuid.UUID]error
	accounts  map[string]gen.BillingAccountRow
	events    map[string]gen.InsertBillableEventParams
	order     []string
}

func newStore() *fakeStore {
	return &fakeStore{
		issuer: uuid.New(), country: map[uuid.UUID]string{}, countryEr: map[uuid.UUID]error{},
		accounts: map[string]gen.BillingAccountRow{}, events: map[string]gen.InsertBillableEventParams{},
	}
}

func (f *fakeStore) GetBillingCountryRoute(_ context.Context, country string) (gen.BillingCountryRouteRow, error) {
	switch country {
	case "ES":
		return gen.BillingCountryRouteRow{Country: "ES", Currency: "EUR", IssuerID: f.issuer}, nil
	case "MX":
		return gen.BillingCountryRouteRow{Country: "MX", Currency: "USD", IssuerID: f.issuer}, nil
	}
	return gen.BillingCountryRouteRow{}, pgx.ErrNoRows
}

func (f *fakeStore) GetBillingIssuerByCode(_ context.Context, code string) (gen.BillingIssuerRow, error) {
	return gen.BillingIssuerRow{ID: f.issuer, Code: code}, nil
}

func (f *fakeStore) InsertBillingAccountIfMissing(_ context.Context, orgID uuid.UUID, currency string, issuerID uuid.UUID, kind string) (gen.BillingAccountRow, error) {
	key := orgID.String() + "|" + currency + "|" + kind
	if acc, ok := f.accounts[key]; ok {
		return acc, nil
	}
	acc := gen.BillingAccountRow{ID: uuid.New(), OrgID: orgID, Currency: currency, IssuerID: issuerID, AccountKind: kind}
	f.accounts[key] = acc
	return acc, nil
}

func (f *fakeStore) InsertBillingAccountTermsIfNone(context.Context, uuid.UUID, time.Time, string, uuid.UUID, string, string) error {
	return nil
}

func (f *fakeStore) GetOrgBillingCountry(_ context.Context, orgID uuid.UUID) (string, error) {
	if err := f.countryEr[orgID]; err != nil {
		return "", err
	}
	return f.country[orgID], nil
}

func (f *fakeStore) InsertBillableEvent(_ context.Context, p gen.InsertBillableEventParams) (uuid.UUID, bool, error) {
	key := p.Kind + "|" + p.SubjectID.String()
	if _, dup := f.events[key]; dup {
		return uuid.Nil, false, nil
	}
	f.events[key] = p
	f.order = append(f.order, key)
	return uuid.New(), true, nil
}

func (f *fakeStore) ListOrgModulePeriodsForMonth(_ context.Context, module string, first, last time.Time, orgID *uuid.UUID) ([]gen.OrgModulePeriodRow, error) {
	var out []gen.OrgModulePeriodRow
	for _, p := range f.periods {
		if p.Module != module || p.StartsOn.After(last) || (p.EndsOn != nil && p.EndsOn.Before(first)) ||
			(orgID != nil && p.OrgID != *orgID) {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeStore) addOrg(country string) uuid.UUID {
	id := uuid.New()
	f.country[id] = country
	return id
}

func (f *fakeStore) period(org uuid.UUID, starts time.Time, ends *time.Time, price *int64, cur *string) gen.OrgModulePeriodRow {
	p := gen.OrgModulePeriodRow{ID: uuid.New(), OrgID: org, Module: "shop", StartsOn: starts, EndsOn: ends,
		MonthlyPriceMinor: price, Currency: cur, Reason: "test"}
	f.periods = append(f.periods, p)
	return p
}

// attrs decodes the event of one period and month.
func (f *fakeStore) attrs(t *testing.T, p gen.OrgModulePeriodRow, month time.Time) (gen.InsertBillableEventParams, billing.ShopModuleMonth, bool) {
	t.Helper()
	ev, ok := f.events[string(billing.KindShopModuleMonth)+"|"+orgmodulecharge.SubjectID(p.ID, month).String()]
	if !ok {
		return ev, billing.ShopModuleMonth{}, false
	}
	var a billing.ShopModuleMonth
	if err := json.Unmarshal(ev.Attributes, &a); err != nil {
		t.Fatal(err)
	}
	return ev, a, true
}

func charger(f *fakeStore, since, now time.Time) *orgmodulecharge.Charger {
	return orgmodulecharge.New(f, orgmodulecharge.Options{Since: since, Now: func() time.Time { return now }})
}

func TestCharger_ProratesAPartialMonthAndIsIdempotent(t *testing.T) {
	f := newStore()
	org := f.addOrg("ES")
	p := f.period(org, day(2026, time.November, 16), nil, nil, nil)
	c := charger(f, day(2026, time.October, 1), day(2026, time.December, 3))

	n, err := c.Run(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("first pass: %d events, %v", n, err)
	}
	ev, a, ok := f.attrs(t, p, day(2026, time.November, 1))
	if !ok {
		t.Fatal("no November event")
	}
	if a.DaysActive != 15 || a.DaysInMonth != 30 || a.Month != "2026-11" || a.Module != "shop" || a.Currency != "EUR" ||
		a.MonthlyPriceMinor != nil || a.PeriodID != p.ID {
		t.Errorf("attributes %+v", a)
	}
	if ev.Kind != "shop_module_month" || !ev.OccurredAt.Equal(time.Date(2026, time.November, 30, 12, 0, 0, 0, time.UTC)) ||
		ev.OrgID != org {
		t.Errorf("event %+v", ev)
	}
	// October is before the period; December is not over.
	if len(f.events) != 1 {
		t.Errorf("%d events, want 1", len(f.events))
	}
	n, err = c.Run(context.Background())
	if err != nil || n != 0 || len(f.events) != 1 {
		t.Errorf("re-run: %d new, %d events, %v", n, len(f.events), err)
	}
	// The month closes: December is charged whole, November stays one event.
	n, err = charger(f, day(2026, time.October, 1), day(2027, time.January, 2)).Run(context.Background())
	if err != nil || n != 1 || len(f.events) != 2 {
		t.Errorf("next month: %d new, %d events, %v", n, len(f.events), err)
	}
	if _, a, _ := f.attrs(t, p, day(2026, time.December, 1)); a.DaysActive != 31 || a.DaysInMonth != 31 {
		t.Errorf("December %+v", a)
	}
}

func TestCharger_DoesNothingWithoutTheDateX(t *testing.T) {
	f := newStore()
	f.period(f.addOrg("ES"), day(2026, time.January, 1), nil, nil, nil)
	c := charger(f, time.Time{}, day(2026, time.December, 3))
	if c.Enabled() {
		t.Error("enabled without the date X")
	}
	if n, err := c.Run(context.Background()); n != 0 || err != nil || len(f.events) != 0 {
		t.Errorf("%d %v %d", n, err, len(f.events))
	}
	var nilCharger *orgmodulecharge.Charger
	if nilCharger.Enabled() {
		t.Error("a nil charger is enabled")
	}
}

func TestCharger_DaysBeforeTheDateXAreNeverCharged(t *testing.T) {
	f := newStore()
	p := f.period(f.addOrg("ES"), day(2026, time.January, 1), nil, nil, nil)
	if _, err := charger(f, day(2026, time.November, 20), day(2026, time.December, 3)).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, a, ok := f.attrs(t, p, day(2026, time.November, 1)); !ok || a.DaysActive != 11 {
		t.Errorf("from the 20th: %+v %v, want 11 days", a, ok)
	}
	if len(f.events) != 1 {
		t.Errorf("%d events, want only the date X month", len(f.events))
	}
}

func TestCharger_EndedPeriodChargesItsDaysThenStops(t *testing.T) {
	f := newStore()
	p := f.period(f.addOrg("ES"), day(2026, time.October, 1), ptr(day(2026, time.November, 10)), nil, nil)
	n, err := charger(f, day(2026, time.October, 1), day(2027, time.February, 5)).Run(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("%d events, %v, want October and November", n, err)
	}
	if _, a, _ := f.attrs(t, p, day(2026, time.October, 1)); a.DaysActive != 31 {
		t.Errorf("October %+v", a)
	}
	if _, a, _ := f.attrs(t, p, day(2026, time.November, 1)); a.DaysActive != 10 || a.DaysInMonth != 30 {
		t.Errorf("November %+v", a)
	}
}

func TestCharger_FreePeriodStillWritesAZeroPriceEvent(t *testing.T) {
	f := newStore()
	p := f.period(f.addOrg("ES"), day(2026, time.October, 1), nil, ptr(int64(0)), ptr("EUR"))
	// A free period in a currency the account no longer uses is not a conflict.
	odd := f.period(f.addOrg("MX"), day(2026, time.October, 1), nil, ptr(int64(0)), ptr("EUR"))
	n, err := charger(f, day(2026, time.October, 1), day(2026, time.November, 3)).Run(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("%d events, %v", n, err)
	}
	for _, per := range []gen.OrgModulePeriodRow{p, odd} {
		if _, a, ok := f.attrs(t, per, day(2026, time.October, 1)); !ok || a.MonthlyPriceMinor == nil || *a.MonthlyPriceMinor != 0 {
			t.Errorf("free period %+v %v", a, ok)
		}
	}
}

func TestCharger_CurrencyMismatchSkipsTheMonthAndTheOthersGoOn(t *testing.T) {
	f := newStore()
	bad := f.period(f.addOrg("MX"), day(2026, time.October, 1), nil, ptr(int64(2900)), ptr("EUR")) // account is USD
	noCur := f.period(f.addOrg("ES"), day(2026, time.October, 1), nil, ptr(int64(2900)), nil)      // price without a currency
	good := f.period(f.addOrg("ES"), day(2026, time.October, 1), nil, ptr(int64(2900)), ptr("EUR"))
	list := f.period(f.addOrg("MX"), day(2026, time.October, 1), nil, nil, nil)
	n, err := charger(f, day(2026, time.October, 1), day(2026, time.November, 3)).Run(context.Background())
	if !errors.Is(err, orgmodulecharge.ErrCurrencyChanged) {
		t.Fatalf("error %v, want ErrCurrencyChanged", err)
	}
	if n != 2 {
		t.Errorf("%d new events, want the good and the price-list period", n)
	}
	for per, want := range map[uuid.UUID]bool{bad.ID: false, noCur.ID: false, good.ID: true, list.ID: true} {
		if _, _, ok := f.attrs(t, gen.OrgModulePeriodRow{ID: per}, day(2026, time.October, 1)); ok != want {
			t.Errorf("period %s written=%v, want %v", per, ok, want)
		}
	}
	if _, a, _ := f.attrs(t, good, day(2026, time.October, 1)); a.MonthlyPriceMinor == nil || *a.MonthlyPriceMinor != 2900 {
		t.Errorf("individual price lost: %+v", a)
	}
}

func TestCharger_OneBrokenPeriodNeverStopsTheOthers(t *testing.T) {
	f := newStore()
	broken := f.addOrg("ES")
	f.countryEr[broken] = fmt.Errorf("db down")
	f.period(broken, day(2026, time.October, 1), nil, nil, nil)
	ok := f.period(f.addOrg("ES"), day(2026, time.October, 1), nil, nil, nil)
	n, err := charger(f, day(2026, time.October, 1), day(2026, time.November, 3)).Run(context.Background())
	if err == nil || n != 1 {
		t.Fatalf("%d events, %v, want one event and an error", n, err)
	}
	if _, _, found := f.attrs(t, ok, day(2026, time.October, 1)); !found {
		t.Error("the healthy period was held back")
	}
}

func TestCharger_OnlyTheShopModuleAndTheNamedOrganization(t *testing.T) {
	f := newStore()
	a, b := f.addOrg("ES"), f.addOrg("ES")
	pa := f.period(a, day(2026, time.October, 1), nil, nil, nil)
	f.period(b, day(2026, time.October, 1), nil, nil, nil)
	other := f.period(a, day(2026, time.October, 1), nil, nil, nil)
	f.periods[len(f.periods)-1].Module = "not_shop"
	c := orgmodulecharge.New(f, orgmodulecharge.Options{Since: day(2026, time.October, 1), OrgID: &a,
		Now: func() time.Time { return day(2026, time.November, 3) }})
	n, err := c.Run(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("%d events, %v", n, err)
	}
	if _, _, ok := f.attrs(t, pa, day(2026, time.October, 1)); !ok {
		t.Error("the shop period of the organization was not charged")
	}
	if _, _, ok := f.attrs(t, other, day(2026, time.October, 1)); ok {
		t.Error("another module was charged as the shop")
	}
}

func TestSubjectID_IsStablePerPeriodAndMonth(t *testing.T) {
	id := uuid.New()
	nov, dec := day(2026, time.November, 1), day(2026, time.December, 1)
	if orgmodulecharge.SubjectID(id, nov) != orgmodulecharge.SubjectID(id, day(2026, time.November, 17)) {
		t.Error("the subject depends on the day, not only the month")
	}
	if orgmodulecharge.SubjectID(id, nov) == orgmodulecharge.SubjectID(id, dec) ||
		orgmodulecharge.SubjectID(id, nov) == orgmodulecharge.SubjectID(uuid.New(), nov) {
		t.Error("subjects collide")
	}
}

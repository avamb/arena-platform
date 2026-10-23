package billing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

// Compile-time proof that the real queries satisfy the narrow interfaces.
var (
	_ billing.RouteQueries   = (*gen.Queries)(nil)
	_ billing.AccountQueries = (*gen.Queries)(nil)
)

type fakeTerms struct {
	effectiveFrom time.Time
	code          string
	issuer        uuid.UUID
}

type fakeQueries struct {
	issuer   uuid.UUID
	routes   map[string]gen.BillingCountryRouteRow
	routeErr error
	accounts map[string]gen.BillingAccountRow
	terms    map[uuid.UUID][]fakeTerms
}

func newFake() *fakeQueries {
	issuer := uuid.New()
	return &fakeQueries{
		issuer: issuer,
		routes: map[string]gen.BillingCountryRouteRow{
			"ES": {Country: "ES", Currency: "EUR", IssuerID: issuer},
			"CZ": {Country: "CZ", Currency: "EUR", IssuerID: issuer},
			"MX": {Country: "MX", Currency: "USD", IssuerID: issuer},
		},
		accounts: map[string]gen.BillingAccountRow{},
		terms:    map[uuid.UUID][]fakeTerms{},
	}
}

func (f *fakeQueries) GetBillingCountryRoute(_ context.Context, country string) (gen.BillingCountryRouteRow, error) {
	if f.routeErr != nil {
		return gen.BillingCountryRouteRow{}, f.routeErr
	}
	r, ok := f.routes[country]
	if !ok {
		return gen.BillingCountryRouteRow{}, pgx.ErrNoRows
	}
	return r, nil
}

func (f *fakeQueries) GetBillingIssuerByCode(_ context.Context, code string) (gen.BillingIssuerRow, error) {
	if code != billing.DefaultIssuerCode {
		return gen.BillingIssuerRow{}, pgx.ErrNoRows
	}
	return gen.BillingIssuerRow{ID: f.issuer, Code: code}, nil
}

func (f *fakeQueries) InsertBillingAccountIfMissing(_ context.Context, orgID uuid.UUID, currency string, issuerID uuid.UUID, kind string) (gen.BillingAccountRow, error) {
	key := orgID.String() + "|" + currency + "|" + kind
	if acc, ok := f.accounts[key]; ok {
		return acc, nil
	}
	acc := gen.BillingAccountRow{ID: uuid.New(), OrgID: orgID, Currency: currency, IssuerID: issuerID, AccountKind: kind}
	f.accounts[key] = acc
	return acc, nil
}

func (f *fakeQueries) InsertBillingAccountTermsIfNone(_ context.Context, accountID uuid.UUID, eff time.Time, code string, issuerID uuid.UUID, _, _ string) error {
	if len(f.terms[accountID]) > 0 {
		return nil
	}
	f.terms[accountID] = append(f.terms[accountID], fakeTerms{effectiveFrom: eff, code: code, issuer: issuerID})
	return nil
}

func TestRouteCurrency(t *testing.T) {
	f := newFake()
	cases := []struct {
		country, want string
	}{
		{"ES", "EUR"},
		{"CZ", "EUR"},
		{"MX", "USD"},
		{" mx ", "USD"},
		{"", "EUR"},
		{"CH", "EUR"},  // no route: default
		{"ESP", "EUR"}, // not alpha-2: default
		{"1A", "EUR"},
	}
	for _, tc := range cases {
		cur, issuer, err := billing.RouteCurrency(context.Background(), f, tc.country)
		if err != nil {
			t.Fatalf("RouteCurrency(%q): %v", tc.country, err)
		}
		if cur != tc.want || issuer != f.issuer {
			t.Errorf("RouteCurrency(%q) = %s, %s; want %s, %s", tc.country, cur, issuer, tc.want, f.issuer)
		}
	}
}

// A failed route lookup is an error, never a silent fall-back to EUR.
func TestRouteCurrency_DatabaseErrorIsNotDefaulted(t *testing.T) {
	f := newFake()
	f.routeErr = errors.New("connection reset")
	if _, _, err := billing.RouteCurrency(context.Background(), f, "MX"); err == nil {
		t.Fatal("RouteCurrency with a failing lookup returned no error")
	}
}

func TestEnsureAccount_CreatesOncePerCurrencyWithFirstTerms(t *testing.T) {
	f := newFake()
	ctx := context.Background()
	org := uuid.New()

	cz, err := billing.EnsureAccount(ctx, f, org, "CZ")
	if err != nil {
		t.Fatal(err)
	}
	mx, err := billing.EnsureAccount(ctx, f, org, "MX")
	if err != nil {
		t.Fatal(err)
	}
	none, err := billing.EnsureAccount(ctx, f, org, "")
	if err != nil {
		t.Fatal(err)
	}
	if cz.Currency != "EUR" || mx.Currency != "USD" || none.Currency != "EUR" {
		t.Fatalf("currencies = %s, %s, %s; want EUR, USD, EUR", cz.Currency, mx.Currency, none.Currency)
	}
	if none.ID != cz.ID {
		t.Fatalf("a second EUR call created another account")
	}
	if cz.AccountKind != billing.AccountKindOrganizer {
		t.Fatalf("account kind = %s", cz.AccountKind)
	}
	if len(f.accounts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(f.accounts))
	}
	for id, terms := range f.terms {
		if len(terms) != 1 {
			t.Fatalf("account %s has %d first-terms rows", id, len(terms))
		}
		if terms[0].code != billing.StandardPriceListCode || !terms[0].effectiveFrom.Equal(billing.InitialTermsEffectiveFrom) {
			t.Fatalf("first terms = %+v", terms[0])
		}
	}
}

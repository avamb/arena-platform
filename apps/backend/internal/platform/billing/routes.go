package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

const (
	// DefaultCurrency is the account currency of a country without a row in
	// billing_country_routes, and of an organization without any country
	// (run decision R-B2, owner question V-B1).
	DefaultCurrency = "EUR"
	// DefaultIssuerCode is the issuer of such an account: the Estonian
	// entity seeded by migration 0157 (owner decision B-14).
	DefaultIssuerCode = "abh-team-ee"
)

// RouteQueries is the narrow read set RouteCurrency needs. *gen.Queries and
// (*gen.Queries).WithTx(tx) both satisfy it.
type RouteQueries interface {
	GetBillingCountryRoute(ctx context.Context, country string) (gen.BillingCountryRouteRow, error)
	GetBillingIssuerByCode(ctx context.Context, code string) (gen.BillingIssuerRow, error)
}

// NormalizeCountry folds an ISO 3166-1 alpha-2 code to upper case without
// surrounding spaces. Anything that is not two Latin letters is "", which
// routes to the default currency, exactly like migration 0157, where such a
// value matches no route.
func NormalizeCountry(country string) string {
	c := strings.ToUpper(strings.TrimSpace(country))
	if len(c) != 2 || c[0] < 'A' || c[0] > 'Z' || c[1] < 'A' || c[1] > 'Z' {
		return ""
	}
	return c
}

// RouteCurrency returns the billing currency and issuer of a sale at a venue
// in country. An empty or unknown country, or a country without a route,
// bills in DefaultCurrency through the DefaultIssuerCode issuer. A database
// failure is returned as an error, never silently routed to the default.
func RouteCurrency(ctx context.Context, q RouteQueries, country string) (string, uuid.UUID, error) {
	if c := NormalizeCountry(country); c != "" {
		route, err := q.GetBillingCountryRoute(ctx, c)
		switch {
		case err == nil:
			return route.Currency, route.IssuerID, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return "", uuid.Nil, fmt.Errorf("billing: route of country %s: %w", c, err)
		}
	}
	issuer, err := q.GetBillingIssuerByCode(ctx, DefaultIssuerCode)
	if err != nil {
		return "", uuid.Nil, fmt.Errorf("billing: default issuer %s: %w", DefaultIssuerCode, err)
	}
	return DefaultCurrency, issuer.ID, nil
}

package billing

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

const (
	// AccountKindOrganizer is the billing account of an organizer that sells
	// tickets. The other kind, "tools", is block B6.
	AccountKindOrganizer = "organizer"
	// StandardPriceListCode is the price list a new account starts on. Its
	// numbers live in price_lists / price_list_rules, never here.
	StandardPriceListCode = "standard-2026"
	// initialTermsActor and initialTermsReason label the terms EnsureAccount
	// writes, next to the migration's own 'migration' / 'initial terms'.
	initialTermsActor  = "system"
	initialTermsReason = "initial terms"
)

// InitialTermsEffectiveFrom is the effective_from of an account's first
// terms, the same date as migration 0157 uses: earlier than any sale billed
// by this platform, so the terms of a real sale are always found.
var InitialTermsEffectiveFrom = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

// AccountQueries is the narrow query set EnsureAccount needs. *gen.Queries
// and (*gen.Queries).WithTx(tx) both satisfy it.
type AccountQueries interface {
	RouteQueries
	InsertBillingAccountIfMissing(ctx context.Context, orgID uuid.UUID, currency string, issuerID uuid.UUID, accountKind string) (gen.BillingAccountRow, error)
	InsertBillingAccountTermsIfNone(ctx context.Context, accountID uuid.UUID, effectiveFrom time.Time, priceListCode string, issuerID uuid.UUID, createdBy, reason string) error
}

// EnsureAccount returns the organizer billing account of orgID for a sale at
// a venue in country, creating it and its first terms (StandardPriceListCode,
// monthly cycle, invoice_postpaid) when they do not exist yet. It applies the
// same rule as migration 0157: the currency and issuer come from
// RouteCurrency. Repeated and concurrent calls return the same account and
// never write a second first-terms row. An existing account keeps its issuer
// and its terms.
//
// For an organization without a venue the caller passes its
// legal_address_country, else its country, as the migration does.
func EnsureAccount(ctx context.Context, q AccountQueries, orgID uuid.UUID, country string) (gen.BillingAccountRow, error) {
	currency, issuerID, err := RouteCurrency(ctx, q, country)
	if err != nil {
		return gen.BillingAccountRow{}, err
	}
	acc, err := q.InsertBillingAccountIfMissing(ctx, orgID, currency, issuerID, AccountKindOrganizer)
	if err != nil {
		return gen.BillingAccountRow{}, fmt.Errorf("billing: ensure %s account of org %s: %w", currency, orgID, err)
	}
	if err := q.InsertBillingAccountTermsIfNone(ctx, acc.ID, InitialTermsEffectiveFrom,
		StandardPriceListCode, acc.IssuerID, initialTermsActor, initialTermsReason); err != nil {
		return gen.BillingAccountRow{}, fmt.Errorf("billing: first terms of account %s: %w", acc.ID, err)
	}
	return acc, nil
}

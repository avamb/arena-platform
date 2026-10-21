package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// BuyerChargeQueries is the read set LoadBuyerCharge needs. *gen.Queries
// satisfies it; it never writes (a checkout must not create a billing
// account, EnsureAccount is the collect job's).
type BuyerChargeQueries interface {
	TermsQueries
	RouteQueries
	OrgHasBuyerPaysTerms(ctx context.Context, orgID uuid.UUID, at time.Time) (bool, error)
	GetSessionBillingCountry(ctx context.Context, sessionID uuid.UUID) (venueCountry, orgCountry string, err error)
	GetBillingAccount(ctx context.Context, orgID uuid.UUID, currency, accountKind string) (gen.BillingAccountRow, error)
}

// ErrBuyerChargeTerms wraps a data defect found while loading the buyer's
// service charge (the account has no usable terms, price list or rules). The
// checkout sells without the commission and logs it; billing then rates the
// sale at what the buyer actually paid.
var ErrBuyerChargeTerms = errors.New("billing: buyer service charge terms unusable")

// BuyerCharge is the platform's commission a buyer pays per ticket when the
// organizer's billing terms say fee_mode buyer_pays: the price-list (or
// individual) rate of one sold ticket, computed by StandardTicketCharge, the
// same code that tells the organizer the amount (service-fee-quote) and that
// bills the sale. A nil *BuyerCharge is "no commission" and is safe to call.
type BuyerCharge struct {
	terms Terms
}

// LoadBuyerCharge resolves the commission a buyer of sessionID pays at `at`.
// The sale is billed to the account billing.collect would pick: the route of
// the session venue's country (else the organization's country), never a
// newly created one. It answers nil, nil when the organization's terms are
// organizer_pays (the default), when the organization has no account for that
// route yet, or when the account's terms in force are not buyer_pays. A data
// defect in the terms is ErrBuyerChargeTerms; anything else is a read error
// the caller must not price around.
func LoadBuyerCharge(ctx context.Context, q BuyerChargeQueries, orgID, sessionID uuid.UUID, at time.Time) (*BuyerCharge, error) {
	ok, err := q.OrgHasBuyerPaysTerms(ctx, orgID, at)
	if err != nil {
		return nil, fmt.Errorf("billing: buyer_pays probe of org %s: %w", orgID, err)
	}
	if !ok {
		return nil, nil
	}
	venueCountry, orgCountry, err := q.GetSessionBillingCountry(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("billing: country of session %s: %w", sessionID, err)
	}
	country := venueCountry
	if NormalizeCountry(country) == "" {
		country = orgCountry
	}
	currency, _, err := RouteCurrency(ctx, q, country)
	if err != nil {
		return nil, err
	}
	acc, err := q.GetBillingAccount(ctx, orgID, currency, AccountKindOrganizer)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("billing: %s account of org %s: %w", currency, orgID, err)
	}
	terms, err := TermsAt(ctx, q, acc.ID, at)
	if err != nil {
		if errors.Is(err, ErrNoTerms) || errors.Is(err, ErrNoPriceList) || errors.Is(err, ErrBadTerms) || errors.Is(err, ErrBadRule) {
			return nil, fmt.Errorf("%w: %w", ErrBuyerChargeTerms, err)
		}
		return nil, err
	}
	if terms.FeeMode() != FeeModeBuyerPays {
		return nil, nil
	}
	return &BuyerCharge{terms: terms}, nil
}

// NewBuyerCharge wraps terms already loaded (tests, callers holding Terms).
// It answers nil unless the terms are buyer_pays.
func NewBuyerCharge(terms Terms) *BuyerCharge {
	if terms.FeeMode() != FeeModeBuyerPays {
		return nil
	}
	return &BuyerCharge{terms: terms}
}

// Currency is the account currency, the only sale currency the commission is
// added in ("" for a nil BuyerCharge).
func (b *BuyerCharge) Currency() string {
	if b == nil {
		return ""
	}
	return b.terms.Currency
}

// PerTicket is the commission on one ticket of unitPriceMinor (the price
// BEFORE any promo code, the price the organizer set) in saleCurrency. It is
// 0 for a free ticket, for a sale in another currency than the account's (no
// exchange rate in code, run decision R-B5: the buyer is never charged an
// amount converted by guess), and when the price list cannot rate the ticket
// (a data defect billing reports on its own). Its signature is the checkout
// pricing hook's (hcheckout.PricingRules.BuyerCharge, ordering
// CreateInput.UnitServiceCharge), so b.PerTicket is passed as is.
func (b *BuyerCharge) PerTicket(saleCurrency string, unitPriceMinor int64, seated bool) int64 {
	if b == nil || unitPriceMinor <= 0 || saleCurrency != b.terms.Currency {
		return 0
	}
	class, _ := ClassifyLowPrice(b.terms, saleCurrency, unitPriceMinor)
	c, err := StandardTicketCharge(b.terms, Event{
		OccurredAt: b.terms.At, Seated: seated, Currency: saleCurrency,
		UnitPriceMinor: unitPriceMinor, PricingClass: class,
	})
	if err != nil || c.Deferred != "" || c.AmountMinor < 0 {
		return 0
	}
	return c.AmountMinor
}

// Func is PerTicket as a plain function value, nil for a nil BuyerCharge, so
// a caller can leave a pricing hook unset when there is no commission.
func (b *BuyerCharge) Func() func(saleCurrency string, unitPriceMinor int64, seated bool) int64 {
	if b == nil {
		return nil
	}
	return b.PerTicket
}

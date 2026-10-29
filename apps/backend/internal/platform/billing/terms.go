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

// Plan kinds of billing_account_terms.plan_kind.
const (
	PlanStandard = "standard"
	PlanVenue    = "venue"
)

var (
	// ErrNoTerms: the account has no terms row in force at the date.
	ErrNoTerms = errors.New("billing: no account terms in force")
	// ErrNoPriceList: no unrevoked version of the account's price list in its
	// currency is in force at the date.
	ErrNoPriceList = errors.New("billing: no price list version in force")
	// ErrBadTerms: the rows that make up an account's conditions contradict
	// each other (a price list in another currency or under another code, an
	// unknown plan kind, an individual rate of another account). Rating cannot
	// go on with them; it is a data defect, never a transient failure.
	ErrBadTerms = errors.New("billing: inconsistent account terms")
)

// TermsQueries is the narrow read set TermsAt needs. *gen.Queries and
// (*gen.Queries).WithTx(tx) both satisfy it; inside an open transaction pass
// the WithTx form, never a pool-bound *gen.Queries (AGENTS.md).
type TermsQueries interface {
	GetBillingAccountByID(ctx context.Context, id uuid.UUID) (gen.BillingAccountRow, error)
	GetBillingAccountTermsAt(ctx context.Context, accountID uuid.UUID, at time.Time) (gen.BillingAccountTermsRow, error)
	GetPriceListVersionAt(ctx context.Context, code, currency string, at time.Time) (gen.PriceListRow, error)
	ListPriceListRules(ctx context.Context, priceListID uuid.UUID) ([]gen.PriceListRuleRow, error)
	ListPriceListPacks(ctx context.Context, priceListID uuid.UUID) ([]gen.PriceListPackRow, error)
	GetOrgRateOverrideAt(ctx context.Context, orgID uuid.UUID, currency string, at time.Time) (gen.OrgRateOverrideRow, error)
}

// Terms is everything Rate needs about one billing account at one instant:
// the account (its Currency is the currency every charge is written in), its
// terms row (plan kind, cycle), the price-list version in force with its
// parsed rules and packs, and the organization's individual rate, if any.
//
// A Terms value is bound to At. Rate refuses an event with another
// OccurredAt, because a later price-list version, terms row or individual
// rate may already be in force then.
type Terms struct {
	At        time.Time
	Account   gen.BillingAccountRow
	Terms     gen.BillingAccountTermsRow
	PriceList gen.PriceListRow
	Rules     []Rule
	Packs     []gen.PriceListPackRow
	// Override is the organization's individual rate in the account's
	// currency in force at At, nil when it has none.
	Override *gen.OrgRateOverrideRow
	// Currency is the account currency (Account.Currency).
	Currency string
}

// PlanKind is the account's plan kind at At (standard or venue).
func (t Terms) PlanKind() string { return t.Terms.PlanKind }

// FeeMode is the account's fee mode at At; an empty value (a row read before
// migration 0160) is organizer_pays.
func (t Terms) FeeMode() string {
	if t.Terms.FeeMode == "" {
		return FeeModeOrganizerPays
	}
	return t.Terms.FeeMode
}

// Complimentary billing modes of billing_account_terms.complimentary_billing
// (migration 0174, spec 46 section 3.6, owner decision R-7).
const (
	// ComplimentaryBillingFree: invitations are free up to the yearly free
	// limit of the price list, then charged as an unseated ticket.
	ComplimentaryBillingFree = "free"
	// ComplimentaryBillingStandard: every invitation pays the standard
	// per-ticket rate (StandardComplimentaryRate).
	ComplimentaryBillingStandard = "standard"
)

// ComplimentaryBilling is the account's invitation billing mode at At; an
// empty value (a row read before migration 0174) is free.
func (t Terms) ComplimentaryBilling() string {
	if t.Terms.ComplimentaryBilling == "" {
		return ComplimentaryBillingFree
	}
	return t.Terms.ComplimentaryBilling
}

// FreeTicketsAnnualLimit is the yearly number of free complimentary tickets
// of the price list version in force, nil when the version sets none (no
// limit: every free invitation stays free).
func (t Terms) FreeTicketsAnnualLimit() *int64 {
	if t.PriceList.FreeTicketsAnnualLimit == nil {
		return nil
	}
	n := int64(*t.PriceList.FreeTicketsAnnualLimit)
	return &n
}

// FreeLimitYearStart is the start of the period the yearly free limit counts
// over: the calendar year (UTC) of at.
func FreeLimitYearStart(at time.Time) time.Time {
	return time.Date(at.UTC().Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
}

// TermsAt loads the conditions of accountID in force at occurredAt: the last
// terms row with effective_from <= occurredAt, the version of its price list
// in the account's currency in force at occurredAt (never the version in
// force when TermsAt runs), that version's rules and packs, and the
// organization's individual rate at occurredAt. A missing account, terms
// row or price list is an error; a missing individual rate is not.
func TermsAt(ctx context.Context, q TermsQueries, accountID uuid.UUID, occurredAt time.Time) (Terms, error) {
	acc, err := q.GetBillingAccountByID(ctx, accountID)
	if err != nil {
		return Terms{}, fmt.Errorf("billing: account %s: %w", accountID, err)
	}
	terms, err := q.GetBillingAccountTermsAt(ctx, acc.ID, occurredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Terms{}, fmt.Errorf("%w: account %s at %s", ErrNoTerms, acc.ID, occurredAt.UTC().Format(time.RFC3339))
	}
	if err != nil {
		return Terms{}, fmt.Errorf("billing: terms of account %s: %w", acc.ID, err)
	}
	return termsWith(ctx, q, acc, terms, occurredAt)
}

// termsWith completes Terms for an account and a terms row already read: the
// price-list version in force at occurredAt, its rules and packs, and the
// organization's individual rate at occurredAt.
func termsWith(ctx context.Context, q TermsQueries, acc gen.BillingAccountRow, terms gen.BillingAccountTermsRow, occurredAt time.Time) (Terms, error) {
	pl, err := q.GetPriceListVersionAt(ctx, terms.PriceListCode, acc.Currency, occurredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Terms{}, fmt.Errorf("%w: %s %s at %s", ErrNoPriceList, terms.PriceListCode, acc.Currency,
			occurredAt.UTC().Format(time.RFC3339))
	}
	if err != nil {
		return Terms{}, fmt.Errorf("billing: price list %s %s: %w", terms.PriceListCode, acc.Currency, err)
	}
	ruleRows, err := q.ListPriceListRules(ctx, pl.ID)
	if err != nil {
		return Terms{}, fmt.Errorf("billing: rules of price list %s: %w", pl.ID, err)
	}
	packs, err := q.ListPriceListPacks(ctx, pl.ID)
	if err != nil {
		return Terms{}, fmt.Errorf("billing: packs of price list %s: %w", pl.ID, err)
	}
	var override *gen.OrgRateOverrideRow
	ov, err := q.GetOrgRateOverrideAt(ctx, acc.OrgID, acc.Currency, occurredAt)
	switch {
	case err == nil:
		override = &ov
	case !errors.Is(err, pgx.ErrNoRows):
		return Terms{}, fmt.Errorf("billing: individual rate of org %s: %w", acc.OrgID, err)
	}
	return NewTerms(occurredAt, acc, terms, pl, ruleRows, packs, override)
}

// NewTerms assembles a Terms from rows already read, parsing and checking
// every rule. TermsAt calls it; tests may too. It refuses a price list whose
// currency is not the account's or whose code is not the one the terms
// name, a plan kind it does not know, an
// individual rate in another currency, and two rules of one type for the
// same line, event kind and sale currency (the choice between them would be
// arbitrary).
func NewTerms(at time.Time, acc gen.BillingAccountRow, terms gen.BillingAccountTermsRow, pl gen.PriceListRow,
	ruleRows []gen.PriceListRuleRow, packs []gen.PriceListPackRow, override *gen.OrgRateOverrideRow,
) (Terms, error) {
	if pl.Currency != acc.Currency {
		return Terms{}, fmt.Errorf("%w: price list %s is in %s, account %s in %s", ErrBadTerms, pl.ID, pl.Currency, acc.ID, acc.Currency)
	}
	if pl.Code != terms.PriceListCode {
		return Terms{}, fmt.Errorf("%w: price list %s is %q, terms %s name %q", ErrBadTerms, pl.ID, pl.Code, terms.ID, terms.PriceListCode)
	}
	if terms.AccountID != acc.ID {
		return Terms{}, fmt.Errorf("%w: terms %s belong to account %s, not %s", ErrBadTerms, terms.ID, terms.AccountID, acc.ID)
	}
	if terms.PlanKind != PlanStandard && terms.PlanKind != PlanVenue {
		return Terms{}, fmt.Errorf("%w: account %s has unknown plan kind %q", ErrBadTerms, acc.ID, terms.PlanKind)
	}
	if override != nil && (override.Currency != acc.Currency || override.OrgID != acc.OrgID) {
		return Terms{}, fmt.Errorf("%w: individual rate %s does not belong to account %s", ErrBadTerms, override.ID, acc.ID)
	}
	type ruleKey struct{ line, kind, saleCurrency string }
	seen := map[ruleKey]map[RuleType]uuid.UUID{}
	rules := make([]Rule, 0, len(ruleRows))
	for _, row := range ruleRows {
		if row.PriceListID != pl.ID {
			return Terms{}, fmt.Errorf("%w: rule %s belongs to price list %s, not %s", ErrBadTerms, row.ID, row.PriceListID, pl.ID)
		}
		r, err := ParseRule(row, pl.Currency)
		if err != nil {
			return Terms{}, err
		}
		k := ruleKey{r.Line, r.EventKind, r.SaleCurrency}
		if seen[k] == nil {
			seen[k] = map[RuleType]uuid.UUID{}
		}
		if prev, dup := seen[k][r.Type]; dup {
			return Terms{}, fmt.Errorf("%w: rules %s and %s are both %s for line %s, event %s",
				ErrBadRule, prev, r.ID, r.Type, r.Line, r.EventKind)
		}
		seen[k][r.Type] = r.ID
		rules = append(rules, r)
	}
	return Terms{
		At:        at,
		Account:   acc,
		Terms:     terms,
		PriceList: pl,
		Rules:     rules,
		Packs:     packs,
		Override:  override,
		Currency:  acc.Currency,
	}, nil
}

// rule returns the rule of type typ for line and event kind; saleCurrency
// narrows to rules with that sale_currency ("" matches rules without one).
func (t Terms) rule(line, kind string, typ RuleType, saleCurrency string) (Rule, bool) {
	for _, r := range t.Rules {
		if r.Line == line && r.EventKind == kind && r.Type == typ && r.SaleCurrency == saleCurrency {
			return r, true
		}
	}
	return Rule{}, false
}

// unimplemented returns a rule of line and event kind that Rate cannot
// compute yet, if the price list carries one.
func (t Terms) unimplemented(line, kind string) (Rule, bool) {
	for _, r := range t.Rules {
		if r.Line == line && r.EventKind == kind && !r.Implemented() {
			return r, true
		}
	}
	return Rule{}, false
}

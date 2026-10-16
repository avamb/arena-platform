// tariff_compat.go - the compatibility read of GET /v1/billing/tariffs/active
// (billing wave B-A, task B1-4).
//
// The `tariffs` table is LEGACY since migration 0157: rates live in
// price_lists / price_list_rules / org_rate_overrides. The route keeps the
// JSON object it always answered (TariffToResponse keys, openapi untouched)
// and fills it from the EFFECTIVE rate of the organization's billing
// account at "now", the same TermsAt the rating engine uses:
//
//	id                    the individual rate's id, else the price-list version's id
//	plan_name             "individual" for an individual rate, else the price list code
//	effective_from        the start of that rate / version (date)
//	per_ticket_fee_minor  an individual rate's unseated rate, else the standard
//	                      line's fixed_per_ticket unseated rate; 0 on a venue plan
//	                      (it pays a share of the price, described in notes)
//	per_event_fee_minor   the standard line's fixed_per_event amount for
//	                      event_published, 0 when the price list has none
//	monthly_fee_minor     0: no subscription rule is computed yet
//	currency              the account's currency
//
// The seated rate has no field in the legacy shape and is not answered.
// An organization without a billing account, terms or price list in force
// still gets its legacy `tariffs` row, so nothing that answered 200 answers
// 404 now. Invoice generation still reads `tariffs` (block B4 replaces it).
package hbilling

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

// individualPlanName is plan_name of an answer that comes from an
// organization's individual rate.
const individualPlanName = "individual"

// tariffCompatQueries is the read set the compatibility answer needs.
// *gen.Queries satisfies it.
type tariffCompatQueries interface {
	billing.TermsQueries
	ListBillingAccountsByOrg(ctx context.Context, orgID uuid.UUID) ([]gen.BillingAccountRow, error)
	GetActiveTariff(ctx context.Context, orgID uuid.UUID, asOfDate time.Time) (gen.TariffRow, error)
}

// effectiveTariff answers the legacy tariff object for orgID (uuid.Nil: the
// platform default) at now. found is false when neither the new tables nor
// the legacy table know a tariff.
func effectiveTariff(ctx context.Context, q tariffCompatQueries, orgID uuid.UUID, now time.Time) (resp map[string]any, found bool, err error) {
	t, ok, err := effectiveTerms(ctx, q, orgID, now)
	if err != nil {
		return nil, false, err
	}
	if ok {
		return tariffFromTerms(t, orgID), true, nil
	}
	row, err := q.GetActiveTariff(ctx, orgID, now)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return TariffToResponse(row), true, nil
}

// effectiveTerms loads the conditions of the organization's billing account.
// ok is false when the organization has no organizer account, or its account
// has no terms or no price list in force (the caller falls back to the legacy
// table). Several accounts (one per currency): the default currency wins,
// else the first by currency. uuid.Nil asks for the default price list
// itself, in the default currency.
func effectiveTerms(ctx context.Context, q tariffCompatQueries, orgID uuid.UUID, now time.Time) (billing.Terms, bool, error) {
	if orgID == uuid.Nil {
		t, ok, err := defaultTerms(ctx, q, now)
		return t, ok, err
	}
	accounts, err := q.ListBillingAccountsByOrg(ctx, orgID)
	if err != nil {
		return billing.Terms{}, false, fmt.Errorf("billing accounts of org %s: %w", orgID, err)
	}
	var pick *gen.BillingAccountRow
	for i := range accounts {
		a := &accounts[i]
		if a.AccountKind != billing.AccountKindOrganizer {
			continue
		}
		if pick == nil || (a.Currency == billing.DefaultCurrency && pick.Currency != billing.DefaultCurrency) {
			pick = a
		}
	}
	if pick == nil {
		return billing.Terms{}, false, nil
	}
	t, err := billing.TermsAt(ctx, q, pick.ID, now)
	switch {
	case err == nil:
		return t, true, nil
	case errors.Is(err, billing.ErrNoTerms), errors.Is(err, billing.ErrNoPriceList):
		return billing.Terms{}, false, nil
	}
	return billing.Terms{}, false, err
}

// defaultTerms is the standard price list in the default currency, without an
// account: what the platform offers an organization that has no individual
// terms. It carries no override.
func defaultTerms(ctx context.Context, q tariffCompatQueries, now time.Time) (billing.Terms, bool, error) {
	pl, err := q.GetPriceListVersionAt(ctx, billing.StandardPriceListCode, billing.DefaultCurrency, now)
	if errors.Is(err, pgx.ErrNoRows) {
		return billing.Terms{}, false, nil
	}
	if err != nil {
		return billing.Terms{}, false, fmt.Errorf("default price list: %w", err)
	}
	ruleRows, err := q.ListPriceListRules(ctx, pl.ID)
	if err != nil {
		return billing.Terms{}, false, fmt.Errorf("rules of price list %s: %w", pl.ID, err)
	}
	rules := make([]billing.Rule, 0, len(ruleRows))
	for _, row := range ruleRows {
		r, err := billing.ParseRule(row, pl.Currency)
		if err != nil {
			return billing.Terms{}, false, err
		}
		rules = append(rules, r)
	}
	return billing.Terms{
		At:        now,
		PriceList: pl,
		Rules:     rules,
		Terms:     gen.BillingAccountTermsRow{PlanKind: billing.PlanStandard, PriceListCode: pl.Code},
		Currency:  pl.Currency,
	}, true, nil
}

// tariffFromTerms maps the effective conditions to the legacy object.
func tariffFromTerms(t billing.Terms, orgID uuid.UUID) map[string]any {
	var perTicket int64
	var notes any
	id := t.PriceList.ID
	planName := t.PriceList.Code
	effectiveFrom := t.PriceList.EffectiveFrom

	switch {
	case t.Override != nil:
		perTicket = t.Override.UnseatedMinor
		id = t.Override.ID
		planName = individualPlanName
		effectiveFrom = t.Override.EffectiveFrom
		notes = "individual rate of the organization, replaces the price list " + t.PriceList.Code
	case t.PlanKind() == billing.PlanVenue:
		if r, ok := findRule(t, "venue", billing.RulePercentOfPriceWithCap); ok && r.Percent != nil {
			notes = fmt.Sprintf("venue plan: %s %% of the price after the promo code, at most %s per ticket (%s)",
				formatBasisPoints(r.Percent.PctBP), formatMinor(r.Percent.CapMinor), t.Currency)
		}
	default:
		if r, ok := findRule(t, "standard", billing.RuleFixedPerTicket); ok && r.Fixed != nil {
			perTicket = r.Fixed.UnseatedMinor
		}
	}

	var perEvent int64
	if r, ok := findRuleForKind(t, "standard", string(billing.KindEventPublished), billing.RuleFixedPerEvent); ok && r.PerEvent != nil {
		perEvent = r.PerEvent.AmountMinor
	}

	resp := map[string]any{
		"id":                   id.String(),
		"plan_name":            planName,
		"effective_from":       effectiveFrom.UTC().Format(billingDateLayout),
		"per_ticket_fee_minor": perTicket,
		"per_event_fee_minor":  perEvent,
		"monthly_fee_minor":    int64(0),
		"currency":             t.Currency,
		"created_at":           t.PriceList.CreatedAt.UTC().Format(time.RFC3339),
		"org_id":               nil,
		"notes":                notes,
		"created_by":           nil,
	}
	if orgID != uuid.Nil {
		resp["org_id"] = orgID.String()
	}
	if t.Override != nil && t.Override.CreatedBy != nil {
		resp["created_by"] = *t.Override.CreatedBy
	} else if t.Override == nil && t.PriceList.CreatedBy != nil {
		resp["created_by"] = *t.PriceList.CreatedBy
	}
	if t.Override != nil {
		resp["created_at"] = t.Override.CreatedAt.UTC().Format(time.RFC3339)
	}
	return resp
}

// findRule returns the rule of type typ for the ticket_sold event of line.
func findRule(t billing.Terms, line string, typ billing.RuleType) (billing.Rule, bool) {
	return findRuleForKind(t, line, string(billing.KindTicketSold), typ)
}

func findRuleForKind(t billing.Terms, line, kind string, typ billing.RuleType) (billing.Rule, bool) {
	for _, r := range t.Rules {
		if r.Line == line && r.EventKind == kind && r.Type == typ && r.SaleCurrency == "" {
			return r, true
		}
	}
	return billing.Rule{}, false
}

// formatBasisPoints prints basis points as a percentage: 200 -> "2", 250 -> "2.5".
func formatBasisPoints(bp int64) string {
	whole, frac := bp/100, bp%100
	switch {
	case frac == 0:
		return fmt.Sprintf("%d", whole)
	case frac%10 == 0:
		return fmt.Sprintf("%d.%d", whole, frac/10)
	}
	return fmt.Sprintf("%d.%02d", whole, frac)
}

// formatMinor prints minor units with two decimals: 300 -> "3.00".
func formatMinor(minor int64) string {
	return fmt.Sprintf("%d.%02d", minor/100, minor%100)
}

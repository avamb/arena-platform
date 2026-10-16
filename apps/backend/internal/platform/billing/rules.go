package billing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// RuleType names a price_list_rules.rule_type. The CHECK of migration 0157
// lists exactly these values.
type RuleType string

const (
	RuleFixedPerTicket        RuleType = "fixed_per_ticket"
	RulePercentOfPriceWithCap RuleType = "percent_of_price_with_cap"
	RuleLowPriceFormula       RuleType = "low_price_formula"
	RuleVolumeAboveBaseline   RuleType = "volume_above_baseline"
	RuleFixedPerEvent         RuleType = "fixed_per_event"
	RuleSubscription          RuleType = "subscription"
	RuleCreditPack            RuleType = "credit_pack"
	RuleGeneratorRun          RuleType = "generator_run"
	RuleOrgRateOverride       RuleType = "org_rate_override"
	maxBasisPoints            int64    = 10_000
	lineStandard                       = "standard"
	lineVenue                          = "venue"
)

var (
	// ErrRuleNotImplemented: the rule type is known but this slice does not
	// compute it (volume_above_baseline, subscription, generator_run). Rate
	// refuses an event whose line carries such a rule rather than charging
	// it by another rule.
	ErrRuleNotImplemented = errors.New("billing: rule type not implemented")
	// ErrBadRule: a rule row whose type is unknown or whose params do not
	// decode into its shape. Never read as a zero rate.
	ErrBadRule = errors.New("billing: bad price list rule")
)

// FixedPerTicket is fixed_per_ticket: a flat rate per sold ticket.
type FixedPerTicket struct {
	UnseatedMinor int64
	SeatedMinor   int64
	// Currency is the currency of the price list the rule belongs to.
	Currency string
}

// PercentOfPriceWithCap is percent_of_price_with_cap (venue line): a share
// of the price after the promo code, capped per ticket.
type PercentOfPriceWithCap struct {
	PctBP    int64
	CapMinor int64
	Currency string
}

// LowPriceFormula is low_price_formula: a ticket whose price before the
// promo code is below ThresholdMinor (in Currency, the SALE currency) pays
// clamp(floor(net × PctBP / 10000), MinMinor, MaxMinor), plus
// SeatedSurchargeMinor for a seated ticket.
type LowPriceFormula struct {
	ThresholdMinor       int64
	StrictBelow          bool
	PctBP                int64
	MinMinor             int64
	MaxMinor             int64
	SeatedSurchargeMinor int64
	// Currency is the rule's sale_currency.
	Currency string
}

// CreditPackRule is credit_pack: the seated surcharge of a pack ticket. The
// per-tier unseated rates live in price_list_packs (PackTicketRate).
type CreditPackRule struct {
	SeatedSurchargeMinor int64
	Currency             string
}

// FixedPerEvent is fixed_per_event: a flat amount per published event.
type FixedPerEvent struct {
	AmountMinor int64
	Currency    string
}

// Rule is one parsed price_list_rules row. Exactly one of the typed params
// pointers is set, matching Type; the three types without a calculation
// (volume_above_baseline, subscription, generator_run) set none.
type Rule struct {
	ID           uuid.UUID
	Type         RuleType
	Line         string
	EventKind    string
	SaleCurrency string
	SortOrder    int32

	Fixed   *FixedPerTicket
	Percent *PercentOfPriceWithCap
	// LowPriceFormula is named after its type, not "LowPrice": the money
	// guard (B1-3) reads a field ending in Price as an amount.
	LowPriceFormula *LowPriceFormula
	Pack            *CreditPackRule
	PerEvent        *FixedPerEvent
	RawParams       json.RawMessage
}

// Implemented reports whether Rate can compute this rule type.
func (r Rule) Implemented() bool {
	switch r.Type {
	case RuleVolumeAboveBaseline, RuleSubscription, RuleGeneratorRun:
		return false
	}
	return true
}

// ParseRule decodes one rule row. priceListCurrency is the currency of the
// row's price list. Unknown types, unknown or missing params keys, values of
// the wrong JSON type, negative amounts and basis points outside 0..10000
// are errors wrapping ErrBadRule: a broken row must stop rating, never rate
// at zero.
func ParseRule(row gen.PriceListRuleRow, priceListCurrency string) (Rule, error) {
	r := Rule{
		ID:        row.ID,
		Type:      RuleType(row.RuleType),
		Line:      row.Line,
		EventKind: row.EventKind,
		SortOrder: row.SortOrder,
		RawParams: append(json.RawMessage(nil), row.Params...),
	}
	if row.SaleCurrency != nil {
		r.SaleCurrency = *row.SaleCurrency
	}
	if r.Line != lineStandard && r.Line != lineVenue {
		return Rule{}, fmt.Errorf("%w: rule %s has unknown line %q", ErrBadRule, row.ID, r.Line)
	}
	if r.EventKind == "" {
		return Rule{}, fmt.Errorf("%w: rule %s has no event kind", ErrBadRule, row.ID)
	}
	bad := func(err error) (Rule, error) {
		return Rule{}, fmt.Errorf("%w: rule %s (%s) params: %v", ErrBadRule, row.ID, r.Type, err)
	}
	switch r.Type {
	case RuleFixedPerTicket:
		var p struct {
			UnseatedMinor *int64 `json:"unseated_minor"`
			SeatedMinor   *int64 `json:"seated_minor"`
		}
		if err := decodeStrict(row.Params, &p); err != nil {
			return bad(err)
		}
		if err := requireAmounts(map[string]*int64{"unseated_minor": p.UnseatedMinor, "seated_minor": p.SeatedMinor}); err != nil {
			return bad(err)
		}
		r.Fixed = &FixedPerTicket{UnseatedMinor: *p.UnseatedMinor, SeatedMinor: *p.SeatedMinor, Currency: priceListCurrency}
	case RulePercentOfPriceWithCap:
		var p struct {
			PctBP    *int64 `json:"pct_bp"`
			CapMinor *int64 `json:"cap_minor"`
		}
		if err := decodeStrict(row.Params, &p); err != nil {
			return bad(err)
		}
		if err := requireAmounts(map[string]*int64{"pct_bp": p.PctBP, "cap_minor": p.CapMinor}); err != nil {
			return bad(err)
		}
		if *p.PctBP > maxBasisPoints {
			return bad(fmt.Errorf("pct_bp %d above %d", *p.PctBP, maxBasisPoints))
		}
		r.Percent = &PercentOfPriceWithCap{PctBP: *p.PctBP, CapMinor: *p.CapMinor, Currency: priceListCurrency}
	case RuleLowPriceFormula:
		var p struct {
			ThresholdMinor       *int64 `json:"threshold_minor"`
			StrictBelow          *bool  `json:"strict_below"`
			PctBP                *int64 `json:"pct_bp"`
			MinMinor             *int64 `json:"min_minor"`
			MaxMinor             *int64 `json:"max_minor"`
			SeatedSurchargeMinor *int64 `json:"seated_surcharge_minor"`
		}
		if err := decodeStrict(row.Params, &p); err != nil {
			return bad(err)
		}
		if err := requireAmounts(map[string]*int64{
			"threshold_minor": p.ThresholdMinor, "pct_bp": p.PctBP, "min_minor": p.MinMinor,
			"max_minor": p.MaxMinor, "seated_surcharge_minor": p.SeatedSurchargeMinor,
		}); err != nil {
			return bad(err)
		}
		switch {
		case p.StrictBelow == nil:
			return bad(errors.New("strict_below is missing"))
		case *p.PctBP > maxBasisPoints:
			return bad(fmt.Errorf("pct_bp %d above %d", *p.PctBP, maxBasisPoints))
		case *p.MinMinor > *p.MaxMinor:
			return bad(fmt.Errorf("min_minor %d above max_minor %d", *p.MinMinor, *p.MaxMinor))
		case r.SaleCurrency == "":
			return bad(errors.New("a low_price_formula rule needs its sale_currency"))
		}
		r.LowPriceFormula = &LowPriceFormula{
			ThresholdMinor: *p.ThresholdMinor, StrictBelow: *p.StrictBelow, PctBP: *p.PctBP,
			MinMinor: *p.MinMinor, MaxMinor: *p.MaxMinor, SeatedSurchargeMinor: *p.SeatedSurchargeMinor,
			Currency: r.SaleCurrency,
		}
	case RuleCreditPack:
		var p struct {
			SeatedSurchargeMinor *int64 `json:"seated_surcharge_minor"`
		}
		if err := decodeStrict(row.Params, &p); err != nil {
			return bad(err)
		}
		if err := requireAmounts(map[string]*int64{"seated_surcharge_minor": p.SeatedSurchargeMinor}); err != nil {
			return bad(err)
		}
		r.Pack = &CreditPackRule{SeatedSurchargeMinor: *p.SeatedSurchargeMinor, Currency: priceListCurrency}
	case RuleFixedPerEvent:
		var p struct {
			AmountMinor *int64 `json:"amount_minor"`
		}
		if err := decodeStrict(row.Params, &p); err != nil {
			return bad(err)
		}
		if err := requireAmounts(map[string]*int64{"amount_minor": p.AmountMinor}); err != nil {
			return bad(err)
		}
		r.PerEvent = &FixedPerEvent{AmountMinor: *p.AmountMinor, Currency: priceListCurrency}
	case RuleVolumeAboveBaseline, RuleSubscription, RuleGeneratorRun:
		// Recognized, not computed in this slice: the params must still be
		// a JSON object so a broken row is noticed now.
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(row.Params, &obj); err != nil || obj == nil {
			return bad(errors.New("params are not a JSON object"))
		}
	default:
		return Rule{}, fmt.Errorf("%w: rule %s has unknown type %q", ErrBadRule, row.ID, row.RuleType)
	}
	return r, nil
}

// decodeStrict decodes one JSON object, refusing unknown keys and trailing
// data. Integers must be JSON integers: 0.5 does not decode into int64.
func decodeStrict(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data after the params object")
	}
	return nil
}

func requireAmounts(fields map[string]*int64) error {
	for name, v := range fields {
		if v == nil {
			return fmt.Errorf("%s is missing", name)
		}
		if *v < 0 {
			return fmt.Errorf("%s is negative (%d)", name, *v)
		}
	}
	return nil
}

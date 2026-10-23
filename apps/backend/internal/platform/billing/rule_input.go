package billing

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ErrRuleInput: a rule an operator typed in cannot become a price-list row
// that Rate understands. The message names the problem for the operator.
var ErrRuleInput = errors.New("billing: invalid price list rule input")

// RuleInput is one rule of a new price-list version as an operator enters it
// (the superadmin screen "Price lists and rates"). Amounts live inside Params
// in minor units and percentages in basis points, exactly as ParseRule reads
// them. No field has a default: every number is the operator's.
type RuleInput struct {
	RuleType     string
	Line         string
	EventKind    string
	SaleCurrency string
	Params       json.RawMessage
}

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// ruleShape is what Rate reads: the line and event kind each rule type is
// consulted for. A rule outside this table would sit in the list unused, so
// the screen refuses it instead of letting an operator believe it works.
type ruleShape struct {
	lines []string
	kind  EventKind
}

var ruleShapes = map[RuleType]ruleShape{
	RuleFixedPerTicket:        {lines: []string{lineStandard}, kind: KindTicketSold},
	RuleLowPriceFormula:       {lines: []string{lineStandard}, kind: KindTicketSold},
	RuleCreditPack:            {lines: []string{lineStandard}, kind: KindTicketSold},
	RulePercentOfPriceWithCap: {lines: []string{lineVenue}, kind: KindTicketSold},
	RuleFixedPerEvent:         {lines: []string{lineStandard, lineVenue}, kind: KindEventPublished},
}

// CheckRuleInput validates one rule of a NEW version against priceListCurrency
// and returns the parsed rule. It runs the very parser the rating engine uses
// (ParseRule), then adds what only a new rule must satisfy: the type is one
// Rate can compute, its line and event kind are the ones Rate consults, and a
// sale currency is given exactly for the low-price formula.
func CheckRuleInput(in RuleInput, priceListCurrency string) (Rule, error) {
	typ := RuleType(in.RuleType)
	shape, known := ruleShapes[typ]
	if !known {
		if typ == RuleVolumeAboveBaseline || typ == RuleSubscription || typ == RuleGeneratorRun {
			return Rule{}, fmt.Errorf("%w: rule type %q is not computed by the rating engine yet", ErrRuleInput, in.RuleType)
		}
		return Rule{}, fmt.Errorf("%w: unknown rule type %q", ErrRuleInput, in.RuleType)
	}
	if !contains(shape.lines, in.Line) {
		return Rule{}, fmt.Errorf("%w: rule %s is not used on line %q", ErrRuleInput, typ, in.Line)
	}
	if in.EventKind != string(shape.kind) {
		return Rule{}, fmt.Errorf("%w: rule %s applies to the event kind %q, not %q", ErrRuleInput, typ, shape.kind, in.EventKind)
	}
	var sale *string
	switch {
	case typ == RuleLowPriceFormula:
		if !currencyCode.MatchString(in.SaleCurrency) {
			return Rule{}, fmt.Errorf("%w: rule %s needs sale_currency as three capital letters", ErrRuleInput, typ)
		}
		sc := in.SaleCurrency
		sale = &sc
	case in.SaleCurrency != "":
		return Rule{}, fmt.Errorf("%w: only low_price_formula has a sale_currency", ErrRuleInput)
	}
	if len(in.Params) == 0 {
		return Rule{}, fmt.Errorf("%w: rule %s has no params", ErrRuleInput, typ)
	}
	r, err := ParseRule(gen.PriceListRuleRow{
		ID:           uuid.Nil,
		RuleType:     in.RuleType,
		Line:         in.Line,
		EventKind:    in.EventKind,
		SaleCurrency: sale,
		Params:       in.Params,
	}, priceListCurrency)
	if err != nil {
		return Rule{}, fmt.Errorf("%w: %v", ErrRuleInput, err)
	}
	return r, nil
}

// CheckRuleSet validates every rule of a new version and refuses two rules of
// one type for the same line, event kind and sale currency: the choice
// between them would be arbitrary (NewTerms refuses such a list at rating
// time, so the screen must refuse it first).
func CheckRuleSet(rules []RuleInput, priceListCurrency string) error {
	type key struct{ typ, line, kind, sale string }
	seen := map[key]int{}
	for i, in := range rules {
		if _, err := CheckRuleInput(in, priceListCurrency); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
		k := key{in.RuleType, in.Line, in.EventKind, in.SaleCurrency}
		if prev, dup := seen[k]; dup {
			return fmt.Errorf("%w: rules %d and %d are both %s for line %s, event %s", ErrRuleInput, prev+1, i+1,
				in.RuleType, in.Line, in.EventKind)
		}
		seen[k] = i
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

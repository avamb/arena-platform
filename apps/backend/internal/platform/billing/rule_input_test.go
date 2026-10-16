package billing

import (
	"encoding/json"
	"errors"
	"testing"
)

func ri(typ, line, kind, sale, params string) RuleInput {
	return RuleInput{RuleType: typ, Line: line, EventKind: kind, SaleCurrency: sale, Params: json.RawMessage(params)}
}

func TestCheckRuleInput_AcceptsEveryRuleShapeRateReads(t *testing.T) {
	// Test numbers, not the policy's: the screen never supplies a number.
	for name, in := range map[string]RuleInput{
		"fixed":   ri("fixed_per_ticket", "standard", "ticket_sold", "", `{"unseated_minor":11,"seated_minor":22}`),
		"percent": ri("percent_of_price_with_cap", "venue", "ticket_sold", "", `{"pct_bp":150,"cap_minor":99}`),
		"low":     ri("low_price_formula", "standard", "ticket_sold", "EUR", `{"threshold_minor":500,"strict_below":true,"pct_bp":300,"min_minor":5,"max_minor":30,"seated_surcharge_minor":7}`),
		"pack":    ri("credit_pack", "standard", "ticket_sold", "", `{"seated_surcharge_minor":9}`),
		"event":   ri("fixed_per_event", "venue", "event_published", "", `{"amount_minor":123}`),
	} {
		r, err := CheckRuleInput(in, "EUR")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if string(r.Type) != in.RuleType {
			t.Errorf("%s: parsed as %s", name, r.Type)
		}
	}
}

func TestCheckRuleInput_RefusesWhatRateWouldNotUse(t *testing.T) {
	for name, in := range map[string]RuleInput{
		"unknown type":        ri("made_up", "standard", "ticket_sold", "", `{}`),
		"not computed":        ri("volume_above_baseline", "standard", "ticket_sold", "", `{}`),
		"venue line fixed":    ri("fixed_per_ticket", "venue", "ticket_sold", "", `{"unseated_minor":1,"seated_minor":2}`),
		"wrong event kind":    ri("fixed_per_ticket", "standard", "event_published", "", `{"unseated_minor":1,"seated_minor":2}`),
		"event fee on ticket": ri("fixed_per_event", "standard", "ticket_sold", "", `{"amount_minor":1}`),
		"missing key":         ri("fixed_per_ticket", "standard", "ticket_sold", "", `{"unseated_minor":1}`),
		"unknown key":         ri("fixed_per_ticket", "standard", "ticket_sold", "", `{"unseated_minor":1,"seated_minor":2,"x":1}`),
		"negative":            ri("fixed_per_ticket", "standard", "ticket_sold", "", `{"unseated_minor":-1,"seated_minor":2}`),
		"fraction":            ri("fixed_per_ticket", "standard", "ticket_sold", "", `{"unseated_minor":0.5,"seated_minor":2}`),
		"percent over 100":    ri("percent_of_price_with_cap", "venue", "ticket_sold", "", `{"pct_bp":10001,"cap_minor":1}`),
		"min above max":       ri("low_price_formula", "standard", "ticket_sold", "EUR", `{"threshold_minor":1,"strict_below":true,"pct_bp":1,"min_minor":9,"max_minor":2,"seated_surcharge_minor":0}`),
		"low without sale":    ri("low_price_formula", "standard", "ticket_sold", "", `{"threshold_minor":1,"strict_below":true,"pct_bp":1,"min_minor":1,"max_minor":2,"seated_surcharge_minor":0}`),
		"sale on fixed":       ri("fixed_per_ticket", "standard", "ticket_sold", "EUR", `{"unseated_minor":1,"seated_minor":2}`),
		"no params":           ri("fixed_per_ticket", "standard", "ticket_sold", "", ``),
	} {
		if _, err := CheckRuleInput(in, "EUR"); !errors.Is(err, ErrRuleInput) {
			t.Errorf("%s: err = %v, want ErrRuleInput", name, err)
		}
	}
}

func TestCheckRuleSet_RefusesTwoRulesForOneSlot(t *testing.T) {
	a := ri("fixed_per_ticket", "standard", "ticket_sold", "", `{"unseated_minor":1,"seated_minor":2}`)
	b := ri("fixed_per_ticket", "standard", "ticket_sold", "", `{"unseated_minor":3,"seated_minor":4}`)
	if err := CheckRuleSet([]RuleInput{a, b}, "EUR"); !errors.Is(err, ErrRuleInput) {
		t.Errorf("duplicate: %v", err)
	}
	low := ri("low_price_formula", "standard", "ticket_sold", "EUR", `{"threshold_minor":1,"strict_below":true,"pct_bp":1,"min_minor":1,"max_minor":2,"seated_surcharge_minor":0}`)
	if err := CheckRuleSet([]RuleInput{a, low}, "EUR"); err != nil {
		t.Errorf("a fixed and a low-price rule are different slots: %v", err)
	}
	lowCZK := low
	lowCZK.SaleCurrency = "CZK"
	if err := CheckRuleSet([]RuleInput{a, low, lowCZK}, "EUR"); err != nil {
		t.Errorf("low-price rules of two sale currencies are different slots: %v", err)
	}
}

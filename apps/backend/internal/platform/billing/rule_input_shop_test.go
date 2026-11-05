package billing

import (
	"errors"
	"testing"
)

// The screen accepts the monthly subscription for both services and nothing
// else for that rule type.
func TestCheckRuleInput_SubscriptionKinds(t *testing.T) {
	for _, kind := range []string{"white_label_month", "shop_module_month"} {
		r, err := CheckRuleInput(ri("subscription", "standard", kind, "", `{"monthly_minor":0}`), "EUR")
		if err != nil || r.Subscription == nil || r.EventKind != kind {
			t.Errorf("%s: %+v %v", kind, r, err)
		}
	}
	for name, in := range map[string]RuleInput{
		"on tickets":    ri("subscription", "standard", "ticket_sold", "", `{"monthly_minor":1}`),
		"venue line":    ri("subscription", "venue", "shop_module_month", "", `{"monthly_minor":1}`),
		"negative":      ri("subscription", "standard", "shop_module_month", "", `{"monthly_minor":-1}`),
		"sale currency": ri("subscription", "standard", "shop_module_month", "EUR", `{"monthly_minor":1}`),
	} {
		if _, err := CheckRuleInput(in, "EUR"); !errors.Is(err, ErrRuleInput) {
			t.Errorf("%s: err = %v, want ErrRuleInput", name, err)
		}
	}
	// One slot per kind: a white label price and a shop price may live together.
	if err := CheckRuleSet([]RuleInput{
		ri("subscription", "standard", "white_label_month", "", `{"monthly_minor":4500}`),
		ri("subscription", "standard", "shop_module_month", "", `{"monthly_minor":0}`),
	}, "EUR"); err != nil {
		t.Errorf("both subscriptions: %v", err)
	}
	if err := CheckRuleSet([]RuleInput{
		ri("subscription", "standard", "shop_module_month", "", `{"monthly_minor":1}`),
		ri("subscription", "standard", "shop_module_month", "", `{"monthly_minor":2}`),
	}, "EUR"); !errors.Is(err, ErrRuleInput) {
		t.Errorf("two shop prices: %v", err)
	}
}

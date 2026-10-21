package billing_test

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

// fee_mode buyer_pays: the organizer keeps the ticket price and the buyer's
// service charge is the platform's commission. The charge is exactly what the
// buyer paid as the service charge; the price-list rate is kept in inputs.
func TestRate_BuyerPaysServiceChargeIsTheCommission(t *testing.T) {
	terms := buildTerms(t, termsSpec{})
	terms.Terms.FeeMode = billing.FeeModeBuyerPays

	ev := sold(t, terms, "EUR", 1500, 0, true)
	ev.ServiceChargeMinor = 120
	c := mustRate(t, terms, ev)
	if c.AmountMinor != 120 || c.RuleType != billing.RuleBuyerServiceCharge || c.Reason != billing.ReasonBuyerPays {
		t.Fatalf("got %d %s %q, want 120 buyer_service_charge buyer_pays", c.AmountMinor, c.RuleType, c.Reason)
	}
	if c.Inputs["standard_minor"] != 109 || c.Inputs["service_charge_minor"] != 120 {
		t.Errorf("inputs %v, want standard_minor 109 and service_charge_minor 120", c.Inputs)
	}

	// No service charge collected: nothing to charge (the report shows the
	// gap through standard_minor).
	ev.ServiceChargeMinor = 0
	if c := mustRate(t, terms, ev); c.AmountMinor != 0 || c.Inputs["standard_minor"] != 109 {
		t.Errorf("no service charge: %d %v", c.AmountMinor, c.Inputs)
	}

	// A free ticket stays free.
	free := sold(t, terms, "EUR", 1500, 1500, false)
	if c := mustRate(t, terms, free); c.AmountMinor != 0 || c.Reason != billing.ReasonFree {
		t.Errorf("free ticket: %d %q", c.AmountMinor, c.Reason)
	}

	// Another currency than the account's waits for an exchange rate.
	usd := sold(t, terms, "USD", 1500, 0, false)
	usd.ServiceChargeMinor = 100
	if c := mustRate(t, terms, usd); c.Deferred != billing.DeferredFXRateMissing {
		t.Errorf("cross-currency service charge: deferred %q", c.Deferred)
	}

	// The default mode is unchanged.
	terms.Terms.FeeMode = ""
	ev.ServiceChargeMinor = 120
	if c := mustRate(t, terms, ev); c.AmountMinor != 109 || c.RuleType != billing.RuleFixedPerTicket {
		t.Errorf("organizer_pays: %d %s", c.AmountMinor, c.RuleType)
	}
}

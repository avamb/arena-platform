package billing_test

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

// fee_mode buyer_pays: the organizer keeps the ticket price and the buyer
// pays the platform's commission inside the service charge, on top of the
// channel's own percentage, which stays the organizer's. The charge is the
// service charge capped at the platform's rate of the ticket (standard_minor,
// at the price before the promo code), so the platform never takes the
// organizer's percentage.
func TestRate_BuyerPaysServiceChargeIsTheCommission(t *testing.T) {
	terms := buildTerms(t, termsSpec{})
	terms.Terms.FeeMode = billing.FeeModeBuyerPays

	// 1.09 commission + 0.11 of the organizer's percentage: 1.09 is charged.
	ev := sold(t, terms, "EUR", 1500, 0, true)
	ev.ServiceChargeMinor = 120
	c := mustRate(t, terms, ev)
	if c.AmountMinor != 109 || c.RuleType != billing.RuleBuyerServiceCharge || c.Reason != billing.ReasonBuyerPays {
		t.Fatalf("got %d %s %q, want 109 buyer_service_charge buyer_pays", c.AmountMinor, c.RuleType, c.Reason)
	}
	if c.Inputs["standard_minor"] != 109 || c.Inputs["service_charge_minor"] != 120 {
		t.Errorf("inputs %v, want standard_minor 109 and service_charge_minor 120", c.Inputs)
	}

	// A service charge below the rate (a sale priced without the commission)
	// is charged as paid; the gap shows through standard_minor.
	ev.ServiceChargeMinor = 100
	if c := mustRate(t, terms, ev); c.AmountMinor != 100 || c.Inputs["standard_minor"] != 109 {
		t.Errorf("service charge below the rate: %d %v", c.AmountMinor, c.Inputs)
	}

	// No service charge collected: nothing to charge.
	ev.ServiceChargeMinor = 0
	if c := mustRate(t, terms, ev); c.AmountMinor != 0 || c.Inputs["standard_minor"] != 109 {
		t.Errorf("no service charge: %d %v", c.AmountMinor, c.Inputs)
	}

	// A promo code does not lower the commission the buyer paid: it is rated
	// on the price before the code, like the checkout adds it.
	promo := sold(t, terms, "EUR", 1500, 500, false)
	promo.ServiceChargeMinor = 70
	if c := mustRate(t, terms, promo); c.AmountMinor != 59 || c.Inputs["standard_minor"] != 59 {
		t.Errorf("promo ticket: %d %v, want 59", c.AmountMinor, c.Inputs)
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

// On a venue plan the commission is a percentage of the price: billing rates
// it on the price BEFORE the promo code, the same base the checkout used, so
// a promo ticket is not charged less than its buyer paid.
func TestRate_BuyerPaysVenuePlanUsesThePriceBeforeThePromo(t *testing.T) {
	terms := buildTerms(t, termsSpec{plan: billing.PlanVenue})
	terms.Terms.FeeMode = billing.FeeModeBuyerPays
	bc := billing.NewBuyerCharge(terms)
	want := bc.PerTicket("EUR", 4000, false)
	if want <= 0 {
		t.Fatalf("venue commission on 40.00 = %d, want > 0", want)
	}
	ev := sold(t, terms, "EUR", 4000, 1000, false)
	ev.ServiceChargeMinor = want + 25
	if c := mustRate(t, terms, ev); c.AmountMinor != want {
		t.Errorf("venue buyer_pays: %d, want %d (inputs %v)", c.AmountMinor, want, c.Inputs)
	}
}

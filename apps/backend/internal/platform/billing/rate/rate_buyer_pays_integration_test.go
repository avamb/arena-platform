//go:build integration

package rate_test

// rate_buyer_pays_integration_test.go - fee_mode buyer_pays (migration
// 0160): the organizer wants the net ticket price, so the buyer pays the
// platform's commission inside the service charge, on top of the channel's
// own percentage (the organizer's). The charge is the service charge capped
// at the price-list rate, kept in the inputs as standard_minor.

import (
	"encoding/json"
	"testing"
)

func TestRate_BuyerPaysChargesTheServiceCharge(t *testing.T) {
	f := newFixture(t)
	f.must(`UPDATE billing_account_terms SET fee_mode = 'buyer_pays' WHERE account_id = $1`, f.acc.ID)
	// The service charge holds the commission and the organizer's percentage.
	ev := f.event(sale{kind: "ticket_sold", at: t0, unit: 2000})
	f.must(`UPDATE billable_events SET service_charge_minor = 1000 WHERE id = $1`, ev.ID)
	// A service charge below the rate is charged as paid.
	low := f.event(sale{kind: "ticket_sold", at: t0, unit: 2000})
	f.must(`UPDATE billable_events SET service_charge_minor = 7 WHERE id = $1`, low.ID)
	free := f.event(sale{kind: "ticket_sold", at: t0, unit: 0})
	run(t, f.rater(nil))

	c := f.charge(ev)
	if c.RuleType != "buyer_service_charge" {
		t.Fatalf("rule %s, want buyer_service_charge", c.RuleType)
	}
	if c.Reason == nil || *c.Reason != "buyer_pays" {
		t.Fatalf("reason %v, want buyer_pays", c.Reason)
	}
	var in map[string]int64
	if err := json.Unmarshal(c.Inputs, &in); err != nil {
		t.Fatal(err)
	}
	if in["standard_minor"] <= 0 || in["standard_minor"] >= 1000 {
		t.Fatalf("inputs %v: the price-list rate must be kept as standard_minor", in)
	}
	if c.AmountMinor != in["standard_minor"] {
		t.Fatalf("charge %d, want the rate %d: the organizer's percentage is not the platform's", c.AmountMinor, in["standard_minor"])
	}
	if lc := f.charge(low); lc.AmountMinor != 7 {
		t.Fatalf("service charge below the rate charged %d, want 7", lc.AmountMinor)
	}
	if fc := f.charge(free); fc.AmountMinor != 0 {
		t.Fatalf("free ticket charged %d", fc.AmountMinor)
	}
}

//go:build integration

package rate_test

// rate_buyer_pays_integration_test.go - fee_mode buyer_pays (migration
// 0160): the organizer wants the net ticket price, so the service charge the
// buyer paid on a ticket IS the platform's charge. The price-list rate is
// kept in the charge inputs as standard_minor for reports.

import (
	"encoding/json"
	"testing"
)

func TestRate_BuyerPaysChargesTheServiceCharge(t *testing.T) {
	f := newFixture(t)
	f.must(`UPDATE billing_account_terms SET fee_mode = 'buyer_pays' WHERE account_id = $1`, f.acc.ID)
	ev := f.event(sale{kind: "ticket_sold", at: t0, unit: 2000})
	f.must(`UPDATE billable_events SET service_charge_minor = 80 WHERE id = $1`, ev.ID)
	free := f.event(sale{kind: "ticket_sold", at: t0, unit: 0})
	run(t, f.rater(nil))

	c := f.charge(ev)
	if c.AmountMinor != 80 || c.RuleType != "buyer_service_charge" {
		t.Fatalf("charge %d %s, want 80 buyer_service_charge", c.AmountMinor, c.RuleType)
	}
	if c.Reason == nil || *c.Reason != "buyer_pays" {
		t.Fatalf("reason %v, want buyer_pays", c.Reason)
	}
	var in map[string]int64
	if err := json.Unmarshal(c.Inputs, &in); err != nil {
		t.Fatal(err)
	}
	if in["standard_minor"] <= 0 {
		t.Fatalf("inputs %v: the price-list rate must be kept as standard_minor", in)
	}
	if fc := f.charge(free); fc.AmountMinor != 0 {
		t.Fatalf("free ticket charged %d", fc.AmountMinor)
	}
}

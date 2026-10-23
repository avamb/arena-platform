package hcheckout

import "testing"

func testCommission(currency string, price int64, seated bool) int64 {
	if currency != "EUR" || price <= 0 {
		return 0
	}
	if seated {
		return 109
	}
	return 59
}

// The buyer commission joins the channel percentage: the percentage is taken
// on the amount after the promo code alone (no fee on the fee), the fixed
// commission is added per ticket, and the total keeps its invariant.
func TestComputePricingLines_BuyerCommissionJoinsThePercentage(t *testing.T) {
	lines := []PricingLineInput{
		{TierID: "ga", Quantity: 2, UnitPrice: 2000},
		{TierID: "seat", Quantity: 1, UnitPrice: 3000, Seated: true},
	}
	rules := PricingRules{PlatformFeeRate: 500, BuyerCharge: testCommission}
	bd := ComputePricingLines(lines, 1000, "EUR", rules)
	// subtotal 70.00, discount 10.00, 5 % of 60.00 = 3.00, commission 0.59×2 + 1.09.
	if bd.PlatformCommission != 2*59+109 {
		t.Fatalf("commission %d, want %d", bd.PlatformCommission, 2*59+109)
	}
	if bd.PlatformFee != 300+bd.PlatformCommission {
		t.Errorf("platform fee %d, want 300 + %d", bd.PlatformFee, bd.PlatformCommission)
	}
	if bd.Total != bd.Subtotal-bd.Discount+bd.PlatformFee+bd.ProviderFee+bd.Tax {
		t.Errorf("total %d breaks the invariant: %+v", bd.Total, bd)
	}

	// Without the hook nothing changes.
	plain := ComputePricingLines(lines, 1000, "EUR", PricingRules{PlatformFeeRate: 500})
	if plain.PlatformCommission != 0 || plain.PlatformFee != 300 {
		t.Errorf("no hook: %+v", plain)
	}

	// A cart the promo code makes free pays no commission: a free checkout
	// stays free and needs no payment page.
	free := ComputePricingLines(lines, 7000, "EUR", rules)
	if free.Total != 0 || free.PlatformCommission != 0 {
		t.Errorf("free cart: %+v", free)
	}

	// Free tickets in a paid cart carry no commission.
	mixed := ComputePricingLines([]PricingLineInput{
		{TierID: "free", Quantity: 3, UnitPrice: 0},
		{TierID: "ga", Quantity: 1, UnitPrice: 2000},
	}, 0, "EUR", PricingRules{BuyerCharge: testCommission})
	if mixed.PlatformCommission != 59 || mixed.Total != 2059 {
		t.Errorf("mixed free and paid: %+v", mixed)
	}
}

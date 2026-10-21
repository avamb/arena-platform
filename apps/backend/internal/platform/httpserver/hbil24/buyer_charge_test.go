package hbil24

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// The gateway keeps its rounding for the channel percentage (half away from
// zero) and adds the buyer commission as whole minor units, so the order it
// writes carries the charge RESERVATION / GET_CART showed.
func TestCartChargeMinor_CommissionJoinsTheRoundedPercentage(t *testing.T) {
	// 5 % of 18.90 is 0.945, rounded to 0.95; plus 0.59 commission.
	if got := cartChargeMinor(1890, 5, 59); got != 95+59 {
		t.Errorf("charge %d, want %d", got, 95+59)
	}
	if got := cartChargeMinor(0, 5, 59); got != 0 {
		t.Errorf("free cart charge %d, want 0", got)
	}
	if got := cartChargeMinor(1890, 0, 0); got != 0 {
		t.Errorf("no fee, no commission: %d", got)
	}

	commission := func(currency string, price int64, seated bool) int64 {
		if price <= 0 {
			return 0
		}
		return 59
	}
	lines := []hcheckout.PricingLineInput{{TierID: "t", Quantity: 1, UnitPrice: 1890}}
	bd := hcheckout.ComputePricingLines(lines, 0, "EUR",
		hcheckout.PricingRules{PlatformFeeRate: 500, BuyerCharge: commission})
	bd = applyGatewayCharge(bd, 5)
	if bd.PlatformFee != 95+59 || bd.PlatformCommission != 59 || bd.Total != 1890+95+59 {
		t.Errorf("CREATE_ORDER_EXT breakdown %+v, want fee 1.54 and total 20.44", bd)
	}
	comp := complimentaryBreakdown(bd)
	if comp.PlatformFee != 0 || comp.PlatformCommission != 0 || comp.Total != 0 {
		t.Errorf("an invitation pays no commission: %+v", comp)
	}
}

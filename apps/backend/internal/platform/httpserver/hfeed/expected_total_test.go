package hfeed

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

func TestPriceChangedDetails(t *testing.T) {
	i64 := func(v int64) *int64 { return &v }
	bd := hcheckout.PricingBreakdown{Total: 5250, Currency: "EUR"}
	free := hcheckout.PricingBreakdown{Total: 0, Currency: "EUR"}

	if _, changed := priceChangedDetails(nil, bd); changed {
		t.Error("absent expected_total must never refuse")
	}
	if _, changed := priceChangedDetails(i64(5250), bd); changed {
		t.Error("matching expected_total refused")
	}
	if _, changed := priceChangedDetails(i64(0), free); changed {
		t.Error("free cart with expected_total 0 refused")
	}
	d, changed := priceChangedDetails(i64(5000), bd)
	if !changed {
		t.Fatal("a different expected_total passed")
	}
	if d["expected_total"] != int64(5000) || d["actual_total"] != int64(5250) || d["currency"] != "EUR" {
		t.Errorf("details = %v", d)
	}
	if _, changed := priceChangedDetails(i64(1000), free); !changed {
		t.Error("a free cart the site priced at 10.00 must be refused")
	}
}

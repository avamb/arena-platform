package ordering

import "testing"

// fixedCommission is a buyer_pays hook: 0.50 a general-admission ticket, 0.90
// an assigned seat, in EUR only.
func fixedCommission(currency string, price int64, seated bool) int64 {
	if currency != "EUR" || price <= 0 {
		return 0
	}
	if seated {
		return 90
	}
	return 50
}

func sum(xs []int64) int64 {
	var s int64
	for _, x := range xs {
		s += x
	}
	return s
}

// Each ticket takes its own commission in full; only the channel percentage
// is spread by price. A cheap ticket would otherwise get a share below its
// commission and billing, which charges min(ticket charge, rate), would bill
// less than the buyer paid.
func TestAllocateCharge_EveryTicketCarriesItsCommission(t *testing.T) {
	units := []unit{{unitPrice: 1000}, {unitPrice: 10000}, {unitPrice: 10000, seated: true}}
	prices := []int64{1000, 10000, 10000}
	// 5 % of 210.00 = 10.50 percentage + 0.50 + 0.50 + 0.90 commission.
	charge := int64(1050 + 190)
	got := allocateCharge(units, prices, 21000, charge, "EUR", fixedCommission)
	if sum(got) != charge {
		t.Fatalf("shares %v sum to %d, want %d", got, sum(got), charge)
	}
	want := []int64{50 + 50, 50 + 500, 90 + 500}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("share %d = %d, want %d (all %v)", i, got[i], want[i], got)
		}
	}
}

func TestAllocateCharge_FallsBackToTheProrate(t *testing.T) {
	units := []unit{{unitPrice: 1000}, {unitPrice: 3000}}
	prices := []int64{1000, 3000}
	plain := prorate(200, prices)
	for name, got := range map[string][]int64{
		"no hook":        allocateCharge(units, prices, 4000, 200, "EUR", nil),
		"free cart":      allocateCharge(units, prices, 0, 200, "EUR", fixedCommission),
		"other currency": allocateCharge(units, prices, 4000, 200, "CZK", fixedCommission),
	} {
		for i := range plain {
			if got[i] != plain[i] {
				t.Errorf("%s: %v, want the plain prorate %v", name, got, plain)
				break
			}
		}
	}
	// A charge smaller than the commissions (priced under other terms) is
	// still spread exactly.
	if got := allocateCharge(units, prices, 4000, 60, "EUR", fixedCommission); sum(got) != 60 {
		t.Errorf("charge below the commissions must still sum to the charge: %v", got)
	}
}

// promo_discount_test.go — table-driven coverage of the PromoCode
// discount-math primitives (AB-46).
package tickets

import "testing"

func TestComputeDiscount_Percent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		value, order int64
		want         int64
	}{
		{"0% of anything is 0", 0, 10_000, 0},
		{"10% of 1000 = 100", 10, 1_000, 100},
		{"floor division: 10% of 1005 = 100 (not 100.5)", 10, 1_005, 100},
		{"100% caps at order amount", 100, 10_000, 10_000},
		{"200% is capped at order amount (guard)", 200, 500, 500},
		{"zero order = zero discount", 25, 0, 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ComputeDiscount("percent", tc.value, tc.order); got != tc.want {
				t.Errorf("got %d want %d", got, tc.want)
			}
		})
	}
}

func TestComputeDiscount_FixedAmount(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		value, order int64
		want         int64
	}{
		{"fixed under order returns value", 250, 10_000, 250},
		{"fixed equal to order returns value", 10_000, 10_000, 10_000},
		{"fixed exceeding order caps at order (no negative totals)", 50_000, 10_000, 10_000},
		{"zero fixed returns 0", 0, 10_000, 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ComputeDiscount("fixed_amount", tc.value, tc.order); got != tc.want {
				t.Errorf("got %d want %d", got, tc.want)
			}
		})
	}
}

func TestComputeDiscount_UnknownType(t *testing.T) {
	t.Parallel()
	// Documented: unknown types produce a zero discount, never an error.
	for _, dt := range []string{"", "percentage", "PERCENT", "loyalty", "amount"} {
		if got := ComputeDiscount(dt, 25, 10_000); got != 0 {
			t.Errorf("unknown type %q: got %d want 0", dt, got)
		}
	}
}

// TestComputeDiscount_NeverExceedsOrder is a property-style check across a
// range of inputs — the docstring guarantees the returned discount can be
// safely subtracted from the order without producing a negative total.
func TestComputeDiscount_NeverExceedsOrder(t *testing.T) {
	t.Parallel()
	orders := []int64{0, 1, 100, 10_000, 500_000, 1_000_000_000}
	values := []int64{0, 1, 10, 50, 99, 100, 250, 10_000, 999_999_999}
	for _, order := range orders {
		for _, v := range values {
			for _, dt := range []string{"percent", "fixed_amount"} {
				got := ComputeDiscount(dt, v, order)
				if got < 0 {
					t.Errorf("negative discount %d for type=%q value=%d order=%d", got, dt, v, order)
				}
				if got > order {
					t.Errorf("discount %d exceeds order %d for type=%q value=%d", got, order, dt, v)
				}
			}
		}
	}
}

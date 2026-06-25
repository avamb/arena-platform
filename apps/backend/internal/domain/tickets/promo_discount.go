// promo_discount.go contains the pure discount-math primitives for the
// PromoCode aggregate (feature #186).
//
// These functions have NO dependencies beyond the standard library, NO
// persistence side effects, and NO time-of-day reads. They are the canonical
// home for promo discount calculations, callable from both the HTTP layer
// and the application orchestrators.
package tickets

// DiscountType enumerates the supported promo-code discount kinds. Storing the
// strings as a named type makes "wrong discount type at the call site" a
// compile-time concern at every domain boundary, while remaining wire-format
// compatible with the existing JSON payloads ("percent" / "fixed_amount").
type DiscountType string

const (
	// DiscountTypePercent applies a percentage discount: floor(amount * pct/100).
	DiscountTypePercent DiscountType = "percent"
	// DiscountTypeFixedAmount applies a fixed discount, capped at the order
	// total so the order can never become negative.
	DiscountTypeFixedAmount DiscountType = "fixed_amount"
)

// ComputeDiscount calculates the discount amount for an order.
//
//	"percent"       : discount = orderAmount * discountValue / 100 (floor division).
//	"fixed_amount"  : discount = min(discountValue, orderAmount).
//	unknown type    : 0
//
// The result is never negative and never exceeds orderAmount, so callers can
// safely subtract it from the order total without an additional bounds check.
//
// This function is pure: same inputs always produce the same output, no
// external state is read or modified.
func ComputeDiscount(discountType string, discountValue, orderAmount int64) int64 {
	switch DiscountType(discountType) {
	case DiscountTypePercent:
		d := orderAmount * discountValue / 100
		if d > orderAmount {
			d = orderAmount
		}
		return d
	case DiscountTypeFixedAmount:
		if discountValue > orderAmount {
			return orderAmount
		}
		return discountValue
	}
	return 0
}

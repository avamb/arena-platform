package hcheckout

// ComplimentaryBreakdown turns a priced cart into an invitation: every
// ticket keeps its face value (subtotal and the per-line prices stay, so
// order_items still say what the seat is worth), the whole subtotal is
// discounted, and no service charge, provider fee or tax applies. The total
// the buyer owes — and PAY_ORDER expects — is 0.
//
// It is the ONE pricing of an invitation: the Bil24 gateway's
// CREATE_ORDER_EXT complimentary flag and the staff invitation issue
// transaction (spec 46, section 3.2) both go through it.
func ComplimentaryBreakdown(bd PricingBreakdown) PricingBreakdown {
	bd.Discount = bd.Subtotal
	bd.PlatformFee = 0
	bd.ProviderFee = 0
	bd.Tax = 0
	bd.Total = 0
	return bd
}

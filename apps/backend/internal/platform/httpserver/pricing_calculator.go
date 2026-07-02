// pricing_calculator.go bridges the *Server god-object to the pricing surface
// that now lives in the hcheckout sub-package (feature #129).
//
// The pricing pipeline (PricingRules / PricingBreakdown / ComputePricing), the
// GET /v1/checkout/quote handler (hcheckout.HandleQuote) and the canonical
// promo validation helpers (hcheckout.ValidatePromoCode / ComputeDiscount)
// all live in hcheckout/ — this file keeps the original package-httpserver
// names alive as thin aliases and forwarders so wire.go (Options.PricingRules),
// server_struct.go (pricingRules field), mount_commerce.go (s.handleQuote) and
// the structural tests (pricing_129_test.go, price_breakdown_163_test.go,
// checkout_133_test.go, promo_128_test.go) compile unchanged. The file name is
// retained because TestPricing129_PricingCalculatorFileExists asserts its
// presence in this directory.
package httpserver

import (
	"net/http"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// ─────────────────────────────────────────────────────────────────────────────
// Type aliases — the canonical definitions live in hcheckout/handler.go
// ─────────────────────────────────────────────────────────────────────────────

// PricingRules holds the platform's fee and tax rates expressed in basis
// points (1 bp = 0.01 %; 10 000 bp = 100 %). Alias of hcheckout.PricingRules
// so Options.PricingRules (wire.go) and the Server.pricingRules field keep
// their public shape.
type PricingRules = hcheckout.PricingRules

// PricingBreakdown is the result of running ComputePricing. Every field is
// expressed in the smallest currency unit (integer cents). Alias of
// hcheckout.PricingBreakdown.
type PricingBreakdown = hcheckout.PricingBreakdown

// ─────────────────────────────────────────────────────────────────────────────
// Pure-function forwarders
// ─────────────────────────────────────────────────────────────────────────────

// ComputePricing runs the pricing pipeline and returns an itemized breakdown.
// Forwards to hcheckout.ComputePricing — see that function for the arithmetic
// contract ((Subtotal - Discount) + PlatformFee + ProviderFee + Tax == Total).
func ComputePricing(unitPrice int64, quantity int32, discount int64, currency string, rules PricingRules) PricingBreakdown {
	return hcheckout.ComputePricing(unitPrice, quantity, discount, currency, rules)
}

// computeDiscount forwards to hcheckout.ComputeDiscount (itself backed by
// ticketsdomain.ComputeDiscount) so promo_128_test.go, pricing_129_test.go and
// checkout_133_test.go keep calling the discount math unqualified.
func computeDiscount(discountType string, discountValue, orderAmount int64) int64 {
	return hcheckout.ComputeDiscount(discountType, discountValue, orderAmount)
}

// validatePromoCode checks whether a promo code is applicable for a given
// order. Returns (discountAmount, errorCode); errorCode is empty when the
// code is valid. Forwards to the canonical hcheckout.ValidatePromoCode;
// feed_shims.go passes this forwarder into hfeed as the PromoValidator
// callback.
func validatePromoCode(pc gen.PromoCodeRow, orderAmount int64, now time.Time) (int64, string) {
	return hcheckout.ValidatePromoCode(pc, orderAmount, now)
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler shim
// ─────────────────────────────────────────────────────────────────────────────

// handleQuote serves GET /v1/checkout/quote (mounted in mount_commerce.go).
func (s *Server) handleQuote(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleQuote(w, r)
}

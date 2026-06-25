// pricing_mode.go contains the pure pricing-mode invariants for the
// TicketTier aggregate (feature #183).
//
// These declarations have NO dependencies beyond the standard library, NO
// persistence side effects, and NO time-of-day reads. They are the canonical
// home for the pricing-mode enum and its validation rules, callable from
// both the HTTP layer and the application orchestrators.
package catalog

// PricingMode enumerates the supported TicketTier pricing modes. The named
// type makes "wrong pricing mode at the call site" a compile-time concern at
// every domain boundary while remaining wire-format compatible with the
// existing JSON payloads ("fixed" / "free" / "pwyw").
type PricingMode string

const (
	// PricingModeFixed sells the tier for the exact price_amount (cents).
	PricingModeFixed PricingMode = "fixed"
	// PricingModeFree sells the tier for free; price_amount MUST be 0.
	PricingModeFree PricingMode = "free"
	// PricingModePWYW (pay-what-you-want) lets the buyer pick the amount
	// inside an optional [pwyw_min, pwyw_max] range.
	PricingModePWYW PricingMode = "pwyw"
)

// ValidPricingModes lists the recognised pricing-mode values. Exposed as a
// map so callers can use the idiomatic ok-pattern check.
var ValidPricingModes = map[PricingMode]bool{
	PricingModeFixed: true,
	PricingModeFree:  true,
	PricingModePWYW:  true,
}

// IsValidPricingMode reports whether the given string is one of the
// recognised TicketTier pricing modes.
func IsValidPricingMode(mode string) bool {
	return ValidPricingModes[PricingMode(mode)]
}

// ValidatePricingMode enforces the per-mode invariants for a TicketTier.
//
// Returns (errorCode, errorMessage) when validation fails; returns ("", "")
// on success. The returned codes are stable wire-format strings used by the
// HTTP error envelope, so renaming them is a public-contract change.
//
// Rules per mode:
//
//	free   : priceAmount must be 0.
//	fixed  : priceAmount must be > 0.
//	pwyw   : priceAmount must be >= 0; if both pwywMin and pwywMax are set,
//	         pwywMin must be <= pwywMax; individually each bound must be >= 0.
//
// Unknown modes pass validation (this matches the historical HTTP-layer
// behavior, where the recognised-mode check is performed by a separate gate
// before ValidatePricingMode is called).
func ValidatePricingMode(mode string, priceAmount int64, pwywMin, pwywMax *int64) (string, string) {
	switch PricingMode(mode) {
	case PricingModeFree:
		if priceAmount != 0 {
			return "tier.invalid_free_price", "price_amount must be 0 for free tiers"
		}
	case PricingModeFixed:
		if priceAmount <= 0 {
			return "tier.invalid_fixed_price", "price_amount must be greater than 0 for fixed tiers"
		}
	case PricingModePWYW:
		if priceAmount < 0 {
			return "tier.invalid_pwyw_price", "price_amount must be >= 0 for pwyw tiers"
		}
		if pwywMin != nil && pwywMax != nil && *pwywMin > *pwywMax {
			return "tier.invalid_pwyw_range", "pwyw_min must be less than or equal to pwyw_max"
		}
		if pwywMin != nil && *pwywMin < 0 {
			return "tier.invalid_pwyw_min", "pwyw_min must be >= 0"
		}
		if pwywMax != nil && *pwywMax < 0 {
			return "tier.invalid_pwyw_max", "pwyw_max must be >= 0"
		}
	}
	return "", ""
}

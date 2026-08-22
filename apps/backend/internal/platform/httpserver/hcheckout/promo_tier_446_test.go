// promo_tier_446_test.go — unit tests for AB-45c: tier-restricted promo validation
// (feature #446). Tests cover ValidatePromoForLines behaviour for unrestricted and
// restricted codes, mixed carts, min-order on eligible subtotal, and the pre-flight
// validate path.
package hcheckout

import (
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

var promoNow = time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

// activePC builds a minimal active PromoCodeRow.
func activePC(discountType string, discountValue int64, tierIDs []string, minOrder int64) gen.PromoCodeRow {
	return gen.PromoCodeRow{
		Status:           "active",
		DiscountType:     discountType,
		DiscountValue:    discountValue,
		AppliesToTierIDs: tierIDs,
		MinOrderAmount:   minOrder,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Unrestricted code — all lines eligible
// ─────────────────────────────────────────────────────────────────────────────

func TestPromoTier446_Unrestricted_AllLinesEligible(t *testing.T) {
	pc := activePC("percent", 10, nil, 0)
	lines := []TierLine{
		{TierID: "tier-a", Amount: 3000},
		{TierID: "tier-b", Amount: 2000},
	}
	discount, errCode := ValidatePromoForLines(pc, lines, promoNow)
	if errCode != "" {
		t.Fatalf("unexpected error code: %q", errCode)
	}
	// 10% of 5000 = 500
	if discount != 500 {
		t.Errorf("got discount=%d, want 500", discount)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Tier-restricted: matching tier → eligible subtotal
// ─────────────────────────────────────────────────────────────────────────────

func TestPromoTier446_Restricted_MatchingTier(t *testing.T) {
	pc := activePC("percent", 20, []string{"tier-vip"}, 0)
	lines := []TierLine{
		{TierID: "tier-vip", Amount: 4000},
	}
	discount, errCode := ValidatePromoForLines(pc, lines, promoNow)
	if errCode != "" {
		t.Fatalf("unexpected error code: %q", errCode)
	}
	// 20% of 4000 = 800
	if discount != 800 {
		t.Errorf("got discount=%d, want 800", discount)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Tier-restricted: NO matching tier → promo.tier_not_applicable
// ─────────────────────────────────────────────────────────────────────────────

func TestPromoTier446_Restricted_NoMatchingTier(t *testing.T) {
	pc := activePC("percent", 20, []string{"tier-vip"}, 0)
	lines := []TierLine{
		{TierID: "tier-ga", Amount: 3000},
	}
	_, errCode := ValidatePromoForLines(pc, lines, promoNow)
	if errCode != "promo.tier_not_applicable" {
		t.Errorf("got errCode=%q, want 'promo.tier_not_applicable'", errCode)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Mixed cart: some tiers match, some don't → discount on eligible subtotal only
// ─────────────────────────────────────────────────────────────────────────────

func TestPromoTier446_MixedCart_DiscountOnEligibleOnly(t *testing.T) {
	pc := activePC("percent", 10, []string{"tier-vip"}, 0)
	lines := []TierLine{
		{TierID: "tier-vip", Amount: 2000}, // eligible
		{TierID: "tier-ga", Amount: 3000},  // not eligible
	}
	discount, errCode := ValidatePromoForLines(pc, lines, promoNow)
	if errCode != "" {
		t.Fatalf("unexpected error code: %q", errCode)
	}
	// 10% of 2000 (eligible only) = 200
	if discount != 200 {
		t.Errorf("got discount=%d, want 200 (eligible subtotal only)", discount)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Min-order check applies to ELIGIBLE subtotal (not total)
// ─────────────────────────────────────────────────────────────────────────────

func TestPromoTier446_MinOrder_AppliesToEligibleSubtotal(t *testing.T) {
	// min_order = 1500; eligible subtotal = 1200 (< 1500) → rejected
	// total cart = 4200 (> 1500) — but must not matter
	pc := activePC("fixed_amount", 500, []string{"tier-vip"}, 1500)
	lines := []TierLine{
		{TierID: "tier-vip", Amount: 1200}, // eligible, below min
		{TierID: "tier-ga", Amount: 3000},  // not eligible
	}
	_, errCode := ValidatePromoForLines(pc, lines, promoNow)
	if errCode != "promo.invalid_order_amount" {
		t.Errorf("got errCode=%q, want 'promo.invalid_order_amount'", errCode)
	}
}

func TestPromoTier446_MinOrder_EligibleMeetsThreshold(t *testing.T) {
	// min_order = 1000; eligible subtotal = 2000 → accepted
	pc := activePC("fixed_amount", 500, []string{"tier-vip"}, 1000)
	lines := []TierLine{
		{TierID: "tier-vip", Amount: 2000},
	}
	discount, errCode := ValidatePromoForLines(pc, lines, promoNow)
	if errCode != "" {
		t.Fatalf("unexpected error code: %q", errCode)
	}
	if discount != 500 {
		t.Errorf("got discount=%d, want 500", discount)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Pre-flight (HandleValidatePromoCode path): restricted code with one matching
// tier computes discount once (not multiplied per matching line)
// ─────────────────────────────────────────────────────────────────────────────

func TestPromoTier446_PreFlight_RestrictedOneMatchingTier(t *testing.T) {
	// Simulate the pre-flight pattern: one synthetic TierLine with the matched
	// tier and the full order_amount.
	pc := activePC("percent", 15, []string{"tier-vip"}, 0)
	orderAmount := int64(6000)
	matchedTier := "tier-vip"
	lines := []TierLine{{TierID: matchedTier, Amount: orderAmount}}

	discount, errCode := ValidatePromoForLines(pc, lines, promoNow)
	if errCode != "" {
		t.Fatalf("unexpected error code: %q", errCode)
	}
	// 15% of 6000 = 900; must not be 900*N for multiple lines
	if discount != 900 {
		t.Errorf("got discount=%d, want 900", discount)
	}
}

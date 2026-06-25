// pricing_calculator.go implements the pricing pipeline for checkout quotes
// (feature #129).
//
// The pricing pipeline runs in this order:
//
//	subtotal    = unit_price × quantity
//	discounted  = subtotal − discount (promo code)
//	platformFee = discounted × platformFeeRate / 10 000
//	providerFee = discounted × providerFeeRate / 10 000
//	tax         = discounted × taxRate        / 10 000
//	total       = discounted + platformFee + providerFee + tax
//
// All monetary values are in the smallest currency unit (integer cents/agorot).
// Rates are expressed in basis points: 10 000 bp = 100 %, 500 bp = 5 %.
//
// Endpoint:
//
//	GET /v1/checkout/quote — returns an itemized price breakdown (pricing.quote)
package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// PricingRules — pluggable fee / tax configuration
// ─────────────────────────────────────────────────────────────────────────────

// PricingRules holds the platform's fee and tax rates expressed in basis points
// (1 bp = 0.01 %; 10 000 bp = 100 %).
//
// All rates default to zero (no fee / no tax) when PricingRules is not
// explicitly configured, giving a safe zero-value default.
//
// Example:
//
//	PricingRules{PlatformFeeRate: 500, ProviderFeeRate: 200, TaxRate: 0}
//	→ 5 % platform fee, 2 % provider fee, no tax.
type PricingRules struct {
	// PlatformFeeRate is the platform service charge in basis points.
	// Applied to (subtotal - discount). 500 = 5 %.
	PlatformFeeRate int64

	// ProviderFeeRate is the payment-provider processing fee in basis points.
	// Applied to (subtotal - discount). 200 = 2 %.
	ProviderFeeRate int64

	// TaxRate is the applicable sales/VAT tax in basis points.
	// Applied to (subtotal - discount). 0 = tax-exempt.
	TaxRate int64
}

// ─────────────────────────────────────────────────────────────────────────────
// PricingBreakdown — itemized pipeline result
// ─────────────────────────────────────────────────────────────────────────────

// PricingBreakdown is the result of running ComputePricing. Every field is
// expressed in the smallest currency unit (integer cents).
//
// Invariant (enforced by ComputePricing):
//
//	Discount + PlatformFee + ProviderFee + Tax + (Total - Subtotal) == 0
//
// In other words: Subtotal - Discount + PlatformFee + ProviderFee + Tax == Total.
type PricingBreakdown struct {
	UnitPrice   int64  `json:"unit_price"`   // price per single ticket
	Quantity    int32  `json:"quantity"`     // number of tickets
	Subtotal    int64  `json:"subtotal"`     // UnitPrice × Quantity
	Discount    int64  `json:"discount"`     // promo discount (≥ 0, ≤ Subtotal)
	PlatformFee int64  `json:"platform_fee"` // platform service charge
	ProviderFee int64  `json:"provider_fee"` // payment-provider processing fee
	Tax         int64  `json:"tax"`          // sales / VAT tax
	Total       int64  `json:"total"`        // all-in amount the customer pays
	Currency    string `json:"currency"`     // ISO 4217 currency code
}

// ─────────────────────────────────────────────────────────────────────────────
// ComputePricing — pure pipeline function
// ─────────────────────────────────────────────────────────────────────────────

// ComputePricing runs the pricing pipeline and returns an itemized breakdown.
//
//   - unitPrice is the per-ticket price in smallest currency units (cents).
//   - quantity is the number of tickets (must be ≥ 1; caller's responsibility).
//   - discount is the total promo-code discount already computed by
//     computeDiscount (≥ 0; capped at subtotal if larger).
//   - currency is the ISO 4217 code (e.g. "ILS", "USD").
//   - rules carries the platform/provider/tax basis-point rates.
//
// All intermediate values use integer arithmetic (floor division) to avoid
// floating-point rounding drift.  The pipeline guarantees:
//
//	(Subtotal - Discount) + PlatformFee + ProviderFee + Tax == Total
func ComputePricing(unitPrice int64, quantity int32, discount int64, currency string, rules PricingRules) PricingBreakdown {
	subtotal := unitPrice * int64(quantity)

	// Cap discount so it never exceeds the subtotal.
	if discount > subtotal {
		discount = subtotal
	}
	if discount < 0 {
		discount = 0
	}

	discounted := subtotal - discount

	// All fees/taxes are applied to the discounted amount (post-promo base).
	platformFee := discounted * rules.PlatformFeeRate / 10_000
	providerFee := discounted * rules.ProviderFeeRate / 10_000
	tax := discounted * rules.TaxRate / 10_000

	total := discounted + platformFee + providerFee + tax

	return PricingBreakdown{
		UnitPrice:   unitPrice,
		Quantity:    quantity,
		Subtotal:    subtotal,
		Discount:    discount,
		PlatformFee: platformFee,
		ProviderFee: providerFee,
		Tax:         tax,
		Total:       total,
		Currency:    currency,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// quoteResponse — JSON envelope for GET /v1/checkout/quote
// ─────────────────────────────────────────────────────────────────────────────

// quoteResponse wraps PricingBreakdown with checkout-context fields.
type quoteResponse struct {
	PricingBreakdown
	TierID    string  `json:"tier_id"`
	SessionID string  `json:"session_id"`
	PromoCode *string `json:"promo_code"` // nil when no promo applied
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/checkout/quote
// ─────────────────────────────────────────────────────────────────────────────

// handleQuote serves GET /v1/checkout/quote.
//
// Query parameters (all required unless marked optional):
//
//	tier_id     — UUID of the ticket tier
//	session_id  — UUID of the session that owns the tier
//	quantity    — number of tickets (integer ≥ 1)
//	org_id      — UUID of the organization (for promo code lookup)
//	promo_code  — (optional) promo code string to apply
//	chosen_price — (optional, pwyw tiers only) buyer-chosen price in cents
//
// The handler:
//  1. Looks up the ticket tier (returns 404 if not found).
//  2. Determines unit price based on pricing_mode (free / fixed / pwyw).
//  3. Optionally looks up and validates the promo code.
//  4. Runs ComputePricing with the server's PricingRules.
//  5. Emits a structured audit log entry (slog.Info).
//  6. Returns the itemized quote.
//
// Requires JWT + "pricing.quote" permission.
func (s *Server) handleQuote(w http.ResponseWriter, r *http.Request) {
	if s.tierQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	// ── Parse query parameters ───────────────────────────────────────────────

	q := r.URL.Query()

	tierIDStr := strings.TrimSpace(q.Get("tier_id"))
	sessionIDStr := strings.TrimSpace(q.Get("session_id"))
	quantityStr := strings.TrimSpace(q.Get("quantity"))
	orgIDStr := strings.TrimSpace(q.Get("org_id"))
	promoCodeStr := strings.TrimSpace(q.Get("promo_code"))
	chosenPriceStr := strings.TrimSpace(q.Get("chosen_price"))

	if tierIDStr == "" || sessionIDStr == "" || quantityStr == "" || orgIDStr == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"pricing.missing_params",
			"tier_id, session_id, quantity, and org_id are required query parameters",
			r,
		))
		return
	}

	tierID, err := uuid.Parse(tierIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("pricing.invalid_tier_id", "tier_id must be a valid UUID", r))
		return
	}
	sessionID, err := uuid.Parse(sessionIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("pricing.invalid_session_id", "session_id must be a valid UUID", r))
		return
	}
	orgID, err := uuid.Parse(orgIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("pricing.invalid_org_id", "org_id must be a valid UUID", r))
		return
	}

	quantity64, err := strconv.ParseInt(quantityStr, 10, 32)
	if err != nil || quantity64 <= 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("pricing.invalid_quantity", "quantity must be a positive integer", r))
		return
	}
	quantity := int32(quantity64)

	// ── Look up ticket tier ──────────────────────────────────────────────────

	tier, err := s.tierQueries.GetTicketTierByID(ctx, tierID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("pricing.tier_not_found", "ticket tier not found", r))
			return
		}
		s.logger.Error("pricing: tier lookup failed",
			slog.String("tier_id", tierID.String()),
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("pricing.tier_lookup_failed", "failed to retrieve ticket tier", r))
		return
	}

	// ── Determine unit price by pricing mode ─────────────────────────────────

	var unitPrice int64
	switch tier.PricingMode {
	case "free":
		unitPrice = 0
	case "fixed":
		unitPrice = tier.PriceAmount
	case "pwyw":
		if chosenPriceStr == "" {
			writeJSON(w, http.StatusBadRequest, errorEnvelope(
				"pricing.chosen_price_required",
				"chosen_price is required for pay-what-you-want tiers",
				r,
			))
			return
		}
		chosen, err := strconv.ParseInt(chosenPriceStr, 10, 64)
		if err != nil || chosen < 0 {
			writeJSON(w, http.StatusBadRequest, errorEnvelope("pricing.invalid_chosen_price", "chosen_price must be a non-negative integer (cents)", r))
			return
		}
		if tier.PwywMin != nil && chosen < *tier.PwywMin {
			writeJSON(w, http.StatusBadRequest, errorEnvelope(
				"pricing.chosen_price_below_min",
				"chosen_price is below the minimum allowed price for this tier",
				r,
			))
			return
		}
		if tier.PwywMax != nil && chosen > *tier.PwywMax {
			writeJSON(w, http.StatusBadRequest, errorEnvelope(
				"pricing.chosen_price_above_max",
				"chosen_price is above the maximum allowed price for this tier",
				r,
			))
			return
		}
		unitPrice = chosen
	default:
		s.logger.Error("pricing: unknown pricing mode",
			slog.String("pricing_mode", tier.PricingMode),
			slog.String("tier_id", tierID.String()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("pricing.unknown_pricing_mode", "ticket tier has an unsupported pricing mode", r))
		return
	}

	subtotal := unitPrice * int64(quantity)

	// ── Optionally validate promo code ───────────────────────────────────────

	var discount int64
	var appliedPromoCode *string
	var promoRow gen.PromoCodeRow

	if promoCodeStr != "" && s.promoQueries != nil {
		promoRow, err = s.promoQueries.GetPromoCodeByCode(ctx, orgID, promoCodeStr)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope("promo.not_found", "promo code not found or not applicable to this organization", r))
				return
			}
			s.logger.Error("pricing: promo lookup failed",
				slog.String("promo_code", promoCodeStr),
				slog.String("org_id", orgID.String()),
				slog.String("error", err.Error()),
			)
			writeJSON(w, http.StatusInternalServerError, errorEnvelope("pricing.promo_lookup_failed", "failed to retrieve promo code", r))
			return
		}

		d, errCode := validatePromoCode(promoRow, subtotal, time.Now().UTC())
		if errCode != "" {
			writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope(errCode, "promo code is not applicable", r))
			return
		}
		discount = d
		appliedPromoCode = &promoCodeStr
	}

	// ── Run pricing pipeline ─────────────────────────────────────────────────

	breakdown := ComputePricing(unitPrice, quantity, discount, tier.Currency, s.pricingRules)

	// ── Audit log (structured) ───────────────────────────────────────────────

	logArgs := []any{
		slog.String("tier_id", tierID.String()),
		slog.String("session_id", sessionID.String()),
		slog.String("org_id", orgID.String()),
		slog.Int64("unit_price", breakdown.UnitPrice),
		slog.Int("quantity", int(breakdown.Quantity)),
		slog.Int64("subtotal", breakdown.Subtotal),
		slog.Int64("discount", breakdown.Discount),
		slog.Int64("platform_fee", breakdown.PlatformFee),
		slog.Int64("provider_fee", breakdown.ProviderFee),
		slog.Int64("tax", breakdown.Tax),
		slog.Int64("total", breakdown.Total),
		slog.String("currency", breakdown.Currency),
	}
	if appliedPromoCode != nil {
		logArgs = append(logArgs,
			slog.String("promo_code", *appliedPromoCode),
			slog.String("promo_code_id", promoRow.ID.String()),
		)
	}
	s.logger.Info("pricing: quote computed", logArgs...)

	// ── Respond ──────────────────────────────────────────────────────────────

	writeJSON(w, http.StatusOK, map[string]any{
		"quote": quoteResponse{
			PricingBreakdown: breakdown,
			TierID:           tierID.String(),
			SessionID:        sessionID.String(),
			PromoCode:        appliedPromoCode,
		},
	})
}

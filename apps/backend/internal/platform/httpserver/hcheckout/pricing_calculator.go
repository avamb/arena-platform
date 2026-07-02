// pricing_calculator.go implements the checkout quote endpoint for the
// pricing pipeline (feature #129).
//
// The pipeline itself (PricingRules / PricingBreakdown / ComputePricing) lives
// in handler.go; the promo-code validation helpers live in promo_codes.go.
// This file adds the HTTP surface plus the exported ValidatePromoCode /
// ComputeDiscount wrappers that the parent httpserver package (and hfeed's
// injected PromoValidator callback) forward to.
//
// Endpoint:
//
//	GET /v1/checkout/quote — returns an itemized price breakdown (pricing.quote)
package hcheckout

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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Exported promo validation wrappers
// ─────────────────────────────────────────────────────────────────────────────

// ComputeDiscount forwards to the unexported computeDiscount helper in
// promo_codes.go (itself a thin forwarder to ticketsdomain.ComputeDiscount)
// so callers outside hcheckout — the httpserver shim layer — can reuse the
// canonical discount math without a copy.
func ComputeDiscount(discountType string, discountValue, orderAmount int64) int64 {
	return computeDiscount(discountType, discountValue, orderAmount)
}

// ValidatePromoCode checks whether a promo code is applicable for a given
// order. Returns (discountAmount, errorCode); errorCode is empty when the
// code is valid. This is the canonical implementation — the httpserver shim
// layer forwards to it and feed_shims.go injects it into hfeed as the
// PromoValidator callback.
func ValidatePromoCode(pc gen.PromoCodeRow, orderAmount int64, now time.Time) (int64, string) {
	return validatePromoCode(pc, orderAmount, now)
}

// ─────────────────────────────────────────────────────────────────────────────
// QuoteResponse — JSON envelope for GET /v1/checkout/quote
// ─────────────────────────────────────────────────────────────────────────────

// QuoteResponse wraps PricingBreakdown with checkout-context fields.
type QuoteResponse struct {
	PricingBreakdown
	TierID    string  `json:"tier_id"`
	SessionID string  `json:"session_id"`
	PromoCode *string `json:"promo_code"` // nil when no promo applied
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/checkout/quote
// ─────────────────────────────────────────────────────────────────────────────

// HandleQuote serves GET /v1/checkout/quote.
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
func (h *Handler) HandleQuote(w http.ResponseWriter, r *http.Request) {
	if h.tierQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
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
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"pricing.missing_params",
			"tier_id, session_id, quantity, and org_id are required query parameters",
			r,
		))
		return
	}

	tierID, err := uuid.Parse(tierIDStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("pricing.invalid_tier_id", "tier_id must be a valid UUID", r))
		return
	}
	sessionID, err := uuid.Parse(sessionIDStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("pricing.invalid_session_id", "session_id must be a valid UUID", r))
		return
	}
	orgID, err := uuid.Parse(orgIDStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("pricing.invalid_org_id", "org_id must be a valid UUID", r))
		return
	}

	quantity64, err := strconv.ParseInt(quantityStr, 10, 32)
	if err != nil || quantity64 <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("pricing.invalid_quantity", "quantity must be a positive integer", r))
		return
	}
	quantity := int32(quantity64)

	// ── Look up ticket tier ──────────────────────────────────────────────────

	tier, err := h.tierQueries.GetTicketTierByID(ctx, tierID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("pricing.tier_not_found", "ticket tier not found", r))
			return
		}
		h.logger.Error("pricing: tier lookup failed",
			slog.String("tier_id", tierID.String()),
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("pricing.tier_lookup_failed", "failed to retrieve ticket tier", r))
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
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"pricing.chosen_price_required",
				"chosen_price is required for pay-what-you-want tiers",
				r,
			))
			return
		}
		chosen, err := strconv.ParseInt(chosenPriceStr, 10, 64)
		if err != nil || chosen < 0 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("pricing.invalid_chosen_price", "chosen_price must be a non-negative integer (cents)", r))
			return
		}
		if tier.PwywMin != nil && chosen < *tier.PwywMin {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"pricing.chosen_price_below_min",
				"chosen_price is below the minimum allowed price for this tier",
				r,
			))
			return
		}
		if tier.PwywMax != nil && chosen > *tier.PwywMax {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"pricing.chosen_price_above_max",
				"chosen_price is above the maximum allowed price for this tier",
				r,
			))
			return
		}
		unitPrice = chosen
	default:
		h.logger.Error("pricing: unknown pricing mode",
			slog.String("pricing_mode", tier.PricingMode),
			slog.String("tier_id", tierID.String()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("pricing.unknown_pricing_mode", "ticket tier has an unsupported pricing mode", r))
		return
	}

	subtotal := unitPrice * int64(quantity)

	// ── Optionally validate promo code ───────────────────────────────────────

	var discount int64
	var appliedPromoCode *string
	var promoRow gen.PromoCodeRow

	if promoCodeStr != "" && h.promoQueries != nil {
		promoRow, err = h.promoQueries.GetPromoCodeByCode(ctx, orgID, promoCodeStr)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope("promo.not_found", "promo code not found or not applicable to this organization", r))
				return
			}
			h.logger.Error("pricing: promo lookup failed",
				slog.String("promo_code", promoCodeStr),
				slog.String("org_id", orgID.String()),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("pricing.promo_lookup_failed", "failed to retrieve promo code", r))
			return
		}

		d, errCode := validatePromoCode(promoRow, subtotal, time.Now().UTC())
		if errCode != "" {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(errCode, "promo code is not applicable", r))
			return
		}
		discount = d
		appliedPromoCode = &promoCodeStr
	}

	// ── Run pricing pipeline ─────────────────────────────────────────────────

	breakdown := ComputePricing(unitPrice, quantity, discount, tier.Currency, h.pricingRules)

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
	h.logger.Info("pricing: quote computed", logArgs...)

	// ── Respond ──────────────────────────────────────────────────────────────

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"quote": QuoteResponse{
			PricingBreakdown: breakdown,
			TierID:           tierID.String(),
			SessionID:        sessionID.String(),
			PromoCode:        appliedPromoCode,
		},
	})
}

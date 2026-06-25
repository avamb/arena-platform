// price_breakdown.go implements GET /v1/checkout/{id}/price-breakdown (feature #163).
//
// Compliance requirement (EU/IL consumer-protection law): the all-in price,
// including taxes and fees, must be displayed to the buyer before payment.
//
// The endpoint reads the stored pricing snapshot from the checkout session
// row and returns it as a structured, itemised list of discounts, fees, and
// taxes.  No re-computation is performed — the snapshot is the single source
// of truth once pricing_confirmed has fired.
//
// Endpoint:
//
//	GET /v1/checkout/{id}/price-breakdown   — itemised breakdown (checkout.read)
//
// Response envelope:
//
//	{
//	  "price_breakdown": {
//	    "subtotal":  5000,
//	    "discounts": [{"label": "Promo discount", "amount": -500}],
//	    "fees":      [{"label": "Platform fee",   "amount": 250},
//	                  {"label": "Provider fee",   "amount": 100}],
//	    "taxes":     [{"label": "VAT",            "amount": 850}],
//	    "total":     5700,
//	    "currency":  "ILS"
//	  }
//	}
//
// Sum invariant enforced: Subtotal + sum(Discounts) + sum(Fees) + sum(Taxes) == Total.
// Discount amounts are expressed as negative numbers (they reduce the total).
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response types
// ─────────────────────────────────────────────────────────────────────────────

// breakdownLineItem is a single named line in the price breakdown.
// Amount is always in the smallest currency unit (integer cents/agorot).
// Discount amounts are negative (they reduce the amount the buyer pays).
type breakdownLineItem struct {
	Label  string `json:"label"`
	Amount int64  `json:"amount"`
}

// priceBreakdownResponse is the JSON body of GET /v1/checkout/{id}/price-breakdown.
//
// The sum invariant holds by construction:
//
//	Subtotal + sum(Discounts[i].Amount) + sum(Fees[i].Amount) + sum(Taxes[i].Amount) == Total
type priceBreakdownResponse struct {
	Subtotal  int64               `json:"subtotal"`
	Discounts []breakdownLineItem `json:"discounts"`
	Fees      []breakdownLineItem `json:"fees"`
	Taxes     []breakdownLineItem `json:"taxes"`
	Total     int64               `json:"total"`
	Currency  string              `json:"currency"`
}

// ─────────────────────────────────────────────────────────────────────────────
// buildPriceBreakdown — pure helper (testable without HTTP)
// ─────────────────────────────────────────────────────────────────────────────

// buildPriceBreakdown constructs a priceBreakdownResponse from the pricing
// snapshot stored in a CheckoutSessionRow.
//
// Returns (zero, false) when the snapshot is absent (i.e. the session has not
// yet been through the pricing_confirmed transition and all pricing columns are
// still nil).
//
// The ctx parameter is used only for i18n label localisation — passing
// context.Background() in tests is valid and results in English labels.
func buildPriceBreakdown(ctx context.Context, cs gen.CheckoutSessionRow) (priceBreakdownResponse, bool) {
	// Pricing snapshot columns are nil until pricing_confirmed has been applied.
	if cs.Subtotal == nil || cs.Total == nil || cs.Currency == nil {
		return priceBreakdownResponse{}, false
	}

	resp := priceBreakdownResponse{
		Subtotal:  *cs.Subtotal,
		Discounts: []breakdownLineItem{},
		Fees:      []breakdownLineItem{},
		Taxes:     []breakdownLineItem{},
		Total:     *cs.Total,
		Currency:  *cs.Currency,
	}

	// Discounts (negative amounts — they reduce the buyer's total).
	if cs.Discount != nil && *cs.Discount > 0 {
		label := i18n.Localize(ctx, "pricing.breakdown.promo_discount", "Promo discount", nil)
		resp.Discounts = append(resp.Discounts, breakdownLineItem{
			Label:  label,
			Amount: -*cs.Discount,
		})
	}

	// Service and payment-provider fees (positive amounts).
	if cs.PlatformFee != nil && *cs.PlatformFee > 0 {
		label := i18n.Localize(ctx, "pricing.breakdown.platform_fee", "Platform fee", nil)
		resp.Fees = append(resp.Fees, breakdownLineItem{
			Label:  label,
			Amount: *cs.PlatformFee,
		})
	}
	if cs.ProviderFee != nil && *cs.ProviderFee > 0 {
		label := i18n.Localize(ctx, "pricing.breakdown.provider_fee", "Provider fee", nil)
		resp.Fees = append(resp.Fees, breakdownLineItem{
			Label:  label,
			Amount: *cs.ProviderFee,
		})
	}

	// Tax (positive amount).
	if cs.Tax != nil && *cs.Tax > 0 {
		label := i18n.Localize(ctx, "pricing.breakdown.vat", "VAT", nil)
		resp.Taxes = append(resp.Taxes, breakdownLineItem{
			Label:  label,
			Amount: *cs.Tax,
		})
	}

	return resp, true
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/checkout/{id}/price-breakdown
// ─────────────────────────────────────────────────────────────────────────────

// handlePriceBreakdown serves GET /v1/checkout/{id}/price-breakdown.
//
// Returns an itemised price breakdown for a checkout session that has
// completed the pricing_confirmed transition.  If the session is still in
// 'created' state (no pricing snapshot yet), the handler returns 409
// checkout.pricing_not_confirmed so the caller knows to call
// POST /v1/checkout/{id}/confirm first.
//
// Requires JWT + "checkout.read" permission.
func (s *Server) handlePriceBreakdown(w http.ResponseWriter, r *http.Request) {
	if s.checkoutQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"checkout.invalid_id", "checkout session id must be a valid UUID", r,
		))
		return
	}

	cs, err := s.checkoutQueries.GetCheckoutSessionByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"checkout.not_found", "checkout session not found", r,
			))
			return
		}
		s.logger.Error("checkout: price_breakdown fetch failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"checkout.get_failed", "failed to retrieve checkout session", r,
		))
		return
	}

	bd, ok := buildPriceBreakdown(ctx, cs)
	if !ok {
		writeJSON(w, http.StatusConflict, errorEnvelope(
			"checkout.pricing_not_confirmed",
			"pricing has not been confirmed for this checkout session yet",
			r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"price_breakdown": bd,
	})
}

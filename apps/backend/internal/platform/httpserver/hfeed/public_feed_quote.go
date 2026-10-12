// public_feed_quote.go implements the read-only price quote of a public cart.
//
// Endpoint:
//
//	POST /v1/public/feeds/{feed_token}/checkout/quote
//
// The widget's cart sheet shows the buyer what they will pay BEFORE they pay:
// the subtotal, the promo discount, the service charge and the total. The
// numbers come from here, never from arithmetic in the browser (guardrail #15),
// and they come through the SAME pricing path as checkout/start — the
// channel's fee (channelPricingRules), the tier price windows, the seat tier
// bindings, ValidatePromoForLines and CheckPromoLimits, then
// ComputePricingLines — so the quote is the amount the payment page asks for.
//
// What it does NOT do: it takes no row lock, bumps no seat_status_version,
// reserves no capacity, inserts no reservation, checkout session or order, and
// calls no payment provider. A quote leaves nothing behind.
//
// Request body: the lines of checkout/start (session_id, seats[], ga_items[])
// plus the optional promo_code. The quote never sees who is buying, so it
// cannot answer whether a given address already used a code: the per-customer
// cap is checked by checkout/start only.
package hfeed

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// PublicFeedCheckoutQuoteRequest is the JSON body for
// POST /v1/public/feeds/{feed_token}/checkout/quote.
type PublicFeedCheckoutQuoteRequest struct {
	SessionID string         `json:"session_id"`
	PromoCode *string        `json:"promo_code"`
	Seats     []string       `json:"seats,omitempty"`
	GaItems   []PublicGAItem `json:"ga_items,omitempty"`
}

// PublicFeedCheckoutQuoteResponse is the 200 body.
type PublicFeedCheckoutQuoteResponse struct {
	Pricing hcheckout.PricingBreakdown `json:"pricing"`
	// PromoApplied is true when a promo_code was supplied and its discount is
	// part of Pricing.
	PromoApplied bool `json:"promo_applied"`
}

// HandlePublicFeedCheckoutQuote serves
// POST /v1/public/feeds/{feed_token}/checkout/quote.
func (h *Handler) HandlePublicFeedCheckoutQuote(w http.ResponseWriter, r *http.Request) {
	// A price is a statement about one moment and one cart: never stored by a
	// browser, a proxy or a CDN, whatever the answer (even a refusal).
	w.Header().Set("Cache-Control", "no-store")
	if h.publicFeedQueries == nil || h.reservationQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	feedToken := chi.URLParam(r, "feed_token")

	// Same buckets as every other public feed route: the feed token's site-wide
	// bucket AND the per-IP one, both always evaluated.
	if !h.enforceRateLimit(w, r, "feed.rate_limited", h.rl.CheckFeedToken, feedToken) {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"checkout.invalid_body", "cannot read request body", r,
		))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"checkout.empty_body", "request body is required", r,
		))
		return
	}
	var req PublicFeedCheckoutQuoteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"checkout.invalid_json", "request body is not valid JSON", r,
		))
		return
	}
	sessionID, err := uuid.Parse(req.SessionID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"checkout.invalid_session_id", "session_id must be a valid UUID", r,
			map[string]any{"field": "session_id"},
		))
		return
	}
	hasSeats := len(req.Seats) > 0
	hasGA := len(req.GaItems) > 0
	if !hasSeats && !hasGA {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"checkout.missing_items", "provide seats[] or ga_items[]", r,
			map[string]any{"field": "seats"},
		))
		return
	}
	parsedGATierIDs := make([]uuid.UUID, 0, len(req.GaItems))
	for i, item := range req.GaItems {
		tid, perr := uuid.Parse(item.TierID)
		if perr != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"checkout.invalid_tier_id", fmt.Sprintf("ga_items[%d].tier_id must be a valid UUID", i), r,
				map[string]any{"field": "ga_items", "index": i},
			))
			return
		}
		if item.Quantity <= 0 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"checkout.invalid_qty", fmt.Sprintf("ga_items[%d].quantity must be >= 1", i), r,
				map[string]any{"field": "ga_items", "index": i},
			))
			return
		}
		parsedGATierIDs = append(parsedGATierIDs, tid)
	}

	if code, msg := checkCartCaps(req.Seats, req.GaItems); code != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(code, msg, r))
		return
	}

	ctx := r.Context()

	checkCtx, err := h.publicFeedQueries.GetPublicCheckoutContext(ctx, feedToken, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
				"feed.session_not_on_feed", "session is not published to this feed token", r,
			))
			return
		}
		h.logger.Error("public_feed_quote: context lookup failed",
			slog.String("session_id", sessionID.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.context_failed", "failed to validate checkout context", r,
		))
		return
	}
	pricingRules := h.channelPricingRules(ctx, checkCtx.OrgID, checkCtx.SalesChannelID)

	// ── GA lines: the SAME resolution checkout/start uses ──────────────────────
	pricedGA, priceErr := h.priceGAItems(ctx, sessionID, req.GaItems, parsedGATierIDs)
	if priceErr != "" {
		h.writePricingError(w, r, priceErr)
		return
	}
	lines := gaPricingLines(pricedGA)
	currency := cartCurrency(pricedGA, "")

	// ── Seated lines (same resolution as checkout/start step 11, minus the
	// locks): the seats' tier bindings decide the prices. ─────────────────────
	if hasSeats {
		normalizedSeats, dupKey, normErr := hcheckout.NormalizeSeatKeys(req.Seats)
		if normErr != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"reservation.duplicate_seat", "seats[] must not contain duplicate keys", r,
				map[string]any{"seat_key": dupKey},
			))
			return
		}
		if len(normalizedSeats) == 0 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"reservation.invalid_seats", "seats[] must contain at least one non-empty seat_key", r,
				map[string]any{"field": "seats"},
			))
			return
		}
		mode, merr := h.reservationQueries.GetSessionAdmissionModeByID(ctx, sessionID)
		if merr != nil {
			if errors.Is(merr, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
					"reservation.session_not_found", "session not found", r,
				))
				return
			}
			h.logger.Error("public_feed_quote: admission mode lookup failed", slog.String("error", merr.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.admission_lookup_failed", "failed to resolve session admission_mode", r,
			))
			return
		}
		if mode.AdmissionMode == hcheckout.AdmissionGeneralAdmission {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				"reservation.seats_not_supported",
				"session is general_admission — pass ga_items[] instead of seats[]",
				r,
				map[string]any{"admission_mode": mode.AdmissionMode},
			))
			return
		}
		seats, serr := h.reservationQueries.ListSessionSeatsByKeys(ctx, sessionID, normalizedSeats)
		if serr != nil {
			h.logger.Error("public_feed_quote: read seats failed", slog.String("error", serr.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.lock_seats_failed", "failed to read the requested seats", r,
			))
			return
		}
		// A seat that is gone or already taken is the same 409 checkout/start
		// answers, so the widget handles both with one branch.
		if conflicts := hcheckout.SeatConflicts(normalizedSeats, seats); len(conflicts) > 0 {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
				"reservation.seats_conflict",
				"one or more requested seats are not available",
				r,
				map[string]any{"conflicts": conflicts},
			))
			return
		}
		seatLines, seatCurrency, errCode := h.seatPricingLines(ctx, h.tierQueries, sessionID, seats)
		if errCode != "" {
			h.writePricingError(w, r, errCode)
			return
		}
		lines = append(seatLines, lines...)
		currency = cartCurrency(pricedGA, seatCurrency)
		if currency == "" {
			currency = "EUR" // defensive default, as in checkout/start: no tier bound anywhere
		}
	}

	// ── Promo: the same lookup and the same two checks as checkout/start ───────
	var discount int64
	promoApplied := false
	if strings.TrimSpace(derefOr(req.PromoCode)) != "" && h.promoAttemptsBlocked(w, r, feedToken) {
		return
	}
	promoRow, lookupErr := h.findPromoCode(ctx, checkCtx.OrgID, derefOr(req.PromoCode))
	if lookupErr != nil {
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			h.refusePromo(w, r, feedToken, "promo.not_found")
			return
		}
		h.logger.Error("public_feed_quote: promo lookup failed", slog.String("error", lookupErr.Error()))
		h.refusePromo(w, r, feedToken, "checkout.promo_lookup_failed")
		return
	}
	if promoRow != nil {
		var errCode string
		discount, _, errCode = h.applyPromoDiscount(ctx, h.promoQueries, promoRow,
			hcheckout.TierLinesForSession(lines, sessionID.String(), currency), "")
		if errCode != "" {
			h.refusePromo(w, r, feedToken, errCode)
			return
		}
		// A code that fits but takes nothing off (free tickets, an eligible
		// subtotal of zero) is not "applied": a buyer must not be told a code
		// worked when the price did not move.
		promoApplied = discount > 0
	}

	bd := hcheckout.ComputePricingLines(lines, discount, currency, pricingRules)
	httputil.WriteJSON(w, http.StatusOK, PublicFeedCheckoutQuoteResponse{
		Pricing:      bd,
		PromoApplied: promoApplied,
	})
}

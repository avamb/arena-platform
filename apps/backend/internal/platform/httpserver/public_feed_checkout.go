// public_feed_checkout.go implements the unauthenticated public checkout start
// endpoint (feature #153).
//
// This allows external consumers browsing via a feed token to initiate a
// checkout without a JWT session.  The feed token acts as the credential
// (ADR-013 federated feeds).
//
// Endpoint:
//
//	POST /v1/public/feeds/{feed_token}/checkout/start
//
// Request body:
//
//	{
//	  "tier_id":      "<uuid>",          // ticket tier to purchase
//	  "session_id":   "<uuid>",          // event session (validated against feed)
//	  "qty":          2,                 // quantity (1–50)
//	  "holder_email": "buyer@example.com",
//	  "promo_code":   "SAVE10"           // optional
//	}
//
// Response (201):
//
//	{
//	  "checkout_session": { ... },       // created checkout session (pricing_confirmed)
//	  "redirect_url":     "/checkout/<id>" // caller redirects buyer here
//	}
//
// Error codes:
//
//	400 — missing or malformed fields
//	403 — session does not belong to this feed token (ADR-013 mismatch)
//	404 — tier not found in this session
//	409 — insufficient capacity
//	429 — rate limited (shared publicFeedRL limiter)
//	503 — database not available
package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ─────────────────────────────────────────────────────────────────────────────
// Request / response types
// ─────────────────────────────────────────────────────────────────────────────

// publicFeedCheckoutStartRequest is the JSON body for
// POST /v1/public/feeds/{feed_token}/checkout/start.
type publicFeedCheckoutStartRequest struct {
	TierID      string  `json:"tier_id"`
	SessionID   string  `json:"session_id"`
	Qty         int32   `json:"qty"`
	HolderEmail string  `json:"holder_email"`
	PromoCode   *string `json:"promo_code"`
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/public/feeds/{feed_token}/checkout/start
// ─────────────────────────────────────────────────────────────────────────────

// handlePublicFeedCheckoutStart serves
// POST /v1/public/feeds/{feed_token}/checkout/start.
//
// No JWT required — the feed token in the path is the credential.
//
// Flow:
//  1. Validate rate limit (per-token + per-IP, shared with browse endpoints).
//  2. Parse + validate request body.
//  3. GetPublicCheckoutContext — validates session belongs to this feed token;
//     returns org_id + sales_channel_id.  Returns 403 on mismatch.
//  4. GetTicketTierByID — confirm tier exists in the session.
//  5. Begin transaction: ReserveCapacity + InsertReservation atomically.
//  6. InsertCheckoutSession.
//  7. Apply pricing pipeline (ComputePricing).
//  8. ConfirmCheckoutSession — transitions to pricing_confirmed.
//  9. Construct redirect URL.
//
// 10. Return 201 with checkout_session + redirect_url.
func (s *Server) handlePublicFeedCheckoutStart(w http.ResponseWriter, r *http.Request) {
	if s.publicFeedQueries == nil || s.checkoutQueries == nil || s.reservationQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	feedToken := chi.URLParam(r, "feed_token")
	clientIP := extractClientIP(r)

	// ── 1. Rate limiting (shared with browse endpoints) ──────────────────────
	if !s.publicFeedRL.checkToken(feedToken) || !s.publicFeedRL.checkIP(clientIP) {
		writeJSON(w, http.StatusTooManyRequests, errorEnvelope(
			"feed.rate_limited", "too many requests; please slow down", r,
		))
		return
	}

	// ── 2. Parse + validate body ──────────────────────────────────────────────
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"checkout.invalid_body", "cannot read request body: "+err.Error(), r,
		))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"checkout.empty_body", "request body is required", r,
		))
		return
	}

	var req publicFeedCheckoutStartRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"checkout.invalid_json", "request body is not valid JSON", r,
		))
		return
	}

	tierID, err := uuid.Parse(req.TierID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"checkout.invalid_tier_id", "tier_id must be a valid UUID", r,
			map[string]any{"field": "tier_id"},
		))
		return
	}

	sessionID, err := uuid.Parse(req.SessionID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"checkout.invalid_session_id", "session_id must be a valid UUID", r,
			map[string]any{"field": "session_id"},
		))
		return
	}

	if req.Qty <= 0 || req.Qty > 50 {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"checkout.invalid_qty", "qty must be between 1 and 50", r,
			map[string]any{"field": "qty"},
		))
		return
	}

	if req.HolderEmail == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"checkout.missing_holder_email", "holder_email is required", r,
			map[string]any{"field": "holder_email"},
		))
		return
	}

	ctx := r.Context()

	// ── 3. Validate session belongs to this feed token ────────────────────────
	// Returns org_id + sales_channel_id needed for reservation + checkout.
	// 403 when session is not published to this feed (ADR-013 mismatch).
	checkCtx, err := s.publicFeedQueries.GetPublicCheckoutContext(ctx, feedToken, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusForbidden, errorEnvelope(
				"feed.session_not_on_feed",
				"session is not published to this feed token",
				r,
			))
			return
		}
		s.logger.Error("public_feed_checkout: context lookup failed",
			slog.String("feed_token", feedToken),
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"checkout.context_failed", "failed to validate checkout context", r,
		))
		return
	}

	// ── 4. Validate tier exists in this session ───────────────────────────────
	if s.tierQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.tier_unavailable", "tier service is not available", r,
		))
		return
	}

	tier, err := s.tierQueries.GetTicketTierByID(ctx, tierID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"checkout.tier_not_found", "ticket tier not found in this session", r,
			))
			return
		}
		s.logger.Error("public_feed_checkout: tier lookup failed",
			slog.String("tier_id", tierID.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"checkout.tier_lookup_failed", "failed to retrieve ticket tier", r,
		))
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
		// Public checkout does not support PWYW — buyer must use the authenticated
		// checkout flow where chosen_price can be negotiated.
		writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope(
			"checkout.pwyw_not_supported",
			"pay-what-you-want tiers require the authenticated checkout flow",
			r,
		))
		return
	default:
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"checkout.unknown_pricing_mode", "ticket tier has an unsupported pricing mode", r,
		))
		return
	}

	// ── Optional promo code ───────────────────────────────────────────────────
	var discount int64
	var promoCodeID *uuid.UUID
	subtotalBeforeDiscount := unitPrice * int64(req.Qty)

	if req.PromoCode != nil && *req.PromoCode != "" && s.promoQueries != nil {
		promoRow, err := s.promoQueries.GetPromoCodeByCode(ctx, checkCtx.OrgID, *req.PromoCode)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope(
					"promo.not_found", "promo code not found", r,
				))
				return
			}
			s.logger.Error("public_feed_checkout: promo lookup failed",
				slog.String("promo_code", *req.PromoCode),
				slog.String("error", err.Error()),
			)
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"checkout.promo_lookup_failed", "failed to retrieve promo code", r,
			))
			return
		}
		d, errCode := validatePromoCode(promoRow, subtotalBeforeDiscount, time.Now().UTC())
		if errCode != "" {
			writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope(errCode, "promo code is not applicable", r))
			return
		}
		discount = d
		promoCodeID = &promoRow.ID
	}

	// ── Compute final pricing ─────────────────────────────────────────────────
	bd := ComputePricing(unitPrice, req.Qty, discount, tier.Currency, s.pricingRules)

	// ── 5. Atomic: reserve capacity + insert reservation ─────────────────────
	if s.inventoryQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	expiresAt := time.Now().UTC().Add(defaultReservationTTL)
	tierIDPtr := &tierID

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	invQ := s.inventoryQueries.WithTx(tx)
	resQ := s.reservationQueries.WithTx(tx)

	if _, err := invQ.ReserveCapacity(ctx, sessionID, tierIDPtr, req.Qty); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusConflict, errorEnvelope(
				"reservation.over_capacity", "insufficient capacity for this reservation", r,
			))
			return
		}
		s.logger.Error("public_feed_checkout: reserve capacity failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"reservation.capacity_failed", "failed to reserve capacity", r,
		))
		return
	}

	reservation, err := resQ.InsertReservation(
		ctx,
		checkCtx.OrgID,
		checkCtx.SalesChannelID,
		sessionID,
		tierIDPtr,
		nil, // userID — anonymous public checkout
		req.Qty,
		expiresAt,
	)
	if err != nil {
		s.logger.Error("public_feed_checkout: insert reservation failed",
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"reservation.insert_failed", "failed to create reservation", r,
		))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"reservation.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	// ── 6. Create checkout session ────────────────────────────────────────────
	csQ := s.checkoutQueries
	cs, err := csQ.InsertCheckoutSession(
		ctx,
		checkCtx.OrgID,
		checkCtx.SalesChannelID,
		reservation.ID,
		nil, // userID — anonymous
	)
	if err != nil {
		s.logger.Error("public_feed_checkout: insert checkout session failed",
			slog.String("reservation_id", reservation.ID.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"checkout.start_failed", "failed to create checkout session", r,
		))
		return
	}

	// ── 7+8. Confirm checkout with pricing snapshot ───────────────────────────
	cs, err = csQ.ConfirmCheckoutSession(
		ctx,
		cs.ID,
		bd.Subtotal,
		bd.Discount,
		bd.PlatformFee,
		bd.ProviderFee,
		bd.Tax,
		bd.Total,
		bd.Currency,
		promoCodeID,
	)
	if err != nil {
		s.logger.Error("public_feed_checkout: confirm checkout session failed",
			slog.String("checkout_session_id", cs.ID.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"checkout.confirm_failed", "failed to confirm checkout session", r,
		))
		return
	}

	// ── 9. Construct redirect URL ─────────────────────────────────────────────
	// For free tickets (total = 0) redirect straight to completion.
	// For paid tickets redirect to the payment page.
	// In production the payment provider URL (e.g. Stripe Checkout, AllPay) would
	// be constructed here from the payment intent.  This scaffold uses an internal
	// URL that the front-end widget resolves.
	redirectURL := fmt.Sprintf("/checkout/%s", cs.ID.String())
	if bd.Total == 0 {
		redirectURL = fmt.Sprintf("/checkout/%s/complete", cs.ID.String())
	}

	s.logger.Info("public_feed_checkout: session created",
		slog.String("feed_token", feedToken),
		slog.String("checkout_session_id", cs.ID.String()),
		slog.String("session_id", sessionID.String()),
		slog.String("holder_email", req.HolderEmail),
		slog.Int64("total", bd.Total),
		slog.String("currency", bd.Currency),
	)

	writeJSON(w, http.StatusCreated, map[string]any{
		"checkout_session": checkoutSessionFromRow(cs),
		"redirect_url":     redirectURL,
	})
}

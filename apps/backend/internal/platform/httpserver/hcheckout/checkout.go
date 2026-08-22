// checkout.go implements the checkout session state machine HTTP API (feature #132).
//
// A checkout session wraps a reservation, a pricing snapshot, and an optional
// payment intent into a single stateful object.
//
// State machine:
//
//	created → pricing_confirmed → completed
//	        ↘ (any non-terminal) → abandoned
//	        ↘ (any non-terminal) → expired   (TTL worker / reservation expiry)
//
// Endpoints:
//
//	POST /v1/checkout/start             — create session (checkout.start)
//	GET  /v1/checkout/{id}              — read session   (checkout.read)
//	POST /v1/checkout/{id}/confirm      — lock in pricing (checkout.confirm)
//	POST /v1/checkout/{id}/complete     — mark paid       (checkout.complete)
//	POST /v1/checkout/{id}/abandon      — abandon session (checkout.abandon)
package hcheckout

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response type
// ─────────────────────────────────────────────────────────────────────────────

// checkoutSessionResponse is the JSON representation of a checkout_session row.
type checkoutSessionResponse = CheckoutSessionResponse

// CheckoutSessionResponse is the exported form of checkoutSessionResponse.
// checkout_132_test.go (package httpserver) references the type via a type alias
// in checkout_shims.go and accesses struct fields directly.
type CheckoutSessionResponse struct {
	ID              string  `json:"id"`
	OrgID           string  `json:"org_id"`
	ChannelID       string  `json:"channel_id"`
	ReservationID   string  `json:"reservation_id"`
	UserID          *string `json:"user_id"`
	State           string  `json:"state"`
	Subtotal        *int64  `json:"subtotal"`
	Discount        *int64  `json:"discount"`
	PlatformFee     *int64  `json:"platform_fee"`
	ProviderFee     *int64  `json:"provider_fee"`
	Tax             *int64  `json:"tax"`
	Total           *int64  `json:"total"`
	Currency        *string `json:"currency"`
	PromoCodeID     *string `json:"promo_code_id"`
	PaymentIntentID *string `json:"payment_intent_id"`
	PaymentProvider *string `json:"payment_provider"`
	CompletedAt     *string `json:"completed_at"`
	AbandonedAt     *string `json:"abandoned_at"`
	ExpiredAt       *string `json:"expired_at"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

// checkoutSessionFromRow converts a CheckoutSessionRow to a checkoutSessionResponse.
func checkoutSessionFromRow(cs gen.CheckoutSessionRow) checkoutSessionResponse {
	resp := checkoutSessionResponse{
		ID:              cs.ID.String(),
		OrgID:           cs.OrgID.String(),
		ChannelID:       cs.ChannelID.String(),
		ReservationID:   cs.ReservationID.String(),
		State:           cs.State,
		Subtotal:        cs.Subtotal,
		Discount:        cs.Discount,
		PlatformFee:     cs.PlatformFee,
		ProviderFee:     cs.ProviderFee,
		Tax:             cs.Tax,
		Total:           cs.Total,
		Currency:        cs.Currency,
		PaymentIntentID: cs.PaymentIntentID,
		PaymentProvider: cs.PaymentProvider,
		CreatedAt:       cs.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:       cs.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if cs.UserID != nil {
		s := cs.UserID.String()
		resp.UserID = &s
	}
	if cs.PromoCodeID != nil {
		s := cs.PromoCodeID.String()
		resp.PromoCodeID = &s
	}
	if cs.CompletedAt != nil {
		s := cs.CompletedAt.UTC().Format(time.RFC3339)
		resp.CompletedAt = &s
	}
	if cs.AbandonedAt != nil {
		s := cs.AbandonedAt.UTC().Format(time.RFC3339)
		resp.AbandonedAt = &s
	}
	if cs.ExpiredAt != nil {
		s := cs.ExpiredAt.UTC().Format(time.RFC3339)
		resp.ExpiredAt = &s
	}
	return resp
}

// CheckoutSessionFromRow is the exported form of checkoutSessionFromRow, for use
// by the httpserver shim layer. Returns the concrete CheckoutSessionResponse type
// so that checkout_132_test.go can access struct fields directly via the type alias
// in checkout_shims.go.
func CheckoutSessionFromRow(cs gen.CheckoutSessionRow) CheckoutSessionResponse {
	return checkoutSessionFromRow(cs)
}

// validCheckoutTransitions defines the valid state transitions for the
// checkout session state machine.  Terminal states map to empty sets.
var validCheckoutTransitions = map[string]map[string]bool{
	"created":           {"pricing_confirmed": true, "abandoned": true, "expired": true},
	"pricing_confirmed": {"completed": true, "payment_started": true, "abandoned": true, "expired": true},
	"payment_started":   {"completed": true, "manual_review": true, "abandoned": true, "expired": true},
	"completed":         {},
	"abandoned":         {},
	"expired":           {},
	"manual_review":     {"completed": true, "abandoned": true},
}

// ValidCheckoutTransitions is the exported form of validCheckoutTransitions,
// for use by the httpserver shim layer (checkout_132_test.go references
// validCheckoutTransitions from package httpserver via checkout_shims.go).
var ValidCheckoutTransitions = validCheckoutTransitions

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/checkout/start
// ─────────────────────────────────────────────────────────────────────────────

// startCheckoutRequest is the request body for POST /v1/checkout/start.
type startCheckoutRequest struct {
	OrgID         string  `json:"org_id"`
	ChannelID     string  `json:"channel_id"`
	ReservationID string  `json:"reservation_id"`
	UserID        *string `json:"user_id"` // optional; nil for anonymous
}

// HandleStartCheckout serves POST /v1/checkout/start.
// Creates a new checkout session in state 'created' linked to a reservation.
// Requires JWT + "checkout.start" permission.
func (h *Handler) HandleStartCheckout(w http.ResponseWriter, r *http.Request) {
	if h.checkoutQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("checkout.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("checkout.empty_body", "request body is required", r))
		return
	}

	var req startCheckoutRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("checkout.invalid_json", "request body is not valid JSON", r))
		return
	}

	orgID, err := uuid.Parse(req.OrgID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"checkout.invalid_org_id", "org_id must be a valid UUID", r,
			map[string]any{"field": "org_id"},
		))
		return
	}
	channelID, err := uuid.Parse(req.ChannelID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"checkout.invalid_channel_id", "channel_id must be a valid UUID", r,
			map[string]any{"field": "channel_id"},
		))
		return
	}
	reservationID, err := uuid.Parse(req.ReservationID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"checkout.invalid_reservation_id", "reservation_id must be a valid UUID", r,
			map[string]any{"field": "reservation_id"},
		))
		return
	}

	var userID *uuid.UUID
	if req.UserID != nil {
		parsed, err := uuid.Parse(*req.UserID)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"checkout.invalid_user_id", "user_id must be a valid UUID when provided", r,
				map[string]any{"field": "user_id"},
			))
			return
		}
		userID = &parsed
	}

	cs, err := h.checkoutQueries.InsertCheckoutSession(ctx, orgID, channelID, reservationID, userID)
	if err != nil {
		h.logger.Error("checkout: start failed",
			slog.String("reservation_id", reservationID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.start_failed", "failed to create checkout session", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"checkout_session": checkoutSessionFromRow(cs),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/checkout/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetCheckoutSession serves GET /v1/checkout/{id}.
// Returns the current state of a checkout session.
// Requires JWT + "checkout.read" permission.
func (h *Handler) HandleGetCheckoutSession(w http.ResponseWriter, r *http.Request) {
	if h.checkoutQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("checkout.invalid_id", "checkout session id must be a valid UUID", r))
		return
	}

	cs, err := h.checkoutQueries.GetCheckoutSessionByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("checkout.not_found", "checkout session not found", r))
			return
		}
		h.logger.Error("checkout: get failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.get_failed", "failed to retrieve checkout session", r))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"checkout_session": checkoutSessionFromRow(cs),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/checkout/{id}/confirm
// ─────────────────────────────────────────────────────────────────────────────

// confirmCheckoutRequest is the request body for POST /v1/checkout/{id}/confirm.
//
// PR2-08 (feature #364) security note: all pricing is now derived from the
// server-side reservation, NOT from client-supplied tier_id / quantity.
// tier_id, session_id, quantity, and org_id are accepted as optional
// cross-validation fields only — if provided, they must match the reservation
// or the request is rejected with 422 checkout.pricing_mismatch.
//
// PromoCode and ChosenPrice remain client-supplied (promo codes are user-chosen;
// chosen_price is the PWYW user input).
type confirmCheckoutRequest struct {
	// Cross-validation only — must match reservation if provided.
	TierID    string `json:"tier_id"`
	SessionID string `json:"session_id"` // event session (not checkout session)
	Quantity  int32  `json:"quantity"`
	OrgID     string `json:"org_id"`

	PromoCode   *string `json:"promo_code"`
	ChosenPrice *int64  `json:"chosen_price"` // required for pwyw tiers
}

// HandleConfirmCheckout serves POST /v1/checkout/{id}/confirm.
//
// Loads the checkout session and its linked reservation; derives all pricing
// from the reservation (tier, quantity, seats) rather than trusting client
// input (PR2-08 / feature #364 — prevents buyers from down-pricing by
// substituting a cheaper tier or smaller quantity in the confirm request).
//
// Transitions created → pricing_confirmed.
// Returns 409 if the session is not in 'created' state.
// Requires JWT + "checkout.confirm" permission.
func (h *Handler) HandleConfirmCheckout(w http.ResponseWriter, r *http.Request) {
	if h.checkoutQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	if h.tierQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.tier_unavailable", "tier service is not available", r,
		))
		return
	}
	if h.reservationQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.reservation_unavailable", "reservation service is not available", r,
		))
		return
	}
	ctx := r.Context()

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("checkout.invalid_id", "checkout session id must be a valid UUID", r))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("checkout.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("checkout.empty_body", "request body is required", r))
		return
	}

	var req confirmCheckoutRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("checkout.invalid_json", "request body is not valid JSON", r))
		return
	}

	// ── Load checkout session → reservation (authoritative pricing source) ────
	// PR2-08: pricing MUST be derived from the server-side reservation so buyers
	// cannot down-price by supplying a different tier_id or smaller quantity.

	checkoutSession, err := h.checkoutQueries.GetCheckoutSessionByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("checkout.not_found", "checkout session not found", r))
			return
		}
		h.logger.Error("checkout: confirm — session lookup failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.get_failed", "failed to retrieve checkout session", r))
		return
	}

	reservation, err := h.reservationQueries.GetReservationByID(ctx, checkoutSession.ReservationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.logger.Error("checkout: confirm — reservation not found",
				slog.String("checkout_id", id.String()),
				slog.String("reservation_id", checkoutSession.ReservationID.String()),
			)
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
				"checkout.reservation_not_found",
				"reservation linked to this checkout session was not found",
				r,
			))
			return
		}
		h.logger.Error("checkout: confirm — reservation lookup failed",
			slog.String("reservation_id", checkoutSession.ReservationID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.reservation_lookup_failed", "failed to retrieve reservation", r))
		return
	}

	// ── Cross-validate optional client-provided fields ────────────────────────
	// Any client-supplied field that disagrees with the reservation is rejected
	// so an attacker cannot confirm a VIP reservation at a cheap-tier price.

	if req.TierID != "" {
		clientTierID, err := uuid.Parse(req.TierID)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"checkout.invalid_tier_id", "tier_id must be a valid UUID", r,
				map[string]any{"field": "tier_id"},
			))
			return
		}
		if reservation.TierID == nil || clientTierID != *reservation.TierID {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
				"checkout.pricing_mismatch",
				"tier_id does not match the reservation; pricing is derived from the reservation",
				r,
			))
			return
		}
	}

	if req.Quantity != 0 && req.Quantity != reservation.Quantity {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"checkout.pricing_mismatch",
			"quantity does not match the reservation; pricing is derived from the reservation",
			r,
			map[string]any{"field": "quantity"},
		))
		return
	}

	if req.SessionID != "" {
		clientSessionID, err := uuid.Parse(req.SessionID)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"checkout.invalid_session_id", "session_id must be a valid UUID", r,
				map[string]any{"field": "session_id"},
			))
			return
		}
		if clientSessionID != reservation.SessionID {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
				"checkout.pricing_mismatch",
				"session_id does not match the reservation",
				r,
			))
			return
		}
	}

	if req.OrgID != "" {
		clientOrgID, err := uuid.Parse(req.OrgID)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"checkout.invalid_org_id", "org_id must be a valid UUID", r,
				map[string]any{"field": "org_id"},
			))
			return
		}
		if clientOrgID != reservation.OrgID {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
				"checkout.pricing_mismatch",
				"org_id does not match the reservation",
				r,
			))
			return
		}
	}

	// ── Authoritative pricing inputs (from reservation, not request) ──────────
	eventSessionID := reservation.SessionID
	orgID := reservation.OrgID

	// ── Seated path: derive pricing from session_seats per-seat tier_id ───────
	// When the reservation has session_seat rows, each seat carries its own
	// tier_id.  buildSeatedPricingLines groups by (tier_id, unit_price) and
	// returns per-group PricingLineInput slices for ComputePricingLines.
	seats, err := h.reservationQueries.ListReservationSeats(ctx, reservation.ID)
	if err != nil {
		h.logger.Error("checkout: confirm — seat lookup failed",
			slog.String("reservation_id", reservation.ID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.confirm_failed", "failed to retrieve reservation seats", r))
		return
	}

	if len(seats) > 0 {
		// AB-48: prefer the price locked at reservation creation; only
		// legacy reservations without lines re-resolve (through the ONE
		// resolver) at confirm time.
		lockedPrices, err := LockedTierPrices(ctx, h.reservationQueries, reservation.ID)
		if err != nil {
			h.logger.Error("checkout: confirm — locked price lookup failed",
				slog.String("reservation_id", reservation.ID.String()),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.confirm_failed", "failed to retrieve locked prices", r))
			return
		}
		tierPriceMap := make(map[string]int64)
		var currency string
		for _, s := range seats {
			if s.TierID == nil {
				continue
			}
			tid := s.TierID.String()
			if _, seen := tierPriceMap[tid]; seen {
				continue
			}
			t, err := h.tierQueries.GetTicketTierByID(ctx, *s.TierID, eventSessionID)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("checkout.tier_not_found", "ticket tier not found", r))
					return
				}
				h.logger.Error("checkout: confirm — seated tier lookup failed",
					slog.String("tier_id", tid),
					slog.String("error", err.Error()),
				)
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.tier_lookup_failed", "failed to retrieve ticket tier", r))
				return
			}
			if locked, ok := lockedPrices[t.ID]; ok {
				tierPriceMap[tid] = locked
			} else {
				eff, effErr := EffectiveFixedPrice(ctx, h.tierQueries, t, time.Now().UTC())
				if effErr != nil {
					h.logger.Error("checkout: confirm — price resolution failed",
						slog.String("tier_id", tid), slog.String("error", effErr.Error()))
					httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.tier_lookup_failed", "failed to resolve ticket tier price", r))
					return
				}
				tierPriceMap[tid] = eff.Amount
			}
			if currency == "" {
				currency = t.Currency
			}
		}

		lines := buildSeatedPricingLines(seats, tierPriceMap)

		var seatedSubtotal int64
		for _, l := range lines {
			seatedSubtotal += l.UnitPrice * int64(l.Quantity)
		}

		var seatedTierIDs []uuid.UUID
		for _, s := range seats {
			if s.TierID != nil {
				seatedTierIDs = append(seatedTierIDs, *s.TierID)
			}
		}
		discount, promoCodeID, ok := h.applyPromoCode(ctx, w, r, req.PromoCode, orgID, checkoutSession.UserID, seatedSubtotal, seatedTierIDs)
		if !ok {
			return
		}

		bd := ComputePricingLines(lines, discount, currency, h.pricingRules)

		cs, err := h.checkoutQueries.ConfirmCheckoutSession(ctx, id,
			bd.Subtotal, bd.Discount, bd.PlatformFee, bd.ProviderFee, bd.Tax, bd.Total,
			bd.Currency, promoCodeID,
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
					"checkout.invalid_transition", "checkout session is not in 'created' state", r,
				))
				return
			}
			h.logger.Error("checkout: confirm failed (seated)",
				slog.String("id", id.String()),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.confirm_failed", "failed to confirm checkout session", r))
			return
		}
		h.logger.Info("checkout: pricing confirmed (seated)",
			slog.String("id", id.String()),
			slog.Int64("total", bd.Total),
			slog.String("currency", bd.Currency),
		)
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"checkout_session": checkoutSessionFromRow(cs)})
		return
	}

	// ── Multi-tier GA path: use stored reservation_ga_items prices ────────────
	// When reservation.TierID is nil the hold was placed against multiple tiers;
	// unit prices are already snapshotted in reservation_ga_items at hold time.
	if reservation.TierID == nil {
		gaItems, err := h.reservationQueries.ListReservationGAItems(ctx, reservation.ID)
		if err != nil {
			h.logger.Error("checkout: confirm — GA items lookup failed",
				slog.String("reservation_id", reservation.ID.String()),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.confirm_failed", "failed to retrieve reservation items", r))
			return
		}
		if len(gaItems) == 0 {
			h.logger.Error("checkout: confirm — no GA items and no tier_id on reservation",
				slog.String("reservation_id", reservation.ID.String()),
			)
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
				"checkout.pricing_unavailable",
				"reservation has no pricing items",
				r,
			))
			return
		}

		lines := make([]PricingLineInput, 0, len(gaItems))
		var currency string
		var gaSubtotal int64
		for _, it := range gaItems {
			lines = append(lines, PricingLineInput{
				TierID:    it.TierID.String(),
				Quantity:  it.Quantity,
				UnitPrice: it.UnitPrice,
			})
			gaSubtotal += it.UnitPrice * int64(it.Quantity)
			if currency == "" {
				currency = it.Currency
			}
		}

		var gaTierIDs []uuid.UUID
		for _, it := range gaItems {
			gaTierIDs = append(gaTierIDs, it.TierID)
		}
		discount, promoCodeID, ok := h.applyPromoCode(ctx, w, r, req.PromoCode, orgID, checkoutSession.UserID, gaSubtotal, gaTierIDs)
		if !ok {
			return
		}

		bd := ComputePricingLines(lines, discount, currency, h.pricingRules)

		cs, err := h.checkoutQueries.ConfirmCheckoutSession(ctx, id,
			bd.Subtotal, bd.Discount, bd.PlatformFee, bd.ProviderFee, bd.Tax, bd.Total,
			bd.Currency, promoCodeID,
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
					"checkout.invalid_transition", "checkout session is not in 'created' state", r,
				))
				return
			}
			h.logger.Error("checkout: confirm failed (multi-tier GA)",
				slog.String("id", id.String()),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.confirm_failed", "failed to confirm checkout session", r))
			return
		}
		h.logger.Info("checkout: pricing confirmed (multi-tier GA)",
			slog.String("id", id.String()),
			slog.Int64("total", bd.Total),
			slog.String("currency", bd.Currency),
		)
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"checkout_session": checkoutSessionFromRow(cs)})
		return
	}

	// ── Single-tier GA path ───────────────────────────────────────────────────
	// Use the reservation's tier_id and quantity — never the client-supplied ones.
	tierID := *reservation.TierID
	quantity := reservation.Quantity

	// ── Look up ticket tier ──────────────────────────────────────────────────

	tier, err := h.tierQueries.GetTicketTierByID(ctx, tierID, eventSessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("checkout.tier_not_found", "ticket tier not found", r))
			return
		}
		h.logger.Error("checkout: tier lookup failed",
			slog.String("tier_id", tierID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.tier_lookup_failed", "failed to retrieve ticket tier", r))
		return
	}

	// ── Determine unit price by pricing mode ─────────────────────────────────

	var unitPrice int64
	switch tier.PricingMode {
	case "free":
		unitPrice = 0
	case "fixed":
		// AB-48: the price locked at reservation creation wins; legacy
		// reservations without a line resolve through the ONE resolver.
		lockedPrices, lockErr := LockedTierPrices(ctx, h.reservationQueries, reservation.ID)
		if lockErr != nil {
			h.logger.Error("checkout: locked price lookup failed", slog.String("error", lockErr.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.confirm_failed", "failed to retrieve locked prices", r))
			return
		}
		if locked, ok := lockedPrices[tier.ID]; ok {
			unitPrice = locked
		} else {
			eff, effErr := EffectiveFixedPrice(ctx, h.tierQueries, tier, time.Now().UTC())
			if effErr != nil {
				h.logger.Error("checkout: price resolution failed", slog.String("error", effErr.Error()))
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.tier_lookup_failed", "failed to resolve ticket tier price", r))
				return
			}
			unitPrice = eff.Amount
		}
	case "pwyw":
		if req.ChosenPrice == nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"checkout.chosen_price_required",
				"chosen_price is required for pay-what-you-want tiers",
				r,
			))
			return
		}
		chosen := *req.ChosenPrice
		if chosen < 0 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("checkout.invalid_chosen_price", "chosen_price must be a non-negative integer", r))
			return
		}
		if tier.PwywMin != nil && chosen < *tier.PwywMin {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("checkout.chosen_price_below_min", "chosen_price is below the minimum allowed price for this tier", r))
			return
		}
		if tier.PwywMax != nil && chosen > *tier.PwywMax {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("checkout.chosen_price_above_max", "chosen_price is above the maximum allowed price for this tier", r))
			return
		}
		unitPrice = chosen
	default:
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.unknown_pricing_mode", "ticket tier has an unsupported pricing mode", r))
		return
	}

	subtotal := unitPrice * int64(quantity)

	// ── Optionally validate promo code ───────────────────────────────────────

	discount, promoCodeID, ok := h.applyPromoCode(ctx, w, r, req.PromoCode, orgID, checkoutSession.UserID, subtotal, []uuid.UUID{tierID})
	if !ok {
		return
	}

	// ── Run pricing pipeline ─────────────────────────────────────────────────

	bd := ComputePricing(unitPrice, quantity, discount, tier.Currency, h.pricingRules)

	// ── Persist pricing_confirmed transition ─────────────────────────────────

	cs, err := h.checkoutQueries.ConfirmCheckoutSession(ctx, id,
		bd.Subtotal, bd.Discount, bd.PlatformFee, bd.ProviderFee, bd.Tax, bd.Total,
		bd.Currency, promoCodeID,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"checkout.invalid_transition",
				"checkout session is not in 'created' state",
				r,
			))
			return
		}
		h.logger.Error("checkout: confirm failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.confirm_failed", "failed to confirm checkout session", r))
		return
	}

	h.logger.Info("checkout: pricing confirmed",
		slog.String("id", id.String()),
		slog.Int64("total", bd.Total),
		slog.String("currency", bd.Currency),
	)

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"checkout_session": checkoutSessionFromRow(cs),
	})
}

// applyPromoCode validates the promo code (if provided) against the given
// subtotal and returns (discount, promoCodeID, ok). When ok is false the
// handler has already written an error response and the caller must return.
//
// PR2-12 (feature #368): also enforces max_uses and max_uses_per_customer by
// querying the promo_code_redemptions table. This is a soft check (no row lock)
// that prevents the most common over-use scenario. The hard concurrency-safe
// check happens at checkout COMPLETION inside completeCheckoutWithPromoTx.
//
// userID is used for per-customer limit enforcement; pass nil for anonymous users.
func (h *Handler) applyPromoCode(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	promoCode *string,
	orgID uuid.UUID,
	userID *uuid.UUID,
	subtotal int64,
	tierIDs []uuid.UUID,
) (discount int64, promoCodeID *uuid.UUID, ok bool) {
	if promoCode == nil || *promoCode == "" || h.promoQueries == nil {
		return 0, nil, true
	}
	promoRow, err := h.promoQueries.GetPromoCodeByCode(ctx, orgID, *promoCode)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope("promo.not_found", "promo code not found", r))
			return 0, nil, false
		}
		h.logger.Error("checkout: promo lookup failed",
			slog.String("promo_code", *promoCode),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.promo_lookup_failed", "failed to retrieve promo code", r))
		return 0, nil, false
	}
	d, errCode := validatePromoCode(promoRow, subtotal, time.Now().UTC())
	if errCode != "" {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(errCode, "promo code is not applicable", r))
		return 0, nil, false
	}

	// ── AB-45: Enforce applies_to_tier_ids restriction ────────────────────────
	// When the promo code is restricted to specific tiers, at least one item in
	// the order must use one of those tiers. An empty AppliesToTierIDs means
	// "applies to all tiers" (unrestricted).
	if len(promoRow.AppliesToTierIDs) > 0 {
		allowed := make(map[string]struct{}, len(promoRow.AppliesToTierIDs))
		for _, tid := range promoRow.AppliesToTierIDs {
			allowed[tid] = struct{}{}
		}
		var tierMatch bool
		for _, tid := range tierIDs {
			if _, ok := allowed[tid.String()]; ok {
				tierMatch = true
				break
			}
		}
		if !tierMatch {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
				"promo.tier_not_applicable",
				"promo code is not applicable to the selected ticket tiers",
				r,
			))
			return 0, nil, false
		}
	}

	// ── PR2-12: Soft check — total redemption limit ───────────────────────────
	if promoRow.MaxUses != nil {
		count, countErr := h.promoQueries.CountPromoCodeRedemptions(ctx, promoRow.ID)
		if countErr != nil {
			h.logger.Error("checkout: promo redemption count failed",
				slog.String("promo_id", promoRow.ID.String()),
				slog.String("error", countErr.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("promo.count_failed", "failed to count redemptions", r))
			return 0, nil, false
		}
		if count >= *promoRow.MaxUses {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"promo.exhausted", "this promo code has reached its maximum number of uses", r,
			))
			return 0, nil, false
		}
	}

	// ── PR2-12: Soft check — per-customer limit ───────────────────────────────
	if promoRow.MaxUsesPerCustomer != nil && userID != nil {
		userCount, countErr := h.promoQueries.CountUserRedemptions(ctx, promoRow.ID, *userID)
		if countErr != nil {
			h.logger.Error("checkout: promo user redemption count failed",
				slog.String("promo_id", promoRow.ID.String()),
				slog.String("error", countErr.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("promo.count_failed", "failed to count user redemptions", r))
			return 0, nil, false
		}
		if userCount >= *promoRow.MaxUsesPerCustomer {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"promo.per_customer_limit", "you have already used this promo code the maximum number of times", r,
			))
			return 0, nil, false
		}
	}

	pid := promoRow.ID
	return d, &pid, true
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/checkout/{id}/complete
// ─────────────────────────────────────────────────────────────────────────────

// completeCheckoutRequest is the request body for POST /v1/checkout/{id}/complete.
type completeCheckoutRequest struct {
	PaymentIntentID string `json:"payment_intent_id"`
	PaymentProvider string `json:"payment_provider"`
}

// HandleCompleteCheckout serves POST /v1/checkout/{id}/complete.
//
// For paid checkouts (total > 0): body must include payment_intent_id and
// payment_provider.  Transitions pricing_confirmed → completed.
//
// For free checkouts (total = 0, i.e. free tier or 100 %-off promo): body
// may be empty or omit payment fields.  The session is completed immediately
// without a payment provider call and an audit entry is emitted.
//
// Returns 409 if the session is not in 'pricing_confirmed' state or (for
// free path) if the session's total is not zero.
// Requires JWT + "checkout.complete" permission.
func (h *Handler) HandleCompleteCheckout(w http.ResponseWriter, r *http.Request) {
	if h.checkoutQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("checkout.invalid_id", "checkout session id must be a valid UUID", r))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("checkout.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}

	var req completeCheckoutRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("checkout.invalid_json", "request body is not valid JSON", r))
			return
		}
	}

	if req.PaymentIntentID == "" && req.PaymentProvider != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"checkout.missing_payment_intent", "payment_intent_id is required when payment_provider is supplied", r,
			map[string]any{"field": "payment_intent_id"},
		))
		return
	}

	// ── Free checkout branch (total = 0) ─────────────────────────────────────
	// When no payment_intent_id is supplied, attempt the free-checkout
	// completion path.  The DB query only succeeds if the session's total = 0.
	if req.PaymentIntentID == "" {
		// ── PR2-12 (feature #368): Pre-load session for promo redemption ──────
		// Fetch the session INSIDE the branch (after all validations) so the pre-load
		// only runs when we're actually about to complete. On any error we fall back
		// gracefully: promoCodeID stays nil and the completion uses the plain path.
		var (
			promoCodeIDForComplete   *uuid.UUID
			userIDForComplete        *uuid.UUID
			reservationIDForComplete uuid.UUID
			discountForComplete      int64
			subtotalForComplete      int64
		)
		if preCS, preErr := h.checkoutQueries.GetCheckoutSessionByID(ctx, id); preErr == nil {
			promoCodeIDForComplete = preCS.PromoCodeID
			userIDForComplete = preCS.UserID
			reservationIDForComplete = preCS.ReservationID
			if preCS.Discount != nil {
				discountForComplete = *preCS.Discount
			}
			if preCS.Subtotal != nil {
				subtotalForComplete = *preCS.Subtotal
			}
		}

		// PR2-12: complete + promo redemption insert in one transaction when a
		// promo code was applied (completeCheckoutWithPromoTx falls back to the
		// plain CompleteFreeCheckoutSession when promoCodeIDForComplete is nil).
		cs, err := h.completeCheckoutWithPromoTx(
			ctx, w, r,
			promoCodeIDForComplete, userIDForComplete, reservationIDForComplete,
			discountForComplete, subtotalForComplete,
			func(txQ *gen.Queries) (gen.CheckoutSessionRow, error) {
				return txQ.CompleteFreeCheckoutSession(ctx, id)
			},
		)
		if err != nil {
			if errors.Is(err, errPromoExhausted) || errors.Is(err, errPromoPerCustomerLimit) {
				return // 409 response already written by completeCheckoutWithPromoTx
			}
			if errors.Is(err, pgx.ErrNoRows) {
				// Session not found, not pricing_confirmed, or total != 0.
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
					"checkout.payment_required",
					"this checkout session requires payment (total > 0); provide payment_intent_id",
					r,
				))
				return
			}
			h.logger.Error("checkout: free complete failed",
				slog.String("id", id.String()),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.complete_failed", "failed to complete checkout session", r))
			return
		}

		h.logger.Info("checkout: free issuance completed",
			slog.String("id", id.String()),
			slog.String("reservation_id", cs.ReservationID.String()),
			slog.String("org_id", cs.OrgID.String()),
		)

		// PR2-27 (feature #383): conversion is now performed atomically inside
		// completeCheckoutWithPromoTx — no separate convertReservationTx call needed.

		// Issue tickets for the free checkout (idempotent).
		if h.ticketQueries != nil && h.reservationQueries != nil && h.issueTickets != nil {
			tickets, ticketErr := h.issueTickets(ctx, cs)
			if ticketErr != nil {
				// Non-fatal: checkout is complete; tickets can be re-issued on retry.
				h.logger.Error("checkout: ticket issuance failed after free checkout",
					slog.String("checkout_session_id", id.String()),
					slog.String("error", ticketErr.Error()),
				)
			} else {
				h.logger.Info("checkout: free tickets issued",
					slog.String("checkout_session_id", id.String()),
					slog.Int("count", len(tickets)),
				)
				// Delivery jobs are enqueued inside IssueTicketsForCheckout (feature #367).
				// A separate enqueueDelivery call here was removed to prevent double-delivery.
			}
		}

		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"checkout_session": checkoutSessionFromRow(cs),
		})
		return
	}

	// ── Paid checkout branch ──────────────────────────────────────────────────
	if req.PaymentProvider == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"checkout.missing_payment_provider", "payment_provider is required", r,
			map[string]any{"field": "payment_provider"},
		))
		return
	}

	// ── PR2-12 (feature #368): Pre-load session for paid promo redemption ────
	// Fetch the session INSIDE the branch (after all validations). On any
	// error we fall back gracefully: promoCodeID stays nil and the completion
	// uses the plain CompleteCheckoutSession (non-transactional) path.
	var (
		promoCodeIDForComplete   *uuid.UUID
		userIDForComplete        *uuid.UUID
		reservationIDForComplete uuid.UUID
		discountForComplete      int64
		subtotalForComplete      int64
	)
	if preCS, preErr := h.checkoutQueries.GetCheckoutSessionByID(ctx, id); preErr == nil {
		promoCodeIDForComplete = preCS.PromoCodeID
		userIDForComplete = preCS.UserID
		reservationIDForComplete = preCS.ReservationID
		if preCS.Discount != nil {
			discountForComplete = *preCS.Discount
		}
		if preCS.Subtotal != nil {
			subtotalForComplete = *preCS.Subtotal
		}
	}

	// PR2-12: complete + promo redemption insert in one transaction when a
	// promo code was applied (completeCheckoutWithPromoTx falls back to the
	// plain CompleteCheckoutSession when promoCodeIDForComplete is nil).
	// AB-41: the channel decides the provider; the org must hold a usable
	// config for it. A client-supplied provider that contradicts the
	// channel is rejected rather than recorded.
	if h.channelQueries != nil && h.orgQueries != nil {
		if preCS, preErr := h.checkoutQueries.GetCheckoutSessionByID(ctx, id); preErr == nil {
			channelProvider, chErr := h.channelProviderForCheckout(ctx, preCS)
			if chErr != nil {
				h.logger.Error("checkout: complete — channel lookup failed", slog.String("error", chErr.Error()))
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
					"checkout.complete_failed", "failed to resolve the sales channel provider", r,
				))
				return
			}
			if !strings.EqualFold(req.PaymentProvider, channelProvider) {
				httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
					ErrCodeProviderMismatch,
					"payment_provider does not match the checkout session's sales channel", r,
					map[string]any{"requested": req.PaymentProvider, "channel_provider": channelProvider},
				))
				return
			}
			if _, cfgErr := ResolveProviderConfig(ctx, h.orgQueries, preCS.OrgID, channelProvider); cfgErr != nil {
				httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
					cfgErr.Code, cfgErr.Message, r, cfgErr.Details,
				))
				return
			}
			req.PaymentProvider = strings.ToLower(channelProvider)
		}
	}

	piID := req.PaymentIntentID
	piProvider := req.PaymentProvider
	cs, err := h.completeCheckoutWithPromoTx(
		ctx, w, r,
		promoCodeIDForComplete, userIDForComplete, reservationIDForComplete,
		discountForComplete, subtotalForComplete,
		func(txQ *gen.Queries) (gen.CheckoutSessionRow, error) {
			return txQ.CompleteCheckoutSession(ctx, id, piID, piProvider)
		},
	)
	if err != nil {
		if errors.Is(err, errPromoExhausted) || errors.Is(err, errPromoPerCustomerLimit) {
			return // 409 response already written by completeCheckoutWithPromoTx
		}
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"checkout.invalid_transition",
				"checkout session is not in 'pricing_confirmed' state",
				r,
			))
			return
		}
		h.logger.Error("checkout: complete failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.complete_failed", "failed to complete checkout session", r))
		return
	}

	h.logger.Info("checkout: completed",
		slog.String("id", id.String()),
		slog.String("payment_provider", req.PaymentProvider),
	)

	// PR2-27 (feature #383): conversion is now performed atomically inside
	// completeCheckoutWithPromoTx — no separate convertReservationTx call needed.

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"checkout_session": checkoutSessionFromRow(cs),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/checkout/{id}/abandon
// ─────────────────────────────────────────────────────────────────────────────

// HandleAbandonCheckout serves POST /v1/checkout/{id}/abandon.
// Transitions any non-terminal state → abandoned.
// Returns 409 when the session is already terminal.
// Requires JWT + "checkout.abandon" permission.
func (h *Handler) HandleAbandonCheckout(w http.ResponseWriter, r *http.Request) {
	if h.checkoutQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("checkout.invalid_id", "checkout session id must be a valid UUID", r))
		return
	}

	cs, err := h.checkoutQueries.AbandonCheckoutSession(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"checkout.already_terminal",
				"checkout session is already in a terminal state",
				r,
			))
			return
		}
		h.logger.Error("checkout: abandon failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.abandon_failed", "failed to abandon checkout session", r))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"checkout_session": checkoutSessionFromRow(cs),
	})
}

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
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
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
// The handler re-quotes the price using the current pricing pipeline and stores
// the snapshot.
type confirmCheckoutRequest struct {
	TierID      string  `json:"tier_id"`
	SessionID   string  `json:"session_id"` // event session (not checkout session)
	Quantity    int32   `json:"quantity"`
	OrgID       string  `json:"org_id"`
	PromoCode   *string `json:"promo_code"`
	ChosenPrice *int64  `json:"chosen_price"` // required for pwyw tiers
}

// HandleConfirmCheckout serves POST /v1/checkout/{id}/confirm.
// Re-quotes the pricing, stores the snapshot, and transitions created →
// pricing_confirmed.  Returns 409 if the session is not in 'created' state.
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

	tierID, err := uuid.Parse(req.TierID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"checkout.invalid_tier_id", "tier_id must be a valid UUID", r,
			map[string]any{"field": "tier_id"},
		))
		return
	}
	eventSessionID, err := uuid.Parse(req.SessionID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"checkout.invalid_session_id", "session_id must be a valid UUID", r,
			map[string]any{"field": "session_id"},
		))
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

	if req.Quantity <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"checkout.invalid_quantity", "quantity must be greater than 0", r,
			map[string]any{"field": "quantity"},
		))
		return
	}

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
		unitPrice = tier.PriceAmount
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

	subtotal := unitPrice * int64(req.Quantity)

	// ── Optionally validate promo code ───────────────────────────────────────

	var discount int64
	var promoCodeID *uuid.UUID

	if req.PromoCode != nil && *req.PromoCode != "" && h.promoQueries != nil {
		promoRow, err := h.promoQueries.GetPromoCodeByCode(ctx, orgID, *req.PromoCode)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope("promo.not_found", "promo code not found", r))
				return
			}
			h.logger.Error("checkout: promo lookup failed",
				slog.String("promo_code", *req.PromoCode),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("checkout.promo_lookup_failed", "failed to retrieve promo code", r))
			return
		}

		d, errCode := validatePromoCode(promoRow, subtotal, time.Now().UTC())
		if errCode != "" {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(errCode, "promo code is not applicable", r))
			return
		}
		discount = d
		promoCodeID = &promoRow.ID
	}

	// ── Run pricing pipeline ─────────────────────────────────────────────────

	bd := ComputePricing(unitPrice, req.Quantity, discount, tier.Currency, h.pricingRules)

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
		cs, err := h.checkoutQueries.CompleteFreeCheckoutSession(ctx, id)
		if err != nil {
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
				// Enqueue email delivery jobs (feature #141). Best-effort.
				if h.enqueueDelivery != nil {
					h.enqueueDelivery(ctx, tickets)
				}
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

	cs, err := h.checkoutQueries.CompleteCheckoutSession(ctx, id, req.PaymentIntentID, req.PaymentProvider)
	if err != nil {
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

// promo_codes.go implements the promo code CRUD API and checkout validation
// endpoints (feature #128).
//
// A promo code is a discount voucher scoped to one organization. It can apply
// a fixed amount or percentage discount to an order, with optional restrictions
// on which ticket tiers it applies to, total usage limits, per-customer limits,
// and a validity date window.
//
// Endpoints:
//
//	POST   /v1/organizations/{org_id}/promo-codes        — create (promo.create)
//	GET    /v1/organizations/{org_id}/promo-codes        — list   (promo.read)
//	GET    /v1/organizations/{org_id}/promo-codes/{id}   — get    (promo.read)
//	PATCH  /v1/organizations/{org_id}/promo-codes/{id}   — update (promo.update)
//	DELETE /v1/organizations/{org_id}/promo-codes/{id}   — delete (promo.delete)
//	POST   /v1/checkout/promo-validate                   — validate (promo.validate)
//
// All endpoints are gated by JWT auth + a named permission.
package hcheckout

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	ticketsdomain "github.com/abhteam/arena_new/apps/backend/internal/domain/tickets"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response type
// ─────────────────────────────────────────────────────────────────────────────

// promoCodeResponse is the JSON representation of a single promo code.
type promoCodeResponse struct {
	ID                 string   `json:"id"`
	OrgID              string   `json:"org_id"`
	Code               string   `json:"code"`
	DiscountType       string   `json:"discount_type"`
	DiscountValue      int64    `json:"discount_value"`
	AppliesToTierIDs   []string `json:"applies_to_tier_ids"`
	MaxUses            *int32   `json:"max_uses"`
	MaxUsesPerCustomer *int32   `json:"max_uses_per_customer"`
	ValidFrom          *string  `json:"valid_from"`
	ValidUntil         *string  `json:"valid_until"`
	MinOrderAmount     int64    `json:"min_order_amount"`
	Status             string   `json:"status"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
}

// promoCodeFromRow converts a PromoCodeRow to a promoCodeResponse.
// Ensures AppliesToTierIDs is never nil in JSON output.
func promoCodeFromRow(pc gen.PromoCodeRow) promoCodeResponse {
	tierIDs := pc.AppliesToTierIDs
	if tierIDs == nil {
		tierIDs = []string{}
	}
	resp := promoCodeResponse{
		ID:                 pc.ID.String(),
		OrgID:              pc.OrgID.String(),
		Code:               pc.Code,
		DiscountType:       pc.DiscountType,
		DiscountValue:      pc.DiscountValue,
		AppliesToTierIDs:   tierIDs,
		MaxUses:            pc.MaxUses,
		MaxUsesPerCustomer: pc.MaxUsesPerCustomer,
		MinOrderAmount:     pc.MinOrderAmount,
		Status:             pc.Status,
		CreatedAt:          pc.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:          pc.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if pc.ValidFrom != nil {
		s := pc.ValidFrom.UTC().Format(time.RFC3339)
		resp.ValidFrom = &s
	}
	if pc.ValidUntil != nil {
		s := pc.ValidUntil.UTC().Format(time.RFC3339)
		resp.ValidUntil = &s
	}
	return resp
}

// ─────────────────────────────────────────────────────────────────────────────
// Discount math (pure functions — directly testable from package tests)
// ─────────────────────────────────────────────────────────────────────────────

// computeDiscount is a thin forwarder to the pure-domain
// ticketsdomain.ComputeDiscount, kept here so existing in-package call sites
// and tests (promo_128_test.go, pricing_129_test.go, checkout_133_test.go)
// continue to compile unchanged after the feature #186 DDD split. New code
// should call ticketsdomain.ComputeDiscount directly.
func computeDiscount(discountType string, discountValue, orderAmount int64) int64 {
	return ticketsdomain.ComputeDiscount(discountType, discountValue, orderAmount)
}

// validatePromoCode checks whether a promo code is applicable for a given order.
// Returns (discountAmount, errorCode) where errorCode is empty when the code is valid.
// The returned errorCode is suitable for use as an API error code (e.g. "promo.expired").
func validatePromoCode(pc gen.PromoCodeRow, orderAmount int64, now time.Time) (int64, string) {
	if pc.Status != "active" {
		return 0, "promo.not_active"
	}
	if pc.ValidFrom != nil && now.Before(*pc.ValidFrom) {
		return 0, "promo.not_yet_valid"
	}
	if pc.ValidUntil != nil && now.After(*pc.ValidUntil) {
		return 0, "promo.expired"
	}
	if orderAmount < pc.MinOrderAmount {
		return 0, "promo.invalid_order_amount"
	}
	return computeDiscount(pc.DiscountType, pc.DiscountValue, orderAmount), ""
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/promo-codes
// ─────────────────────────────────────────────────────────────────────────────

// createPromoCodeRequest is the request body for POST /v1/organizations/{org_id}/promo-codes.
type createPromoCodeRequest struct {
	Code               string   `json:"code"`
	DiscountType       string   `json:"discount_type"`
	DiscountValue      int64    `json:"discount_value"`
	AppliesToTierIDs   []string `json:"applies_to_tier_ids"`
	MaxUses            *int32   `json:"max_uses"`
	MaxUsesPerCustomer *int32   `json:"max_uses_per_customer"`
	ValidFrom          *string  `json:"valid_from"`
	ValidUntil         *string  `json:"valid_until"`
	MinOrderAmount     int64    `json:"min_order_amount"`
	Status             string   `json:"status"`
}

// HandleCreatePromoCode serves POST /v1/organizations/{org_id}/promo-codes.
// Requires JWT + "promo.create" permission.
func (h *Handler) HandleCreatePromoCode(w http.ResponseWriter, r *http.Request) {
	if h.promoQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("promo.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("promo.empty_body", "request body is required", r))
		return
	}

	var req createPromoCodeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("promo.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Code = strings.TrimSpace(req.Code)
	if req.Code == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"promo.invalid_code", "code is required", r,
			map[string]any{"field": "code"},
		))
		return
	}

	if req.DiscountType != "percent" && req.DiscountType != "fixed_amount" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"promo.invalid_discount_type", "discount_type must be 'percent' or 'fixed_amount'", r,
			map[string]any{"field": "discount_type"},
		))
		return
	}
	if req.DiscountValue <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"promo.invalid_discount_value", "discount_value must be greater than 0", r,
			map[string]any{"field": "discount_value"},
		))
		return
	}
	if req.DiscountType == "percent" && (req.DiscountValue < 1 || req.DiscountValue > 100) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"promo.invalid_discount_value", "discount_value for percent type must be between 1 and 100", r,
			map[string]any{"field": "discount_value"},
		))
		return
	}

	if req.AppliesToTierIDs == nil {
		req.AppliesToTierIDs = []string{}
	}
	if req.Status == "" {
		req.Status = "active"
	}

	var validFrom, validUntil *time.Time
	if req.ValidFrom != nil {
		t, err := time.Parse(time.RFC3339, *req.ValidFrom)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"promo.invalid_valid_from", "valid_from must be RFC3339 format", r,
				map[string]any{"field": "valid_from"},
			))
			return
		}
		validFrom = &t
	}
	if req.ValidUntil != nil {
		t, err := time.Parse(time.RFC3339, *req.ValidUntil)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"promo.invalid_valid_until", "valid_until must be RFC3339 format", r,
				map[string]any{"field": "valid_until"},
			))
			return
		}
		validUntil = &t
	}

	pc, err := h.promoQueries.InsertPromoCode(ctx,
		orgID, req.Code, req.DiscountType, req.DiscountValue,
		req.AppliesToTierIDs, req.MaxUses, req.MaxUsesPerCustomer,
		validFrom, validUntil, req.MinOrderAmount, req.Status,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"promo.duplicate",
				"a promo code with that code already exists in this organization",
				r,
			))
			return
		}
		h.logger.Error("promo: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"promo.insert_failed", "failed to create promo code", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"promo_code": promoCodeFromRow(pc),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/promo-codes
// ─────────────────────────────────────────────────────────────────────────────

// HandleListPromoCodes serves GET /v1/organizations/{org_id}/promo-codes.
// Requires JWT + "promo.read" permission.
func (h *Handler) HandleListPromoCodes(w http.ResponseWriter, r *http.Request) {
	if h.promoQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}

	rows, err := h.promoQueries.ListPromoCodesByOrg(ctx, orgID)
	if err != nil {
		h.logger.Error("promo: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"promo.list_failed", "failed to list promo codes", r,
		))
		return
	}

	result := make([]promoCodeResponse, 0, len(rows))
	for _, pc := range rows {
		result = append(result, promoCodeFromRow(pc))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"promo_codes": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/promo-codes/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetPromoCode serves GET /v1/organizations/{org_id}/promo-codes/{id}.
// Requires JWT + "promo.read" permission.
func (h *Handler) HandleGetPromoCode(w http.ResponseWriter, r *http.Request) {
	if h.promoQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	pcID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	pc, err := h.promoQueries.GetPromoCodeByID(ctx, pcID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("promo.not_found", "promo code not found", r))
			return
		}
		h.logger.Error("promo: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"promo.get_failed", "failed to get promo code", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"promo_code": promoCodeFromRow(pc),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/organizations/{org_id}/promo-codes/{id}
// ─────────────────────────────────────────────────────────────────────────────

// updatePromoCodeRequest is the request body for PATCH .../promo-codes/{id}.
// All fields are optional; empty/nil values leave the existing value unchanged.
type updatePromoCodeRequest struct {
	DiscountType       string   `json:"discount_type"`
	DiscountValue      *int64   `json:"discount_value"`
	AppliesToTierIDs   []string `json:"applies_to_tier_ids"`
	MaxUses            *int32   `json:"max_uses"`
	MaxUsesPerCustomer *int32   `json:"max_uses_per_customer"`
	ValidFrom          *string  `json:"valid_from"`
	ValidUntil         *string  `json:"valid_until"`
	MinOrderAmount     *int64   `json:"min_order_amount"`
	Status             string   `json:"status"`
}

// HandleUpdatePromoCode serves PATCH /v1/organizations/{org_id}/promo-codes/{id}.
// Requires JWT + "promo.update" permission.
func (h *Handler) HandleUpdatePromoCode(w http.ResponseWriter, r *http.Request) {
	if h.promoQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	pcID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("promo.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("promo.empty_body", "request body is required", r))
		return
	}

	var req updatePromoCodeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("promo.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.DiscountType = strings.TrimSpace(req.DiscountType)
	if req.DiscountType != "" && req.DiscountType != "percent" && req.DiscountType != "fixed_amount" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"promo.invalid_discount_type", "discount_type must be 'percent' or 'fixed_amount'", r,
			map[string]any{"field": "discount_type"},
		))
		return
	}

	var validFrom, validUntil *time.Time
	if req.ValidFrom != nil {
		t, err := time.Parse(time.RFC3339, *req.ValidFrom)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"promo.invalid_valid_from", "valid_from must be RFC3339 format", r,
				map[string]any{"field": "valid_from"},
			))
			return
		}
		validFrom = &t
	}
	if req.ValidUntil != nil {
		t, err := time.Parse(time.RFC3339, *req.ValidUntil)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"promo.invalid_valid_until", "valid_until must be RFC3339 format", r,
				map[string]any{"field": "valid_until"},
			))
			return
		}
		validUntil = &t
	}

	updated, err := h.promoQueries.UpdatePromoCode(ctx,
		pcID, orgID,
		req.DiscountType, req.DiscountValue, req.AppliesToTierIDs,
		req.MaxUses, req.MaxUsesPerCustomer,
		validFrom, validUntil,
		req.MinOrderAmount, req.Status,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("promo.not_found", "promo code not found", r))
			return
		}
		h.logger.Error("promo: update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"promo.update_failed", "failed to update promo code", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"promo_code": promoCodeFromRow(updated),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/promo-codes/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleDeletePromoCode serves DELETE /v1/organizations/{org_id}/promo-codes/{id}.
// Performs a soft-delete (sets deleted_at = now()).
// Requires JWT + "promo.delete" permission.
func (h *Handler) HandleDeletePromoCode(w http.ResponseWriter, r *http.Request) {
	if h.promoQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	pcID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	deleted, err := h.promoQueries.SoftDeletePromoCode(ctx, pcID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("promo.not_found", "promo code not found", r))
			return
		}
		h.logger.Error("promo: soft-delete failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"promo.delete_failed", "failed to delete promo code", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"promo_code": promoCodeFromRow(deleted),
		"deleted":    true,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/checkout/promo-validate
// ─────────────────────────────────────────────────────────────────────────────

// validatePromoCodeRequest is the request body for POST /v1/checkout/promo-validate.
type validatePromoCodeRequest struct {
	OrgID       string `json:"org_id"`
	Code        string `json:"code"`
	OrderAmount int64  `json:"order_amount"`
	UserID      string `json:"user_id"`
}

// HandleValidatePromoCode serves POST /v1/checkout/promo-validate.
// Validates a promo code against the given order and computes the discount.
// Requires JWT + "promo.validate" permission.
//
// Validation steps:
//  1. Look up the code via GetPromoCodeByCode (org_id + code)
//  2. Check status, validity window, and min order amount
//  3. Count total redemptions — if max_uses set and exhausted → 409
//  4. Count user redemptions if user_id provided — if per-customer limit exceeded → 409
//  5. Return discount computation result
func (h *Handler) HandleValidatePromoCode(w http.ResponseWriter, r *http.Request) {
	if h.promoQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("promo.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("promo.empty_body", "request body is required", r))
		return
	}

	var req validatePromoCodeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("promo.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Code = strings.TrimSpace(req.Code)
	if req.Code == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"promo.invalid_code", "code is required", r,
			map[string]any{"field": "code"},
		))
		return
	}

	orgID, err := uuid.Parse(req.OrgID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"promo.invalid_org_id", "org_id must be a valid UUID", r,
			map[string]any{"field": "org_id"},
		))
		return
	}

	// Step 1: look up the promo code.
	pc, err := h.promoQueries.GetPromoCodeByCode(ctx, orgID, req.Code)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("promo.not_found", "promo code not found", r))
			return
		}
		h.logger.Error("promo: validate lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"promo.lookup_failed", "failed to look up promo code", r,
		))
		return
	}

	// Step 2: validate status, dates, and minimum order amount.
	discountAmount, errCode := validatePromoCode(pc, req.OrderAmount, time.Now().UTC())
	if errCode != "" {
		var msg string
		switch errCode {
		case "promo.not_active":
			msg = "promo code is not active"
		case "promo.not_yet_valid":
			msg = "promo code is not yet valid"
		case "promo.expired":
			msg = "promo code has expired"
		case "promo.invalid_order_amount":
			msg = "order amount does not meet the minimum required for this promo code"
		default:
			msg = "promo code cannot be applied to this order"
		}
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(errCode, msg, r))
		return
	}

	// Step 3: check total redemption limit.
	if pc.MaxUses != nil {
		count, err := h.promoQueries.CountPromoCodeRedemptions(ctx, pc.ID)
		if err != nil {
			h.logger.Error("promo: count redemptions failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"promo.count_failed", "failed to count redemptions", r,
			))
			return
		}
		if count >= *pc.MaxUses {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"promo.exhausted", "this promo code has reached its maximum number of uses", r,
			))
			return
		}
	}

	// Step 4: check per-customer redemption limit.
	if pc.MaxUsesPerCustomer != nil && req.UserID != "" {
		userID, err := uuid.Parse(req.UserID)
		if err == nil {
			// Only enforce if the user_id parses — anonymous users get a pass.
			userCount, err := h.promoQueries.CountUserRedemptions(ctx, pc.ID, userID)
			if err != nil {
				h.logger.Error("promo: count user redemptions failed", slog.String("error", err.Error()))
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
					"promo.count_failed", "failed to count user redemptions", r,
				))
				return
			}
			if userCount >= *pc.MaxUsesPerCustomer {
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
					"promo.per_customer_limit",
					"you have already used this promo code the maximum number of times",
					r,
				))
				return
			}
		}
	}

	// Step 5: return the validated discount.
	finalAmount := req.OrderAmount - discountAmount

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"valid":           true,
		"discount_amount": discountAmount,
		"final_amount":    finalAmount,
		"promo_code":      promoCodeFromRow(pc),
	})
}

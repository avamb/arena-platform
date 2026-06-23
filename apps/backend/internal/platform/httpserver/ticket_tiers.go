// ticket_tiers.go implements the ticket tier CRUD API endpoints (feature #127).
//
// A Ticket Tier defines a pricing option within a Session.  Three pricing
// modes are supported:
//
//	free  — no charge; price_amount is forced to 0.
//	fixed — a set price; price_amount must be > 0 (cents).
//	pwyw  — pay-what-you-want; optional pwyw_min and pwyw_max bounds (cents).
//	        When both are present, pwyw_min <= pwyw_max is enforced.
//
// price_amount and the pwyw bounds are stored in the smallest currency unit
// (integer cents).  Clients send and receive these as JSON integers.
//
// Endpoints:
//
//	POST   /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers        — create (tier.create)
//	GET    /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers        — list   (tier.read)
//	GET    /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}   — get    (tier.read)
//	PATCH  /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}   — update (tier.update)
//	DELETE /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}   — delete (tier.delete)
package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/jackc/pgx/v5"
)

// ─────────────────────────────────────────────────────────────────────────────
// Valid pricing modes
// ─────────────────────────────────────────────────────────────────────────────

// validPricingModes lists the allowed pricing_mode values.
var validPricingModes = map[string]bool{
	"fixed": true,
	"free":  true,
	"pwyw":  true,
}

// ─────────────────────────────────────────────────────────────────────────────
// Response types
// ─────────────────────────────────────────────────────────────────────────────

// tierResponse is the JSON representation of a single ticket tier.
// PriceAmount, PwywMin, and PwywMax are in the smallest currency unit (cents).
type tierResponse struct {
	ID              string  `json:"id"`
	SessionID       string  `json:"session_id"`
	Name            string  `json:"name"`
	PricingMode     string  `json:"pricing_mode"`
	PriceAmount     int64   `json:"price_amount"` // cents
	Currency        string  `json:"currency"`
	PwywMin         *int64  `json:"pwyw_min"`
	PwywMax         *int64  `json:"pwyw_max"`
	Capacity        *int32  `json:"capacity"`
	SaleWindowStart *string `json:"sale_window_start"`
	SaleWindowEnd   *string `json:"sale_window_end"`
	SortOrder       int32   `json:"sort_order"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

// tierFromRow converts a TicketTierRow to a tierResponse.
func tierFromRow(t gen.TicketTierRow) tierResponse {
	resp := tierResponse{
		ID:          t.ID.String(),
		SessionID:   t.SessionID.String(),
		Name:        t.Name,
		PricingMode: t.PricingMode,
		PriceAmount: t.PriceAmount,
		Currency:    t.Currency,
		PwywMin:     t.PwywMin,
		PwywMax:     t.PwywMax,
		Capacity:    t.Capacity,
		SortOrder:   t.SortOrder,
		CreatedAt:   t.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   t.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if t.SaleWindowStart != nil {
		s := t.SaleWindowStart.UTC().Format(time.RFC3339)
		resp.SaleWindowStart = &s
	}
	if t.SaleWindowEnd != nil {
		s := t.SaleWindowEnd.UTC().Format(time.RFC3339)
		resp.SaleWindowEnd = &s
	}
	return resp
}

// ─────────────────────────────────────────────────────────────────────────────
// Pricing mode validators
// ─────────────────────────────────────────────────────────────────────────────

// validatePricingMode enforces the invariants for each pricing mode.
// Returns (errorCode, errorMessage) when validation fails; returns ("", "") on success.
func validatePricingMode(mode string, priceAmount int64, pwywMin, pwywMax *int64) (string, string) {
	switch mode {
	case "free":
		// free: price_amount must be 0.
		if priceAmount != 0 {
			return "tier.invalid_free_price", "price_amount must be 0 for free tiers"
		}
	case "fixed":
		// fixed: price_amount must be positive.
		if priceAmount <= 0 {
			return "tier.invalid_fixed_price", "price_amount must be greater than 0 for fixed tiers"
		}
	case "pwyw":
		// pwyw: price_amount >= 0 (suggested amount, may be 0).
		if priceAmount < 0 {
			return "tier.invalid_pwyw_price", "price_amount must be >= 0 for pwyw tiers"
		}
		// pwyw_min and pwyw_max are both optional; when both are provided, min <= max.
		if pwywMin != nil && pwywMax != nil && *pwywMin > *pwywMax {
			return "tier.invalid_pwyw_range", "pwyw_min must be less than or equal to pwyw_max"
		}
		// Individual bound sanity checks.
		if pwywMin != nil && *pwywMin < 0 {
			return "tier.invalid_pwyw_min", "pwyw_min must be >= 0"
		}
		if pwywMax != nil && *pwywMax < 0 {
			return "tier.invalid_pwyw_max", "pwyw_max must be >= 0"
		}
	}
	return "", ""
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers
// ─────────────────────────────────────────────────────────────────────────────

// createTierRequest is the request body for POST .../tiers.
type createTierRequest struct {
	Name            string  `json:"name"`
	PricingMode     string  `json:"pricing_mode"`
	PriceAmount     int64   `json:"price_amount"` // cents; omit or 0 for free tiers
	Currency        string  `json:"currency"`     // ISO 4217; defaults to "USD"
	PwywMin         *int64  `json:"pwyw_min"`
	PwywMax         *int64  `json:"pwyw_max"`
	Capacity        *int32  `json:"capacity"`
	SaleWindowStart *string `json:"sale_window_start"` // RFC3339 or omit
	SaleWindowEnd   *string `json:"sale_window_end"`   // RFC3339 or omit
	SortOrder       int32   `json:"sort_order"`
}

// handleCreateTier serves POST .../sessions/{session_id}/tiers.
// Requires JWT + "tier.create" permission.
func (s *Server) handleCreateTier(w http.ResponseWriter, r *http.Request) {
	if s.tierQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	_, ok = uuidPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := uuidPathParam(w, r, "session_id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("tier.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("tier.empty_body", "request body is required", r))
		return
	}

	var req createTierRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("tier.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.PricingMode = strings.TrimSpace(req.PricingMode)
	req.Currency = strings.TrimSpace(req.Currency)

	// Name is required.
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"tier.missing_name", "name is required", r,
			map[string]any{"field": "name"},
		))
		return
	}

	// pricing_mode is required and must be a known value.
	if req.PricingMode == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"tier.missing_pricing_mode", "pricing_mode is required", r,
			map[string]any{"field": "pricing_mode"},
		))
		return
	}
	if !validPricingModes[req.PricingMode] {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"tier.invalid_pricing_mode", "pricing_mode must be one of: fixed, free, pwyw", r,
			map[string]any{"field": "pricing_mode"},
		))
		return
	}

	// Currency defaults to "USD" when not provided.
	if req.Currency == "" {
		req.Currency = "USD"
	}

	// Validate pricing mode invariants.
	if errCode, errMsg := validatePricingMode(req.PricingMode, req.PriceAmount, req.PwywMin, req.PwywMax); errCode != "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(errCode, errMsg, r))
		return
	}

	// Parse optional sale_window_start.
	var saleStart *time.Time
	if req.SaleWindowStart != nil {
		trimmed := strings.TrimSpace(*req.SaleWindowStart)
		if trimmed != "" {
			t, parseErr := time.Parse(time.RFC3339, trimmed)
			if parseErr != nil {
				writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
					"tier.invalid_sale_window_start", "sale_window_start must be a valid RFC3339 timestamp", r,
					map[string]any{"field": "sale_window_start"},
				))
				return
			}
			saleStart = &t
		}
	}

	// Parse optional sale_window_end.
	var saleEnd *time.Time
	if req.SaleWindowEnd != nil {
		trimmed := strings.TrimSpace(*req.SaleWindowEnd)
		if trimmed != "" {
			t, parseErr := time.Parse(time.RFC3339, trimmed)
			if parseErr != nil {
				writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
					"tier.invalid_sale_window_end", "sale_window_end must be a valid RFC3339 timestamp", r,
					map[string]any{"field": "sale_window_end"},
				))
				return
			}
			saleEnd = &t
		}
	}

	// Sale window invariant: end must be after start when both are provided.
	if saleStart != nil && saleEnd != nil && !saleEnd.After(*saleStart) {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"tier.invalid_sale_window", "sale_window_end must be after sale_window_start", r,
			map[string]any{"field": "sale_window_end"},
		))
		return
	}

	// Capacity must be positive when provided.
	if req.Capacity != nil && *req.Capacity <= 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"tier.invalid_capacity", "capacity must be greater than 0", r,
			map[string]any{"field": "capacity"},
		))
		return
	}

	tier, err := s.tierQueries.InsertTicketTier(ctx,
		sessionID,
		req.Name, req.PricingMode,
		req.PriceAmount, req.Currency,
		req.PwywMin, req.PwywMax,
		req.Capacity,
		saleStart, saleEnd,
		req.SortOrder,
	)
	if err != nil {
		s.logger.Error("tier: insert failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"tier.insert_failed", "failed to create ticket tier", r,
		))
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"tier": tierFromRow(tier),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers
// ─────────────────────────────────────────────────────────────────────────────

// handleListTiers serves GET .../sessions/{session_id}/tiers.
// Requires JWT + "tier.read" permission.
func (s *Server) handleListTiers(w http.ResponseWriter, r *http.Request) {
	if s.tierQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	_, ok = uuidPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := uuidPathParam(w, r, "session_id")
	if !ok {
		return
	}

	rows, err := s.tierQueries.ListTicketTiersBySession(ctx, sessionID)
	if err != nil {
		s.logger.Error("tier: list failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"tier.list_failed", "failed to list ticket tiers", r,
		))
		return
	}

	result := make([]tierResponse, 0, len(rows))
	for _, t := range rows {
		result = append(result, tierFromRow(t))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tiers": result,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}
// ─────────────────────────────────────────────────────────────────────────────

// handleGetTier serves GET .../sessions/{session_id}/tiers/{id}.
// Requires JWT + "tier.read" permission.
func (s *Server) handleGetTier(w http.ResponseWriter, r *http.Request) {
	if s.tierQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	_, ok = uuidPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := uuidPathParam(w, r, "session_id")
	if !ok {
		return
	}
	tierID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	tier, err := s.tierQueries.GetTicketTierByID(ctx, tierID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("tier.not_found", "ticket tier not found", r))
			return
		}
		s.logger.Error("tier: get failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"tier.get_failed", "failed to get ticket tier", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tier": tierFromRow(tier),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}
// ─────────────────────────────────────────────────────────────────────────────

// updateTierRequest is the request body for PATCH .../tiers/{id}.
// All fields are optional; nil/empty values leave the existing value unchanged.
type updateTierRequest struct {
	Name            *string `json:"name"`
	PricingMode     *string `json:"pricing_mode"`
	PriceAmount     *int64  `json:"price_amount"`
	Currency        *string `json:"currency"`
	PwywMin         *int64  `json:"pwyw_min"`
	PwywMax         *int64  `json:"pwyw_max"`
	Capacity        *int32  `json:"capacity"`
	SaleWindowStart *string `json:"sale_window_start"`
	SaleWindowEnd   *string `json:"sale_window_end"`
	SortOrder       *int32  `json:"sort_order"`
}

// handleUpdateTier serves PATCH .../sessions/{session_id}/tiers/{id}.
// Requires JWT + "tier.update" permission.
func (s *Server) handleUpdateTier(w http.ResponseWriter, r *http.Request) {
	if s.tierQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	_, ok = uuidPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := uuidPathParam(w, r, "session_id")
	if !ok {
		return
	}
	tierID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("tier.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("tier.empty_body", "request body is required", r))
		return
	}

	var req updateTierRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("tier.invalid_json", "request body is not valid JSON", r))
		return
	}

	// Validate pricing_mode if provided.
	pricingMode := ""
	if req.PricingMode != nil {
		pricingMode = strings.TrimSpace(*req.PricingMode)
		if pricingMode != "" && !validPricingModes[pricingMode] {
			writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
				"tier.invalid_pricing_mode", "pricing_mode must be one of: fixed, free, pwyw", r,
				map[string]any{"field": "pricing_mode"},
			))
			return
		}
	}

	// Validate capacity if provided.
	if req.Capacity != nil && *req.Capacity <= 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"tier.invalid_capacity", "capacity must be greater than 0", r,
			map[string]any{"field": "capacity"},
		))
		return
	}

	// Parse optional sale_window_start.
	var saleStart *time.Time
	if req.SaleWindowStart != nil {
		trimmed := strings.TrimSpace(*req.SaleWindowStart)
		if trimmed != "" {
			t, parseErr := time.Parse(time.RFC3339, trimmed)
			if parseErr != nil {
				writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
					"tier.invalid_sale_window_start", "sale_window_start must be a valid RFC3339 timestamp", r,
					map[string]any{"field": "sale_window_start"},
				))
				return
			}
			saleStart = &t
		}
	}

	// Parse optional sale_window_end.
	var saleEnd *time.Time
	if req.SaleWindowEnd != nil {
		trimmed := strings.TrimSpace(*req.SaleWindowEnd)
		if trimmed != "" {
			t, parseErr := time.Parse(time.RFC3339, trimmed)
			if parseErr != nil {
				writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
					"tier.invalid_sale_window_end", "sale_window_end must be a valid RFC3339 timestamp", r,
					map[string]any{"field": "sale_window_end"},
				))
				return
			}
			saleEnd = &t
		}
	}

	// Sale window invariant: end must be after start when both provided.
	if saleStart != nil && saleEnd != nil && !saleEnd.After(*saleStart) {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"tier.invalid_sale_window", "sale_window_end must be after sale_window_start", r,
			map[string]any{"field": "sale_window_end"},
		))
		return
	}

	// Fetch the current tier to validate cross-field pricing invariants
	// when the mode or price changes together.
	current, err := s.tierQueries.GetTicketTierByID(ctx, tierID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("tier.not_found", "ticket tier not found", r))
			return
		}
		s.logger.Error("tier: get for update failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"tier.get_failed", "failed to get ticket tier", r,
		))
		return
	}

	// Resolve effective values for cross-field validation.
	effectiveMode := current.PricingMode
	if pricingMode != "" {
		effectiveMode = pricingMode
	}
	effectivePrice := current.PriceAmount
	if req.PriceAmount != nil {
		effectivePrice = *req.PriceAmount
	}
	effectivePwywMin := current.PwywMin
	if req.PwywMin != nil {
		effectivePwywMin = req.PwywMin
	}
	effectivePwywMax := current.PwywMax
	if req.PwywMax != nil {
		effectivePwywMax = req.PwywMax
	}

	// Re-validate pricing invariants with effective values.
	if errCode, errMsg := validatePricingMode(effectiveMode, effectivePrice, effectivePwywMin, effectivePwywMax); errCode != "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(errCode, errMsg, r))
		return
	}

	// Resolve string fields for the query (empty string = keep existing).
	name := ""
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		if name == "" {
			writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
				"tier.invalid_name", "name cannot be empty", r,
				map[string]any{"field": "name"},
			))
			return
		}
	}
	currency := ""
	if req.Currency != nil {
		currency = strings.TrimSpace(*req.Currency)
	}

	updated, err := s.tierQueries.UpdateTicketTier(ctx,
		tierID, sessionID,
		name, pricingMode,
		req.PriceAmount, currency,
		req.PwywMin, req.PwywMax,
		req.Capacity,
		saleStart, saleEnd,
		req.SortOrder,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("tier.not_found", "ticket tier not found", r))
			return
		}
		s.logger.Error("tier: update failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"tier.update_failed", "failed to update ticket tier", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tier": tierFromRow(updated),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}
// ─────────────────────────────────────────────────────────────────────────────

// handleDeleteTier serves DELETE .../sessions/{session_id}/tiers/{id}.
// Performs a soft-delete (sets deleted_at = now()) and writes an audit event.
// Requires JWT + "tier.delete" permission.
func (s *Server) handleDeleteTier(w http.ResponseWriter, r *http.Request) {
	if s.tierQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	_, ok = uuidPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := uuidPathParam(w, r, "session_id")
	if !ok {
		return
	}
	tierID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	// Open transaction: soft-delete + audit in one atomic write.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.tierQueries.WithTx(tx)

	deleted, err := qtx.SoftDeleteTicketTier(ctx, tierID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("tier.not_found", "ticket tier not found", r))
			return
		}
		s.logger.Error("tier: soft-delete failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"tier.delete_failed", "failed to delete ticket tier", r,
		))
		return
	}

	// Write audit event inside the same transaction.
	if s.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		auditEv := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.tier.delete",
			ResourceType: "ticket_tier",
			ResourceID:   tierID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           extractClientIP(r),
			Metadata: map[string]any{
				"session_id":   sessionID.String(),
				"pricing_mode": deleted.PricingMode,
			},
		}
		if err := s.audit.WriteTx(ctx, tx, auditEv); err != nil {
			s.logger.Error("tier: audit write failed", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"tier.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"tier.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tier":    tierFromRow(deleted),
		"deleted": true,
	})
}

// extractClientIP is declared in echo.go — reuse it via same package.

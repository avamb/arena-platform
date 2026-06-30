// ticket_tiers.go implements the ticket tier CRUD API endpoints (feature #127).
package hcatalog

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	catalogdomain "github.com/abhteam/arena_new/apps/backend/internal/domain/catalog"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// ValidPricingModes lists the allowed pricing_mode values.
var ValidPricingModes = map[string]bool{
	string(catalogdomain.PricingModeFixed): true,
	string(catalogdomain.PricingModeFree):  true,
	string(catalogdomain.PricingModePWYW):  true,
}

// ValidatePricingMode enforces pricing-mode invariants. Returns (errorCode,
// errorMessage) on failure, ("", "") on success. Exported so httpserver
// shims and tests can call it without importing the domain layer.
func ValidatePricingMode(mode string, priceAmount int64, pwywMin, pwywMax *int64) (string, string) {
	return catalogdomain.ValidatePricingMode(mode, priceAmount, pwywMin, pwywMax)
}

// ─────────────────────────────────────────────────────────────────────────────
// Response type
// ─────────────────────────────────────────────────────────────────────────────

type tierResponse struct {
	ID              string  `json:"id"`
	SessionID       string  `json:"session_id"`
	Name            string  `json:"name"`
	PricingMode     string  `json:"pricing_mode"`
	PriceAmount     int64   `json:"price_amount"`
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

// TierResponse is the exported alias of tierResponse for use by the httpserver
// shim layer (ticket_tiers_test.go in package httpserver references tierFromRow
// via catalog_shims.go and reads response fields directly).
type TierResponse = tierResponse

// TierFromRow is the exported alias of tierFromRow for use by the httpserver
// shim layer (ticket_tiers_test.go calls tierFromRow via catalog_shims.go).
func TierFromRow(t gen.TicketTierRow) TierResponse { return tierFromRow(t) }

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
// POST .../sessions/{session_id}/tiers
// ─────────────────────────────────────────────────────────────────────────────

type createTierRequest struct {
	Name            string  `json:"name"`
	PricingMode     string  `json:"pricing_mode"`
	PriceAmount     int64   `json:"price_amount"`
	Currency        string  `json:"currency"`
	PwywMin         *int64  `json:"pwyw_min"`
	PwywMax         *int64  `json:"pwyw_max"`
	Capacity        *int32  `json:"capacity"`
	SaleWindowStart *string `json:"sale_window_start"`
	SaleWindowEnd   *string `json:"sale_window_end"`
	SortOrder       int32   `json:"sort_order"`
}

func (h *Handler) HandleCreateTier(w http.ResponseWriter, r *http.Request) {
	if h.tierQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	_, ok = httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("tier.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("tier.empty_body", "request body is required", r))
		return
	}

	var req createTierRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("tier.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.PricingMode = strings.TrimSpace(req.PricingMode)
	req.Currency = strings.TrimSpace(req.Currency)

	if req.Name == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"tier.missing_name", "name is required", r,
			map[string]any{"field": "name"},
		))
		return
	}

	if req.PricingMode == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"tier.missing_pricing_mode", "pricing_mode is required", r,
			map[string]any{"field": "pricing_mode"},
		))
		return
	}
	if !ValidPricingModes[req.PricingMode] {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"tier.invalid_pricing_mode", "pricing_mode must be one of: fixed, free, pwyw", r,
			map[string]any{"field": "pricing_mode"},
		))
		return
	}

	if req.Currency == "" {
		req.Currency = "USD"
	}

	if errCode, errMsg := ValidatePricingMode(req.PricingMode, req.PriceAmount, req.PwywMin, req.PwywMax); errCode != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(errCode, errMsg, r))
		return
	}

	var saleStart *time.Time
	if req.SaleWindowStart != nil {
		trimmed := strings.TrimSpace(*req.SaleWindowStart)
		if trimmed != "" {
			t, parseErr := time.Parse(time.RFC3339, trimmed)
			if parseErr != nil {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"tier.invalid_sale_window_start", "sale_window_start must be a valid RFC3339 timestamp", r,
					map[string]any{"field": "sale_window_start"},
				))
				return
			}
			saleStart = &t
		}
	}

	var saleEnd *time.Time
	if req.SaleWindowEnd != nil {
		trimmed := strings.TrimSpace(*req.SaleWindowEnd)
		if trimmed != "" {
			t, parseErr := time.Parse(time.RFC3339, trimmed)
			if parseErr != nil {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"tier.invalid_sale_window_end", "sale_window_end must be a valid RFC3339 timestamp", r,
					map[string]any{"field": "sale_window_end"},
				))
				return
			}
			saleEnd = &t
		}
	}

	if saleStart != nil && saleEnd != nil && !saleEnd.After(*saleStart) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"tier.invalid_sale_window", "sale_window_end must be after sale_window_start", r,
			map[string]any{"field": "sale_window_end"},
		))
		return
	}

	if req.Capacity != nil && *req.Capacity <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"tier.invalid_capacity", "capacity must be greater than 0", r,
			map[string]any{"field": "capacity"},
		))
		return
	}

	tier, err := h.tierQueries.InsertTicketTier(ctx,
		sessionID,
		req.Name, req.PricingMode,
		req.PriceAmount, req.Currency,
		req.PwywMin, req.PwywMax,
		req.Capacity,
		saleStart, saleEnd,
		req.SortOrder,
	)
	if err != nil {
		h.logger.Error("tier: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"tier.insert_failed", "failed to create ticket tier", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"tier": tierFromRow(tier),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET .../sessions/{session_id}/tiers
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleListTiers(w http.ResponseWriter, r *http.Request) {
	if h.tierQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	_, ok = httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}

	rows, err := h.tierQueries.ListTicketTiersBySession(ctx, sessionID)
	if err != nil {
		h.logger.Error("tier: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"tier.list_failed", "failed to list ticket tiers", r,
		))
		return
	}

	result := make([]tierResponse, 0, len(rows))
	for _, t := range rows {
		result = append(result, tierFromRow(t))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"tiers": result,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET .../sessions/{session_id}/tiers/{id}
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleGetTier(w http.ResponseWriter, r *http.Request) {
	if h.tierQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	_, ok = httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}
	tierID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	tier, err := h.tierQueries.GetTicketTierByID(ctx, tierID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("tier.not_found", "ticket tier not found", r))
			return
		}
		h.logger.Error("tier: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"tier.get_failed", "failed to get ticket tier", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"tier": tierFromRow(tier),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH .../sessions/{session_id}/tiers/{id}
// ─────────────────────────────────────────────────────────────────────────────

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

func (h *Handler) HandleUpdateTier(w http.ResponseWriter, r *http.Request) {
	if h.tierQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	_, ok = httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}
	tierID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("tier.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("tier.empty_body", "request body is required", r))
		return
	}

	var req updateTierRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("tier.invalid_json", "request body is not valid JSON", r))
		return
	}

	pricingMode := ""
	if req.PricingMode != nil {
		pricingMode = strings.TrimSpace(*req.PricingMode)
		if pricingMode != "" && !ValidPricingModes[pricingMode] {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"tier.invalid_pricing_mode", "pricing_mode must be one of: fixed, free, pwyw", r,
				map[string]any{"field": "pricing_mode"},
			))
			return
		}
	}

	if req.Capacity != nil && *req.Capacity <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"tier.invalid_capacity", "capacity must be greater than 0", r,
			map[string]any{"field": "capacity"},
		))
		return
	}

	var saleStart *time.Time
	if req.SaleWindowStart != nil {
		trimmed := strings.TrimSpace(*req.SaleWindowStart)
		if trimmed != "" {
			t, parseErr := time.Parse(time.RFC3339, trimmed)
			if parseErr != nil {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"tier.invalid_sale_window_start", "sale_window_start must be a valid RFC3339 timestamp", r,
					map[string]any{"field": "sale_window_start"},
				))
				return
			}
			saleStart = &t
		}
	}

	var saleEnd *time.Time
	if req.SaleWindowEnd != nil {
		trimmed := strings.TrimSpace(*req.SaleWindowEnd)
		if trimmed != "" {
			t, parseErr := time.Parse(time.RFC3339, trimmed)
			if parseErr != nil {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"tier.invalid_sale_window_end", "sale_window_end must be a valid RFC3339 timestamp", r,
					map[string]any{"field": "sale_window_end"},
				))
				return
			}
			saleEnd = &t
		}
	}

	if saleStart != nil && saleEnd != nil && !saleEnd.After(*saleStart) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"tier.invalid_sale_window", "sale_window_end must be after sale_window_start", r,
			map[string]any{"field": "sale_window_end"},
		))
		return
	}

	current, err := h.tierQueries.GetTicketTierByID(ctx, tierID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("tier.not_found", "ticket tier not found", r))
			return
		}
		h.logger.Error("tier: get for update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"tier.get_failed", "failed to get ticket tier", r,
		))
		return
	}

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

	if errCode, errMsg := ValidatePricingMode(effectiveMode, effectivePrice, effectivePwywMin, effectivePwywMax); errCode != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(errCode, errMsg, r))
		return
	}

	name := ""
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		if name == "" {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
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

	updated, err := h.tierQueries.UpdateTicketTier(ctx,
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
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("tier.not_found", "ticket tier not found", r))
			return
		}
		h.logger.Error("tier: update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"tier.update_failed", "failed to update ticket tier", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"tier": tierFromRow(updated),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE .../sessions/{session_id}/tiers/{id}
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleDeleteTier(w http.ResponseWriter, r *http.Request) {
	if h.tierQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	_, ok = httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}
	tierID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := h.tierQueries.WithTx(tx)

	deleted, err := qtx.SoftDeleteTicketTier(ctx, tierID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("tier.not_found", "ticket tier not found", r))
			return
		}
		h.logger.Error("tier: soft-delete failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"tier.delete_failed", "failed to delete ticket tier", r,
		))
		return
	}

	if h.audit != nil {
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
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"session_id":   sessionID.String(),
				"pricing_mode": deleted.PricingMode,
			},
		}
		if err := h.audit.WriteTx(ctx, tx, auditEv); err != nil {
			h.logger.Error("tier: audit write failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"tier.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"tier.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"tier":    tierFromRow(deleted),
		"deleted": true,
	})
}

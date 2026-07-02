// external_allocations.go implements the external allocation quota HTTP API (feature #145).
//
// External allocations let partner organisations (resellers, box offices) reserve
// quota blocks from the platform's inventory. The platform inventory is reduced
// atomically when an allocation is activated, and restored when unused quota is
// returned at reconciliation time.
//
// # Allocation status lifecycle
//
//	pending → active      : inventory held (ReserveCapacity)
//	active  → reconciled  : inventory settled (ConfirmCapacity + ReleaseCapacity)
//	active  → disputed    : inventory remains held
//	disputed→ reconciled  : inventory settled (ConfirmCapacity + ReleaseCapacity)
//
// # Endpoints (all require JWT auth)
//
//	POST  /v1/organizations/{org_id}/external-allocations            — create allocation (allocation.create)
//	GET   /v1/organizations/{org_id}/external-allocations            — list by partner org (allocation.read)
//	GET   /v1/organizations/{org_id}/external-allocations/{id}       — get detail (allocation.read)
//	PATCH /v1/organizations/{org_id}/external-allocations/{id}       — update status / report consumption (allocation.update)
package hinventory

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	inventorydomain "github.com/abhteam/arena_new/apps/backend/internal/domain/inventory"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// ─────────────────────────────────────────────────────────────────────────────
// State transition table
// ─────────────────────────────────────────────────────────────────────────────

// validAllocationTransitions mirrors the pure-domain transition table from
// internal/domain/inventory.ValidAllocationTransitions (feature #184),
// projected back to a string-keyed map so the in-package state-machine
// tests (external_allocations_145_test.go, via the inventory_shims.go
// forwarders) can inspect terminal-state emptiness without importing the
// domain package. Allowed transitions:
// pending → active|reconciled, active → reconciled|disputed, disputed →
// reconciled; reconciled is terminal.
var validAllocationTransitions = func() map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(inventorydomain.ValidAllocationTransitions))
	for from, allowed := range inventorydomain.ValidAllocationTransitions {
		row := make(map[string]bool, len(allowed))
		for to := range allowed {
			row[string(to)] = true
		}
		out[string(from)] = row
	}
	return out
}()

// isTerminalAllocationStatus returns true for statuses that admit no
// further transitions. 1-line forwarder to the pure-domain predicate in
// internal/domain/inventory (feature #184).
func isTerminalAllocationStatus(status string) bool {
	return inventorydomain.IsTerminalAllocationStatus(status)
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/external-allocations
// ─────────────────────────────────────────────────────────────────────────────

type createExternalAllocationRequest struct {
	SessionID string  `json:"session_id"`
	TierID    *string `json:"tier_id"`
	QuotaQty  int32   `json:"quota_qty"`
	Status    string  `json:"status"` // "pending" or "active"
	Notes     *string `json:"notes"`
}

// HandleCreateExternalAllocation serves POST /v1/organizations/{org_id}/external-allocations.
func (h *Handler) HandleCreateExternalAllocation(w http.ResponseWriter, r *http.Request) {
	if h.allocationQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	orgIDStr := chi.URLParam(r, "org_id")
	partnerOrgID, err := uuid.Parse(orgIDStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"allocation.invalid_org_id", "org_id must be a valid UUID", r,
		))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"allocation.read_body_failed", "failed to read request body", r,
		))
		return
	}

	var req createExternalAllocationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"allocation.invalid_json", "request body is not valid JSON", r,
		))
		return
	}

	// Validate session_id.
	sessionID, err := uuid.Parse(req.SessionID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"allocation.invalid_session_id", "session_id must be a valid UUID", r,
		))
		return
	}

	// Validate quota_qty.
	if req.QuotaQty <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"allocation.invalid_quota", "quota_qty must be a positive integer", r,
		))
		return
	}

	// Validate and default status.
	if req.Status == "" {
		req.Status = "pending"
	}
	if req.Status != "pending" && req.Status != "active" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"allocation.invalid_status", "initial status must be 'pending' or 'active'", r,
		))
		return
	}

	// Parse optional tier_id.
	var tierID *uuid.UUID
	if req.TierID != nil && *req.TierID != "" {
		tid, err := uuid.Parse(*req.TierID)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"allocation.invalid_tier_id", "tier_id must be a valid UUID", r,
			))
			return
		}
		tierID = &tid
	}

	ctx := r.Context()

	// If creating as 'active', we need to atomically reserve inventory + insert allocation.
	if req.Status == "active" {
		if h.inventoryQueries == nil {
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
				"dependency.database_unavailable", "inventory service is not available", r,
			))
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

		invQ := h.inventoryQueries.WithTx(tx)
		allocQ := h.allocationQueries.WithTx(tx)

		// Reserve capacity — returns pgx.ErrNoRows on over-capacity.
		if _, err := invQ.ReserveCapacity(ctx, sessionID, tierID, req.QuotaQty); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
					"allocation.quota_overflow", "insufficient platform inventory for this allocation quota", r,
				))
				return
			}
			h.logger.Error("external_allocation: reserve capacity failed",
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"allocation.capacity_failed", "failed to reserve inventory capacity", r,
			))
			return
		}

		alloc, err := allocQ.InsertExternalAllocation(
			ctx, sessionID, partnerOrgID, tierID, req.QuotaQty, "active", req.Notes,
		)
		if err != nil {
			h.logger.Error("external_allocation: insert failed",
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"allocation.insert_failed", "failed to create external allocation", r,
			))
			return
		}

		if err := tx.Commit(ctx); err != nil {
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"allocation.commit_failed", "failed to commit allocation transaction", r,
			))
			return
		}

		httputil.WriteJSON(w, http.StatusCreated, map[string]any{
			"allocation": externalAllocationFromRow(alloc),
		})
		return
	}

	// Default: create in 'pending' status (no inventory change yet).
	alloc, err := h.allocationQueries.InsertExternalAllocation(
		ctx, sessionID, partnerOrgID, tierID, req.QuotaQty, "pending", req.Notes,
	)
	if err != nil {
		h.logger.Error("external_allocation: insert failed",
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"allocation.insert_failed", "failed to create external allocation", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"allocation": externalAllocationFromRow(alloc),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/external-allocations
// ─────────────────────────────────────────────────────────────────────────────

// HandleListExternalAllocations serves GET /v1/organizations/{org_id}/external-allocations.
func (h *Handler) HandleListExternalAllocations(w http.ResponseWriter, r *http.Request) {
	if h.allocationQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	orgIDStr := chi.URLParam(r, "org_id")
	partnerOrgID, err := uuid.Parse(orgIDStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"allocation.invalid_org_id", "org_id must be a valid UUID", r,
		))
		return
	}

	// Optional status filter.
	var statusFilter *string
	if sf := r.URL.Query().Get("status"); sf != "" {
		statusFilter = &sf
	}

	ctx := r.Context()
	rows, err := h.allocationQueries.ListExternalAllocationsByOrg(ctx, partnerOrgID, statusFilter)
	if err != nil {
		h.logger.Error("external_allocation: list failed",
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"allocation.list_failed", "failed to list external allocations", r,
		))
		return
	}

	allocations := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		allocations = append(allocations, externalAllocationFromRow(row))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"allocations": allocations,
		"total":       len(allocations),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/external-allocations/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetExternalAllocation serves GET /v1/organizations/{org_id}/external-allocations/{id}.
func (h *Handler) HandleGetExternalAllocation(w http.ResponseWriter, r *http.Request) {
	if h.allocationQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"allocation.invalid_id", "id must be a valid UUID", r,
		))
		return
	}

	ctx := r.Context()
	alloc, err := h.allocationQueries.GetExternalAllocationByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"allocation.not_found", "external allocation not found", r,
			))
			return
		}
		h.logger.Error("external_allocation: get failed",
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"allocation.get_failed", "failed to retrieve external allocation", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"allocation": externalAllocationFromRow(alloc),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/organizations/{org_id}/external-allocations/{id}
// ─────────────────────────────────────────────────────────────────────────────

type patchExternalAllocationRequest struct {
	// Status is the new target status. Optional when only reporting consumption.
	Status *string `json:"status"`
	// QuotaConsumed is the final units consumed by the partner.
	// Required when transitioning to 'reconciled'.
	QuotaConsumed *int32 `json:"quota_consumed"`
	// Notes is an optional free-text annotation.
	Notes *string `json:"notes"`
}

// HandlePatchExternalAllocation serves PATCH /v1/organizations/{org_id}/external-allocations/{id}.
func (h *Handler) HandlePatchExternalAllocation(w http.ResponseWriter, r *http.Request) {
	if h.allocationQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"allocation.invalid_id", "id must be a valid UUID", r,
		))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"allocation.read_body_failed", "failed to read request body", r,
		))
		return
	}

	var req patchExternalAllocationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"allocation.invalid_json", "request body is not valid JSON", r,
		))
		return
	}

	ctx := r.Context()

	// Fetch the current allocation.
	current, err := h.allocationQueries.GetExternalAllocationByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"allocation.not_found", "external allocation not found", r,
			))
			return
		}
		h.logger.Error("external_allocation: get failed",
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"allocation.get_failed", "failed to retrieve external allocation", r,
		))
		return
	}

	// Cannot modify terminal allocations.
	if isTerminalAllocationStatus(current.Status) {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"allocation.terminal_status",
			"allocation is in a terminal status and cannot be modified",
			r,
		))
		return
	}

	// Determine target status.
	targetStatus := current.Status
	if req.Status != nil {
		targetStatus = *req.Status
	}

	// Validate state transition.
	if targetStatus != current.Status {
		allowed, ok := validAllocationTransitions[current.Status]
		if !ok || !allowed[targetStatus] {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"allocation.invalid_transition",
				"invalid status transition: "+current.Status+" → "+targetStatus,
				r,
			))
			return
		}
	}

	// Handle inventory-affecting transitions.
	switch {
	case current.Status == "pending" && targetStatus == "active":
		// Reserve capacity atomically with status update.
		if h.inventoryQueries == nil {
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
				"dependency.database_unavailable", "inventory service is not available", r,
			))
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

		invQ := h.inventoryQueries.WithTx(tx)
		allocQ := h.allocationQueries.WithTx(tx)

		// Reserve capacity — returns pgx.ErrNoRows on over-capacity.
		if _, err := invQ.ReserveCapacity(ctx, current.SessionID, current.TierID, current.QuotaQty); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
					"allocation.quota_overflow",
					"insufficient platform inventory for this allocation quota",
					r,
				))
				return
			}
			h.logger.Error("external_allocation: reserve capacity failed",
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"allocation.capacity_failed", "failed to reserve inventory capacity", r,
			))
			return
		}

		alloc, err := allocQ.UpdateExternalAllocationStatus(ctx, id, "active")
		if err != nil {
			h.logger.Error("external_allocation: status update failed",
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"allocation.update_failed", "failed to update allocation status", r,
			))
			return
		}

		if err := tx.Commit(ctx); err != nil {
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"allocation.commit_failed", "failed to commit allocation transaction", r,
			))
			return
		}

		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"allocation": externalAllocationFromRow(alloc),
		})
		return

	case (current.Status == "active" || current.Status == "disputed") && targetStatus == "reconciled":
		// Settle inventory: confirm consumed + release remainder.
		if h.inventoryQueries == nil {
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
				"dependency.database_unavailable", "inventory service is not available", r,
			))
			return
		}

		// quota_consumed must be provided for reconciliation.
		consumed := current.QuotaConsumed
		if req.QuotaConsumed != nil {
			consumed = *req.QuotaConsumed
		}
		if consumed < 0 || consumed > current.QuotaQty {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"allocation.invalid_consumed",
				"quota_consumed must be between 0 and quota_qty",
				r,
			))
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

		invQ := h.inventoryQueries.WithTx(tx)
		allocQ := h.allocationQueries.WithTx(tx)

		// Confirm consumed capacity (held → sold).
		if consumed > 0 {
			if _, err := invQ.ConfirmCapacity(ctx, current.SessionID, current.TierID, consumed); err != nil {
				h.logger.Error("external_allocation: confirm capacity failed",
					slog.String("error", err.Error()),
				)
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
					"allocation.confirm_failed", "failed to confirm consumed capacity", r,
				))
				return
			}
		}

		// Release unused capacity back to available.
		remainder := current.QuotaQty - consumed
		if remainder > 0 {
			if _, err := invQ.ReleaseCapacity(ctx, current.SessionID, current.TierID, remainder); err != nil {
				h.logger.Error("external_allocation: release capacity failed",
					slog.String("error", err.Error()),
				)
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
					"allocation.release_failed", "failed to release unused capacity", r,
				))
				return
			}
		}

		alloc, err := allocQ.ReportAllocationConsumption(ctx, id, consumed, "reconciled")
		if err != nil {
			h.logger.Error("external_allocation: report consumption failed",
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"allocation.update_failed", "failed to reconcile allocation", r,
			))
			return
		}

		if err := tx.Commit(ctx); err != nil {
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"allocation.commit_failed", "failed to commit reconciliation transaction", r,
			))
			return
		}

		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"allocation": externalAllocationFromRow(alloc),
		})
		return

	default:
		// For transitions that don't affect inventory (e.g. active→disputed,
		// pending→reconciled without consumption): just update the status.
		consumed := current.QuotaConsumed
		if req.QuotaConsumed != nil {
			consumed = *req.QuotaConsumed
		}

		var alloc gen.ExternalAllocationRow
		if req.QuotaConsumed != nil {
			alloc, err = h.allocationQueries.ReportAllocationConsumption(ctx, id, consumed, targetStatus)
		} else {
			alloc, err = h.allocationQueries.UpdateExternalAllocationStatus(ctx, id, targetStatus)
		}
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
					"allocation.not_found", "external allocation not found", r,
				))
				return
			}
			h.logger.Error("external_allocation: update failed",
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"allocation.update_failed", "failed to update allocation", r,
			))
			return
		}

		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"allocation": externalAllocationFromRow(alloc),
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Response helper
// ─────────────────────────────────────────────────────────────────────────────

// externalAllocationFromRow converts a gen.ExternalAllocationRow to a JSON-serializable map.
func externalAllocationFromRow(r gen.ExternalAllocationRow) map[string]any {
	out := map[string]any{
		"id":             r.ID,
		"session_id":     r.SessionID,
		"partner_org_id": r.PartnerOrgID,
		"tier_id":        r.TierID,
		"quota_qty":      r.QuotaQty,
		"quota_consumed": r.QuotaConsumed,
		"status":         r.Status,
		"notes":          r.Notes,
		"created_at":     r.CreatedAt,
		"updated_at":     r.UpdatedAt,
	}

	return out
}

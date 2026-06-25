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
package httpserver

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
)

// ─────────────────────────────────────────────────────────────────────────────
// State transition table
// ─────────────────────────────────────────────────────────────────────────────

// validAllocationTransitions defines allowed status transitions.
// Terminal states (reconciled) map to empty sets.
var validAllocationTransitions = map[string]map[string]bool{
	"pending": {
		"active":     true,
		"reconciled": true, // direct reconcile without activation (edge case)
	},
	"active": {
		"reconciled": true,
		"disputed":   true,
	},
	"disputed": {
		"reconciled": true,
	},
	"reconciled": {}, // terminal
}

// allAllocationStatuses is the complete set of valid external allocation statuses.
var allAllocationStatuses = []string{"pending", "active", "reconciled", "disputed"}

// isTerminalAllocationStatus returns true for statuses that admit no further transitions.
func isTerminalAllocationStatus(status string) bool {
	targets, exists := validAllocationTransitions[status]
	return exists && len(targets) == 0
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

func (s *Server) handleCreateExternalAllocation(w http.ResponseWriter, r *http.Request) {
	if s.allocationQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	orgIDStr := chi.URLParam(r, "org_id")
	partnerOrgID, err := uuid.Parse(orgIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"allocation.invalid_org_id", "org_id must be a valid UUID", r,
		))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"allocation.read_body_failed", "failed to read request body", r,
		))
		return
	}

	var req createExternalAllocationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"allocation.invalid_json", "request body is not valid JSON", r,
		))
		return
	}

	// Validate session_id.
	sessionID, err := uuid.Parse(req.SessionID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"allocation.invalid_session_id", "session_id must be a valid UUID", r,
		))
		return
	}

	// Validate quota_qty.
	if req.QuotaQty <= 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"allocation.invalid_quota", "quota_qty must be a positive integer", r,
		))
		return
	}

	// Validate and default status.
	if req.Status == "" {
		req.Status = "pending"
	}
	if req.Status != "pending" && req.Status != "active" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"allocation.invalid_status", "initial status must be 'pending' or 'active'", r,
		))
		return
	}

	// Parse optional tier_id.
	var tierID *uuid.UUID
	if req.TierID != nil && *req.TierID != "" {
		tid, err := uuid.Parse(*req.TierID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelope(
				"allocation.invalid_tier_id", "tier_id must be a valid UUID", r,
			))
			return
		}
		tierID = &tid
	}

	ctx := r.Context()

	// If creating as 'active', we need to atomically reserve inventory + insert allocation.
	if req.Status == "active" {
		if s.inventoryQueries == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
				"dependency.database_unavailable", "inventory service is not available", r,
			))
			return
		}

		tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
				"dependency.database_unavailable", "failed to begin transaction", r,
			))
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		invQ := s.inventoryQueries.WithTx(tx)
		allocQ := s.allocationQueries.WithTx(tx)

		// Reserve capacity — returns pgx.ErrNoRows on over-capacity.
		if _, err := invQ.ReserveCapacity(ctx, sessionID, tierID, req.QuotaQty); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSON(w, http.StatusConflict, errorEnvelope(
					"allocation.quota_overflow", "insufficient platform inventory for this allocation quota", r,
				))
				return
			}
			s.logger.Error("external_allocation: reserve capacity failed",
				slog.String("error", err.Error()),
			)
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"allocation.capacity_failed", "failed to reserve inventory capacity", r,
			))
			return
		}

		alloc, err := allocQ.InsertExternalAllocation(
			ctx, sessionID, partnerOrgID, tierID, req.QuotaQty, "active", req.Notes,
		)
		if err != nil {
			s.logger.Error("external_allocation: insert failed",
				slog.String("error", err.Error()),
			)
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"allocation.insert_failed", "failed to create external allocation", r,
			))
			return
		}

		if err := tx.Commit(ctx); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"allocation.commit_failed", "failed to commit allocation transaction", r,
			))
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"allocation": externalAllocationFromRow(alloc),
		})
		return
	}

	// Default: create in 'pending' status (no inventory change yet).
	alloc, err := s.allocationQueries.InsertExternalAllocation(
		ctx, sessionID, partnerOrgID, tierID, req.QuotaQty, "pending", req.Notes,
	)
	if err != nil {
		s.logger.Error("external_allocation: insert failed",
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"allocation.insert_failed", "failed to create external allocation", r,
		))
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"allocation": externalAllocationFromRow(alloc),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/external-allocations
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleListExternalAllocations(w http.ResponseWriter, r *http.Request) {
	if s.allocationQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	orgIDStr := chi.URLParam(r, "org_id")
	partnerOrgID, err := uuid.Parse(orgIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
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
	rows, err := s.allocationQueries.ListExternalAllocationsByOrg(ctx, partnerOrgID, statusFilter)
	if err != nil {
		s.logger.Error("external_allocation: list failed",
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"allocation.list_failed", "failed to list external allocations", r,
		))
		return
	}

	allocations := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		allocations = append(allocations, externalAllocationFromRow(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"allocations": allocations,
		"total":       len(allocations),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/external-allocations/{id}
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleGetExternalAllocation(w http.ResponseWriter, r *http.Request) {
	if s.allocationQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"allocation.invalid_id", "id must be a valid UUID", r,
		))
		return
	}

	ctx := r.Context()
	alloc, err := s.allocationQueries.GetExternalAllocationByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"allocation.not_found", "external allocation not found", r,
			))
			return
		}
		s.logger.Error("external_allocation: get failed",
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"allocation.get_failed", "failed to retrieve external allocation", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
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

func (s *Server) handlePatchExternalAllocation(w http.ResponseWriter, r *http.Request) {
	if s.allocationQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"allocation.invalid_id", "id must be a valid UUID", r,
		))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"allocation.read_body_failed", "failed to read request body", r,
		))
		return
	}

	var req patchExternalAllocationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"allocation.invalid_json", "request body is not valid JSON", r,
		))
		return
	}

	ctx := r.Context()

	// Fetch the current allocation.
	current, err := s.allocationQueries.GetExternalAllocationByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"allocation.not_found", "external allocation not found", r,
			))
			return
		}
		s.logger.Error("external_allocation: get failed",
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"allocation.get_failed", "failed to retrieve external allocation", r,
		))
		return
	}

	// Cannot modify terminal allocations.
	if isTerminalAllocationStatus(current.Status) {
		writeJSON(w, http.StatusConflict, errorEnvelope(
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
			writeJSON(w, http.StatusConflict, errorEnvelope(
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
		if s.inventoryQueries == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
				"dependency.database_unavailable", "inventory service is not available", r,
			))
			return
		}

		tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
				"dependency.database_unavailable", "failed to begin transaction", r,
			))
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		invQ := s.inventoryQueries.WithTx(tx)
		allocQ := s.allocationQueries.WithTx(tx)

		// Reserve capacity — returns pgx.ErrNoRows on over-capacity.
		if _, err := invQ.ReserveCapacity(ctx, current.SessionID, current.TierID, current.QuotaQty); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSON(w, http.StatusConflict, errorEnvelope(
					"allocation.quota_overflow",
					"insufficient platform inventory for this allocation quota",
					r,
				))
				return
			}
			s.logger.Error("external_allocation: reserve capacity failed",
				slog.String("error", err.Error()),
			)
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"allocation.capacity_failed", "failed to reserve inventory capacity", r,
			))
			return
		}

		alloc, err := allocQ.UpdateExternalAllocationStatus(ctx, id, "active")
		if err != nil {
			s.logger.Error("external_allocation: status update failed",
				slog.String("error", err.Error()),
			)
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"allocation.update_failed", "failed to update allocation status", r,
			))
			return
		}

		if err := tx.Commit(ctx); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"allocation.commit_failed", "failed to commit allocation transaction", r,
			))
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"allocation": externalAllocationFromRow(alloc),
		})
		return

	case (current.Status == "active" || current.Status == "disputed") && targetStatus == "reconciled":
		// Settle inventory: confirm consumed + release remainder.
		if s.inventoryQueries == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
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
			writeJSON(w, http.StatusBadRequest, errorEnvelope(
				"allocation.invalid_consumed",
				"quota_consumed must be between 0 and quota_qty",
				r,
			))
			return
		}

		tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
				"dependency.database_unavailable", "failed to begin transaction", r,
			))
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		invQ := s.inventoryQueries.WithTx(tx)
		allocQ := s.allocationQueries.WithTx(tx)

		// Confirm consumed capacity (held → sold).
		if consumed > 0 {
			if _, err := invQ.ConfirmCapacity(ctx, current.SessionID, current.TierID, consumed); err != nil {
				s.logger.Error("external_allocation: confirm capacity failed",
					slog.String("error", err.Error()),
				)
				writeJSON(w, http.StatusInternalServerError, errorEnvelope(
					"allocation.confirm_failed", "failed to confirm consumed capacity", r,
				))
				return
			}
		}

		// Release unused capacity back to available.
		remainder := current.QuotaQty - consumed
		if remainder > 0 {
			if _, err := invQ.ReleaseCapacity(ctx, current.SessionID, current.TierID, remainder); err != nil {
				s.logger.Error("external_allocation: release capacity failed",
					slog.String("error", err.Error()),
				)
				writeJSON(w, http.StatusInternalServerError, errorEnvelope(
					"allocation.release_failed", "failed to release unused capacity", r,
				))
				return
			}
		}

		alloc, err := allocQ.ReportAllocationConsumption(ctx, id, consumed, "reconciled")
		if err != nil {
			s.logger.Error("external_allocation: report consumption failed",
				slog.String("error", err.Error()),
			)
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"allocation.update_failed", "failed to reconcile allocation", r,
			))
			return
		}

		if err := tx.Commit(ctx); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"allocation.commit_failed", "failed to commit reconciliation transaction", r,
			))
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
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
			alloc, err = s.allocationQueries.ReportAllocationConsumption(ctx, id, consumed, targetStatus)
		} else {
			alloc, err = s.allocationQueries.UpdateExternalAllocationStatus(ctx, id, targetStatus)
		}
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, errorEnvelope(
					"allocation.not_found", "external allocation not found", r,
				))
				return
			}
			s.logger.Error("external_allocation: update failed",
				slog.String("error", err.Error()),
			)
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"allocation.update_failed", "failed to update allocation", r,
			))
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
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

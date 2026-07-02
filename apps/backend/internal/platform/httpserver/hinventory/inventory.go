// inventory.go implements the inventory ledger HTTP API endpoints (feature #130).
//
// The inventory ledger tracks real-time capacity state for each Session.
// It implements the GA-first capacity model: one ledger row per session for
// General Admission (tier_id = NULL); per-tier rows are supported at the DB
// level but deferred to a later wave.
//
// Endpoints:
//
//	GET  /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory          — list (inventory.read)
//	POST /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory          — init (inventory.reserve)
//	POST /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory/reserve  — reserve (inventory.reserve)
//	POST /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory/release  — release (inventory.release)
//	POST /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory/confirm  — confirm (inventory.confirm)
package hinventory

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response types
// ─────────────────────────────────────────────────────────────────────────────

// InventoryRowResponse is the JSON representation of a single inventory_ledger row.
// capacity_available is computed: capacity_total - capacity_held - capacity_sold.
// When capacity_total is nil (unlimited), capacity_available is also nil.
type InventoryRowResponse struct {
	ID                string  `json:"id"`
	SessionID         string  `json:"session_id"`
	TierID            *string `json:"tier_id"`
	CapacityTotal     *int32  `json:"capacity_total"`     // nil = unlimited
	CapacityHeld      int32   `json:"capacity_held"`      // reserved, not confirmed
	CapacitySold      int32   `json:"capacity_sold"`      // confirmed (sold)
	CapacityAvailable *int32  `json:"capacity_available"` // nil when total is unlimited
	UpdatedAt         string  `json:"updated_at"`
}

// InventoryRowFromLedger converts a gen.InventoryLedgerRow to an InventoryRowResponse.
func InventoryRowFromLedger(row gen.InventoryLedgerRow) InventoryRowResponse {
	resp := InventoryRowResponse{
		ID:           row.ID.String(),
		SessionID:    row.SessionID.String(),
		CapacityHeld: row.CapacityHeld,
		CapacitySold: row.CapacitySold,
		UpdatedAt:    row.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if row.TierID != nil {
		s := row.TierID.String()
		resp.TierID = &s
	}
	if row.CapacityTotal != nil {
		resp.CapacityTotal = row.CapacityTotal
		avail := *row.CapacityTotal - row.CapacityHeld - row.CapacitySold
		resp.CapacityAvailable = &avail
	}
	return resp
}

// inventoryQuantityRequest is the request body for reserve, release, and confirm endpoints.
type inventoryQuantityRequest struct {
	Quantity int32 `json:"quantity"`
}

// inventoryInitRequest is the request body for the init endpoint.
type inventoryInitRequest struct {
	CapacityTotal *int32 `json:"capacity_total"` // nil = unlimited
}

// readAndParseQuantityRequest reads and validates a quantity request body.
// Returns the parsed request and true on success; writes error response and returns false on failure.
func readAndParseQuantityRequest(w http.ResponseWriter, r *http.Request) (inventoryQuantityRequest, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("inventory.invalid_body", "cannot read request body: "+err.Error(), r))
		return inventoryQuantityRequest{}, false
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("inventory.empty_body", "request body is required", r))
		return inventoryQuantityRequest{}, false
	}
	var req inventoryQuantityRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("inventory.invalid_json", "request body is not valid JSON", r))
		return inventoryQuantityRequest{}, false
	}
	if req.Quantity <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("inventory.invalid_quantity", "quantity must be greater than 0", r))
		return inventoryQuantityRequest{}, false
	}
	return req, true
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory
// ─────────────────────────────────────────────────────────────────────────────

// HandleListInventory serves GET .../sessions/{session_id}/inventory.
// Returns all ledger rows for the session (session-level first, then tier rows).
// Requires JWT + "inventory.read" permission.
func (h *Handler) HandleListInventory(w http.ResponseWriter, r *http.Request) {
	if h.inventoryQueries == nil {
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

	rows, err := h.inventoryQueries.ListInventoryLedgersBySession(ctx, sessionID)
	if err != nil {
		h.logger.Error("inventory: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"inventory.list_failed", "failed to retrieve inventory", r,
		))
		return
	}

	result := make([]InventoryRowResponse, 0, len(rows))
	for _, row := range rows {
		result = append(result, InventoryRowFromLedger(row))
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"inventory": result,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory
// ─────────────────────────────────────────────────────────────────────────────

// HandleInitInventory serves POST .../sessions/{session_id}/inventory.
// Initializes (or returns existing) the session-level inventory ledger row.
// Requires JWT + "inventory.reserve" permission.
func (h *Handler) HandleInitInventory(w http.ResponseWriter, r *http.Request) {
	if h.inventoryQueries == nil || h.pool == nil {
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
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("inventory.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}

	var req inventoryInitRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("inventory.invalid_json", "request body is not valid JSON", r))
			return
		}
	}

	row, err := h.inventoryQueries.InsertInventoryLedger(ctx, sessionID, nil, req.CapacityTotal)
	if err != nil {
		h.logger.Error("inventory: init failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"inventory.init_failed", "failed to initialize inventory ledger", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"inventory": InventoryRowFromLedger(row),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory/reserve
// ─────────────────────────────────────────────────────────────────────────────

// HandleReserveCapacity serves POST .../inventory/reserve.
// Atomically reserves quantity capacity units for the session-level ledger row.
// Returns 409 "inventory.over_capacity" when the request would exceed capacity.
// Requires JWT + "inventory.reserve" permission.
func (h *Handler) HandleReserveCapacity(w http.ResponseWriter, r *http.Request) {
	if h.inventoryQueries == nil || h.pool == nil {
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

	req, ok := readAndParseQuantityRequest(w, r)
	if !ok {
		return
	}

	// ReserveCapacity uses SELECT FOR UPDATE in a CTE to enforce the invariant atomically.
	// Returns pgx.ErrNoRows when over-capacity or no ledger row exists.
	updated, err := h.inventoryQueries.ReserveCapacity(ctx, sessionID, nil, req.Quantity)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"inventory.over_capacity",
				"insufficient capacity available for the requested quantity",
				r,
			))
			return
		}
		h.logger.Error("inventory: reserve failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"inventory.reserve_failed", "failed to reserve capacity", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"inventory": InventoryRowFromLedger(updated),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory/release
// ─────────────────────────────────────────────────────────────────────────────

// HandleReleaseCapacity serves POST .../inventory/release.
// Releases previously reserved capacity units back to available.
// Returns 409 "inventory.insufficient_held" when held < quantity.
// Requires JWT + "inventory.release" permission.
func (h *Handler) HandleReleaseCapacity(w http.ResponseWriter, r *http.Request) {
	if h.inventoryQueries == nil || h.pool == nil {
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

	req, ok := readAndParseQuantityRequest(w, r)
	if !ok {
		return
	}

	// ReleaseCapacity uses SELECT FOR UPDATE in a CTE to enforce held >= amount atomically.
	// Returns pgx.ErrNoRows when held < amount or no ledger row exists.
	updated, err := h.inventoryQueries.ReleaseCapacity(ctx, sessionID, nil, req.Quantity)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"inventory.insufficient_held",
				"held capacity is less than the quantity to release",
				r,
			))
			return
		}
		h.logger.Error("inventory: release failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"inventory.release_failed", "failed to release capacity", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"inventory": InventoryRowFromLedger(updated),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory/confirm
// ─────────────────────────────────────────────────────────────────────────────

// HandleConfirmCapacity serves POST .../inventory/confirm.
// Moves quantity units from held to sold (purchase confirmed).
// Returns 409 "inventory.insufficient_held" when held < quantity.
// Requires JWT + "inventory.confirm" permission.
func (h *Handler) HandleConfirmCapacity(w http.ResponseWriter, r *http.Request) {
	if h.inventoryQueries == nil || h.pool == nil {
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

	req, ok := readAndParseQuantityRequest(w, r)
	if !ok {
		return
	}

	// ConfirmCapacity uses SELECT FOR UPDATE in a CTE to enforce held >= amount atomically.
	// Returns pgx.ErrNoRows when held < amount or no ledger row exists.
	updated, err := h.inventoryQueries.ConfirmCapacity(ctx, sessionID, nil, req.Quantity)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"inventory.insufficient_held",
				"held capacity is less than the quantity to confirm",
				r,
			))
			return
		}
		h.logger.Error("inventory: confirm failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"inventory.confirm_failed", "failed to confirm capacity", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"inventory": InventoryRowFromLedger(updated),
	})
}

// network_users.go implements the network-user assignment endpoints (feature #209).
package hnetworks

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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

const pgForeignKeyViolation = "23503"

// networkUserResponse is the JSON projection of a network_users row.
type networkUserResponse struct {
	ID        string `json:"id"`
	NetworkID string `json:"network_id"`
	UserID    string `json:"user_id"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func networkUserFromRow(row gen.NetworkUserRow) networkUserResponse {
	return networkUserResponse{
		ID:        row.ID.String(),
		NetworkID: row.NetworkID.String(),
		UserID:    row.UserID.String(),
		Role:      row.Role,
		Status:    row.Status,
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// WriteNetworkUserAudit emits a v1.network.users.<verb> audit_events row.
func (h *Handler) WriteNetworkUserAudit(r *http.Request, action, networkID, userID string, metadata map[string]any) {
	if h.audit == nil {
		return
	}
	actorID := ""
	actorType := "anonymous"
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		actorID = a.ID
		actorType = "user"
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["network_id"] = networkID
	metadata["target_user_id"] = userID
	metadata["target"] = userID

	ev := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    actorType,
		ActorID:      actorID,
		Action:       action,
		ResourceType: "network_user",
		ResourceID:   networkID + ":" + userID,
		RequestID:    logging.RequestID(r.Context()),
		TraceID:      logging.TraceID(r.Context()),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     metadata,
	}
	if err := h.audit.Write(r.Context(), ev); err != nil {
		h.logger.Warn("network_user: audit write failed",
			slog.String("action", action),
			slog.String("network_id", networkID),
			slog.String("user_id", userID),
			slog.Any("error", err),
		)
	}
}

// assertNetworkExists verifies the parent network is present and non-archived.
// Returns true only when the caller may proceed. On any failure it has already
// written the response.
func (h *Handler) assertNetworkExists(w http.ResponseWriter, r *http.Request, networkID uuid.UUID) bool {
	row, err := h.queries.GetOperatorNetworkByID(r.Context(), networkID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"operator_network.not_found", "operator network not found", r))
			return false
		}
		h.logger.Error("network_user: lookup parent failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"network_user.lookup_failed", "failed to look up operator network", r))
		return false
	}
	if row.ArchivedAt != nil {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"operator_network.archived",
			"operator network is archived; user roster is read-only", r))
		return false
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/admin/networks/{id}/users
// ─────────────────────────────────────────────────────────────────────────────

type assignNetworkUserRequest struct {
	UserID string `json:"user_id"`
}

func (h *Handler) HandleAssignNetworkUser(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	networkID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"network_user.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"network_user.empty_body", "request body is required", r))
		return
	}
	var req assignNetworkUserRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"network_user.invalid_json", "request body is not valid JSON", r))
		return
	}
	req.UserID = strings.TrimSpace(req.UserID)
	if req.UserID == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"network_user.invalid_user_id", "user_id is required", r,
			map[string]any{"field": "user_id"},
		))
		return
	}
	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"network_user.invalid_user_id", "user_id must be a valid UUID", r,
			map[string]any{"field": "user_id"},
		))
		return
	}

	if !h.assertNetworkExists(w, r, networkID) {
		return
	}

	row, err := h.queries.InsertNetworkUser(r.Context(), networkID, userID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			existing, sErr := h.queries.SetNetworkUserStatus(
				r.Context(), networkID, userID, "active")
			if sErr != nil {
				if errors.Is(sErr, pgx.ErrNoRows) {
					httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
						"network_user.duplicate",
						"user is already assigned to this network", r))
					return
				}
				h.logger.Error("network_user: re-activate failed",
					slog.String("error", sErr.Error()))
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
					"network_user.assign_failed",
					"failed to re-activate network user assignment", r))
				return
			}
			h.WriteNetworkUserAudit(r, "v1.network.users.reactivate",
				networkID.String(), userID.String(),
				map[string]any{"status": existing.Status, "reason": reason})
			httputil.WriteJSON(w, http.StatusOK, map[string]any{
				"network_user": networkUserFromRow(existing),
				"reactivated":  true,
			})
			return
		}
		if errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"network_user.user_not_found",
				"target user does not exist", r))
			return
		}
		h.logger.Error("network_user: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"network_user.assign_failed",
			"failed to assign user to network", r))
		return
	}

	h.WriteNetworkUserAudit(r, "v1.network.users.assign",
		networkID.String(), userID.String(),
		map[string]any{"role": row.Role, "status": row.Status, "reason": reason})

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"network_user": networkUserFromRow(row),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/admin/networks/{id}/users/{userId}
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleRemoveNetworkUser(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	networkID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	userID, ok := httputil.UUIDPathParam(w, r, "userId")
	if !ok {
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}

	if !h.assertNetworkExists(w, r, networkID) {
		return
	}

	row, err := h.queries.SetNetworkUserStatus(
		r.Context(), networkID, userID, "revoked")
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"network_user.not_found",
				"user is not assigned to this network", r))
			return
		}
		h.logger.Error("network_user: revoke failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"network_user.remove_failed",
			"failed to remove user from network", r))
		return
	}

	h.WriteNetworkUserAudit(r, "v1.network.users.remove",
		networkID.String(), userID.String(),
		map[string]any{"status": row.Status, "reason": reason})

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"network_user": networkUserFromRow(row),
		"removed":      true,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/admin/networks/{id}/users
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleListNetworkUsers(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	networkID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	if !h.assertNetworkExists(w, r, networkID) {
		return
	}

	rows, err := h.queries.ListNetworkUsersByNetwork(r.Context(), networkID)
	if err != nil {
		h.logger.Error("network_user: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"network_user.list_failed",
			"failed to list network users", r))
		return
	}
	out := make([]networkUserResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, networkUserFromRow(row))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"network_id":    networkID.String(),
		"network_users": out,
		"total":         len(out),
	})
}

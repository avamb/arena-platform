// network_users.go implements the network-user assignment endpoints
// (feature #209). These let a platform_superadmin (the only role bound to the
// `network.manage_users` permission per 0044_network_permissions.sql) attach,
// list, and detach `network_operator` users on an operator_network.
//
// Endpoints:
//
//	POST   /v1/admin/networks/{id}/users               — assign user (network.manage_users)
//	DELETE /v1/admin/networks/{id}/users/{userId}      — remove user (network.manage_users)
//	GET    /v1/admin/networks/{id}/users               — list active users (network.manage_users)
//
// Implementation notes:
//
//   - Backed by the operator_networks / network_users sqlc helpers landed in
//     feature #205: InsertNetworkUser, SetNetworkUserStatus,
//     ListNetworkUsersByNetwork.
//   - DELETE is implemented as a soft-revoke
//     (SetNetworkUserStatus(..., "revoked")) rather than DeleteNetworkUser so
//     the lifecycle remains auditable.
//   - All three handlers verify the parent operator_network exists (and is
//     not archived) before touching network_users, so the API returns a typed
//     404 instead of leaking a 23503 foreign-key violation on assignment.
//   - Each mutation writes a v1.network.users.* audit_events row carrying the
//     network id, user id, and resulting status. Fire-and-forget like the
//     other handlers in this package.
package httpserver

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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

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

// writeNetworkUserAudit emits a v1.network.users.<verb> audit_events row.
func (s *Server) writeNetworkUserAudit(r *http.Request, action, networkID, userID string, metadata map[string]any) {
	if s.audit == nil {
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

	ev := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    actorType,
		ActorID:      actorID,
		Action:       action,
		ResourceType: "network_user",
		ResourceID:   networkID + ":" + userID,
		RequestID:    logging.RequestID(r.Context()),
		TraceID:      logging.TraceID(r.Context()),
		IP:           extractClientIP(r),
		Metadata:     metadata,
	}
	if err := s.audit.Write(r.Context(), ev); err != nil {
		s.logger.Warn("network_user: audit write failed",
			slog.String("action", action),
			slog.String("network_id", networkID),
			slog.String("user_id", userID),
			slog.Any("error", err),
		)
	}
}

// assertNetworkExists verifies the parent network is present and non-archived.
// Returns (true, _) only when the caller may proceed. On any failure it has
// already written the response.
func (s *Server) assertNetworkExists(w http.ResponseWriter, r *http.Request, networkID uuid.UUID) bool {
	row, err := s.networkQueries.GetOperatorNetworkByID(r.Context(), networkID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"operator_network.not_found", "operator network not found", r))
			return false
		}
		s.logger.Error("network_user: lookup parent failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"network_user.lookup_failed", "failed to look up operator network", r))
		return false
	}
	if row.ArchivedAt != nil {
		writeJSON(w, http.StatusConflict, errorEnvelope(
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

func (s *Server) handleAssignNetworkUser(w http.ResponseWriter, r *http.Request) {
	if s.networkQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	networkID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"network_user.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"network_user.empty_body", "request body is required", r))
		return
	}
	var req assignNetworkUserRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"network_user.invalid_json", "request body is not valid JSON", r))
		return
	}
	req.UserID = strings.TrimSpace(req.UserID)
	if req.UserID == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"network_user.invalid_user_id", "user_id is required", r,
			map[string]any{"field": "user_id"},
		))
		return
	}
	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"network_user.invalid_user_id", "user_id must be a valid UUID", r,
			map[string]any{"field": "user_id"},
		))
		return
	}

	if !s.assertNetworkExists(w, r, networkID) {
		return
	}

	// Re-activation path: if a row already exists for (network, user) the
	// 23505 unique-violation surfaces; re-bring it to 'active' so the API is
	// idempotent and the operator does not have to delete-then-readd to
	// restore a revoked user.
	row, err := s.networkQueries.InsertNetworkUser(r.Context(), networkID, userID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			existing, sErr := s.networkQueries.SetNetworkUserStatus(
				r.Context(), networkID, userID, "active")
			if sErr != nil {
				if errors.Is(sErr, pgx.ErrNoRows) {
					writeJSON(w, http.StatusConflict, errorEnvelope(
						"network_user.duplicate",
						"user is already assigned to this network", r))
					return
				}
				s.logger.Error("network_user: re-activate failed",
					slog.String("error", sErr.Error()))
				writeJSON(w, http.StatusInternalServerError, errorEnvelope(
					"network_user.assign_failed",
					"failed to re-activate network user assignment", r))
				return
			}
			s.writeNetworkUserAudit(r, "v1.network.users.reactivate",
				networkID.String(), userID.String(),
				map[string]any{"status": existing.Status})
			writeJSON(w, http.StatusOK, map[string]any{
				"network_user": networkUserFromRow(existing),
				"reactivated":  true,
			})
			return
		}
		if errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"network_user.user_not_found",
				"target user does not exist", r))
			return
		}
		s.logger.Error("network_user: insert failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"network_user.assign_failed",
			"failed to assign user to network", r))
		return
	}

	s.writeNetworkUserAudit(r, "v1.network.users.assign",
		networkID.String(), userID.String(),
		map[string]any{"role": row.Role, "status": row.Status})

	writeJSON(w, http.StatusCreated, map[string]any{
		"network_user": networkUserFromRow(row),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/admin/networks/{id}/users/{userId}
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleRemoveNetworkUser(w http.ResponseWriter, r *http.Request) {
	if s.networkQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	networkID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}
	userID, ok := uuidPathParam(w, r, "userId")
	if !ok {
		return
	}

	if !s.assertNetworkExists(w, r, networkID) {
		return
	}

	row, err := s.networkQueries.SetNetworkUserStatus(
		r.Context(), networkID, userID, "revoked")
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"network_user.not_found",
				"user is not assigned to this network", r))
			return
		}
		s.logger.Error("network_user: revoke failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"network_user.remove_failed",
			"failed to remove user from network", r))
		return
	}

	s.writeNetworkUserAudit(r, "v1.network.users.remove",
		networkID.String(), userID.String(),
		map[string]any{"status": row.Status})

	writeJSON(w, http.StatusOK, map[string]any{
		"network_user": networkUserFromRow(row),
		"removed":      true,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/admin/networks/{id}/users
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleListNetworkUsers(w http.ResponseWriter, r *http.Request) {
	if s.networkQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	networkID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	if !s.assertNetworkExists(w, r, networkID) {
		return
	}

	rows, err := s.networkQueries.ListNetworkUsersByNetwork(r.Context(), networkID)
	if err != nil {
		s.logger.Error("network_user: list failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"network_user.list_failed",
			"failed to list network users", r))
		return
	}
	out := make([]networkUserResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, networkUserFromRow(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"network_id":    networkID.String(),
		"network_users": out,
		"total":         len(out),
	})
}

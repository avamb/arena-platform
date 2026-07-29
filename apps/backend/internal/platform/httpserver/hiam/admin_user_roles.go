package hiam

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// HandleAdminGrantGlobalRole grants one platform-wide role to an existing user.
// The role is deliberately stored only in user_roles with a NULL org_id; tenant
// roles must use the organization membership endpoints instead.
func (h *Handler) HandleAdminGrantGlobalRole(w http.ResponseWriter, r *http.Request) {
	if h.pool == nil {
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	reason, ok := requireAdminReason(w, r)
	if !ok {
		return
	}
	userID, ok := httputil.UUIDPathParam(w, r, "user_id")
	if !ok {
		return
	}
	var req struct {
		Role string `json:"role"`
	}
	if !decodeAdminGlobalRoleRequest(w, r, &req) {
		return
	}
	role := strings.TrimSpace(req.Role)
	if !globalAdminUserRoles[role] {
		h.writeAdminGlobalRoleError(w, r, http.StatusBadRequest, "admin_user.invalid_global_role", "role must be a global platform role")
		return
	}

	tx, err := h.pool.BeginTx(r.Context(), pgx.TxOptions{})
	if err != nil {
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	var assigned string
	err = tx.QueryRow(r.Context(), `INSERT INTO user_roles (user_id, role_id, org_id)
		SELECT $1, id, NULL FROM roles WHERE name = $2 AND org_id IS NULL
		RETURNING $2`, userID, role).Scan(&assigned)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.writeAdminGlobalRoleError(w, r, http.StatusUnprocessableEntity, "admin_user.invalid_user_or_role", "user or global role does not exist")
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			h.writeAdminGlobalRoleError(w, r, http.StatusConflict, "admin_user.global_role_exists", "user already holds this global role")
			return
		}
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	if !h.writeAdminGlobalRoleAudit(r, tx, "v1.admin.user.global_role.grant", userID, assigned, reason) {
		h.writeAdminGlobalRoleError(w, r, http.StatusInternalServerError, "admin_user.audit_failed", "failed to write audit event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"user_id": userID.String(), "role": assigned, "scope": "global"})
}

// HandleAdminRevokeGlobalRole removes a platform-wide role. Removing the final
// active platform_superadmin is refused so an operator cannot lock out every
// administrative account (including through a URL edited in the browser).
func (h *Handler) HandleAdminRevokeGlobalRole(w http.ResponseWriter, r *http.Request) {
	if h.pool == nil {
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	reason, ok := requireAdminReason(w, r)
	if !ok {
		return
	}
	userID, ok := httputil.UUIDPathParam(w, r, "user_id")
	if !ok {
		return
	}
	role := strings.TrimSpace(chi.URLParam(r, "role"))
	if !globalAdminUserRoles[role] {
		h.writeAdminGlobalRoleError(w, r, http.StatusBadRequest, "admin_user.invalid_global_role", "role must be a global platform role")
		return
	}
	tx, err := h.pool.BeginTx(r.Context(), pgx.TxOptions{})
	if err != nil {
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	if role == "platform_superadmin" {
		// Serialise every superadmin removal. A plain count followed by delete
		// would let two concurrent requests both observe two admins and remove
		// them both. The transaction-scoped advisory lock is released on commit
		// or rollback and covers all mutations through this endpoint.
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext('arena:global-platform-superadmin'))`); err != nil {
			h.adminRoleDependencyUnavailable(w, r)
			return
		}
		var count int
		err = tx.QueryRow(r.Context(), `SELECT count(*) FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE r.name = 'platform_superadmin' AND ur.org_id IS NULL`).Scan(&count)
		if err != nil {
			h.adminRoleDependencyUnavailable(w, r)
			return
		}
		if count <= 1 {
			h.writeAdminGlobalRoleError(w, r, http.StatusConflict, "admin_user.last_superadmin", "cannot remove the last active platform superadmin")
			return
		}
	}
	var removed string
	err = tx.QueryRow(r.Context(), `DELETE FROM user_roles ur USING roles r WHERE ur.role_id = r.id AND ur.user_id = $1 AND r.name = $2 AND ur.org_id IS NULL RETURNING $2`, userID, role).Scan(&removed)
	if errors.Is(err, pgx.ErrNoRows) {
		h.writeAdminGlobalRoleError(w, r, http.StatusNotFound, "admin_user.global_role_not_found", "user does not hold this global role")
		return
	}
	if err != nil {
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	if !h.writeAdminGlobalRoleAudit(r, tx, "v1.admin.user.global_role.revoke", userID, removed, reason) {
		h.writeAdminGlobalRoleError(w, r, http.StatusInternalServerError, "admin_user.audit_failed", "failed to write audit event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"user_id": userID.String(), "role": removed, "revoked": true})
}

func decodeAdminGlobalRoleRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil || len(body) == 0 {
		h := "request body is required"
		if err != nil {
			h = "cannot read request body"
		}
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("admin_user.invalid_body", h, r))
		return false
	}
	if err := json.Unmarshal(body, target); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("admin_user.invalid_json", "request body is not valid JSON", r))
		return false
	}
	return true
}
func (h *Handler) adminRoleDependencyUnavailable(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
}
func (h *Handler) writeAdminGlobalRoleError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	httputil.WriteJSON(w, status, httputil.ErrorEnvelope(code, message, r))
}
func (h *Handler) writeAdminGlobalRoleAudit(r *http.Request, tx pgx.Tx, action string, userID uuid.UUID, role, reason string) bool {
	if h.audit == nil {
		return true
	}
	actor, _ := auth.ActorFromContext(r.Context())
	err := h.audit.WriteTx(r.Context(), tx, audit.Event{OccurredAt: time.Now().UTC(), ActorType: "user", ActorID: actor.ID, Action: action, ResourceType: "user_role", ResourceID: userID.String(), RequestID: logging.RequestID(r.Context()), TraceID: logging.TraceID(r.Context()), IP: httputil.ExtractClientIP(r), Metadata: map[string]any{"reason": reason, "user_id": userID.String(), "role": role, "scope": "global"}})
	return err == nil
}

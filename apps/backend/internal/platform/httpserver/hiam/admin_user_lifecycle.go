package hiam

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// HandleAdminDeactivateUser soft-deactivates an account, preserving its
// memberships and evidence while atomically revoking every refresh session.
func (h *Handler) HandleAdminDeactivateUser(w http.ResponseWriter, r *http.Request) {
	h.changeUserLifecycle(w, r, true)
}

// HandleAdminReactivateUser restores a soft-deactivated account. It never
// recreates sessions; the user must sign in again.
func (h *Handler) HandleAdminReactivateUser(w http.ResponseWriter, r *http.Request) {
	h.changeUserLifecycle(w, r, false)
}

func (h *Handler) changeUserLifecycle(w http.ResponseWriter, r *http.Request, deactivate bool) {
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
	tx, err := h.pool.BeginTx(r.Context(), pgx.TxOptions{})
	if err != nil {
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	if deactivate {
		if !h.allowSuperadminDeactivation(w, r, tx, userID) {
			return
		}
		var changed uuid.UUID
		err = tx.QueryRow(r.Context(), `UPDATE users SET deactivated_at = now() WHERE id = $1 AND deactivated_at IS NULL RETURNING id`, userID).Scan(&changed)
		if errors.Is(err, pgx.ErrNoRows) {
			h.writeAdminGlobalRoleError(w, r, http.StatusConflict, "admin_user.already_deactivated", "user is already deactivated")
			return
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
		}
	} else {
		var changed uuid.UUID
		err = tx.QueryRow(r.Context(), `UPDATE users SET deactivated_at = NULL WHERE id = $1 AND deactivated_at IS NOT NULL RETURNING id`, userID).Scan(&changed)
		if errors.Is(err, pgx.ErrNoRows) {
			h.writeAdminGlobalRoleError(w, r, http.StatusConflict, "admin_user.not_deactivated", "user is not deactivated")
			return
		}
	}
	if err != nil {
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	action := "v1.admin.user.reactivate"
	if deactivate {
		action = "v1.admin.user.deactivate"
	}
	if !h.writeAdminUserLifecycleAudit(r, tx, action, userID, reason) {
		h.writeAdminGlobalRoleError(w, r, http.StatusInternalServerError, "admin_user.audit_failed", "failed to write audit event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	if deactivate {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"user_id": userID.String(), "deactivated": true})
	} else {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"user_id": userID.String(), "reactivated": true})
	}
}

// HandleAdminDeleteUser permanently removes only a never-active identity with
// no business or audit evidence. Accounts with retained history are safely
// deactivated instead, avoiding a destructive and surprising admin action.
func (h *Handler) HandleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
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
	tx, err := h.pool.BeginTx(r.Context(), pgx.TxOptions{})
	if err != nil {
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	if !h.allowSuperadminDeactivation(w, r, tx, userID) {
		return
	}
	var canDelete bool
	err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM users u WHERE u.id=$1 AND u.email_verified_at IS NULL AND NOT EXISTS (SELECT 1 FROM refresh_tokens rt WHERE rt.user_id=u.id) AND NOT EXISTS (SELECT 1 FROM audit_events ae WHERE ae.actor_id=u.id OR (ae.resource_type='user' AND ae.resource_id=u.id::text)) AND NOT EXISTS (SELECT 1 FROM reservations r WHERE r.user_id=u.id) AND NOT EXISTS (SELECT 1 FROM checkout_sessions c WHERE c.user_id=u.id))`, userID).Scan(&canDelete)
	if err != nil {
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	if canDelete {
		var deleted uuid.UUID
		err = tx.QueryRow(r.Context(), `DELETE FROM users WHERE id=$1 RETURNING id`, userID).Scan(&deleted)
		if errors.Is(err, pgx.ErrNoRows) {
			h.writeAdminGlobalRoleError(w, r, http.StatusNotFound, "admin_user.not_found", "user does not exist")
			return
		}
		if err != nil {
			h.adminRoleDependencyUnavailable(w, r)
			return
		}
		if !h.writeAdminUserLifecycleAudit(r, tx, "v1.admin.user.delete", userID, reason) {
			h.writeAdminGlobalRoleError(w, r, http.StatusInternalServerError, "admin_user.audit_failed", "failed to write audit event")
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			h.adminRoleDependencyUnavailable(w, r)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"user_id": userID.String(), "deleted": true})
		return
	}
	var changed uuid.UUID
	err = tx.QueryRow(r.Context(), `UPDATE users SET deactivated_at=COALESCE(deactivated_at, now()) WHERE id=$1 RETURNING id`, userID).Scan(&changed)
	if errors.Is(err, pgx.ErrNoRows) {
		h.writeAdminGlobalRoleError(w, r, http.StatusNotFound, "admin_user.not_found", "user does not exist")
		return
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE refresh_tokens SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, userID)
	}
	if err != nil {
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	if !h.writeAdminUserLifecycleAudit(r, tx, "v1.admin.user.deactivate", userID, reason) {
		h.writeAdminGlobalRoleError(w, r, http.StatusInternalServerError, "admin_user.audit_failed", "failed to write audit event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"user_id": userID.String(), "deleted": false, "deactivated": true, "message": "user has retained activity and was deactivated instead"})
}

func (h *Handler) allowSuperadminDeactivation(w http.ResponseWriter, r *http.Request, tx pgx.Tx, userID uuid.UUID) bool {
	var isSuperadmin bool
	if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM user_roles ur JOIN roles ro ON ro.id=ur.role_id WHERE ur.user_id=$1 AND ur.org_id IS NULL AND ro.name='platform_superadmin')`, userID).Scan(&isSuperadmin); err != nil {
		h.adminRoleDependencyUnavailable(w, r)
		return false
	}
	if !isSuperadmin {
		return true
	}
	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext('arena:global-platform-superadmin'))`); err != nil {
		h.adminRoleDependencyUnavailable(w, r)
		return false
	}
	var count int
	if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM user_roles ur JOIN roles ro ON ro.id=ur.role_id JOIN users u ON u.id=ur.user_id WHERE ur.org_id IS NULL AND ro.name='platform_superadmin' AND u.deactivated_at IS NULL`).Scan(&count); err != nil {
		h.adminRoleDependencyUnavailable(w, r)
		return false
	}
	if count <= 1 {
		h.writeAdminGlobalRoleError(w, r, http.StatusConflict, "admin_user.last_superadmin", "cannot deactivate the last active platform superadmin")
		return false
	}
	return true
}

func (h *Handler) writeAdminUserLifecycleAudit(r *http.Request, tx pgx.Tx, action string, userID uuid.UUID, reason string) bool {
	if h.audit == nil {
		return true
	}
	actor, _ := auth.ActorFromContext(r.Context())
	err := h.audit.WriteTx(r.Context(), tx, audit.Event{OccurredAt: time.Now().UTC(), ActorType: "user", ActorID: actor.ID, Action: action, ResourceType: "user", ResourceID: userID.String(), RequestID: logging.RequestID(r.Context()), TraceID: logging.TraceID(r.Context()), IP: httputil.ExtractClientIP(r), Metadata: map[string]any{"reason": reason, "user_id": userID.String()}})
	return err == nil
}

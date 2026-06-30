// admin_memberships.go implements admin-namespace organization-memberships
// endpoints (feature #234).
//
// These are the admin-console-facing counterparts to
// /v1/organizations/{org_id}/members (feature #120). They expose the same
// list / add surface plus a PATCH path that switches a member's role and a
// DELETE path that soft-deactivates (status='revoked') a membership row
// without physically deleting it.
//
//	GET    /v1/admin/organizations/{org_id}/members
//	POST   /v1/admin/organizations/{org_id}/members
//	PATCH  /v1/admin/organizations/{org_id}/members/{membership_id}
//	DELETE /v1/admin/organizations/{org_id}/members/{membership_id}
//
// All four routes are gated by:
//   - JWT auth (Server.applyAuth middleware)
//   - RBAC permission (membership.read | membership.grant | membership.revoke)
//   - X-Admin-Reason header (the same audit-reason gate the rest of the
//     /v1/admin namespace already enforces — see admin_orgs.go and
//     apps/admin-web/src/lib/api/reason.ts)
//
// Every successful write records an audit event under
// "v1.admin.membership.{create,update,deactivate}" with resource_type
// "membership", resource_id = membership.id, and the X-Admin-Reason string
// carried in metadata.
//
// The POST handler accepts either user_id (UUID) or email (case-insensitive).
// When email is supplied, the user is resolved via GetUserByEmail before the
// membership is inserted. Both fields are mutually exclusive — supplying both
// is a 400.
//
// Roles are validated against validMembershipRoles (memberships.go), which
// mirrors the memberships_role_check CHECK constraint as extended by
// migration 0042_network_operator_role.sql.
package hiam

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

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// adminAddMemberRequest is the request body for
// POST /v1/admin/organizations/{org_id}/members.
//
// Exactly one of UserID / Email must be supplied. When Email is supplied, the
// user is resolved via GetUserByEmail; a missing user yields 422
// "admin_membership.user_not_found".
type adminAddMemberRequest struct {
	UserID string `json:"user_id,omitempty"`
	Email  string `json:"email,omitempty"`
	Role   string `json:"role"`
}

// adminChangeMemberRoleRequest is the request body for
// PATCH /v1/admin/organizations/{org_id}/members/{membership_id}.
type adminChangeMemberRoleRequest struct {
	Role string `json:"role"`
}

// HandleAdminListMembers serves GET /v1/admin/organizations/{org_id}/members.
// Requires JWT + membership.read + X-Admin-Reason.
// handleAdminListMembers is the legacy name for this operation.
//
// The handler reuses ListMembershipsByOrg (feature #120) so the admin
// console sees the same authoritative active-membership view that the
// non-admin org-members listing exposes.
func (h *Handler) HandleAdminListMembers(w http.ResponseWriter, r *http.Request) {
	if h.membershipQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	if _, ok := requireAdminReason(w, r); !ok {
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}

	rows, err := h.membershipQueries.ListMembershipsByOrg(ctx, orgID)
	if err != nil {
		h.logger.Error("admin_membership: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"admin_membership.list_failed", "failed to list memberships", r,
		))
		return
	}

	result := make([]membershipResponse, 0, len(rows))
	for _, m := range rows {
		result = append(result, membershipResponse{
			ID:       m.ID.String(),
			UserID:   m.UserID.String(),
			OrgID:    m.OrgID.String(),
			Role:     m.Role,
			Status:   m.Status,
			JoinedAt: m.JoinedAt.UTC().Format(time.RFC3339),
		})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"memberships": result})
}

// HandleAdminAddMember serves POST /v1/admin/organizations/{org_id}/members.
// Requires JWT + membership.grant + X-Admin-Reason.
// handleAdminAddMember is the legacy name for this operation.
func (h *Handler) HandleAdminAddMember(w http.ResponseWriter, r *http.Request) {
	if h.membershipQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	reason, ok := requireAdminReason(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"admin_membership.invalid_body",
			"cannot read request body: "+err.Error(), r,
		))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"admin_membership.empty_body", "request body is required", r,
		))
		return
	}

	var req adminAddMemberRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"admin_membership.invalid_json",
			"request body is not valid JSON", r,
		))
		return
	}

	req.UserID = strings.TrimSpace(req.UserID)
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.Role = strings.TrimSpace(req.Role)

	if req.UserID == "" && req.Email == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"admin_membership.missing_user",
			"either user_id or email is required", r,
			map[string]any{"fields": []string{"user_id", "email"}},
		))
		return
	}
	if req.UserID != "" && req.Email != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"admin_membership.ambiguous_user",
			"user_id and email are mutually exclusive — supply exactly one", r,
			map[string]any{"fields": []string{"user_id", "email"}},
		))
		return
	}
	if req.Role == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"admin_membership.invalid_role", "role is required", r,
			map[string]any{"field": "role", "allowed": MembershipRoleList()},
		))
		return
	}
	if !validMembershipRoles[req.Role] {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"admin_membership.invalid_role",
			"role must be one of: organizer, agent, platform_operator, external_ticketing_operator, platform_superadmin, network_operator",
			r,
			map[string]any{"field": "role", "allowed": MembershipRoleList()},
		))
		return
	}

	var userID uuid.UUID
	if req.UserID != "" {
		parsed, err := uuid.Parse(req.UserID)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"admin_membership.invalid_user_id",
				"user_id must be a valid UUID", r,
				map[string]any{"field": "user_id"},
			))
			return
		}
		userID = parsed
	} else {
		row, err := h.membershipQueries.GetUserByEmail(ctx, req.Email)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
					"admin_membership.user_not_found",
					"no user exists with that email", r,
					map[string]any{"field": "email"},
				))
				return
			}
			h.logger.Error("admin_membership: GetUserByEmail failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"admin_membership.user_lookup_failed",
				"failed to resolve user by email", r,
			))
			return
		}
		userID = row.ID
	}

	m, err := h.membershipQueries.InsertMembership(ctx, userID, orgID, req.Role)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case pgUniqueViolation:
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
					"admin_membership.duplicate",
					"user already holds that role in this organization", r,
				))
				return
			case pgForeignKeyViolation:
				httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
					"admin_membership.invalid_reference",
					"user_id or org_id does not exist", r,
				))
				return
			}
		}
		h.logger.Error("admin_membership: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"admin_membership.insert_failed", "failed to add member", r,
		))
		return
	}

	h.writeAdminMembershipAudit(r, "v1.admin.membership.create", m.ID.String(), reason, map[string]any{
		"org_id":  m.OrgID.String(),
		"user_id": m.UserID.String(),
		"role":    m.Role,
	})

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"membership": membershipResponse{
			ID:       m.ID.String(),
			UserID:   m.UserID.String(),
			OrgID:    m.OrgID.String(),
			Role:     m.Role,
			Status:   m.Status,
			JoinedAt: m.JoinedAt.UTC().Format(time.RFC3339),
		},
	})
}

// HandleAdminChangeMemberRole serves
// PATCH /v1/admin/organizations/{org_id}/members/{membership_id}.
// Requires JWT + membership.grant + X-Admin-Reason.
// handleAdminChangeMemberRole is the legacy name for this operation.
func (h *Handler) HandleAdminChangeMemberRole(w http.ResponseWriter, r *http.Request) {
	if h.membershipQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	reason, ok := requireAdminReason(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	membershipID, ok := httputil.UUIDPathParam(w, r, "membership_id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"admin_membership.invalid_body",
			"cannot read request body: "+err.Error(), r,
		))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"admin_membership.empty_body", "request body is required", r,
		))
		return
	}

	var req adminChangeMemberRoleRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"admin_membership.invalid_json",
			"request body is not valid JSON", r,
		))
		return
	}
	req.Role = strings.TrimSpace(req.Role)
	if req.Role == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"admin_membership.invalid_role", "role is required", r,
			map[string]any{"field": "role", "allowed": MembershipRoleList()},
		))
		return
	}
	if !validMembershipRoles[req.Role] {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"admin_membership.invalid_role",
			"role must be one of: organizer, agent, platform_operator, external_ticketing_operator, platform_superadmin, network_operator",
			r,
			map[string]any{"field": "role", "allowed": MembershipRoleList()},
		))
		return
	}

	updated, err := h.membershipQueries.ChangeMembershipRole(ctx, membershipID, orgID, req.Role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"admin_membership.not_found",
				"no active membership matches this id within the organization", r,
			))
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"admin_membership.duplicate",
				"user already holds the requested role in this organization", r,
			))
			return
		}
		h.logger.Error("admin_membership: change role failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"admin_membership.update_failed", "failed to change member role", r,
		))
		return
	}

	h.writeAdminMembershipAudit(r, "v1.admin.membership.update", updated.ID.String(), reason, map[string]any{
		"org_id":   updated.OrgID.String(),
		"user_id":  updated.UserID.String(),
		"new_role": updated.Role,
	})

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"membership": membershipResponse{
			ID:       updated.ID.String(),
			UserID:   updated.UserID.String(),
			OrgID:    updated.OrgID.String(),
			Role:     updated.Role,
			Status:   updated.Status,
			JoinedAt: updated.JoinedAt.UTC().Format(time.RFC3339),
		},
	})
}

// HandleAdminDeactivateMember serves
// DELETE /v1/admin/organizations/{org_id}/members/{membership_id}.
// Soft-removes the membership (status='revoked'). Requires JWT +
// membership.revoke + X-Admin-Reason.
// handleAdminDeactivateMember is the legacy name for this operation.
func (h *Handler) HandleAdminDeactivateMember(w http.ResponseWriter, r *http.Request) {
	if h.membershipQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	reason, ok := requireAdminReason(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	membershipID, ok := httputil.UUIDPathParam(w, r, "membership_id")
	if !ok {
		return
	}

	deactivated, err := h.membershipQueries.DeactivateMembership(ctx, membershipID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"admin_membership.not_found",
				"no active membership matches this id within the organization", r,
			))
			return
		}
		h.logger.Error("admin_membership: deactivate failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"admin_membership.delete_failed", "failed to deactivate member", r,
		))
		return
	}

	h.writeAdminMembershipAudit(r, "v1.admin.membership.deactivate", deactivated.ID.String(), reason, map[string]any{
		"org_id":  deactivated.OrgID.String(),
		"user_id": deactivated.UserID.String(),
		"role":    deactivated.Role,
	})

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"membership": membershipResponse{
			ID:       deactivated.ID.String(),
			UserID:   deactivated.UserID.String(),
			OrgID:    deactivated.OrgID.String(),
			Role:     deactivated.Role,
			Status:   deactivated.Status,
			JoinedAt: deactivated.JoinedAt.UTC().Format(time.RFC3339),
		},
		"deactivated": true,
	})
}

// writeAdminMembershipAudit emits a single audit event for an admin-membership
// write. The current revisions of the spec do not require atomic in-tx audit
// for membership writes; this fire-and-forget helper logs failures but does
// not abort the response. If a future revision requires atomicity, switch to
// the WriteTx pattern used by HandleAdminArchiveOrg.
func (h *Handler) writeAdminMembershipAudit(r *http.Request, action, resourceID, reason string, extra map[string]any) {
	if h.audit == nil {
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	metadata := map[string]any{"reason": reason}
	for k, v := range extra {
		metadata[k] = v
	}
	ev := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    "user",
		ActorID:      actor.ID,
		Action:       action,
		ResourceType: "membership",
		ResourceID:   resourceID,
		RequestID:    logging.RequestID(r.Context()),
		TraceID:      logging.TraceID(r.Context()),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     metadata,
	}
	if err := h.audit.Write(r.Context(), ev); err != nil {
		h.logger.Warn("admin_membership: audit write failed",
			slog.String("action", action),
			slog.Any("error", err),
		)
	}
}

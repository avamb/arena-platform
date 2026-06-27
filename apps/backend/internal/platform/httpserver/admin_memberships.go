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

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/admin/organizations/{org_id}/members
// ─────────────────────────────────────────────────────────────────────────────

// handleAdminListMembers serves GET /v1/admin/organizations/{org_id}/members.
// Requires JWT + membership.read + X-Admin-Reason.
//
// The handler reuses ListMembershipsByOrg (feature #120) so the admin
// console sees the same authoritative active-membership view that the
// non-admin org-members listing exposes.
func (s *Server) handleAdminListMembers(w http.ResponseWriter, r *http.Request) {
	if s.membershipQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	if _, ok := requireAdminReason(w, r); !ok {
		return
	}
	ctx := r.Context()

	orgID, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}

	rows, err := s.membershipQueries.ListMembershipsByOrg(ctx, orgID)
	if err != nil {
		s.logger.Error("admin_membership: list failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
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
	writeJSON(w, http.StatusOK, map[string]any{"memberships": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/admin/organizations/{org_id}/members
// ─────────────────────────────────────────────────────────────────────────────

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

// handleAdminAddMember serves POST /v1/admin/organizations/{org_id}/members.
// Requires JWT + membership.grant + X-Admin-Reason.
func (s *Server) handleAdminAddMember(w http.ResponseWriter, r *http.Request) {
	if s.membershipQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	reason, ok := requireAdminReason(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	orgID, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"admin_membership.invalid_body",
			"cannot read request body: "+err.Error(), r,
		))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"admin_membership.empty_body", "request body is required", r,
		))
		return
	}

	var req adminAddMemberRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"admin_membership.invalid_json",
			"request body is not valid JSON", r,
		))
		return
	}

	req.UserID = strings.TrimSpace(req.UserID)
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.Role = strings.TrimSpace(req.Role)

	if req.UserID == "" && req.Email == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"admin_membership.missing_user",
			"either user_id or email is required", r,
			map[string]any{"fields": []string{"user_id", "email"}},
		))
		return
	}
	if req.UserID != "" && req.Email != "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"admin_membership.ambiguous_user",
			"user_id and email are mutually exclusive — supply exactly one", r,
			map[string]any{"fields": []string{"user_id", "email"}},
		))
		return
	}
	if req.Role == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"admin_membership.invalid_role", "role is required", r,
			map[string]any{"field": "role", "allowed": membershipRoleList()},
		))
		return
	}
	if !validMembershipRoles[req.Role] {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"admin_membership.invalid_role",
			"role must be one of: organizer, agent, platform_operator, external_ticketing_operator, platform_superadmin, network_operator",
			r,
			map[string]any{"field": "role", "allowed": membershipRoleList()},
		))
		return
	}

	// Resolve user_id either directly or by email lookup.
	var userID uuid.UUID
	if req.UserID != "" {
		parsed, err := uuid.Parse(req.UserID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
				"admin_membership.invalid_user_id",
				"user_id must be a valid UUID", r,
				map[string]any{"field": "user_id"},
			))
			return
		}
		userID = parsed
	} else {
		row, err := s.membershipQueries.GetUserByEmail(ctx, req.Email)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSON(w, http.StatusUnprocessableEntity, errorEnvelopeWithDetails(
					"admin_membership.user_not_found",
					"no user exists with that email", r,
					map[string]any{"field": "email"},
				))
				return
			}
			s.logger.Error("admin_membership: GetUserByEmail failed", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"admin_membership.user_lookup_failed",
				"failed to resolve user by email", r,
			))
			return
		}
		userID = row.ID
	}

	m, err := s.membershipQueries.InsertMembership(ctx, userID, orgID, req.Role)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case pgUniqueViolation:
				writeJSON(w, http.StatusConflict, errorEnvelope(
					"admin_membership.duplicate",
					"user already holds that role in this organization", r,
				))
				return
			case pgForeignKeyViolation:
				writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope(
					"admin_membership.invalid_reference",
					"user_id or org_id does not exist", r,
				))
				return
			}
		}
		s.logger.Error("admin_membership: insert failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"admin_membership.insert_failed", "failed to add member", r,
		))
		return
	}

	s.writeAdminMembershipAudit(r, "v1.admin.membership.create", m.ID.String(), reason, map[string]any{
		"org_id":  m.OrgID.String(),
		"user_id": m.UserID.String(),
		"role":    m.Role,
	})

	writeJSON(w, http.StatusCreated, map[string]any{
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

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/admin/organizations/{org_id}/members/{membership_id}
// ─────────────────────────────────────────────────────────────────────────────

// adminChangeMemberRoleRequest is the request body for
// PATCH /v1/admin/organizations/{org_id}/members/{membership_id}.
type adminChangeMemberRoleRequest struct {
	Role string `json:"role"`
}

// handleAdminChangeMemberRole serves
// PATCH /v1/admin/organizations/{org_id}/members/{membership_id}.
// Requires JWT + membership.grant + X-Admin-Reason.
func (s *Server) handleAdminChangeMemberRole(w http.ResponseWriter, r *http.Request) {
	if s.membershipQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	reason, ok := requireAdminReason(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	orgID, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	membershipID, ok := uuidPathParam(w, r, "membership_id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"admin_membership.invalid_body",
			"cannot read request body: "+err.Error(), r,
		))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"admin_membership.empty_body", "request body is required", r,
		))
		return
	}

	var req adminChangeMemberRoleRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"admin_membership.invalid_json",
			"request body is not valid JSON", r,
		))
		return
	}
	req.Role = strings.TrimSpace(req.Role)
	if req.Role == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"admin_membership.invalid_role", "role is required", r,
			map[string]any{"field": "role", "allowed": membershipRoleList()},
		))
		return
	}
	if !validMembershipRoles[req.Role] {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"admin_membership.invalid_role",
			"role must be one of: organizer, agent, platform_operator, external_ticketing_operator, platform_superadmin, network_operator",
			r,
			map[string]any{"field": "role", "allowed": membershipRoleList()},
		))
		return
	}

	updated, err := s.membershipQueries.ChangeMembershipRole(ctx, membershipID, orgID, req.Role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"admin_membership.not_found",
				"no active membership matches this id within the organization", r,
			))
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			writeJSON(w, http.StatusConflict, errorEnvelope(
				"admin_membership.duplicate",
				"user already holds the requested role in this organization", r,
			))
			return
		}
		s.logger.Error("admin_membership: change role failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"admin_membership.update_failed", "failed to change member role", r,
		))
		return
	}

	s.writeAdminMembershipAudit(r, "v1.admin.membership.update", updated.ID.String(), reason, map[string]any{
		"org_id":   updated.OrgID.String(),
		"user_id":  updated.UserID.String(),
		"new_role": updated.Role,
	})

	writeJSON(w, http.StatusOK, map[string]any{
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

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/admin/organizations/{org_id}/members/{membership_id}
// ─────────────────────────────────────────────────────────────────────────────

// handleAdminDeactivateMember serves
// DELETE /v1/admin/organizations/{org_id}/members/{membership_id}.
// Soft-removes the membership (status='revoked'). Requires JWT +
// membership.revoke + X-Admin-Reason.
func (s *Server) handleAdminDeactivateMember(w http.ResponseWriter, r *http.Request) {
	if s.membershipQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	reason, ok := requireAdminReason(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	orgID, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	membershipID, ok := uuidPathParam(w, r, "membership_id")
	if !ok {
		return
	}

	deactivated, err := s.membershipQueries.DeactivateMembership(ctx, membershipID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"admin_membership.not_found",
				"no active membership matches this id within the organization", r,
			))
			return
		}
		s.logger.Error("admin_membership: deactivate failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"admin_membership.delete_failed", "failed to deactivate member", r,
		))
		return
	}

	s.writeAdminMembershipAudit(r, "v1.admin.membership.deactivate", deactivated.ID.String(), reason, map[string]any{
		"org_id":  deactivated.OrgID.String(),
		"user_id": deactivated.UserID.String(),
		"role":    deactivated.Role,
	})

	writeJSON(w, http.StatusOK, map[string]any{
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

// ─────────────────────────────────────────────────────────────────────────────
// Shared audit helper (fire-and-forget, non-tx)
// ─────────────────────────────────────────────────────────────────────────────

// writeAdminMembershipAudit emits a single audit event for an admin-membership
// write. The current revisions of the spec do not require atomic in-tx audit
// for membership writes; this fire-and-forget helper logs failures but does
// not abort the response. If a future revision requires atomicity, switch to
// the WriteTx pattern used by handleAdminArchiveOrg.
func (s *Server) writeAdminMembershipAudit(r *http.Request, action, resourceID, reason string, extra map[string]any) {
	if s.audit == nil {
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
		IP:           extractClientIP(r),
		Metadata:     metadata,
	}
	if err := s.audit.Write(r.Context(), ev); err != nil {
		s.logger.Warn("admin_membership: audit write failed",
			slog.String("action", action),
			slog.Any("error", err),
		)
	}
}

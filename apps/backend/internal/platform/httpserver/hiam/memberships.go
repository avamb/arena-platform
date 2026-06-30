// memberships.go implements the membership management API endpoints (feature #120).
//
// Memberships bind a user to an organization with a named role (organizer, agent,
// platform_operator, external_ticketing_operator, platform_superadmin). A user may
// hold multiple roles within the same organization.
//
// Endpoints:
//
//	POST   /v1/organizations/{org_id}/members           — grant a role (membership.grant)
//	GET    /v1/organizations/{org_id}/members           — list all members (membership.read)
//	DELETE /v1/organizations/{org_id}/members/{user_id} — revoke a role (membership.revoke)
//
// All endpoints require JWT authentication and a named permission.
//
// Permission resolution integration (feature #120 step 3):
//
//	When the server is started with a PgxPool, DBChecker is constructed with a
//	MembershipQuerier so that permissions.Check() unions the actor's JWT roles
//	with their active membership roles on every request. This means grant/revoke
//	operations take effect immediately without requiring the user to re-authenticate.
package hiam

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// membershipResponse is the JSON representation of a single membership.
type membershipResponse struct {
	ID       string `json:"id"`
	UserID   string `json:"user_id"`
	OrgID    string `json:"org_id"`
	Role     string `json:"role"`
	Status   string `json:"status"`
	JoinedAt string `json:"joined_at"`
}

// validMembershipRoles is the set of allowed role values for the memberships.role
// column. Mirrors the CHECK constraint in migration 0011_memberships.sql.
var validMembershipRoles = map[string]bool{
	"organizer":                   true,
	"agent":                       true,
	"platform_operator":           true,
	"external_ticketing_operator": true,
	"platform_superadmin":         true,
	// network_operator is the external operator role introduced in
	// feature #203. It is intentionally distinct from platform_operator
	// (internal Arena staff) — see migration 0042_network_operator_role.sql
	// and 09_autoforge/admin_ui/operator_network_design_note.md.
	"network_operator": true,
}

// ValidMembershipRoles is the exported form of validMembershipRoles, for use by
// the httpserver shim layer (memberships_network_operator_203_test.go references
// validMembershipRoles from package httpserver via iam_shims.go).
var ValidMembershipRoles = validMembershipRoles

// grantMembershipRequest is the request body for POST /v1/organizations/{org_id}/members.
type grantMembershipRequest struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// revokeMembershipRequest is the request body for
// DELETE /v1/organizations/{org_id}/members/{user_id}.
// The role to revoke must be supplied because a user may hold multiple roles
// in the same organization.
type revokeMembershipRequest struct {
	Role string `json:"role"`
}

// HandleGrantMembership serves POST /v1/organizations/{org_id}/members.
// Requires JWT + "membership.grant" permission (enforced by middleware in mountV1Routes).
// handleGrantMembership is the legacy name for this operation.
func (h *Handler) HandleGrantMembership(w http.ResponseWriter, r *http.Request) {
	if h.membershipQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("membership.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("membership.empty_body", "request body is required", r))
		return
	}

	var req grantMembershipRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("membership.invalid_json", "request body is not valid JSON", r))
		return
	}

	if req.UserID == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"membership.missing_user_id", "user_id is required", r,
			map[string]any{"field": "user_id"},
		))
		return
	}
	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"membership.invalid_user_id", "user_id must be a valid UUID", r,
			map[string]any{"field": "user_id"},
		))
		return
	}

	if req.Role == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"membership.invalid_role", "role is required", r,
			map[string]any{"field": "role", "allowed": MembershipRoleList()},
		))
		return
	}
	if !validMembershipRoles[req.Role] {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"membership.invalid_role",
			"role must be one of: organizer, agent, platform_operator, external_ticketing_operator, platform_superadmin, network_operator",
			r,
			map[string]any{"field": "role", "allowed": MembershipRoleList()},
		))
		return
	}

	m, err := h.membershipQueries.InsertMembership(ctx, userID, orgID, req.Role)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case pgUniqueViolation:
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
					"membership.duplicate",
					"user already holds that role in this organization",
					r,
				))
				return
			case pgForeignKeyViolation:
				httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
					"membership.invalid_reference",
					"user_id or org_id does not exist",
					r,
				))
				return
			}
		}
		h.logger.Error("membership: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"membership.insert_failed", "failed to grant membership", r,
		))
		return
	}

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

// HandleListMembers serves GET /v1/organizations/{org_id}/members.
// Requires JWT + "membership.read" permission.
// handleListMembers is the legacy name for this operation.
func (h *Handler) HandleListMembers(w http.ResponseWriter, r *http.Request) {
	if h.membershipQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}

	rows, err := h.membershipQueries.ListMembershipsByOrg(ctx, orgID)
	if err != nil {
		h.logger.Error("membership: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"membership.list_failed", "failed to list memberships", r,
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

// HandleRevokeMembership serves DELETE /v1/organizations/{org_id}/members/{user_id}.
// The role to revoke is provided in the request body.
// Requires JWT + "membership.revoke" permission.
// handleRevokeMembership is the legacy name for this operation.
func (h *Handler) HandleRevokeMembership(w http.ResponseWriter, r *http.Request) {
	if h.membershipQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	userID, ok := httputil.UUIDPathParam(w, r, "user_id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("membership.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("membership.empty_body", "request body with role is required", r))
		return
	}

	var req revokeMembershipRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("membership.invalid_json", "request body is not valid JSON", r))
		return
	}

	if req.Role == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"membership.invalid_role", "role is required", r,
			map[string]any{"field": "role", "allowed": MembershipRoleList()},
		))
		return
	}
	if !validMembershipRoles[req.Role] {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"membership.invalid_role",
			"role must be one of: organizer, agent, platform_operator, external_ticketing_operator, platform_superadmin, network_operator",
			r,
			map[string]any{"field": "role", "allowed": MembershipRoleList()},
		))
		return
	}

	deleted, err := h.membershipQueries.RevokeMembership(ctx, userID, orgID, req.Role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"membership.not_found",
				"no active membership found for this user, org, and role combination",
				r,
			))
			return
		}
		h.logger.Error("membership: revoke failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"membership.revoke_failed", "failed to revoke membership", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"membership": membershipResponse{
			ID:       deleted.ID.String(),
			UserID:   deleted.UserID.String(),
			OrgID:    deleted.OrgID.String(),
			Role:     deleted.Role,
			Status:   deleted.Status,
			JoinedAt: deleted.JoinedAt.UTC().Format(time.RFC3339),
		},
		"revoked": true,
	})
}

// MembershipRoleList returns the allowed membership role names as a sorted slice.
// membershipRoleList is the legacy name for this function.
func MembershipRoleList() []string {
	return []string{
		"agent",
		"external_ticketing_operator",
		"network_operator",
		"organizer",
		"platform_operator",
		"platform_superadmin",
	}
}

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
package httpserver

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
)

// ─────────────────────────────────────────────────────────────────────────────
// Response type
// ─────────────────────────────────────────────────────────────────────────────

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

// pgForeignKeyViolation is the PostgreSQL error code for foreign-key violations.
const pgForeignKeyViolation = "23503"

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/members
// ─────────────────────────────────────────────────────────────────────────────

// grantMembershipRequest is the request body for POST /v1/organizations/{org_id}/members.
type grantMembershipRequest struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// handleGrantMembership serves POST /v1/organizations/{org_id}/members.
// Requires JWT + "membership.grant" permission (enforced by middleware in mountV1Routes).
func (s *Server) handleGrantMembership(w http.ResponseWriter, r *http.Request) {
	if s.membershipQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("membership.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("membership.empty_body", "request body is required", r))
		return
	}

	var req grantMembershipRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("membership.invalid_json", "request body is not valid JSON", r))
		return
	}

	// Validate user_id.
	if req.UserID == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"membership.missing_user_id", "user_id is required", r,
			map[string]any{"field": "user_id"},
		))
		return
	}
	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"membership.invalid_user_id", "user_id must be a valid UUID", r,
			map[string]any{"field": "user_id"},
		))
		return
	}

	// Validate role.
	if req.Role == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"membership.invalid_role", "role is required", r,
			map[string]any{"field": "role", "allowed": membershipRoleList()},
		))
		return
	}
	if !validMembershipRoles[req.Role] {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"membership.invalid_role",
			"role must be one of: organizer, agent, platform_operator, external_ticketing_operator, platform_superadmin, network_operator",
			r,
			map[string]any{"field": "role", "allowed": membershipRoleList()},
		))
		return
	}

	m, err := s.membershipQueries.InsertMembership(ctx, userID, orgID, req.Role)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case pgUniqueViolation:
				writeJSON(w, http.StatusConflict, errorEnvelope(
					"membership.duplicate",
					"user already holds that role in this organization",
					r,
				))
				return
			case pgForeignKeyViolation:
				// user_id or org_id does not exist.
				writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope(
					"membership.invalid_reference",
					"user_id or org_id does not exist",
					r,
				))
				return
			}
		}
		s.logger.Error("membership: insert failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"membership.insert_failed", "failed to grant membership", r,
		))
		return
	}

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
// GET /v1/organizations/{org_id}/members
// ─────────────────────────────────────────────────────────────────────────────

// handleListMembers serves GET /v1/organizations/{org_id}/members.
// Requires JWT + "membership.read" permission.
func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	if s.membershipQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}

	rows, err := s.membershipQueries.ListMembershipsByOrg(ctx, orgID)
	if err != nil {
		s.logger.Error("membership: list failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
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
	writeJSON(w, http.StatusOK, map[string]any{"memberships": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/members/{user_id}
// ─────────────────────────────────────────────────────────────────────────────

// revokeMembershipRequest is the request body for
// DELETE /v1/organizations/{org_id}/members/{user_id}.
// The role to revoke must be supplied because a user may hold multiple roles
// in the same organization.
type revokeMembershipRequest struct {
	Role string `json:"role"`
}

// handleRevokeMembership serves DELETE /v1/organizations/{org_id}/members/{user_id}.
// The role to revoke is provided in the request body.
// Requires JWT + "membership.revoke" permission.
func (s *Server) handleRevokeMembership(w http.ResponseWriter, r *http.Request) {
	if s.membershipQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	userID, ok := uuidPathParam(w, r, "user_id")
	if !ok {
		return
	}

	// Read role from request body (DELETE with body is idiomatic for this use case).
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("membership.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("membership.empty_body", "request body with role is required", r))
		return
	}

	var req revokeMembershipRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("membership.invalid_json", "request body is not valid JSON", r))
		return
	}

	if req.Role == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"membership.invalid_role", "role is required", r,
			map[string]any{"field": "role", "allowed": membershipRoleList()},
		))
		return
	}
	if !validMembershipRoles[req.Role] {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"membership.invalid_role",
			"role must be one of: organizer, agent, platform_operator, external_ticketing_operator, platform_superadmin, network_operator",
			r,
			map[string]any{"field": "role", "allowed": membershipRoleList()},
		))
		return
	}

	deleted, err := s.membershipQueries.RevokeMembership(ctx, userID, orgID, req.Role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"membership.not_found",
				"no active membership found for this user, org, and role combination",
				r,
			))
			return
		}
		s.logger.Error("membership: revoke failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"membership.revoke_failed", "failed to revoke membership", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
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

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// membershipRoleList returns the allowed membership role names as a sorted slice.
func membershipRoleList() []string {
	return []string{
		"agent",
		"external_ticketing_operator",
		"network_operator",
		"organizer",
		"platform_operator",
		"platform_superadmin",
	}
}

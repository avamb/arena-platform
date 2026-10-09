// server_orgauth.go provides a server-level org-membership enforcement shim
// for sub-packages that do not embed their own membershipQueries field
// (e.g. hinventory, hfeed). Handlers in those packages delegate to these
// *Server methods before passing control to the sub-package handler.
//
// Fail-closed: when s.membershipQueries is nil, access is denied (403) rather
// than silently granted. This prevents accidental bypass of org isolation in
// environments where the membership queries handle was not wired.
package httpserver

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// actorIsMemberOfOrgServer reports whether the authenticated actor holds an
// active membership in orgID. Mirrors the same helper in hiam and hcatalog
// sub-packages but operates against the *Server's membershipQueries field.
func actorIsMemberOfOrgServer(ctx context.Context, q *gen.Queries, orgID uuid.UUID) (bool, error) {
	// Organization API keys (spec §13.1) hold no org_memberships row: their
	// reach is exactly api_keys.org_id. Decide here and never touch q.
	if isService, allowed := auth.ServiceActorInOrg(ctx, orgID.String()); isService {
		return allowed, nil
	}
	actor, ok := auth.ActorFromContext(ctx)
	if !ok || actor.ID == "" {
		return false, nil
	}
	userID, err := uuid.Parse(actor.ID)
	if err != nil {
		return false, nil
	}
	memberships, err := q.ListMembershipsByUser(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, m := range memberships {
		if m.OrgID == orgID {
			return true, nil
		}
	}
	return false, nil
}

// enforceOrgMembership extracts the URL parameter named paramName as a UUID,
// then verifies that the authenticated actor holds an active membership in that
// organization.
//
// Returns false (and writes 403) when access is denied.
// Returns true when the actor is a confirmed member and the request may proceed.
//
// Fail-closed: when s.membershipQueries is nil, 403 is returned. This ensures
// that an unintentionally-nil membershipQueries cannot silently bypass
// org-isolation enforcement.
func (s *Server) enforceOrgMembership(w http.ResponseWriter, r *http.Request, paramName string) bool {
	orgID, ok := httputil.UUIDPathParam(w, r, paramName)
	if !ok {
		return false
	}
	return s.enforceMembershipInOrg(w, r, orgID)
}

// enforceMembershipInOrg verifies that the authenticated actor holds an active
// membership in the already-resolved orgID (e.g. taken from a row the caller
// loaded, for routes where the organization is not a URL parameter — PR2-31).
//
// Same fail-closed contract as enforceOrgMembership: returns false and writes
// 403 (or 500 on lookup error) when access is denied.
func (s *Server) enforceMembershipInOrg(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) bool {
	if auth.HasSuperadminOrgAccess(r.Context()) {
		if _, ok := httputil.RequireAdminReason(w, r); !ok {
			return false
		}
		return true
	}
	if isService, allowed := httputil.ServiceActorDecision(w, r, orgID); isService {
		return allowed
	}
	if s.membershipQueries == nil {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"org.access_denied", "caller is not a member of this organization", r,
		))
		return false
	}

	ctx := r.Context()
	member, err := actorIsMemberOfOrgServer(ctx, s.membershipQueries, orgID)
	if err != nil {
		s.logger.Error("server: org membership check failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"org.membership_check_failed", "failed to verify org membership", r,
		))
		return false
	}
	if !member {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"org.access_denied", "caller is not a member of this organization", r,
		))
		return false
	}
	return true
}

// rowOrgAccess is the hcheckout.RowOrgAccess the server wires for the flat
// refund and payment-intent routes (PAY-00): the organization comes from the
// row the handler loaded, not from the path. Same decision as
// enforceMembershipInOrg — a platform superadmin acts everywhere (a write
// needs X-Admin-Reason, a read does not, as in orgread), an organization API
// key only inside api_keys.org_id, a user only inside their memberships —
// but a refusal is the route's OWN 404, never 403, so a foreign refund or
// payment id cannot be told apart from a missing one. Fail-closed when the
// membership queries are not wired.
func (s *Server) rowOrgAccess(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, write bool, notFoundCode, notFoundMessage string) bool {
	ctx := r.Context()
	if auth.HasSuperadminOrgAccess(ctx) {
		if !write {
			return true
		}
		_, ok := httputil.RequireAdminReason(w, r)
		return ok
	}
	notFound := func() bool {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(notFoundCode, notFoundMessage, r))
		return false
	}
	if isService, allowed := auth.ServiceActorInOrg(ctx, orgID.String()); isService {
		if !allowed {
			return notFound()
		}
		return true
	}
	if s.membershipQueries == nil {
		return notFound()
	}
	member, err := actorIsMemberOfOrgServer(ctx, s.membershipQueries, orgID)
	if err != nil {
		s.logger.Error("server: org membership check failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"org.membership_check_failed", "failed to verify org membership", r,
		))
		return false
	}
	if !member {
		return notFound()
	}
	return true
}

package httpserver

// org_isolation_guards.go — the server-side guards of SEC-2 for routes whose
// handlers live in sub-packages that know nothing about the caller's
// memberships: the seating bind / seat block shims and the external allocation
// shims. The class: a route with {org_id} plus a child id must verify that the
// CHILD belongs to the path organization (see org_isolation_sweep_table_test.go).

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// callerActsFor reports whether the caller may act inside orgID: a platform
// superadmin anywhere, an organization API key only inside api_keys.org_id, a
// user inside the organizations they hold a membership of. It writes nothing;
// the caller decides the response. An error is an infrastructure failure.
func (s *Server) callerActsFor(r *http.Request, orgID uuid.UUID) (bool, error) {
	ctx := r.Context()
	if auth.HasSuperadminOrgAccess(ctx) {
		return true, nil
	}
	if isService, allowed := auth.ServiceActorInOrg(ctx, orgID.String()); isService {
		return allowed, nil
	}
	if s.membershipQueries == nil {
		return false, nil
	}
	return actorIsMemberOfOrgServer(ctx, s.membershipQueries, orgID)
}

// writeOrgLookupFailed answers an infrastructure failure of a guard.
func (s *Server) writeOrgLookupFailed(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.logger.Error("server: "+what+" failed", slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"org.membership_check_failed", "failed to verify access to the organization", r,
	))
}

// requireSessionInPathOrg is the guard of the seating bind and seat
// block/unblock shims: the session of the path must belong to {org_id}. (The
// bind core and the seat patch already scope their reads by (session, event), so
// together they keep the session inside the path event of the path organization.)
// A session of another organization, or none, is the route's own 404.
func (s *Server) requireSessionInPathOrg(w http.ResponseWriter, r *http.Request, sessionParam string) bool {
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return false
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, sessionParam)
	if !ok {
		return false
	}
	if s.sessionQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return false
	}
	row, err := s.sessionQueries.GetSessionOrgContext(r.Context(), sessionID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.logger.Error("seating: session organization lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.session_lookup_failed", "failed to look up the session", r,
		))
		return false
	}
	if err != nil || row.OrgID != orgID {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
		return false
	}
	return true
}

// ─── external allocations ───────────────────────────────────────────────────
//
// An external allocation is a block of a session's capacity handed to a PARTNER
// organization (a reseller, an agent, a box office). Two organizations stand to
// it, and the path {org_id} of its routes names the partner:
//
//   - the ORGANIZER owns the session (session -> event -> organization). Only it
//     may create an allocation for a partner and run its whole life: activating
//     it holds the organizer's capacity (ledger and category places), reconciling
//     settles that capacity. Creating names the session in the body, so the
//     caller must act for the SESSION's organization — not for the path.
//   - the PARTNER (the path organization) may read its own allocation, report
//     what it consumed and dispute it, but never creates one and never holds or
//     settles the organizer's inventory.
//
// An allocation that names another partner, and a session or category the caller
// does not organize, are the route's own 404.

type allocationAccess struct {
	alloc    gen.ExternalAllocationRow
	organize bool // the caller acts for the session's organization
}

// allocationForPath loads {id} and decides whether the caller may touch it
// through the path organization. It writes the response itself when not.
func (s *Server) allocationForPath(w http.ResponseWriter, r *http.Request) (allocationAccess, bool) {
	// Without the lookup queries (unit-test servers) keep the PR2-31 rule: the
	// caller must at least be a member of the path organization. Never open.
	if s.allocationQueries == nil {
		if !s.enforceOrgMembership(w, r, "org_id") {
			return allocationAccess{}, false
		}
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.org_access_unavailable", "organization lookup is unavailable", r,
		))
		return allocationAccess{}, false
	}
	notFound := func() (allocationAccess, bool) {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"allocation.not_found", "external allocation not found", r,
		))
		return allocationAccess{}, false
	}
	partnerOrg, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return allocationAccess{}, false
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return allocationAccess{}, false
	}
	alloc, err := s.allocationQueries.GetExternalAllocationByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound()
		}
		s.writeOrgLookupFailed(w, r, "allocation lookup", err)
		return allocationAccess{}, false
	}
	if alloc.PartnerOrgID != partnerOrg {
		return notFound()
	}
	actsPartner, err := s.callerActsFor(r, alloc.PartnerOrgID)
	if err != nil {
		s.writeOrgLookupFailed(w, r, "org membership check", err)
		return allocationAccess{}, false
	}
	organize := false
	if sess, sErr := s.allocationQueries.GetSessionOrgContext(r.Context(), alloc.SessionID); sErr == nil {
		if organize, err = s.callerActsFor(r, sess.OrgID); err != nil {
			s.writeOrgLookupFailed(w, r, "org membership check", err)
			return allocationAccess{}, false
		}
	} else if !errors.Is(sErr, pgx.ErrNoRows) {
		s.writeOrgLookupFailed(w, r, "allocation session lookup", sErr)
		return allocationAccess{}, false
	}
	if !actsPartner && !organize {
		return notFound()
	}
	if auth.HasSuperadminOrgAccess(r.Context()) {
		if _, ok := httputil.RequireAdminReason(w, r); !ok {
			return allocationAccess{}, false
		}
	}
	return allocationAccess{alloc: alloc, organize: organize}, true
}

// peekBody reads the (bounded) request body and puts it back for the handler.
func peekBody(r *http.Request) []byte {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		body = nil
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body
}

// guardAllocationCreate checks a create: the session named in the body belongs
// to an organization the caller acts for (404 otherwise), the category belongs to
// that session, and the partner (the path organization) exists. A body the
// handler would reject on its own (bad JSON, bad UUIDs) is passed through: it is
// answered 400 before anything is read or written.
func (s *Server) guardAllocationCreate(w http.ResponseWriter, r *http.Request) bool {
	// Without the lookup queries (unit-test servers) keep the PR2-31 rule: the
	// caller must at least be a member of the path organization. Never open.
	if s.allocationQueries == nil {
		if !s.enforceOrgMembership(w, r, "org_id") {
			return false
		}
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.org_access_unavailable", "organization lookup is unavailable", r,
		))
		return false
	}
	partnerOrg, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return false
	}
	var probe struct {
		SessionID string  `json:"session_id"`
		TierID    *string `json:"tier_id"`
		QuotaQty  int32   `json:"quota_qty"`
		Status    string  `json:"status"`
	}
	if err := json.Unmarshal(peekBody(r), &probe); err != nil {
		return true
	}
	// A body the handler rejects on its own (no quota, unknown status) is
	// answered 400 before anything is read: pass it through.
	if probe.QuotaQty <= 0 || (probe.Status != "" && probe.Status != "pending" && probe.Status != "active") {
		return true
	}
	sessionID, err := uuid.Parse(probe.SessionID)
	if err != nil {
		return true
	}
	ctx := r.Context()
	sess, err := s.allocationQueries.GetSessionOrgContext(ctx, sessionID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.writeOrgLookupFailed(w, r, "allocation session lookup", err)
		return false
	}
	acts := false
	if err == nil {
		if acts, err = s.callerActsFor(r, sess.OrgID); err != nil {
			s.writeOrgLookupFailed(w, r, "org membership check", err)
			return false
		}
	}
	if !acts {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
		return false
	}
	if auth.HasSuperadminOrgAccess(ctx) {
		if _, ok := httputil.RequireAdminReason(w, r); !ok {
			return false
		}
	}
	if probe.TierID != nil && *probe.TierID != "" {
		if tierID, tErr := uuid.Parse(*probe.TierID); tErr == nil {
			if _, tErr := s.allocationQueries.GetTicketTierByID(ctx, tierID, sessionID); tErr != nil {
				if errors.Is(tErr, pgx.ErrNoRows) {
					httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("tier.not_found", "ticket category not found", r))
					return false
				}
				s.writeOrgLookupFailed(w, r, "allocation tier lookup", tErr)
				return false
			}
		}
	}
	if _, err := s.allocationQueries.GetOrganizationByID(ctx, partnerOrg); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("org.not_found", "organization not found", r))
			return false
		}
		s.writeOrgLookupFailed(w, r, "allocation partner lookup", err)
		return false
	}
	return true
}

// guardAllocationPatch lets the organizer do everything and the partner only
// report consumption, add notes or dispute: moving the allocation to any other
// status holds or settles the ORGANIZER's inventory.
func (s *Server) guardAllocationPatch(w http.ResponseWriter, r *http.Request) bool {
	acc, ok := s.allocationForPath(w, r)
	if !ok {
		return false
	}
	if acc.organize {
		return true
	}
	var probe struct {
		Status *string `json:"status"`
	}
	if err := json.Unmarshal(peekBody(r), &probe); err != nil {
		return true // the handler answers 400
	}
	if probe.Status != nil && *probe.Status != "" && *probe.Status != acc.alloc.Status && *probe.Status != "disputed" {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"allocation.organizer_only",
			"only the organizer of the session can activate or reconcile an allocation; a partner can report consumption or dispute it", r,
		))
		return false
	}
	return true
}

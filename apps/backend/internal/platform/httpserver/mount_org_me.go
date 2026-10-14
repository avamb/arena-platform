package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hiam"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
)

// mountOrgMeRoutes mounts GET /v1/organizations/{org_id}/me (web event
// center, A1): the caller's role and effective permissions inside one
// organization.
//
// The group records NO required permission: every member may read their own
// access, and the handler itself admits only members, the organization's own
// API keys and a platform superadmin. The organization still goes into the
// context (orgScopedFor), so the checker resolves roles inside it.
func (s *Server) mountOrgMeRoutes(r chi.Router) {
	if !s.authEnabled() || s.membershipQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		pr.Use(s.authMiddleware())
		pr.Use(s.orgScopedFor("", true))
		pr.Use(s.markSuperadminOrgAccess)
		pr.Get("/organizations/{org_id}/me", s.handleOrgMe)
	})
}

func (s *Server) handleOrgMe(w http.ResponseWriter, r *http.Request) {
	var resolver permissions.OrgEffectiveResolver
	if res, ok := s.perms.(permissions.OrgEffectiveResolver); ok {
		resolver = res
	}
	var q hiam.OrgMeQuerier
	var isMember hiam.MemberCheck
	if s.membershipQueries != nil {
		q = s.membershipQueries
		isMember = hiam.MembershipCheck(s.membershipQueries)
	}
	hiam.HandleOrgMe(q, isMember, resolver, s.logger)(w, r)
}

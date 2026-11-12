package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hgetstarted"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hiam"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
)

// mountGettingStartedRoutes mounts GET /v1/organizations/{org_id}/getting-started:
// which of the four first steps of the organization are done.
//
// Same gate as GET .../me: no required permission (every member may read
// it; the handler lists only the steps the caller may do and admits only
// members, the organization's own API keys and a platform superadmin), the
// organization goes into the context for the per-organization checker, and
// an archived organization answers 404 like the other member reads.
func (s *Server) mountGettingStartedRoutes(r chi.Router) {
	if !s.authEnabled() || s.membershipQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		pr.Use(s.authMiddleware())
		pr.Use(s.orgScopedFor("", true))
		pr.Use(s.markSuperadminOrgAccess)
		pr.Use(s.refuseArchivedOrganization)
		pr.Get("/organizations/{org_id}/getting-started", s.handleGettingStarted)
	})
}

func (s *Server) handleGettingStarted(w http.ResponseWriter, r *http.Request) {
	var resolver permissions.OrgEffectiveResolver
	if res, ok := s.perms.(permissions.OrgEffectiveResolver); ok {
		resolver = res
	}
	var (
		isMember hgetstarted.MemberCheck
		facts    hgetstarted.FactsReader
		seller   hgetstarted.SellerCheck
	)
	if s.membershipQueries != nil {
		isMember = hgetstarted.MemberCheck(hiam.MembershipCheck(s.membershipQueries))
	}
	if s.pgxPool != nil {
		facts = gen.New(s.pgxPool)
		seller = s.sellerHandler().SellerDataComplete
	}
	hgetstarted.Handle(isMember, resolver, facts, seller, s.logger)(w, r)
}
